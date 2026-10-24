package identity

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/disposabledb"
)

// EnsureInstallID requires a migrated schema (instance_bootstrap row exists)
// and is stable across repeated calls. The schema is reset to the pristine
// baseline through the shared disposable guard (fail-closed, never skipped).

func openIdentityTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

func TestEnsureInstallIDIsStable(t *testing.T) {
	db := openIdentityTestDB(t)
	ctx := context.Background()

	first, err := EnsureInstallID(ctx, db)
	if err != nil {
		t.Fatalf("first EnsureInstallID: %v", err)
	}
	if first == "" || first == pendingInstallID {
		t.Fatalf("install id = %q, want a real UUID", first)
	}
	second, err := EnsureInstallID(ctx, db)
	if err != nil {
		t.Fatalf("second EnsureInstallID: %v", err)
	}
	if second != first {
		t.Fatalf("install id changed across calls: %q -> %q", first, second)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT install_id FROM instance_bootstrap WHERE singleton_id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read persisted install id: %v", err)
	}
	if stored != first {
		t.Fatalf("persisted install id = %q, want %q", stored, first)
	}
}
