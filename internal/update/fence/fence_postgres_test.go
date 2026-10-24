package fence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"bansoko/internal/testutil/disposabledb"
)

func TestStoreRejectsInvalidAdmissionInputs(t *testing.T) {
	ctx := context.Background()
	if err := New(nil).Acquire(ctx, "op", AdmittedByEmbedded, 1, "digest"); err == nil || err.Error() != "update fence database is not configured" {
		t.Fatalf("missing database error = %v", err)
	}
	store := New(&sql.DB{})

	for name, input := range map[string]struct {
		operationID string
		owner       string
		sequence    int64
		digest      string
		want        error
	}{
		"missing sequence":  {operationID: "op", owner: AdmittedByEmbedded, sequence: 0, digest: "digest", want: ErrSequenceMissing},
		"missing operation": {operationID: "", owner: AdmittedByEmbedded, sequence: 1, digest: "digest", want: ErrStateConflict},
		"missing digest":    {operationID: "op", owner: AdmittedByEmbedded, sequence: 1, digest: "", want: ErrManifestConflict},
		"invalid owner":     {operationID: "op", owner: "unknown", sequence: 1, digest: "digest", want: ErrOwnerInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.Acquire(ctx, input.operationID, input.owner, input.sequence, input.digest); !errors.Is(err, input.want) {
				t.Fatalf("Acquire() = %v, want %v", err, input.want)
			}
		})
	}
}

func TestStoreOwnsOpenStateTransitions(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()

	ctx := context.Background()
	store := New(db)
	if err := store.Acquire(ctx, "direct-active", AdmittedByEmbedded, 100, "digest-active"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	rows, err := store.ListOpen(ctx, AdmittedByEmbedded)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(rows) != 1 || rows[0].OperationID != "direct-active" || rows[0].State != "active" {
		t.Fatalf("ListOpen = %+v, want one active embedded row", rows)
	}

	rows, err = store.ListOpenAll(ctx)
	if err != nil {
		t.Fatalf("ListOpenAll: %v", err)
	}
	if len(rows) != 1 || rows[0].State != "active" {
		t.Fatalf("ListOpenAll = %+v, want one active row", rows)
	}
	if _, err := db.ExecContext(ctx, `UPDATE embedded_update_locks SET state = 'pending' WHERE op_id = $1`, "direct-active"); err == nil {
		t.Fatal("the fence state CHECK accepted the retired pending state")
	}

	if err := store.ReconcileExactCurrentState(ctx, "direct-active", AdmittedByEmbedded, "digest-active", 100, "succeeded"); err != nil {
		t.Fatalf("ReconcileExactCurrentState: %v", err)
	}
	if rows, err := store.ListOpen(ctx, AdmittedByEmbedded); err != nil {
		t.Fatalf("ListOpen after reconcile: %v", err)
	} else if len(rows) != 0 {
		t.Fatalf("ListOpen after reconcile = %+v, want empty", rows)
	}
	if err := store.ReconcileExact(ctx, "direct-active", AdmittedByEmbedded, "digest-active", 100, "active", "succeeded"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("strict re-close = %v, want ErrStateConflict", err)
	}
	if err := store.Acquire(ctx, "direct-next", AdmittedByEmbedded, 101, "digest-next"); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := store.Acquire(ctx, "direct-stale", AdmittedByEmbedded, 101, "digest-stale"); !errors.Is(err, ErrTargetStale) {
		t.Fatalf("stale Acquire = %v, want ErrTargetStale", err)
	}
}

func TestStoreConcurrentAcquireKeepsOneOpenRow(t *testing.T) {
	rootDB := disposabledb.ResetDisposableSchema(t)
	defer rootDB.Close()

	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	left, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open left database: %v", err)
	}
	defer left.Close()
	right, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open right database: %v", err)
	}
	defer right.Close()

	type result struct {
		operationID string
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var group sync.WaitGroup
	for i, candidate := range []*sql.DB{left, right} {
		candidate := candidate
		operationID := fmt.Sprintf("direct-concurrent-%d", i)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- result{operationID: operationID, err: New(candidate).Acquire(context.Background(), operationID, AdmittedByEmbedded, int64(200+i), "digest-"+operationID)}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var winner string
	rejected := 0
	for result := range results {
		if result.err == nil {
			winner = result.operationID
			continue
		}
		if !errors.Is(result.err, ErrLocked) && !errors.Is(result.err, ErrTargetStale) {
			t.Fatalf("concurrent loser error = %v, want ErrLocked or ErrTargetStale", result.err)
		}
		rejected++
	}
	if winner == "" || rejected != 1 {
		t.Fatalf("concurrent acquire winner=%q rejected=%d, want one winner and one rejection", winner, rejected)
	}
	if err := New(rootDB).ReconcileExactCurrentState(context.Background(), winner, AdmittedByEmbedded, "digest-"+winner, winnerSequence(winner), "succeeded"); err != nil {
		t.Fatalf("release concurrent winner: %v", err)
	}
}

// TestRecoverExactRecordsEvidenceOnTheLockRow proves the folded fence audit:
// the operator recovery evidence and terminal state are stored on the closed
// lock row itself, and a replay with the same identity leaves them untouched.
func TestRecoverExactRecordsEvidenceOnTheLockRow(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := New(db)
	ctx := context.Background()
	const (
		operationID = "embedded-recover-evidence"
		digest      = "recover-evidence-digest"
		evidence    = "operator closed an orphaned fence after a crash"
	)
	if err := store.Acquire(ctx, operationID, AdmittedByEmbedded, 900, digest); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := store.RecoverExact(ctx, operationID, AdmittedByEmbedded, digest, "active", "succeeded", evidence); err != nil {
		t.Fatalf("recover: %v", err)
	}
	var (
		state, terminal, storedEvidence string
		recoveredAt, releasedAt         time.Time
	)
	if err := db.QueryRowContext(ctx, `
SELECT state, recovered_at, recovery_terminal_state, recovery_evidence, released_at
FROM embedded_update_locks WHERE op_id = $1`, operationID).
		Scan(&state, &recoveredAt, &terminal, &storedEvidence, &releasedAt); err != nil {
		t.Fatalf("read recovered lock: %v", err)
	}
	if state != "settled" || terminal != "succeeded" || storedEvidence != evidence {
		t.Fatalf("recovered lock = state %q terminal %q evidence %q", state, terminal, storedEvidence)
	}
	if recoveredAt.IsZero() || releasedAt.IsZero() {
		t.Fatalf("recovered_at/released_at not set: %v/%v", recoveredAt, releasedAt)
	}
	// The replay closes nothing new and leaves the recorded evidence intact.
	if err := store.RecoverExact(ctx, operationID, AdmittedByEmbedded, digest, "active", "succeeded", evidence); err != nil {
		t.Fatalf("replay recover: %v", err)
	}
	var replayEvidence string
	if err := db.QueryRowContext(ctx, `SELECT recovery_evidence FROM embedded_update_locks WHERE op_id = $1`, operationID).Scan(&replayEvidence); err != nil {
		t.Fatal(err)
	}
	if replayEvidence != evidence {
		t.Fatalf("replay rewrote evidence to %q", replayEvidence)
	}
	// A blank evidence payload never reaches SQL.
	if err := store.RecoverExact(ctx, operationID, AdmittedByEmbedded, digest, "active", "rolled_back", "   "); !errors.Is(err, ErrEvidenceMissing) {
		t.Fatalf("blank evidence = %v, want ErrEvidenceMissing", err)
	}
}

// winnerSequence is the release sequence the concurrent-acquire test assigned to
// the winning candidate.
func winnerSequence(winner string) int64 {
	if winner == "direct-concurrent-1" {
		return 201
	}
	return 200
}

// TestReconcileExactRejectsAMismatchedSequence proves the release-sequence floor
// is part of the close CAS: a row whose stored sequence disagrees with the
// journal evidence is never closed, and the row is left untouched.
func TestReconcileExactRejectsAMismatchedSequence(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := New(db)
	ctx := context.Background()
	const (
		operationID = "embedded-sequence-cas"
		digest      = "sequence-cas-digest"
		sequence    = 77
	)
	if err := store.Acquire(ctx, operationID, AdmittedByEmbedded, sequence, digest); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	openRow := func() (string, bool) {
		t.Helper()
		rows, err := store.ListOpen(ctx, AdmittedByEmbedded)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.OperationID == operationID {
				return row.State, true
			}
		}
		return "", false
	}

	// A wrong sequence is refused by the SQL CAS, not by an in-memory pre-check.
	if err := store.ReconcileExact(ctx, operationID, AdmittedByEmbedded, digest, sequence+1, "active", "failed"); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("close with a wrong sequence = %v, want ErrSequenceConflict", err)
	}
	if state, found := openRow(); !found || state != "active" {
		t.Fatalf("refused close changed the row: state=%q found=%v", state, found)
	}
	if err := store.ReconcileExactCurrentState(ctx, operationID, AdmittedByEmbedded, digest, sequence+1, "failed"); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("current-state close with a wrong sequence = %v, want ErrSequenceConflict", err)
	}
	if state, found := openRow(); !found || state != "active" {
		t.Fatalf("refused current-state close changed the row: state=%q found=%v", state, found)
	}
	// A missing expectation is refused before any statement runs.
	if err := store.ReconcileExact(ctx, operationID, AdmittedByEmbedded, digest, 0, "active", "failed"); !errors.Is(err, ErrSequenceMissing) {
		t.Fatalf("close without an expected sequence = %v, want ErrSequenceMissing", err)
	}
	if err := store.ReconcileExactCurrentState(ctx, operationID, AdmittedByEmbedded, digest, 0, "failed"); !errors.Is(err, ErrSequenceMissing) {
		t.Fatalf("current-state close without an expected sequence = %v, want ErrSequenceMissing", err)
	}
	if state, found := openRow(); !found || state != "active" {
		t.Fatalf("refused closes changed the row: state=%q found=%v", state, found)
	}

	// The exact sequence closes the row.
	if err := store.ReconcileExactCurrentState(ctx, operationID, AdmittedByEmbedded, digest, sequence, "failed"); err != nil {
		t.Fatalf("close with the exact sequence: %v", err)
	}
	if _, found := openRow(); found {
		t.Fatal("the exact close left the row open")
	}
	// A released row cannot be re-closed with a different sequence either.
	if err := store.ReconcileExact(ctx, operationID, AdmittedByEmbedded, digest, sequence+1, "active", "succeeded"); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("re-close with a wrong sequence = %v, want ErrSequenceConflict", err)
	}
}
