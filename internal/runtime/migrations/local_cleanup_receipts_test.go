package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// TestLocalCleanupReplayIdempotency pins the cleanup replay record that
// replaced local_cleanup_receipts: it lives in
// idempotency_records(kind='local_cleanup', owner_type='managed_anime') and
// survives the deleted owner.
func TestLocalCleanupReplayIdempotency(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	retireAnime(t, db, "anime-replay")
	epoch := retireEpoch(t, db, "anime-replay")
	key := "cleanup-replay-key-0001"
	hash := "sha256:" + strings.Repeat("a", 64)

	first, err := runLocalCleanup(ctx, db, "anime-replay", key, hash, epoch)
	if err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if first["status"] != "performed" {
		t.Fatalf("first cleanup status = %v, want performed", first["status"])
	}
	var ownerRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id='anime-replay'`).Scan(&ownerRows); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 0 {
		t.Fatalf("cleanup left the owner row: %d", ownerRows)
	}
	var kind, ownerType string
	var storedTotal float64
	if err := db.QueryRowContext(ctx, `
SELECT kind, owner_type, (result_snapshot->>'deleted_total')::float8
FROM idempotency_records
WHERE kind='local_cleanup' AND owner_type='managed_anime' AND owner_id='anime-replay' AND idempotency_key=$1`, key).
		Scan(&kind, &ownerType, &storedTotal); err != nil {
		t.Fatalf("read replay record: %v", err)
	}
	if kind != "local_cleanup" || ownerType != "managed_anime" {
		t.Fatalf("replay record = %s/%s, want local_cleanup/managed_anime", kind, ownerType)
	}
	if storedTotal != first["deleted_total"] {
		t.Fatalf("stored total = %v, want %v", storedTotal, first["deleted_total"])
	}

	// Same key and hash replays the stored totals.
	replayed, err := runLocalCleanup(ctx, db, "anime-replay", key, hash, epoch)
	if err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	if replayed["status"] != "replayed" {
		t.Fatalf("replay status = %v, want replayed", replayed["status"])
	}
	if replayed["deleted_total"] != first["deleted_total"] {
		t.Fatalf("replay total = %v, want %v", replayed["deleted_total"], first["deleted_total"])
	}

	// Same key with a different hash fails closed.
	_, err = runLocalCleanup(ctx, db, "anime-replay", key, "sha256:"+strings.Repeat("b", 64), epoch)
	assertMessage(t, err, "IDEMPOTENCY_KEY_REUSED")

	// An unknown key on the cleaned id is a not-found, not a silent success.
	_, err = runLocalCleanup(ctx, db, "anime-replay", "cleanup-unknown-key-0001", hash, epoch)
	assertMessage(t, err, "LOCAL_CLEANUP_NOT_FOUND")
}

// TestManagedAnimeDeleteRequiresCleanupFlag pins the owner-row delete guard:
// a retired owner can only be removed inside the cleanup transaction that
// names it in the transaction-local flag.
func TestManagedAnimeDeleteRequiresCleanupFlag(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	retireAnime(t, db, "anime-delete-flag")
	assertMessage(t, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM managed_anime WHERE id='anime-delete-flag'`)
		return err
	}(), "MANAGED_ANIME_DELETE_FORBIDDEN")

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('bansoko.local_cleanup_owner', 'anime-delete-flag', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_anime WHERE id='anime-delete-flag'`); err != nil {
		t.Fatalf("flagged owner delete: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit flagged delete: %v", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id='anime-delete-flag'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("flagged delete left %d rows", rows)
	}
}

// TestLocalCleanupPlacementObligationFence pins the placement obligation state
// machine and its retirement boundary. The obligation fence is one of the kept
// effect entries and reads the same transaction-local cleanup flag.
func TestLocalCleanupPlacementObligationFence(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	exec := func(query string, args ...any) error {
		_, err := db.ExecContext(ctx, query, args...)
		return err
	}
	fit := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seedObligationChain := func(animeID, suffix string) (fileOperationID string) {
		t.Helper()
		fileOperationID = "fop-" + suffix
		seedCompletedFileOperation(t, db, animeID[len("anime-"):], fileOperationID)
		return fileOperationID
	}

	fo := seedObligationChain("anime-cleanup-obligation", "obl1")
	if err := exec(`INSERT INTO placement_obligations(file_operation_id, state, max_attempts) VALUES ($1, 'pending', 3)`, fo); err != nil {
		t.Fatalf("insert pending obligation: %v", err)
	}
	// The owner identity and start shape are constrained.
	assertMessage(t, func() error {
		return exec(`UPDATE placement_obligations SET file_operation_id = 'other' WHERE file_operation_id = $1`, fo)
	}(), "PLACEMENT_OBLIGATION_OWNER_IMMUTABLE")
	assertMessage(t, func() error {
		_, err := db.ExecContext(ctx, `INSERT INTO placement_obligations(file_operation_id, state, attempts, max_attempts, settled_at) VALUES ($1, 'admitted', 1, 3, CURRENT_TIMESTAMP)`, "fop-admitted-start")
		return err
	}(), "PLACEMENT_OBLIGATION_START_INVALID")

	// admitted is sticky; the explicit failed -> pending re-open is allowed.
	fit(`UPDATE placement_obligations SET state='admitted', attempts=1, settled_at=CURRENT_TIMESTAMP WHERE file_operation_id=$1`, fo)
	assertMessage(t, func() error {
		return exec(`UPDATE placement_obligations SET state='pending', settled_at=NULL WHERE file_operation_id=$1`, fo)
	}(), "PLACEMENT_OBLIGATION_ADMITTED_STICKY")

	// Retire: the owner fence now refuses every write and delete.
	if err := exec(`UPDATE managed_anime SET lifecycle_state='retired', fence_epoch=fence_epoch+1,
retired_at=CURRENT_TIMESTAMP, retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-cleanup-obligation'`); err != nil {
		t.Fatal(err)
	}
	assertMessage(t, func() error {
		return exec(`DELETE FROM placement_obligations WHERE file_operation_id=$1`, fo)
	}(), "MANAGED_ANIME_RETIRED")

	// The cleanup transaction may delete it under the transaction-local flag.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('bansoko.local_cleanup_owner', 'anime-cleanup-obligation', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM placement_obligations WHERE file_operation_id=$1`, fo); err != nil {
		t.Fatalf("authorized obligation delete: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit authorized obligation delete: %v", err)
	}
}

func runLocalCleanup(ctx context.Context, db *sql.DB, animeID, key, hash string, epoch int64) (map[string]any, error) {
	var raw string
	if err := db.QueryRowContext(ctx, `
SELECT cleanup_managed_anime_local_data($1, $2, $3, $4)::text`, animeID, key, hash, epoch).Scan(&raw); err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func retireAnime(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ($1, 'Retired')`, animeID); err != nil {
		t.Fatalf("seed %s: %v", animeID, err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id=$1 AND lifecycle_state='active'`, animeID); err != nil {
		t.Fatalf("retire %s: %v", animeID, err)
	}
}

func retireEpoch(t *testing.T, db *sql.DB, animeID string) int64 {
	t.Helper()
	var epoch int64
	if err := db.QueryRowContext(context.Background(), `SELECT fence_epoch FROM managed_anime WHERE id=$1`, animeID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}
