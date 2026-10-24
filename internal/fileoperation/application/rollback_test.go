package application

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// fileFailureInjection installs a PostgreSQL trigger that raises inside the
// file_execute fenced transaction at the stage named by the case. The pattern
// mirrors artifact/application/rollback_test.go: the trigger is the fault, and
// every owned row must be unchanged after the failed finish.
type fileFailureInjection struct {
	name  string
	table string
	event string
}

func installFileFailureTrigger(t *testing.T, db *sql.DB, injection fileFailureInjection) {
	t.Helper()
	trigger := fmt.Sprintf(`
CREATE FUNCTION reject_file_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected file operation write failure';
END;
$$;
CREATE TRIGGER reject_file_write
%s ON %s
FOR EACH ROW EXECUTE FUNCTION reject_file_write();`, injection.event, injection.table)
	if _, err := ownerTestDB(t).ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("install failure trigger on %s: %v", injection.table, err)
	}
}

// assertFileOperationRollbackClean verifies that a failed fenced write left
// no partial state: the operation is still pending with no target evidence,
// no activity rows exist, and the work item is still leased (never terminal).
func assertFileOperationRollbackClean(t *testing.T, db *sql.DB, artifactID string) {
	t.Helper()
	ctx := context.Background()

	// The operation id is generated at Prepare time; resolve it through the
	// artifact instead of assuming the artifact file id.
	var operationID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM file_operations WHERE artifact_id = $1`, artifactID).Scan(&operationID); err != nil {
		t.Fatalf("file operation row missing after rollback: %v", err)
	}

	var state string
	var version int64
	var targetFingerprint, observedAt, failureCode, failureReason sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT state, version, target_fingerprint, observed_at, failure_code, failure_reason
FROM file_operations WHERE id = $1`, operationID).Scan(&state, &version, &targetFingerprint, &observedAt, &failureCode, &failureReason); err != nil {
		t.Fatalf("file operation row missing after rollback: %v", err)
	}
	if state != ownerStatePending || version != 1 || targetFingerprint.Valid || observedAt.Valid || failureCode.Valid || failureReason.Valid {
		t.Fatalf("file operation owner escaped rollback: state=%q version=%d fingerprint=%v observed=%v failure=%v/%v",
			state, version, targetFingerprint.Valid, observedAt.Valid, failureCode.Valid, failureReason.Valid)
	}

	var terminalItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'file_operation' AND owner_id = $1
  AND state IN ('succeeded', 'failed')`, operationID).Scan(&terminalItems); err != nil {
		t.Fatal(err)
	}
	if terminalItems != 0 {
		t.Fatalf("work item escaped rollback: %d terminal rows", terminalItems)
	}
	var leasedItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_items
WHERE owner_type = 'file_operation' AND owner_id = $1 AND state = 'leased'`, operationID).Scan(&leasedItems); err != nil {
		t.Fatal(err)
	}
	if leasedItems != 1 {
		t.Fatalf("work item lease after rollback = %d, want exactly 1 (the original claim)", leasedItems)
	}

	for _, table := range []string{"subscription_log_entries", "placements"} {
		var rows int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", table, rows)
		}
	}
}
