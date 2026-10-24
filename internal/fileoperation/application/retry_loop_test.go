package application

import (
	"context"
	"database/sql"
	"testing"
)

// makeFileOperationWorkItemDue moves a retried work item into the claimable
// window (store.Retry schedules it at now+retryAfter).
func makeFileOperationWorkItemDue(t *testing.T, db *sql.DB, workItemID string) {
	t.Helper()
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `
UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, workItemID); err != nil {
		t.Fatalf("make work item due: %v", err)
	}
}
