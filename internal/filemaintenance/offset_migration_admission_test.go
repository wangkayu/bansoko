package filemaintenance

import (
	"context"
	"strings"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func TestOffsetMigrationAdmissionRSSSource(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-admission', 'Admission')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-admission', 'anime-admission', 'custom', 'Admission', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-admission', 'sub-admission', 'custom_rss', 'https://example.com/rss', 'https://example.com/rss', 'Group', 'Admission')`); err != nil {
		t.Fatal(err)
	}

	service := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, created, err := service.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-admission", "source-admission", 2, 3, -1)
	if err != nil || !created || sourceID == "" || len(sourceID) > 128 {
		t.Fatalf("source admission id=%q created=%v err=%v", sourceID, created, err)
	}
	if noOpID, noOpCreated, err := service.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-admission", "source-admission", 3, 4, 4); err != nil || noOpCreated || noOpID != "" {
		t.Fatalf("unchanged admission id=%q created=%v err=%v", noOpID, noOpCreated, err)
	}
	if _, _, err := service.AdmitRSSSourceTx(ctx, txscope.Tx{}, "anime-admission", "source-admission", 3, 4, 5); err == nil || !strings.Contains(err.Error(), "transaction is nil") {
		t.Fatalf("invalid source scope error=%v", err)
	}
	if _, _, err := service.AdmitRSSSourceTx(ctx, txscope.Tx{}, "anime-admission", "source-admission", 3, 4, 4); err == nil || !strings.Contains(err.Error(), "transaction is nil") {
		t.Fatalf("invalid unchanged source scope error=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayID, created, err := service.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-admission", "source-admission", 2, 3, -1)
	if err != nil || created || replayID != sourceID {
		t.Fatalf("replay id=%q created=%v err=%v", replayID, created, err)
	}
	if _, _, err := service.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-admission", "source-admission", 2, 1, -1); err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM offset_migration_generations WHERE state = 'pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending generations=%d, want 1", pending)
	}
}

// Offsets are owned by RSS sources only: the schema refuses any other
// generation scope.
func TestOffsetMigrationScopeIsRSSSourceOnly(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-scope', 'Scope')`); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "completed"} {
		_, err := db.ExecContext(ctx, `
INSERT INTO offset_migration_generations(id, managed_anime_id, scope_kind, scope_owner_id, policy_version, old_offset, new_offset, state, completed_at)
VALUES ($1, 'anime-scope', 'instance', 'anime-scope', 2, 0, 1, $2, CASE WHEN $2 = 'completed' THEN CURRENT_TIMESTAMP END)`, "offgen-instance-"+state, state)
		if err == nil || !strings.Contains(err.Error(), "offset_migration_generations_scope_kind_check") {
			t.Fatalf("%s instance generation error=%v, want the scope kind check", state, err)
		}
	}
}
