package migrations

import (
	"context"
	"testing"
)

// TestPlacementObligations proves the durable post-completion
// placement obligation shape: it is bound to exactly one
// completed file operation, only a terminal failure keeps an explanation, and
// the admitted success state is timestamped exactly once. The table is the
// independent stage state of the placement handoff, so it is deliberately
// writable (unlike the append-only evidence tables) while manual retry re-opens
// a failed row in place.
func TestPlacementObligations(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedCompletedFileOperation(t, db, "obligation", "op-obligation")

	// 1. A pending obligation needs nothing but its owner and defaults to a
	// zero-attempt pending state with the bounded retry budget.
	if _, err := db.ExecContext(ctx, `
INSERT INTO placement_obligations(file_operation_id) VALUES ('op-obligation')`); err != nil {
		t.Fatalf("insert pending obligation: %v", err)
	}
	var state string
	var attempts, maxAttempts int
	if err := db.QueryRowContext(ctx, `
SELECT state, attempts, max_attempts FROM placement_obligations WHERE file_operation_id = 'op-obligation'`).
		Scan(&state, &attempts, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 0 || maxAttempts != 3 {
		t.Fatalf("pending obligation = %s/%d/%d, want pending/0/3", state, attempts, maxAttempts)
	}

	// 2. The obligation is bound to one real file operation.
	_, err := db.ExecContext(ctx, `
INSERT INTO placement_obligations(file_operation_id) VALUES ('op-missing')`)
	expectPgErrorCode(t, err, "23503")

	// 3. One obligation per file operation (the durability handoff is not
	// duplicated by a replayed completion).
	_, err = db.ExecContext(ctx, `
INSERT INTO placement_obligations(file_operation_id) VALUES ('op-obligation')`)
	expectPgErrorCode(t, err, "23505")

	// 4. Only a terminal failure keeps an explanation.
	_, err = db.ExecContext(ctx, `
UPDATE placement_obligations SET reason_code = 'placement_admission_failed' WHERE file_operation_id = 'op-obligation'`)
	expectPgErrorCode(t, err, "23514")

	// 5. A transient stage failure records waiting_retry with its stable
	// reason and an attempt.
	if _, err := db.ExecContext(ctx, `
UPDATE placement_obligations
SET state = 'waiting_retry', attempts = 1, reason_code = 'placement_admission_failed', reason = 'placement owner rejected the admission'
WHERE file_operation_id = 'op-obligation'`); err != nil {
		t.Fatalf("record waiting_retry: %v", err)
	}

	// 6. A terminal failure keeps its explanation and clears the settlement.
	if _, err := db.ExecContext(ctx, `
UPDATE placement_obligations
SET state = 'failed', attempts = max_attempts, reason_code = 'placement_admission_failed',
    reason = 'attempt budget exhausted', settled_at = NULL
WHERE file_operation_id = 'op-obligation'`); err != nil {
		t.Fatalf("fail obligation: %v", err)
	}

	// 7. Manual retry re-opens a failed obligation in place, leaving the
	// completed file operation untouched.
	if _, err := db.ExecContext(ctx, `
UPDATE placement_obligations
SET state = 'pending', attempts = 0, reason_code = NULL, reason = NULL, settled_at = NULL
WHERE file_operation_id = 'op-obligation'`); err != nil {
		t.Fatalf("reopen obligation: %v", err)
	}
	var operationState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM file_operations WHERE id = 'op-obligation'`).Scan(&operationState); err != nil {
		t.Fatal(err)
	}
	if operationState != "completed" {
		t.Fatalf("file operation must stay completed across obligation re-open, got %q", operationState)
	}

	// 8. admitted is the settled success state and must be timestamped with the
	// reason cleared.
	_, err = db.ExecContext(ctx, `
UPDATE placement_obligations SET state = 'admitted' WHERE file_operation_id = 'op-obligation'`)
	expectPgErrorCode(t, err, "23514")
	if _, err := db.ExecContext(ctx, `
UPDATE placement_obligations
SET state = 'admitted', reason_code = NULL, reason = NULL, settled_at = CURRENT_TIMESTAMP
WHERE file_operation_id = 'op-obligation'`); err != nil {
		t.Fatalf("settle admitted: %v", err)
	}

	// 9. A replayed/late transition can never reopen an admitted obligation:
	// the settled success is sticky and a settled row cannot silently lose its
	// settlement timestamp.
	_, err = db.ExecContext(ctx, `
UPDATE placement_obligations SET settled_at = NULL WHERE file_operation_id = 'op-obligation'`)
	expectPgErrorCode(t, err, "23514")
	_, err = db.ExecContext(ctx, `
UPDATE placement_obligations SET state = 'failed' WHERE file_operation_id = 'op-obligation'`)
	expectPgErrorCode(t, err, "23514")

	// 10. Unknown states and an over-budget attempt count are rejected.
	_, err = db.ExecContext(ctx, `
UPDATE placement_obligations SET state = 'unknown' WHERE file_operation_id = 'op-obligation'`)
	expectPgErrorCode(t, err, "23514")
	_, err = db.ExecContext(ctx, `
UPDATE placement_obligations SET attempts = max_attempts + 1 WHERE file_operation_id = 'op-obligation'`)
	expectPgErrorCode(t, err, "23514")
}
