package application

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func fileOperationID(t *testing.T, db *sql.DB, artifactID string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM file_operations WHERE artifact_id = $1`, artifactID).Scan(&id); err != nil {
		t.Fatalf("read file operation id: %v", err)
	}
	return id
}

// TestTypedFileFailureOpensDiagnosticEpisode proves the visible fault signal of
// the typed lane through the real worker entry and the production
// observability writer: a terminal typed failure opens one diagnostic episode
// on the operation scope, a completed operation opens none. The Activity
// Journal entry for the file result is not part of the finish write set; it
// belongs to the independent activity consumer.
func TestTypedFileFailureOpensDiagnosticEpisode(t *testing.T) {
	episodes := func(t *testing.T, db *sql.DB) (open, total int) {
		t.Helper()
		if err := db.QueryRow(`
SELECT count(*) FILTER (WHERE succeeded_after_at IS NULL), count(*)
FROM diagnostic_episodes
WHERE kind = 'internal' AND target_id = 'fileoperation.execute'`).Scan(&open, &total); err != nil {
			t.Fatal(err)
		}
		return open, total
	}
	t.Run("failure", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		wireRealDiagnostics(t, x.db, x.s)
		if err := os.Remove(x.source); err != nil {
			t.Fatal(err)
		}
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed failure run: handled=%v err=%v", handled, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != "SOURCE_MISSING" {
			t.Fatalf("operation not failed: %+v", got)
		}
		if open, total := episodes(t, x.db); open != 1 || total != 1 {
			t.Fatalf("typed failure diagnostic episodes open=%d total=%d, want 1/1", open, total)
		}
	})
	t.Run("success", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		wireRealDiagnostics(t, x.db, x.s)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
		}
		assertOperationCompleted(t, x.db, x.opID)
		if _, total := episodes(t, x.db); total != 0 {
			t.Fatalf("completed operation opened %d diagnostic episodes", total)
		}
	})
}
