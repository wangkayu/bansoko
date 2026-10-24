package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/runtime/database"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/disposabledb/guard"
	"bansoko/internal/worktree"
)

// realChainOwnerDB opens the schema-owner handle (BANSOKO_TEST_DATABASE_URL,
// through the disposable guard) for fixture writes that the runtime role must
// not be able to perform. It is opened per call and closed by the caller: every
// disposable reset drops and re-clones the test database WITH (FORCE), which
// terminates the connections of any handle kept across tests (57P01).
func realChainOwnerDB(ctx context.Context) (*sql.DB, error) {
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if err := guard.Check(ctx, dsn, guard.Options{RequireDisposable: true}); err != nil {
		return nil, err
	}
	return database.Open(ctx, database.Config{DSN: dsn})
}

// advanceQBSyncPollClock runs the qB poll cadence on a test clock. Production
// freezes a fixed 60s poll interval into every DownloadTask (LimitsV1
// sync.poll_interval_seconds, minimum 10s), so the first qb_sync after a submit
// and every later poll wait a full interval. The real chain must still talk to
// a real qB and a real filesystem, but it must not sleep a minute per poll: a
// qb_sync that is only waiting for its interval is made due now, and the task's
// monotone budget clock is moved to that poll boundary (never past it), the
// same arrangement the download package's typed-sync tests use. Nothing else
// about the poll (ticket, grant, provider read, settlement) is bypassed.
func advanceQBSyncPollClock(ctx context.Context, _ *sql.DB) error {
	// The test clock is fixture arrangement, not application code: it runs as
	// the schema owner, like every other owner-only fixture write, so the
	// application handle keeps the real runtime privilege boundary.
	db, err := realChainOwnerDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
UPDATE download_tasks task
SET budget_clock_at = greatest(task.budget_clock_at,
    (task.active_poll_ticket->>'started_at')::timestamptz + task.poll_interval_seconds * INTERVAL '1 second')
WHERE task.active_poll_ticket IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM work_items work
      WHERE work.owner_type = 'download_task' AND work.owner_id = task.id
        AND work.kind = 'qb_sync' AND work.state = 'available'
        AND work.available_at > CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("advance qB poll budget clock: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE work_items
SET available_at = CURRENT_TIMESTAMP
WHERE owner_type = 'download_task' AND kind = 'qb_sync' AND state = 'available'
  AND available_at > CURRENT_TIMESTAMP`); err != nil {
		return fmt.Errorf("make qB poll due: %w", err)
	}
	return nil
}

// scanTreeTop derives one work's scan scope the way the file-maintenance
// scheduler does: the deepest directory holding the files its own active
// placements put on disk. A work with no active placement evidence has no tree
// top, and the fixture must not invent one.
func scanTreeTop(t *testing.T, ctx context.Context, db *sql.DB, animeID string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	top, ok := worktree.Top(paths)
	if !ok {
		t.Fatalf("anime %s has no active placement evidence to scope a scan to", animeID)
	}
	return top
}

// configureRealChainNaming stores the naming rules the way an operator does: a
// naming_rules section whose directory templates are absolute under the test
// root. Naming targets are absolute by construction, so the fixture templates
// name the test root explicitly instead of relying on a global anchor; the code
// defaults point at the deployment mount /media.
func configureRealChainNaming(t *testing.T, ctx context.Context, db *sql.DB, root string) {
	t.Helper()
	storeRealChainNamingRules(t, ctx, db, root, rootNamingRules(root))
}

// storeRealChainNamingRules stores rules as the naming_rules section. root only
// labels the failure message; rules already carry their absolute directories.
func storeRealChainNamingRules(t *testing.T, ctx context.Context, db *sql.DB, root string, rules naming.NamingRules) {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("marshal naming rules: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO global_config_sections (section_name, schema_version, value, version)
VALUES ('naming_rules', 'naming-rules-v1', $1, 1)
ON CONFLICT (section_name) DO UPDATE
SET value = EXCLUDED.value, version = global_config_sections.version + 1, updated_at = CURRENT_TIMESTAMP`, raw); err != nil {
		t.Fatalf("configure naming_rules under %q: %v", root, err)
	}
}

// rootNamingRules returns the default naming rules with every directory template
// named under root. Naming targets are absolute by construction — the render,
// not a global anchor, decides where a file lands — so a fixture that executes
// the real file effect must write the directory into the template; otherwise the
// code default would target the deployment mount /media.
func rootNamingRules(root string) naming.NamingRules {
	prefix := filepath.ToSlash(strings.TrimRight(root, "/"))
	absolute := func(template string) string {
		return strings.Replace(template, "/media", prefix, 1)
	}
	rules := naming.DefaultNamingRules()
	rules.TV.DirectoryTemplate = absolute(rules.TV.DirectoryTemplate)
	rules.Movie.DirectoryTemplate = absolute(rules.Movie.DirectoryTemplate)
	rules.Unknown.DirectoryTemplate = absolute(rules.Unknown.DirectoryTemplate)
	return rules
}

// realChainObservedYear is the owner year the real-chain fixtures set. The
// shipped default directory templates name `${anime.year}`, which reads only
// the owner-edited year (anime_metadata.year) — never a provider year or a guess
// taken from the release title or a file name. The fixture's own release titles
// carry other years precisely so the assertions can tell the two apart.
const realChainObservedYear = 2025

// observeBoundWorkYear sets the owner year of the anime. It must run before the
// release is admitted: acceptance freezes it into the Vars input, which is the
// only thing `anime.year` reads.
func observeBoundWorkYear(t *testing.T, ctx context.Context, db *sql.DB, animeID string, year int) {
	t.Helper()
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `
UPDATE anime_metadata SET year = $2 WHERE managed_anime_id = $1`, animeID, year); err != nil {
		t.Fatalf("set the owner year of %s: %v", animeID, err)
	}
}
