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

	animedomain "bansoko/internal/anime/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// typedRuntimeFixture is a prepared copy operation driven through the real
// worker entry (Worker.RunOnce -> Service.Handle -> handleTypedExecution) with
// the production filesystem adapter and observer.
type typedRuntimeFixture struct {
	db      *sql.DB
	f       fileFixture
	s       *Service
	store   *workitems.Store
	planID  string
	opID    string
	workID  string
	target  string
	source  string
	subtile string // subtitle operation id when present
	subSrc  string // subtitle source path when present
}

func newTypedRuntimeFixture(t *testing.T, mutate func(map[string]any), withSubtitle bool) typedRuntimeFixture {
	t.Helper()
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "fail"}
		if withSubtitle {
			bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		}
		if mutate != nil {
			mutate(p)
		}
	})
	subtitleSource := ""
	if withSubtitle {
		source := t.TempDir() + "/source.chs.ass"
		if err := os.WriteFile(source, []byte("subtitle"), 0600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, f, "typed-subtitle", "subtitle", "source.chs.ass", source, "subtitle", 8)
		subtitleSource = source
	}
	s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	x := typedRuntimeFixture{db: db, f: f, s: s, store: &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}, source: f.sourcePath, subSrc: subtitleSource}
	x.planID = seedA11Plan(t, f.artifactID, nil)
	runA11PlanWorker(t, db, s)
	if err := db.QueryRow(`SELECT o.id,o.target_path,w.id FROM file_operations o JOIN work_item_history w ON w.owner_type='file_operation' AND w.owner_id=o.id WHERE o.artifact_id=$1 AND o.file_ref='primary'`, f.artifactID).Scan(&x.opID, &x.target, &x.workID); err != nil {
		t.Fatalf("typed video operation/work: %v", err)
	}
	// B5b: no target directory is pre-created; the typed adapter creates the
	// missing parent after the attempt grant.
	if withSubtitle {
		if err := db.QueryRow(`SELECT id FROM file_operations WHERE artifact_id=$1 AND file_ref='subtitle'`, f.artifactID).Scan(&x.subtile); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func (x typedRuntimeFixture) runFileWorker(t *testing.T) (bool, error) {
	t.Helper()
	w := worker.NewWorker(x.store, "typed-file-worker", time.Minute)
	if err := w.Register(workitems.KindFileExecute, x.s); err != nil {
		t.Fatal(err)
	}
	return w.RunOnce(context.Background())
}

type typedOpState struct {
	opState, opCode, workState string
	attempt                    int
}

func (x typedRuntimeFixture) state(t *testing.T, opID string) typedOpState {
	t.Helper()
	var st typedOpState
	var code sql.NullString
	if err := x.db.QueryRow(`SELECT o.state,o.failure_code,COALESCE((SELECT w.state FROM work_item_history w WHERE w.owner_type='file_operation' AND w.owner_id=o.id ORDER BY w.created_at, w.id LIMIT 1),''),COALESCE((SELECT w.attempt FROM work_item_history w WHERE w.owner_type='file_operation' AND w.owner_id=o.id ORDER BY w.created_at, w.id LIMIT 1),0) FROM file_operations o WHERE o.id=$1`, opID).Scan(&st.opState, &code, &st.workState, &st.attempt); err != nil {
		t.Fatal(err)
	}
	st.opCode = code.String
	return st
}

type typedDeliveryResult struct{ outcome, effect, code string }

func (x typedRuntimeFixture) delivery(t *testing.T, fileRef string) typedDeliveryResult {
	t.Helper()
	var r typedDeliveryResult
	var code sql.NullString
	if err := x.db.QueryRow(`SELECT r.outcome,r.effect_class,r.delivery_reason_code FROM acquisition_files r JOIN file_operations o ON o.acquisition_id=r.acquisition_id AND o.file_ref=r.file_ref WHERE o.artifact_id=$1 AND r.file_ref=$2`, x.f.artifactID, fileRef).Scan(&r.outcome, &r.effect, &code); err != nil {
		t.Fatalf("delivery result %s: %v", fileRef, err)
	}
	r.code = code.String
	return r
}

// B5 step 2 (was the step-1 characterisation): a typed explicit failure now
// terminates through the fixed 184 command via the real worker entry. The
// video operation and its work fail with the typed code and a no-effect
// delivery result; the dependent subtitle fails DEPENDENCY_FAILED without any
// work; no placement obligation is created (sink wired) and the target is
// untouched. Covers the legacy explicit-failure pending group (failed video
// blocks the subtitle, failed operation never reaches the placement sink, a
// source size mismatch is the terminal SOURCE_CHANGED). Path-level
// verification compares the frozen size only; a same-size source change is
// published (TestTypedSameSizeSourceChangePublishes).
func TestTypedExplicitFailureTerminates(t *testing.T) {
	for _, c := range []struct{ name, code string }{
		{"source_missing", "SOURCE_MISSING"},
		{"source_size_changed", "SOURCE_CHANGED"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newTypedRuntimeFixture(t, nil, true)
			sink := &recordingPlacementSink{}
			x.s.SetPlacementSink(sink)
			switch c.name {
			case "source_missing":
				if err := os.Remove(x.source); err != nil {
					t.Fatal(err)
				}
			case "source_size_changed":
				if err := os.WriteFile(x.source, []byte("source-content-longer"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if handled, err := x.runFileWorker(t); !handled || err != nil {
				t.Fatalf("typed failure run: handled=%v err=%v", handled, err)
			}
			if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != c.code || got.workState != workitems.StateFailed {
				t.Fatalf("video not terminal with %s: %+v", c.code, got)
			}
			if got := x.delivery(t, "primary"); got.outcome != "failed" || got.effect != "no_external_effect" || got.code != c.code {
				t.Fatalf("video delivery result: %+v", got)
			}
			if got := x.state(t, x.subtile); got.opState != ownerStateFailed || got.opCode != "DEPENDENCY_FAILED" || got.workState != "" {
				t.Fatalf("subtitle must fail DEPENDENCY_FAILED without work: %+v", got)
			}
			if got := x.delivery(t, "subtitle"); got.outcome != "failed" || got.code != "DEPENDENCY_FAILED" {
				t.Fatalf("subtitle delivery result: %+v", got)
			}
			assertPlacementAbsent(t, x.db, x.opID)
			if len(sink.calls) != 0 {
				t.Fatalf("placement sink invoked on failure: %v", sink.calls)
			}
			if _, err := os.Lstat(x.target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target touched by a failed operation: %v", err)
			}
			// No live work remains: the worker finds nothing more to do.
			if handled, err := x.runFileWorker(t); handled || err != nil {
				t.Fatalf("work left after terminal failure: handled=%v err=%v", handled, err)
			}
		})
	}
}

// The same fixture's happy path: one real worker run copies the file and the
// fixed finish completes the operation and its work.
func TestTypedRuntimeCopyCompletes(t *testing.T) {
	x := newTypedRuntimeFixture(t, nil, false)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
	}
	if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded {
		t.Fatalf("typed copy not completed: %+v", got)
	}
	raw, err := os.ReadFile(x.target)
	if err != nil || string(raw) != "source-content" {
		t.Fatalf("target content=%q err=%v", raw, err)
	}
}

// Same path + same size is accepted weak identity: the
// downloader's piece verification is the content proof, so a same-size source
// change is not detected and the frozen operation publishes it.
func TestTypedSameSizeSourceChangePublishes(t *testing.T) {
	x := newTypedRuntimeFixture(t, nil, false)
	if err := os.WriteFile(x.source, []byte("SOURCE-CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
	}
	if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded {
		t.Fatalf("same-size source change not published: %+v", got)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "SOURCE-CONTENT" {
		t.Fatalf("target content=%q err=%v", raw, err)
	}
}

func (x typedRuntimeFixture) runFileWorkerCtx(t *testing.T, ctx context.Context) (bool, error) {
	t.Helper()
	w := worker.NewWorker(x.store, "typed-file-worker", time.Minute)
	if err := w.Register(workitems.KindFileExecute, x.s); err != nil {
		t.Fatal(err)
	}
	return w.RunOnce(ctx)
}

func (x typedRuntimeFixture) expireWork(t *testing.T) {
	t.Helper()
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second', available_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, x.workID); err != nil {
		t.Fatal(err)
	}
}

func (x typedRuntimeFixture) attemptsStarted(t *testing.T) int {
	t.Helper()
	var n int
	if err := x.db.QueryRow(`SELECT file_attempts_started FROM file_operations WHERE id=$1`, x.opID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// B5 step 3 (A10 narrowed): after a crash or retry the same work converges
// from persistent evidence only — a closed proof completes, an interrupted
// publication ends visibly without redoing the effect, and an interruption
// before the permit simply runs again.
func TestTypedConvergenceAfterCrash(t *testing.T) {
	t.Run("interrupted_before_permit_then_completes", func(t *testing.T) {
		// Revives TestFileObserverTransientThenRetryCompletes on the typed
		// lane: the first claim takes its attempt ticket and the process dies
		// before any permit; the re-claim runs the chain again and completes.
		x := newTypedRuntimeFixture(t, nil, false)
		c, err := x.store.ClaimDue(context.Background(), "typed-dead-worker", []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if grant, err := x.s.startFileAttempt(context.Background(), c, "crashed-session"); err != nil || !grant.Granted {
			t.Fatalf("crashed attempt: %+v %v", grant, err)
		}
		x.s.executor = filesystem.OSFileExecutor{}
		x.expireWork(t)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("re-claim: handled=%v err=%v", handled, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded || got.attempt != 2 || x.attemptsStarted(t) != 2 {
			t.Fatalf("re-claim must complete on the second attempt: %+v started=%d", got, x.attemptsStarted(t))
		}
	})
	t.Run("interrupted_mid_publication_ends_visibly", func(t *testing.T) {
		// A move is used: path-level publication renames the staged temp into
		// place, so a copy's temp_cleanup finds nothing to remove and has no
		// cleanup point; the move's source_cleanup is the first cleanup point
		// after the target was published.
		x := newTypedRuntimeFixture(t, func(p map[string]any) {
			p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "move", "collision_policy": "fail"}
		}, false)
		ctx, cancel := context.WithCancel(context.Background())
		// The process stops inside the publication (at its first cleanup
		// point, after the target was published) — evidence stays unclosed.
		x.s.executor = filesystem.OSFileExecutor{Hooks: filesystem.ExecutorHooks{PublicationCleanupPoint: func(string) error {
			cancel()
			return context.Canceled
		}}}
		if _, err := x.runFileWorkerCtx(t, ctx); err == nil {
			t.Fatal("interrupted run reported success")
		}
		x.s.executor = filesystem.OSFileExecutor{}
		x.expireWork(t)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("re-claim: handled=%v err=%v", handled, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != "FILE_EXECUTION_INTERRUPTED" || got.workState != workitems.StateFailed {
			t.Fatalf("interrupted publication must end visibly: %+v", got)
		}
		if got := x.delivery(t, "primary"); got.outcome != "failed" || got.effect != "unknown" {
			t.Fatalf("interrupted publication delivery: %+v", got)
		}
		// The published object is left exactly as the interrupted run left it:
		// not removed, not republished, and the move source is not removed.
		if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "source-content" {
			t.Fatalf("interrupted target must be kept as is: %q %v", raw, err)
		}
		if raw, err := os.ReadFile(x.source); err != nil || string(raw) != "source-content" {
			t.Fatalf("interrupted move source must be kept: %q %v", raw, err)
		}
		var evidence int
		if err := x.db.QueryRow(`SELECT count(*) FROM file_operations WHERE id=$1 AND publication_permit_id IS NOT NULL`, x.opID).Scan(&evidence); err != nil || evidence != 1 {
			t.Fatalf("evidence must be kept: %d %v", evidence, err)
		}
		if x.attemptsStarted(t) != 1 {
			t.Fatalf("no new attempt may start after an interrupted publication: %d", x.attemptsStarted(t))
		}
	})
	t.Run("interrupted_at_the_publish_boundary_ends_visibly_without_redoing", func(t *testing.T) {
		// The typed publication path exposes no hook between "stage_temp
		// recorded" and "publish_target granted"; the earliest reachable
		// interruption after staging is BeforePublish (after stage_temp and
		// reserve_version are recorded, before the publish rename; path-level
		// publication has no exchange). Version collision is used so the
		// reservation shows the publish mutation did not run.
		x := newTypedRuntimeFixture(t, func(p map[string]any) {
			p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "versioned"}
		}, false)
		ctx, cancel := context.WithCancel(context.Background())
		x.s.executor = filesystem.OSFileExecutor{Hooks: filesystem.ExecutorHooks{BeforePublish: func(string, string) error {
			cancel()
			return context.Canceled
		}}}
		if _, err := x.runFileWorkerCtx(t, ctx); err == nil {
			t.Fatal("interrupted run reported success")
		}
		x.s.executor = filesystem.OSFileExecutor{}
		// Only the empty reserved candidate exists: version collision reserves
		// its first candidate at the base target path (file_version_candidate
		// ordinal 1 == base), and the publish mutation — which would rename
		// the staged temp over it — did not run.
		reservedInfo, err := os.Stat(x.target)
		if err != nil || reservedInfo.Size() != 0 {
			t.Fatalf("a pre-publish interruption must leave only the empty reservation: %v %+v", err, reservedInfo)
		}
		var reserved string
		if err := x.db.QueryRow(`SELECT publication_step_checkpoint->'reserve_version'->'proof'->'object'->'location'->>'canonical_path' FROM file_operations WHERE id=$1`, x.opID).Scan(&reserved); err != nil {
			t.Fatalf("reserved candidate: %v", err)
		}
		if reserved != x.target {
			t.Fatalf("reserved candidate %q is not the base target %q", reserved, x.target)
		}
		x.expireWork(t)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("re-claim: handled=%v err=%v", handled, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != "FILE_EXECUTION_INTERRUPTED" || got.workState != workitems.StateFailed {
			t.Fatalf("interrupted publication must end visibly: %+v", got)
		}
		if got := x.delivery(t, "primary"); got.outcome != "failed" || got.effect != "unknown" {
			t.Fatalf("interrupted publication delivery: %+v", got)
		}
		// The reserved candidate is left exactly as the interrupted run left
		// it: still the empty reservation, not removed and not republished.
		reservedInfo, err = os.Stat(reserved)
		if err != nil || reservedInfo.Size() != 0 {
			t.Fatalf("nothing may be republished after an interruption: %v %+v", err, reservedInfo)
		}
		var evidence int
		if err := x.db.QueryRow(`SELECT count(*) FROM file_operations WHERE id=$1 AND publication_permit_id IS NOT NULL`, x.opID).Scan(&evidence); err != nil || evidence != 1 {
			t.Fatalf("evidence must be kept: %d %v", evidence, err)
		}
		if x.attemptsStarted(t) != 1 {
			t.Fatalf("no new attempt may start after an interrupted publication: %d", x.attemptsStarted(t))
		}
	})
	t.Run("closed_proof_finish_retried_on_new_token", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		x.s.SetPlacementSink(&recordingPlacementSink{})
		injectPlacementObligationFailure(t)
		handled, _ := x.runFileWorker(t)
		if !handled {
			t.Fatal("first run not handled")
		}
		if got := x.state(t, x.opID); got.opState == ownerStateCompleted || got.opState == ownerStateFailed {
			t.Fatalf("a rolled-back finish must leave the operation live: %+v", got)
		}
		removePlacementObligationFailure(t)
		x.expireWork(t)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("re-claim finish: handled=%v err=%v", handled, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded || x.attemptsStarted(t) != 1 {
			t.Fatalf("closed proof must complete on the new token without a new attempt: %+v started=%d", got, x.attemptsStarted(t))
		}
		assertPlacementHandoff(t, x.db, x.opID, x.workID)
	})
	t.Run("recovery_claim_finishes_closed_proof_without_executor", func(t *testing.T) {
		// Revives TestPGFileRecoveryObservesFrozenTargetWithoutExecutor: the
		// recovery observer completes from the persisted proof alone.
		x := newTypedRuntimeFixture(t, nil, false)
		x.s.SetPlacementSink(&recordingPlacementSink{})
		injectPlacementObligationFailure(t)
		c, err := x.store.ClaimDue(context.Background(), "typed-recovery", []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// The finish rolls back cleanly (ErrFinishNotCommitted); the claim stays leased.
		_, _ = x.s.Handle(context.Background(), c)
		if got := x.state(t, x.opID); got.opState == ownerStateCompleted {
			t.Fatalf("rolled-back finish completed: %+v", got)
		}
		removePlacementObligationFailure(t)
		if err := x.store.BlockRecovery(context.Background(), c, "attempt_exhausted"); err != nil {
			t.Fatal(err)
		}
		x.expireWork(t)
		rc, err := x.store.ClaimRecovery(context.Background(), []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		x.s.executor = &testExecutor{}
		out, err := x.s.ObserveRecovery(context.Background(), rc)
		if err != nil || out.Kind != worker.OutcomeCommitted || out.OwnerResult.State != ownerStateCompleted {
			t.Fatalf("recovery must finish the closed proof: %+v %v", out, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded {
			t.Fatalf("recovery finish: %+v", got)
		}
		if ex := x.s.executor.(*testExecutor); ex.calls != 0 {
			t.Fatalf("recovery ran the executor: %d", ex.calls)
		}
	})
}

// B5b: the first episode of a new series (no series directory) and of a new
// season (series directory present, season missing) lands through the real
// worker entry; a symlinked ancestor or a file in the path fails closed with
// zero publication.
func TestTypedCreatesMissingTargetParent(t *testing.T) {
	for _, c := range []string{"new_series", "new_season", "symlinked_ancestor", "file_in_path"} {
		t.Run(c, func(t *testing.T) {
			x := newTypedRuntimeFixture(t, nil, true)
			root := canonicalFixtureRoot(t, x.f)
			rel, err := filepath.Rel(root, filepath.Dir(x.target))
			if err != nil {
				t.Fatal(err)
			}
			series := filepath.Join(root, strings.Split(rel, string(filepath.Separator))[0])
			if _, err := os.Lstat(filepath.Dir(x.target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target parent must not pre-exist: %v", err)
			}
			outside := t.TempDir()
			switch c {
			case "new_season":
				if series == filepath.Dir(x.target) {
					t.Fatalf("target %s has no season level under the series directory", x.target)
				}
				if err := os.Mkdir(series, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlinked_ancestor":
				if err := os.Symlink(outside, series); err != nil {
					t.Fatal(err)
				}
			case "file_in_path":
				if err := os.WriteFile(series, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if handled, err := x.runFileWorker(t); !handled || err != nil {
				t.Fatalf("video run: %v %v", handled, err)
			}
			got := x.state(t, x.opID)
			if c == "new_series" || c == "new_season" {
				if got.opState != ownerStateCompleted {
					t.Fatalf("first episode did not land: %+v", got)
				}
				if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "source-content" {
					t.Fatalf("video target %q %v", raw, err)
				}
				if info, err := os.Lstat(filepath.Dir(x.target)); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					t.Fatalf("created parent: %v %v", info, err)
				}
				// The subtitle shares the created directory.
				if handled, err := x.runFileWorker(t); !handled || err != nil {
					t.Fatalf("subtitle run: %v %v", handled, err)
				}
				if sub := x.state(t, x.subtile); sub.opState != ownerStateCompleted {
					t.Fatalf("subtitle did not land: %+v", sub)
				}
				return
			}
			if got.opState != ownerStateFailed || got.opCode != "target_parent_invalid" || got.workState != workitems.StateFailed {
				t.Fatalf("unsafe parent must fail closed: %+v", got)
			}
			if d := x.delivery(t, "primary"); d.effect != "no_external_effect" {
				t.Fatalf("unsafe parent delivery: %+v", d)
			}
			var evidence int
			if err := x.db.QueryRow(`SELECT count(*) FROM file_operations WHERE id=$1 AND publication_permit_id IS NOT NULL`, x.opID).Scan(&evidence); err != nil || evidence != 0 {
				t.Fatalf("no permit may exist: %d %v", evidence, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatalf("nothing may be created through the symlink: %v %v", entries, err)
			}
			if c == "file_in_path" {
				if raw, err := os.ReadFile(series); err != nil || string(raw) != "not a directory" {
					t.Fatalf("file in path changed: %q %v", raw, err)
				}
			}
		})
	}
}
