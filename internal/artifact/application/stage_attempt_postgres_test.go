package application

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Inspect business-attempt budget through the real worker
// entry (Worker.RunOnce -> InspectHandler). The migration SQL guards themselves are
// proven by the migration tests; these tests prove the handler wiring:
// where the gate sits, what a crash on either side of it costs, that the same
// token is never re-granted, and that exhaustion is a visible terminal state.

// crashingInspector simulates the process dying during the inspection I/O:
// it cancels the run's context and fails, so no owner transition commits.
type crashingInspector struct {
	cancel context.CancelFunc
	calls  int
}

func (c *crashingInspector) Inspect(context.Context, string) (domain.ArtifactFacts, error) {
	c.calls++
	c.cancel()
	return domain.ArtifactFacts{}, context.Canceled
}

type a11InspectBudget struct {
	started  int
	max      int
	states   string
	workAtt  int
	workSt   string
	artState string
	artCode  string
}

func readA11InspectBudget(t *testing.T, db *sql.DB, artifactID, workID string) a11InspectBudget {
	t.Helper()
	var b a11InspectBudget
	var code sql.NullString
	if err := db.QueryRow(`
SELECT a.inspect_attempts_started, a.inspect_max_attempts,
       COALESCE((SELECT string_agg(e->>'state', ',' ORDER BY (e->>'ordinal')::int) FROM jsonb_array_elements(a.inspect_attempt_index) e), ''),
       w.attempt, w.state, a.state, a.failure_code
  FROM artifacts a, work_item_history w WHERE a.id=$1 AND w.id=$2`, artifactID, workID).Scan(
		&b.started, &b.max, &b.states, &b.workAtt, &b.workSt, &b.artState, &code); err != nil {
		t.Fatal(err)
	}
	b.artCode = code.String
	return b
}

func seedA11InspectArtifact(t *testing.T, db *sql.DB, name string, inspectMaxAttempts any) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:` + name + `","artifact_path":"` + canonical + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwnerBudget(t, db, "artifact-"+name, "task-"+name, "completed", "pending", correlation, `{}`, inspectMaxAttempts)
	return "artifact-" + name, canonical
}

func expireA11Lease(t *testing.T, workID string) {
	t.Helper()
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second', available_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, workID); err != nil {
		t.Fatal(err)
	}
}

func newA11InspectWorker(t *testing.T, store *workitems.Store, service *Service) *worker.Worker {
	t.Helper()
	w := worker.NewWorker(store, "a11-inspect-worker", time.Minute)
	if err := w.Register(workitems.KindArtifactInspect, service.InspectHandler()); err != nil {
		t.Fatal(err)
	}
	return w
}

// Gate-after crash costs a business attempt; replaying the same token is
// refused without I/O; a crash before the gate costs only the claim; the next
// real run supersedes the interrupted ticket and verifies.
func TestA11InspectGateCrashSidesAndSameTokenThroughWorker(t *testing.T) {
	db := openArtifactTestDB(t)
	artifactID, canonical := seedA11InspectArtifact(t, db, "a11-sides", nil)
	claim, store := claimArtifactWorkItem(t, db, artifactID, []byte(`{"download_task_id":"task-a11-sides","provider_task_id":"tag:a11-sides"}`))

	// 1. Crash after the gate (inside the inspection I/O).
	runCtx, cancel := context.WithCancel(context.Background())
	crash := &crashingInspector{cancel: cancel}
	service, err := NewService(db, crash)
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	if _, err := service.InspectHandler().Handle(runCtx, claim); err == nil {
		t.Fatal("crashed run must not report an outcome")
	}
	if got := readA11InspectBudget(t, db, artifactID, claim.ID); got.started != 1 || got.states != "issued" || got.workAtt != 1 || got.workSt != "leased" || got.artState != "pending" || crash.calls != 1 {
		t.Fatalf("gate-after crash must cost exactly one business attempt and write no owner state: %+v calls=%d", got, crash.calls)
	}

	// 2. The same claim token replayed: no new grant, no I/O, no write.
	replay := &fakeInspector{err: errors.New("must not inspect on a replayed token")}
	service.inspector = replay
	if _, err := service.InspectHandler().Handle(context.Background(), claim); !errors.Is(err, workitems.ErrStageAttemptAlreadyIssued) {
		t.Fatalf("same-token replay must be refused: %v", err)
	}
	if got := readA11InspectBudget(t, db, artifactID, claim.ID); got.started != 1 || len(replay.paths) != 0 {
		t.Fatalf("same-token replay re-granted or inspected: %+v paths=%v", got, replay.paths)
	}

	// 3. Crash before the gate: the claim is taken and the process dies.
	expireA11Lease(t, claim.ID)
	abandoned, err := store.ClaimDue(context.Background(), "a11-dead-worker", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil || abandoned.ID != claim.ID {
		t.Fatalf("reclaim for the pre-gate crash: %v %+v", err, abandoned)
	}
	if got := readA11InspectBudget(t, db, artifactID, claim.ID); got.started != 1 || got.workAtt != 2 {
		t.Fatalf("pre-gate crash must cost only the claim: %+v", got)
	}

	// 4. Recovery: the next real run (third claim) supersedes and verifies.
	expireA11Lease(t, claim.ID)
	good := &fakeInspector{facts: domain.ArtifactFacts{Path: canonical, SizeBytes: 1024, SHA256: strings.Repeat("e", 64), ObservedAt: time.Now().UTC(), MediaKind: "video"}}
	service.inspector = good
	if handled, err := newA11InspectWorker(t, store, service).RunOnce(context.Background()); err != nil || !handled {
		t.Fatalf("recovery run: handled=%v err=%v", handled, err)
	}
	got := readA11InspectBudget(t, db, artifactID, claim.ID)
	if got.started != 2 || got.states != "superseded,issued" || got.workAtt != 3 || got.workSt != workitems.StateSucceeded || got.artState != "verified" || len(good.paths) != 1 {
		t.Fatalf("recovery must verify with exactly two business attempts: %+v paths=%v", got, good.paths)
	}
}

// A business budget spent by interrupted attempts terminates visibly at the
// gate (no further I/O), not by silently re-queueing.
func TestA11InspectBudgetExhaustionIsVisibleTerminal(t *testing.T) {
	db := openArtifactTestDB(t)
	artifactID, _ := seedA11InspectArtifact(t, db, "a11-exhaust", 1)
	claim, store := claimArtifactWorkItem(t, db, artifactID, []byte(`{"download_task_id":"task-a11-exhaust","provider_task_id":"tag:a11-exhaust"}`))
	runCtx, cancel := context.WithCancel(context.Background())
	crash := &crashingInspector{cancel: cancel}
	service, err := NewService(db, crash)
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	if _, err := service.InspectHandler().Handle(runCtx, claim); err == nil {
		t.Fatal("crashed run must not report an outcome")
	}
	expireA11Lease(t, claim.ID)
	if handled, err := newA11InspectWorker(t, store, service).RunOnce(context.Background()); err != nil || !handled {
		t.Fatalf("exhausted run: handled=%v err=%v", handled, err)
	}
	got := readA11InspectBudget(t, db, artifactID, claim.ID)
	if got.started != 1 || got.max != 1 || got.workSt != workitems.StateFailed || got.artState != "failed" || got.artCode != inspectBudgetExhaustedCode || crash.calls != 1 {
		t.Fatalf("exhaustion must be a typed terminal failure without more I/O: %+v calls=%d", got, crash.calls)
	}
}

// Interrupted attempts are counted in the business budget until the claim
// budget runs out; the recovery observer then terminates the artifact with
// ARTIFACT_INSPECT_INTERRUPTED (no WORKER_EXECUTION_STOPPED, no silent stall).
func TestA11InspectInterruptedUntilRecoveryTerminal(t *testing.T) {
	db := openArtifactTestDB(t)
	artifactID, _ := seedA11InspectArtifact(t, db, "a11-interrupt", nil)
	claim, store := claimArtifactWorkItem(t, db, artifactID, []byte(`{"download_task_id":"task-a11-interrupt","provider_task_id":"tag:a11-interrupt"}`))
	service, err := NewService(db, &fakeInspector{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	for run := 1; run <= claim.MaxAttempts; run++ {
		if run > 1 {
			expireA11Lease(t, claim.ID)
			next, err := store.ClaimDue(context.Background(), "a11-interrupt-worker", []string{workitems.KindArtifactInspect}, time.Minute)
			if err != nil {
				t.Fatalf("claim %d: %v", run, err)
			}
			claim = next
		}
		runCtx, cancel := context.WithCancel(context.Background())
		service.inspector = &crashingInspector{cancel: cancel}
		if _, err := service.InspectHandler().Handle(runCtx, claim); err == nil {
			t.Fatalf("run %d: crashed run must not report an outcome", run)
		}
	}
	expireA11Lease(t, claim.ID)
	if _, err := store.ClaimDue(context.Background(), "a11-interrupt-worker", []string{workitems.KindArtifactInspect}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("an exhausted claim budget must not be reclaimed for execution: %v", err)
	}
	rc, err := store.ClaimRecovery(context.Background(), []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service.inspector = &fakeInspector{err: errors.New("recovery must not inspect")}
	observer := service.InspectHandler().(worker.RecoveryObserver)
	o, err := observer.ObserveRecovery(context.Background(), rc)
	if err != nil || o.Kind != worker.OutcomeCommitted || o.OwnerResult.ReasonCode != "ARTIFACT_INSPECT_INTERRUPTED" {
		t.Fatalf("recovery terminal: %+v %v", o, err)
	}
	got := readA11InspectBudget(t, db, artifactID, claim.ID)
	if got.started != claim.MaxAttempts || got.states != "superseded,superseded,issued" || got.workSt != workitems.StateFailed || got.artState != "failed" || got.artCode != "ARTIFACT_INSPECT_INTERRUPTED" {
		t.Fatalf("interrupted attempts must be counted and end visibly: %+v", got)
	}
}
