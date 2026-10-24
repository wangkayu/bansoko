package migrations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/runtime/database"
)

// runtimeRoleTestName is the fixed login role this test converges through
// database.ProvisionRuntimeRole. It is granted only the bansoko_runtime group
// boundary; the test then proves the boundary is real.
const runtimeRoleTestName = "bansoko_test_runtime_role"

// TestRuntimeRoleBoundaryAndCleanupProcedure is the runtime-role acceptance:
// provision a real login role, connect with it, and assert the whole privilege
// model. The owner applies migrations; the runtime can only EXECUTE the
// owner-held cleanup procedure and cannot mint a receipt, delete a retired
// owner, disable a trigger, set session_replication_role or run DDL. After the
// procedure removes the owner, the durable receipt tombstones the id for every
// later writer.
func TestRuntimeRoleBoundaryAndCleanupProcedure(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()

	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{
		Role:     runtimeRoleTestName,
		Password: password,
	}); err != nil {
		t.Fatalf("provision runtime role: %v", err)
	}
	// Re-running provisioning converges to the same state (role upgrade
	// idempotency) with a fresh password; the second value is the active one.
	password = randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{
		Role:     runtimeRoleTestName,
		Password: password,
	}); err != nil {
		t.Fatalf("re-provision runtime role must be idempotent: %v", err)
	}

	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()

	// The runtime role is accepted as separated; the owner connection is not.
	if err := database.AssertRuntimeRoleSeparated(ctx, runtime); err != nil {
		t.Fatalf("runtime role must satisfy the separation assertion: %v", err)
	}
	if err := database.AssertRuntimeRoleSeparated(ctx, owner); err == nil {
		t.Fatal("the schema owner connection must fail the runtime separation assertion")
	}

	seedRetiredOwner(t, owner, "anime-runtime-role")

	// Direct owner-table and trigger manipulation stays revoked at the privilege
	// level, not just fenced.
	for _, statement := range []string{
		`ALTER TABLE work_items DISABLE TRIGGER work_items_retirement_fence`,
		`SET session_replication_role = 'replica'`,
		`CREATE TABLE runtime_boundary_probe(id int)`,
		`ALTER TABLE work_items ADD COLUMN runtime_boundary_probe int`,
		`DROP TABLE idempotency_records`,
	} {
		if _, err := runtime.ExecContext(ctx, statement); err == nil {
			t.Fatalf("runtime role must be denied: %s", statement)
		} else if !isInsufficientPrivilege(err) {
			t.Fatalf("runtime statement %q: error = %v, want insufficient_privilege", statement, err)
		}
	}

	// A directly deleted retired owner is still fenced.
	// A directly deleted retired owner is rejected without the cleanup flag.
	assertRuntimeFenceMessage(t, runtime,
		`DELETE FROM managed_anime WHERE id = 'anime-runtime-role'`,
		"MANAGED_ANIME_DELETE_FORBIDDEN")

	// The one allowed capability: the owner-held SECURITY DEFINER procedure.
	requestHash := cleanupRequestHash("anime-runtime-role", 2)
	var result string
	if err := runtime.QueryRowContext(ctx, `
SELECT cleanup_managed_anime_local_data('anime-runtime-role', 'runtime-cleanup-0001', $1, 2)::text`,
		requestHash).Scan(&result); err != nil {
		t.Fatalf("runtime must be able to execute the cleanup procedure: %v", err)
	}
	if !strings.Contains(result, `"status": "performed"`) && !strings.Contains(result, `"status":"performed"`) {
		t.Fatalf("cleanup result = %s, want performed", result)
	}

	// The replay record survives the removed owner.
	var receipts int
	if err := runtime.QueryRowContext(ctx, `
SELECT count(*) FROM idempotency_records WHERE kind = 'local_cleanup' AND owner_type = 'managed_anime' AND owner_id = 'anime-runtime-role'`).Scan(&receipts); err != nil {
		t.Fatalf("runtime must be able to read the receipt: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("cleanup receipt count = %d, want 1", receipts)
	}
	var animeRows int
	if err := runtime.QueryRowContext(ctx, `
SELECT count(*) FROM managed_anime WHERE id = 'anime-runtime-role'`).Scan(&animeRows); err != nil {
		t.Fatal(err)
	}
	if animeRows != 0 {
		t.Fatalf("managed anime rows = %d, want 0", animeRows)
	}

	// After cleanup the owner row is gone, so a late child insert fails on the
	// foreign key instead of a row fence.
	assertForeignKeyViolation(t, runtime,
		`INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-runtime-role')`)
	if _, err := runtime.ExecContext(ctx,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		 VALUES ('sub-late', 'anime-runtime-role', 'custom', 'late', 'active')`); err == nil {
		t.Fatal("a late subscription insert for a cleaned owner must be rejected")
	}
	// WorkItem rows are written only through owner-held functions; the runtime
	// role has no direct INSERT privilege on the table.
	if _, err := runtime.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-late', 'file_maintenance', 'anime-runtime-role', 'replacement_evaluate', 'available', 'repl:late', 3, '{}', 1)`); !isInsufficientPrivilege(err) {
		t.Fatalf("runtime WorkItem insert = %v, want insufficient_privilege", err)
	}

	// Idempotent replay of the same request returns the original success.
	var replayed string
	if err := runtime.QueryRowContext(ctx, `
SELECT cleanup_managed_anime_local_data('anime-runtime-role', 'runtime-cleanup-0001', $1, 2)::text`,
		requestHash).Scan(&replayed); err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	if !strings.Contains(replayed, `"replayed"`) {
		t.Fatalf("replay result = %s, want replayed", replayed)
	}
	// Same key with a different request fails closed.
	assertRuntimeFenceMessage(t, runtime, `
SELECT cleanup_managed_anime_local_data('anime-runtime-role', 'runtime-cleanup-0001', 'sha256:`+strings.Repeat("c", 64)+`', 2)`,
		"IDEMPOTENCY_KEY_REUSED")
}

func randomTestPassword(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random password: %v", err)
	}
	return hex.EncodeToString(buf)
}

// openRuntimeConnection rewrites the disposable owner DSN onto the runtime
// login role. It is only used inside the test process; the password never
// reaches a log or an evidence file.
func openRuntimeConnection(t *testing.T, password string) *sql.DB {
	t.Helper()
	ownerDSN := strings.TrimSpace(os.Getenv("BANSOKO_TEST_DATABASE_URL"))
	parsed, err := url.Parse(ownerDSN)
	if err != nil {
		t.Fatalf("parse disposable DSN: %v", err)
	}
	parsed.User = url.UserPassword(runtimeRoleTestName, password)
	dsn := parsed.String()
	db, err := database.Open(context.Background(), database.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open runtime connection: %v", err)
	}
	return db
}

// seedRetiredOwner inserts a minimal active owner graph and retires it with the
// next lifecycle epoch, so the runtime cleanup procedure has a legal subject.
func seedRetiredOwner(t *testing.T, owner *sql.DB, animeID string) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('` + animeID + `', 'Runtime role')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		 VALUES ('sub-` + animeID + `', '` + animeID + `', 'custom', 'Runtime role', 'active')`,
		`INSERT INTO anime_metadata(managed_anime_id) VALUES ('` + animeID + `')`,
		`UPDATE managed_anime
		 SET lifecycle_state = 'retired',
		     fence_epoch = fence_epoch + 1,
		     retired_at = CURRENT_TIMESTAMP,
		     retired_subscription_version = 1,
		     lifecycle_version = lifecycle_version + 1
		 WHERE id = '` + animeID + `'`,
	} {
		if _, err := owner.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed retired owner: %v", err)
		}
	}
}

func cleanupRequestHash(animeID string, epoch int64) string {
	// The procedure only requires a bounded sha256-shaped string for the replay
	// comparison; this test controls both sides of that comparison.
	return "sha256:" + strings.Repeat("d", 64)
}

func isInsufficientPrivilege(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func assertRuntimeFenceMessage(t *testing.T, db *sql.DB, statement, want string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), statement)
	if err == nil {
		t.Fatalf("expected %s, got success for %s", want, statement)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Message != want {
		t.Fatalf("error = %v, want PostgreSQL message %s", err, want)
	}
}

func assertForeignKeyViolation(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), statement)
	if err == nil {
		t.Fatalf("expected a foreign key violation, got success for %s", statement)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("error = %v, want SQLSTATE 23503", err)
	}
}
