package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

// createSourceOwner builds the minimal owner chain for one RSS source runtime.
func createSourceOwner(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Title')`, "anime-"+id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ($1, $2, 'custom', 'WorkItem test', 'active')`, "sub-"+id, "anime-"+id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', 'https://feeds.example.com/rss.xml', 'https://feeds.example.com/rss.xml', 'Test Group', 'Title')`, id, "sub-"+id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func newTestStore(t *testing.T, db *sql.DB) *Store {
	t.Helper()
	store, err := NewStore(db, 0, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func createRefreshItem(t *testing.T, store *Store, sourceID, effectKey string, maxAttempts int) string {
	t.Helper()
	ctx := context.Background()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := store.Create(ctx, tx, WorkItemSpec{
		Owner:                OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID},
		Kind:                 KindRSSRefresh,
		IdempotencyKey:       effectKey,
		MaxAttempts:          maxAttempts,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatalf("create work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

// runtimeOwnerWriter transitions the real RSSSourceRuntime owner row with CAS.
func runtimeOwnerWriter(sourceID string, expectedVersion int64) OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		health := "ready"
		switch result.State {
		case "waiting_retry":
			health = "degraded"
		case StateFailed:
			health = "failed"
		}
		var version int64
		err := tx.QueryRowContext(ctx, `
UPDATE rss_source_runtime
SET health = $2, reason_code = NULLIF($3, ''), reason_detail = NULLIF($4, ''),
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE rss_source_id = $1 AND version = $5
RETURNING version`, sourceID, health, result.ReasonCode, result.Reason, expectedVersion).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("owner version conflict for %s", sourceID)
		}
		return version, err
	}
}

func runtimeOwnerReader(sourceID string) OwnerReader {
	return func(ctx context.Context, scope txscope.Tx) (int64, error) {
		tx := scope.SQLTx()
		var version int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version); err != nil {
			return 0, err
		}
		return version, nil
	}
}

type workItemRow struct {
	state                string
	fencingToken         int64
	attempt              int
	leaseUntil           *time.Time
	availableAt          time.Time
	lastErrorCode        *string
	expectedOwnerVersion int64
	terminalAt           *time.Time
	retentionUntil       *time.Time
	idempotencyKey       string
}

func readWorkItem(t *testing.T, db *sql.DB, id string) workItemRow {
	t.Helper()
	var row workItemRow
	err := db.QueryRowContext(context.Background(), `
SELECT state, fencing_token, attempt, lease_until, available_at, last_error_code, expected_owner_version, terminal_at, retention_until, idempotency_key
FROM work_item_history WHERE id = $1`, id).Scan(
		&row.state, &row.fencingToken, &row.attempt, &row.leaseUntil, &row.availableAt,
		&row.lastErrorCode, &row.expectedOwnerVersion, &row.terminalAt, &row.retentionUntil, &row.idempotencyKey)
	if err != nil {
		t.Fatalf("read work item %s: %v", id, err)
	}
	return row
}

func readRuntimeVersion(t *testing.T, db *sql.DB, sourceID string) (int64, string) {
	t.Helper()
	var version int64
	var health string
	if err := db.QueryRowContext(context.Background(), `SELECT version, health FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version, &health); err != nil {
		t.Fatal(err)
	}
	return version, health
}

func TestCreateValidation(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	defer tx.Rollback()

	valid := WorkItemSpec{
		Owner:                OwnerRef{Type: OwnerRSSSourceRuntime, ID: "src"},
		Kind:                 KindRSSRefresh,
		IdempotencyKey:       "effect",
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	}
	cases := []func(WorkItemSpec) WorkItemSpec{
		func(s WorkItemSpec) WorkItemSpec { s.Owner.Type = ""; return s },
		func(s WorkItemSpec) WorkItemSpec { s.Owner.Type = "plan"; return s },
		func(s WorkItemSpec) WorkItemSpec { s.Kind = "qb_submit"; return s },
		func(s WorkItemSpec) WorkItemSpec { s.Kind = "generic_command"; return s },
		func(s WorkItemSpec) WorkItemSpec { s.IdempotencyKey = " "; return s },
		func(s WorkItemSpec) WorkItemSpec { s.MaxAttempts = 0; return s },
		func(s WorkItemSpec) WorkItemSpec { s.ExpectedOwnerVersion = 0; return s },
		func(s WorkItemSpec) WorkItemSpec { s.InputSnapshot = json.RawMessage(`[1]`); return s },
	}
	for i, mutate := range cases {
		if _, err := store.Create(ctx, tx, mutate(valid)); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("case %d: expected ErrInvalidSpec, got %v", i, err)
		}
	}
	if _, err := NewStore(db, 31, nil); !errors.Is(err, ErrRetentionInvalid) {
		t.Fatalf("expected retention bound rejection, got %v", err)
	}
}

func TestLiveUniqueness(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-live")
	first := createRefreshItem(t, store, sourceID, "effect-1", 3)

	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	_, err := store.Create(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID}, Kind: KindRSSRefresh,
		IdempotencyKey: "effect-2", MaxAttempts: 3, ExpectedOwnerVersion: 1,
	})
	if !errors.Is(err, ErrLiveItemConflict) {
		t.Fatalf("expected live conflict, got %v", err)
	}
	tx.Rollback()

	// Terminal rows do not block a new live item.
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
SELECT public.work_item_close(id,'failed') FROM work_items
WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	second := createRefreshItem(t, store, sourceID, "effect-1", 3)
	if second == first {
		t.Fatal("new live item must have a new ID")
	}
}

func TestLiveConflictDoesNotAbortCallerTransaction(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-live-commit")
	first := createRefreshItem(t, store, sourceID, "effect-commit-1", 3)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rss_source_runtime SET reason_code = 'rediscovery_conflict' WHERE rss_source_id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID}, Kind: KindRSSRefresh,
		IdempotencyKey: "effect-commit-2", MaxAttempts: 3, ExpectedOwnerVersion: 1,
	})
	if !errors.Is(err, ErrLiveItemConflict) {
		t.Fatalf("expected live conflict, got %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("conflict must not abort caller transaction: %v", err)
	}

	var reason string
	if err := db.QueryRowContext(ctx, `SELECT reason_code FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "rediscovery_conflict" {
		t.Fatalf("caller transaction write was lost, reason=%q", reason)
	}
	var liveCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE owner_type = $1 AND owner_id = $2 AND kind = $3 AND state IN ('available', 'leased')`, OwnerRSSSourceRuntime, sourceID, KindRSSRefresh).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 1 {
		t.Fatalf("live work item count=%d, want 1 (first=%s)", liveCount, first)
	}
}

func TestCreateOrMergeRefreshesLiveAndFencesLeased(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()

	// available item: merge refreshes version/input and makes it immediately due.
	srcAvailable := createSourceOwner(t, db, "merge-avail")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstID, created, err := store.CreateOrMerge(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: srcAvailable}, Kind: KindRSSRefresh,
		IdempotencyKey: "merge-effect-avail", MaxAttempts: 3, ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatalf("first create-or-merge: %v", err)
	}
	if !created || firstID == "" {
		t.Fatalf("first create-or-merge must create, got id=%q created=%v", firstID, created)
	}
	sameID, created, err := store.CreateOrMerge(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: srcAvailable}, Kind: KindRSSRefresh,
		IdempotencyKey: "merge-effect-avail", MaxAttempts: 5, InputSnapshot: json.RawMessage(`{"trigger":"rules_patch"}`),
		ExpectedOwnerVersion: 2,
	})
	if err != nil {
		t.Fatalf("merge create-or-merge: %v", err)
	}
	if created || sameID != firstID {
		t.Fatalf("merge must reuse the live item, got id=%q created=%v", sameID, created)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	merged := readWorkItem(t, db, firstID)
	if merged.state != StateAvailable || merged.expectedOwnerVersion != 2 || merged.idempotencyKey != "merge-effect-avail" {
		t.Fatalf("available merge must refresh version and keep effect key: %+v", merged)
	}
	if !merged.availableAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("available merge must make the item immediately due: %+v", merged)
	}

	// A different idempotency key for the same owner/kind is a live conflict.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateOrMerge(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: srcAvailable}, Kind: KindRSSRefresh,
		IdempotencyKey: "merge-effect-other", MaxAttempts: 3, ExpectedOwnerVersion: 2,
	}); !errors.Is(err, ErrLiveItemConflict) {
		t.Fatalf("different effect key must conflict, got %v", err)
	}
	tx.Rollback()

	// Retire the available item so ClaimDue below targets only the leased item.
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
SELECT public.work_item_close(id,'failed') FROM work_items
WHERE id = $1`, firstID); err != nil {
		t.Fatal(err)
	}

	// leased item: merge expires the lease so the old claim is fenced out and
	// the next reclaim sees the refreshed inputs.
	srcLeased := createSourceOwner(t, db, "merge-leased")
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	leasedID, _, err := store.CreateOrMerge(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: srcLeased}, Kind: KindRSSRefresh,
		IdempotencyKey: "merge-effect-leased", MaxAttempts: 3, ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatalf("create leased item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "worker-old", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim leased item: %v", err)
	}
	if claim.ID != leasedID {
		t.Fatalf("claim id = %q, want %q", claim.ID, leasedID)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, created, err = store.CreateOrMerge(ctx, tx, WorkItemSpec{
		Owner: OwnerRef{Type: OwnerRSSSourceRuntime, ID: srcLeased}, Kind: KindRSSRefresh,
		IdempotencyKey: "merge-effect-leased", MaxAttempts: 3, InputSnapshot: json.RawMessage(`{"trigger":"rules_patch"}`),
		ExpectedOwnerVersion: 7,
	})
	if err != nil {
		t.Fatalf("merge leased item: %v", err)
	}
	if created {
		t.Fatal("merge of a leased item must not create a second live row")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	leasedRow := readWorkItem(t, db, leasedID)
	if leasedRow.state != StateLeased || leasedRow.leaseUntil == nil || leasedRow.expectedOwnerVersion != 7 {
		t.Fatalf("leased merge must refresh inputs and expire the lease: %+v", leasedRow)
	}
	// Old claim cannot finish; reclaim observes the refreshed owner version.
	if err := store.Finish(ctx, claim, StateSucceeded, runtimeOwnerWriter(srcLeased, claim.ExpectedOwnerVersion)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("expired lease must reject the old claim, got %v", err)
	}
	reclaim, err := store.ClaimDue(ctx, "worker-new", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("reclaim after merge: %v", err)
	}
	if reclaim.ID != leasedID || reclaim.ExpectedOwnerVersion != 7 || reclaim.FencingToken != claim.FencingToken+1 {
		t.Fatalf("reclaim must observe merged inputs: %+v", reclaim)
	}
}

func TestClaimDueIncrementsFencingAndAttemptOnce(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-claim")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 3)

	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claim.ID != itemID || claim.FencingToken != 1 || claim.Attempt != 1 {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.leaseUntil == nil || row.fencingToken != 1 || row.attempt != 1 {
		t.Fatalf("unexpected row after claim: %+v", row)
	}
	if _, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second); !errors.Is(err, ErrNoDueWork) {
		t.Fatalf("expected no due work while leased, got %v", err)
	}

	// Reclaim after lease expiry increments fencing and attempt exactly once.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	reclaim, err := store.ClaimDue(context.Background(), "worker-2", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaim.FencingToken != 2 || reclaim.Attempt != 2 {
		t.Fatalf("reclaim must increment fencing and attempt once: %+v", reclaim)
	}
}

func TestClaimDueSkipsLockedCandidate(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-locked-claim")
	itemID := createRefreshItem(t, store, sourceID, "effect-locked-claim", 3)

	fixtureDB := workItemFixtureDB(t)
	lockTx, err := fixtureDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback()
	var lockedID string
	if err := lockTx.QueryRowContext(context.Background(), `
SELECT id FROM work_items WHERE id = $1 FOR UPDATE`, itemID).Scan(&lockedID); err != nil {
		t.Fatalf("lock work item: %v", err)
	}
	if lockedID != itemID {
		t.Fatalf("locked id = %s, want %s", lockedID, itemID)
	}

	if _, err := store.ClaimDue(context.Background(), "worker-while-locked", []string{KindRSSRefresh}, 30*time.Second); !errors.Is(err, ErrNoDueWork) {
		t.Fatalf("ClaimDue while candidate is locked = %v, want ErrNoDueWork", err)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateAvailable {
		t.Fatalf("locked candidate changed state = %q, want available", row.state)
	}

	if err := lockTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(context.Background(), "worker-after-unlock", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimDue after releasing lock: %v", err)
	}
	if claim.ID != itemID || claim.FencingToken != 1 || claim.Attempt != 1 {
		t.Fatalf("claim after releasing lock = %+v, want item %s at first lease", claim, itemID)
	}
}

func TestExpiredMaxAttemptReclaimSurvivesRepeatedCrashesAndManualRecovery(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	sourceID := createSourceOwner(t, db, "src-max-reclaim")
	itemID := createRefreshItem(t, store, sourceID, "effect-max-reclaim", 1)

	first, err := store.ClaimDue(ctx, "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Attempt != first.MaxAttempts {
		t.Fatalf("fixture must start recovery at max attempts: %+v", first)
	}

	if _, err := workItemFixtureDB(t).ExecContext(ctx, `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDue(ctx, "worker-2", []string{KindRSSRefresh}, 30*time.Second); !errors.Is(err, ErrNoDueWork) {
		t.Fatalf("exhausted business reclaim: %v", err)
	}
	current := first
	if err := db.QueryRowContext(ctx, `SELECT fencing_token FROM work_item_history WHERE id=$1 AND recovery_blocked`, itemID).Scan(&current.FencingToken); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, first, StateSucceeded, nil); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("old generation survived quarantine: %v", err)
	}
	if err := store.ResolveBlockedFailure(ctx, current, DispositionNoExternalEffect, runtimeOwnerWriter(sourceID, current.ExpectedOwnerVersion)); err != nil {
		t.Fatalf("typed owner recovery: %v", err)
	}
	row := readWorkItem(t, db, itemID)
	ownerVersion, health := readRuntimeVersion(t, db, sourceID)
	if row.state != StateFailed || row.attempt != current.MaxAttempts || row.fencingToken != current.FencingToken || ownerVersion != 2 || health != "failed" {
		t.Fatalf("terminal owner/current state was not atomic: row=%+v owner_version=%d health=%s", row, ownerVersion, health)
	}

	newID, err := store.ManualRetry(ctx, OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID}, itemID,
		"operator verified recovery is safe", "max-reclaim-manual-1", runtimeOwnerReader(sourceID))
	if err != nil {
		t.Fatalf("eventual manual recovery: %v", err)
	}
	manual := readWorkItem(t, db, newID)
	if newID == itemID || manual.state != StateAvailable || manual.attempt != 0 || manual.fencingToken != 0 || manual.idempotencyKey != row.idempotencyKey {
		t.Fatalf("manual recovery must create fresh work with stable effect identity: %+v", manual)
	}
}

func TestExpiredClaimFinishAndRetryAreRejected(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-expired-claim")
	itemID := createRefreshItem(t, store, sourceID, "effect-expired-claim", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `
UPDATE work_items
SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second'
WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}

	if err := store.Finish(context.Background(), claim, StateSucceeded, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("expired finish must be rejected as stale: %v", err)
	}
	if _, err := store.Retry(context.Background(), claim, "EXPIRED", "claim expired", time.Minute, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("expired retry must be rejected as stale: %v", err)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.fencingToken != claim.FencingToken || row.attempt != claim.Attempt {
		t.Fatalf("expired claim attempts must not mutate the row: %+v", row)
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 1 || health != "unknown" {
		t.Fatalf("expired claim attempts must not mutate the owner: version=%d health=%s", version, health)
	}
}

func TestRetryReturnsToAvailableWithoutAttemptIncrement(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-retry")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := store.Retry(context.Background(), claim, "FEED_TIMEOUT", "feed timed out", time.Hour, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if outcome.Terminal {
		t.Fatal("retry below max attempts must not be terminal")
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateAvailable || row.attempt != 1 || row.leaseUntil != nil {
		t.Fatalf("retry must preserve attempt and clear lease: %+v", row)
	}
	if row.lastErrorCode == nil || *row.lastErrorCode != "FEED_TIMEOUT" {
		t.Fatalf("retry must persist last_error_code: %+v", row)
	}
	if !row.availableAt.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("retry must schedule a future database-time available_at: %v", row.availableAt)
	}
	if row.expectedOwnerVersion != 2 {
		t.Fatalf("owner version must advance to 2, got %d", row.expectedOwnerVersion)
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 2 || health != "degraded" {
		t.Fatalf("owner must show degraded retry state: version=%d health=%s", version, health)
	}

	// The next successful claim performs the next single increment.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempt != 2 || next.FencingToken != 2 {
		t.Fatalf("next claim must increment once: %+v", next)
	}
}

func TestRetryAtAttemptBudgetTerminates(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-exhaust")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 1)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.Retry(context.Background(), claim, "FEED_GONE", "feed is gone", time.Minute, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion))
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Terminal {
		t.Fatal("retry at attempt budget must be terminal")
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateFailed || row.terminalAt == nil || row.retentionUntil == nil {
		t.Fatalf("terminal retry must set failed/terminal/retention: %+v", row)
	}
	if got := row.retentionUntil.Sub(*row.terminalAt); got < 6*24*time.Hour || got > 8*24*time.Hour {
		t.Fatalf("default retention must be 7 days, got %v", got)
	}
	_, health := readRuntimeVersion(t, db, sourceID)
	if health != "failed" {
		t.Fatalf("owner must record terminal failure, got %s", health)
	}
}

func TestRetryWithoutAttemptBudgetRemainsAvailableAtAttemptBudget(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-recovery-retry")
	itemID := createRefreshItem(t, store, sourceID, "effect-recovery", 1)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := store.RetryWithoutAttemptBudget(context.Background(), claim, "RECOVERY_PENDING", "waiting for external resolution", time.Minute, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Terminal {
		t.Fatal("recovery retry at attempt budget must remain non-terminal")
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.terminalAt != nil || row.retentionUntil != nil || row.attempt != claim.Attempt || row.idempotencyKey != claim.IdempotencyKey {
		t.Fatalf("unknown recovery lost effect evidence: %+v", row)
	}
	var blocked bool
	var reason string
	if err := db.QueryRow(`SELECT recovery_blocked,recovery_reason FROM work_item_history WHERE id=$1`, itemID).Scan(&blocked, &reason); err != nil {
		t.Fatal(err)
	}
	if !blocked || reason != "effect_unknown" {
		t.Fatalf("unknown effect not isolated: %v %s", blocked, reason)
	}
	if _, err := store.ClaimDue(context.Background(), "worker-2", []string{KindRSSRefresh}, 30*time.Second); !errors.Is(err, ErrNoDueWork) {
		t.Fatalf("unknown effect restarted business: %v", err)
	}
}

func TestFinishCommitsTerminalOwnerState(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-finish")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, StateSucceeded, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err != nil {
		t.Fatalf("finish: %v", err)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateSucceeded || row.terminalAt == nil || row.retentionUntil == nil || row.leaseUntil != nil {
		t.Fatalf("unexpected terminal row: %+v", row)
	}
	var liveCount int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_items
WHERE owner_type = $1 AND owner_id = $2 AND kind = $3 AND state IN ('available', 'leased')`,
		OwnerRSSSourceRuntime, sourceID, KindRSSRefresh).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("finish must not create another future WorkItem, got %d live rows", liveCount)
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 2 || health != "ready" {
		t.Fatalf("owner must commit with the finish: version=%d health=%s", version, health)
	}
}

func TestFinishRollsBackOwnerAndWorkItemTogether(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-atomic")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	failingWriter := func(ctx context.Context, scope txscope.Tx, result OwnerResult) (int64, error) {
		return 0, errors.New("owner write exploded")
	}
	if err := store.Finish(context.Background(), claim, StateSucceeded, failingWriter); err == nil {
		t.Fatal("expected owner write failure")
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.fencingToken != claim.FencingToken {
		t.Fatalf("item must stay leased with the same fencing token: %+v", row)
	}
	var live int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_items WHERE owner_id = $1 AND state IN ('available','leased')`, sourceID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("the current WorkItem must survive the rollback, got %d live rows", live)
	}
	version, _ := readRuntimeVersion(t, db, sourceID)
	if version != 1 {
		t.Fatalf("owner version must be unchanged after rollback, got %d", version)
	}
}

func TestRetirementFencesClaimedOwnerFinish(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-retirement-fence")
	itemID := createRefreshItem(t, store, sourceID, "effect-retirement", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	animeID := "anime-" + sourceID
	if _, err := db.ExecContext(context.Background(), `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1, updated_at=CURRENT_TIMESTAMP
WHERE id=$1`, animeID); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, StateSucceeded, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err == nil {
		t.Fatal("claimed writer committed after retirement")
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 1 || health == "ready" {
		t.Fatalf("late writer changed retired owner: version=%d health=%s", version, health)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.fencingToken != claim.FencingToken {
		t.Fatalf("failed late finish changed item: %+v", row)
	}
}

func TestStaleFencingTokenRejected(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-stale")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 3)
	stale, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	current, err := store.ClaimDue(context.Background(), "worker-2", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if current.FencingToken != stale.FencingToken+1 {
		t.Fatalf("reclaim must advance the fencing token: %+v", current)
	}
	if err := store.Finish(context.Background(), stale, StateSucceeded, runtimeOwnerWriter(sourceID, stale.ExpectedOwnerVersion)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale finish must be rejected, got %v", err)
	}
	if _, err := store.Retry(context.Background(), stale, "X", "stale", time.Minute, nil); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale retry must be rejected, got %v", err)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.fencingToken != current.FencingToken {
		t.Fatalf("stale writes must not mutate the item: %+v", row)
	}
	version, _ := readRuntimeVersion(t, db, sourceID)
	if version != 1 {
		t.Fatalf("stale writes must not mutate the owner, got version %d", version)
	}
}

func TestManualRetryCreatesNewRowWithStableEffectKey(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-manual")
	itemID := createRefreshItem(t, store, sourceID, "effect-stable", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, StateFailed, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err != nil {
		t.Fatal(err)
	}
	owner := OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID}
	newID, err := store.ManualRetry(context.Background(), owner, itemID, "operator retry", "request-key-0001", runtimeOwnerReader(sourceID))
	if err != nil {
		t.Fatalf("manual retry: %v", err)
	}
	if newID == itemID {
		t.Fatal("manual retry must create a new WorkItem ID")
	}
	newRow := readWorkItem(t, db, newID)
	if newRow.state != StateAvailable || newRow.attempt != 0 || newRow.idempotencyKey != "effect-stable" {
		t.Fatalf("new row must restart attempt and reuse the stable effect key: %+v", newRow)
	}
	if newRow.expectedOwnerVersion != 2 {
		t.Fatalf("new row must track the current owner version, got %d", newRow.expectedOwnerVersion)
	}
	oldRow := readWorkItem(t, db, itemID)
	if oldRow.state != StateFailed || oldRow.terminalAt == nil {
		t.Fatalf("old terminal evidence must be retained: %+v", oldRow)
	}

	// Replaying the same HTTP request key returns the original new row.
	replayID, err := store.ManualRetry(context.Background(), owner, itemID, "operator retry", "request-key-0001", runtimeOwnerReader(sourceID))
	if err != nil {
		t.Fatalf("manual retry replay: %v", err)
	}
	if replayID != newID {
		t.Fatalf("replay must return the original row %s, got %s", newID, replayID)
	}
	// Reusing the key with a different body is a conflict.
	if _, err := store.ManualRetry(context.Background(), owner, itemID, "different reason", "request-key-0001", runtimeOwnerReader(sourceID)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	// A distinct request key while a live retry item exists is a live conflict.
	if _, err := store.ManualRetry(context.Background(), owner, itemID, "operator retry", "request-key-0002", runtimeOwnerReader(sourceID)); !errors.Is(err, ErrLiveItemConflict) {
		t.Fatalf("expected live item conflict, got %v", err)
	}
	// Release owners do not accept manual retry in the baseline.
	if _, err := store.ManualRetry(context.Background(), OwnerRef{Type: OwnerRelease, ID: "rel-1"}, "wi-x", "reason", "request-key-0003", nil); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("release manual retry must be rejected, got %v", err)
	}
	// A live item is not a manual-retry source.
	if _, err := store.ManualRetry(context.Background(), owner, newID, "reason", "request-key-0004", runtimeOwnerReader(sourceID)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("manual retry of a live item must be rejected, got %v", err)
	}
}

func TestManualRetryWithOwnerResetUpdatesVersionAndReplaysWithoutReset(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-reset")
	itemID := createRefreshItem(t, store, sourceID, "effect-reset", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, StateFailed, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err != nil {
		t.Fatal(err)
	}

	resetCalls := 0
	resetter := func(ctx context.Context, scope txscope.Tx, expectedVersion int64) (int64, error) {
		resetCalls++
		tx := scope.SQLTx()
		var newVersion int64
		if err := tx.QueryRowContext(ctx, `
UPDATE rss_source_runtime
SET health = 'unknown', reason_code = NULL, reason_detail = NULL,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE rss_source_id = $1 AND version = $2
RETURNING version`, sourceID, expectedVersion).Scan(&newVersion); err != nil {
			return 0, err
		}
		return newVersion, nil
	}
	owner := OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID}
	newID, err := store.ManualRetryWithOwnerReset(context.Background(), owner, itemID, "operator reset", "reset-key-0001", runtimeOwnerReader(sourceID), resetter)
	if err != nil {
		t.Fatalf("manual retry with reset: %v", err)
	}
	if resetCalls != 1 {
		t.Fatalf("owner reset must run once, got %d calls", resetCalls)
	}
	newRow := readWorkItem(t, db, newID)
	if newRow.expectedOwnerVersion != 3 || newRow.attempt != 0 || newRow.state != StateAvailable {
		t.Fatalf("new WorkItem must use the reset owner version: %+v", newRow)
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 3 || health != "unknown" {
		t.Fatalf("owner reset must commit its new version/state: version=%d health=%s", version, health)
	}

	replayID, err := store.ManualRetryWithOwnerReset(context.Background(), owner, itemID, "operator reset", "reset-key-0001", runtimeOwnerReader(sourceID), func(context.Context, txscope.Tx, int64) (int64, error) {
		t.Fatalf("idempotency replay must not invoke owner reset")
		return 0, nil
	})
	if err != nil || replayID != newID {
		t.Fatalf("manual retry replay mismatch: id=%s err=%v", replayID, err)
	}
}

func TestManualRetryWithOwnerResetRollsBackOwnerWhenResetFails(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-reset-rollback")
	itemID := createRefreshItem(t, store, sourceID, "effect-reset-rollback", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, StateFailed, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err != nil {
		t.Fatal(err)
	}

	resetter := func(ctx context.Context, scope txscope.Tx, expectedVersion int64) (int64, error) {
		tx := scope.SQLTx()
		var newVersion int64
		if err := tx.QueryRowContext(ctx, `
UPDATE rss_source_runtime
SET health = 'unknown', reason_code = NULL, reason_detail = NULL,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE rss_source_id = $1 AND version = $2
RETURNING version`, sourceID, expectedVersion).Scan(&newVersion); err != nil {
			return 0, err
		}
		return 0, errors.New("forced owner reset failure after mutation")
	}
	owner := OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID}
	if _, err := store.ManualRetryWithOwnerReset(context.Background(), owner, itemID, "operator reset", "reset-key-0002", runtimeOwnerReader(sourceID), resetter); err == nil {
		t.Fatal("manual retry must fail when owner reset fails")
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 2 || health != "failed" {
		t.Fatalf("owner reset failure must roll back owner mutation: version=%d health=%s", version, health)
	}
	var liveCount int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_items
WHERE owner_id = $1 AND state IN ('available', 'leased')`, sourceID).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("failed manual retry must not create a live WorkItem: %d", liveCount)
	}
}

func TestPurgeExpiredKeepsLiveRowsAndOwners(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-purge")
	itemID := createRefreshItem(t, store, sourceID, "effect-1", 3)
	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, StateSucceeded, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err != nil {
		t.Fatal(err)
	}
	liveID := createRefreshItem(t, store, sourceID, "effect-2", 3)
	// Make the terminal row expired; the live row must survive any purge. The
	// baseline requires retention_until = terminal_at + retention_days, so the
	// two timestamps move together.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `
UPDATE work_item_outcomes
SET terminal_at = CURRENT_TIMESTAMP - INTERVAL '8 days',
    retention_until = CURRENT_TIMESTAMP - INTERVAL '1 day'
WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	purged, err := store.PurgeExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("expected exactly one purged row, got %d", purged)
	}
	var remaining int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_item_history WHERE id = $1`, itemID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("expired terminal row must be purged")
	}
	row := readWorkItem(t, db, liveID)
	if row.state != StateAvailable {
		t.Fatalf("live row must survive purge: %+v", row)
	}
	if _, err := db.ExecContext(context.Background(), `SELECT 1 FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID); err != nil {
		t.Fatal("owner rows must survive purge")
	}
}

func createTerminalRefreshItem(t *testing.T, db *sql.DB, store *Store, suffix string, oneShot *OneShotRef) (string, string) {
	t.Helper()
	sourceID := createSourceOwner(t, db, "src-terminal-"+suffix)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := store.Create(ctx, tx, WorkItemSpec{
		Owner:                OwnerRef{Type: OwnerRSSSourceRuntime, ID: sourceID},
		Kind:                 KindRSSRefresh,
		IdempotencyKey:       "terminal-effect-" + suffix,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
		OneShot:              oneShot,
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("create terminal work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "terminal-worker-"+suffix, []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim terminal work item: %v", err)
	}
	if err := store.Finish(ctx, claim, StateSucceeded, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); err != nil {
		t.Fatalf("finish terminal work item: %v", err)
	}
	return itemID, sourceID
}

func TestPurgeExpiredTxPreservesOneShotLiveAndFutureRows(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	ordinaryID, ordinaryOwner := createTerminalRefreshItem(t, db, store, "ordinary", nil)
	oneShotID, _ := createTerminalRefreshItem(t, db, store, "one-shot", &OneShotRef{
		MarkerID: "marker-purge-one-shot", CorrelationID: "correlation-purge-one-shot", AdmissionEpoch: 1,
	})
	futureID, _ := createTerminalRefreshItem(t, db, store, "future", nil)
	liveOwner := createSourceOwner(t, db, "src-live-purge")
	liveID := createRefreshItem(t, store, liveOwner, "live-effect", 3)

	for _, id := range []string{ordinaryID, oneShotID} {
		if _, err := workItemFixtureDB(t).ExecContext(ctx, `
	UPDATE work_item_outcomes
	SET terminal_at = CURRENT_TIMESTAMP - INTERVAL '8 days',
	    retention_until = CURRENT_TIMESTAMP - INTERVAL '1 day'
	WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
	UPDATE work_item_outcomes
	SET terminal_at = CURRENT_TIMESTAMP - INTERVAL '1 day',
	    retention_until = CURRENT_TIMESTAMP + INTERVAL '6 days'
	WHERE id = $1`, futureID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	purged, err := store.PurgeExpiredTx(ctx, txscope.Wrap(tx), 500)
	if err != nil {
		t.Fatalf("purge expired work items: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged rows = %d, want 1 ordinary row", purged)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		name string
		id   string
		want int
	}{
		{name: "ordinary expired", id: ordinaryID, want: 0},
		{name: "one-shot expired", id: oneShotID, want: 1},
		{name: "future terminal", id: futureID, want: 1},
		{name: "live", id: liveID, want: 1},
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE id = $1`, check.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Fatalf("%s row count = %d, want %d", check.name, count, check.want)
		}
	}
	var ownerCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_source_runtime WHERE rss_source_id = $1`, ordinaryOwner).Scan(&ownerCount); err != nil {
		t.Fatal(err)
	}
	if ownerCount != 1 {
		t.Fatalf("owner truth row count = %d, want 1", ownerCount)
	}
}

func TestPurgeExpiredTxUsesStableRetentionOrderAndBatchLimit(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		createTerminalRefreshItem(t, db, store, fmt.Sprintf("ordered-%d", i), nil)
	}
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
	UPDATE work_item_outcomes
	SET terminal_at = CURRENT_TIMESTAMP - INTERVAL '8 days',
	    retention_until = CURRENT_TIMESTAMP - INTERVAL '1 day'
	WHERE state = 'succeeded'`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `
	SELECT id FROM work_item_history
	WHERE state = 'succeeded' AND retention_until <= CURRENT_TIMESTAMP
	ORDER BY retention_until, id`)
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		expected = append(expected, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(expected) != 3 {
		t.Fatalf("expired ordered rows = %d, want 3", len(expected))
	}

	for i, wantID := range expected {
		var nextID string
		if err := db.QueryRowContext(ctx, `
		SELECT id FROM work_item_history
		WHERE state = 'succeeded' AND retention_until <= CURRENT_TIMESTAMP
		ORDER BY retention_until, id LIMIT 1`).Scan(&nextID); err != nil {
			t.Fatalf("read next ordered row %d: %v", i, err)
		}
		if nextID != wantID {
			t.Fatalf("next ordered row %d = %q, want %q", i, nextID, wantID)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		purged, purgeErr := store.PurgeExpiredTx(ctx, txscope.Wrap(tx), 1)
		if purgeErr != nil {
			tx.Rollback()
			t.Fatalf("purge ordered row %d: %v", i, purgeErr)
		}
		if purged != 1 {
			tx.Rollback()
			t.Fatalf("purge batch %d deleted %d rows, want 1", i, purged)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var remaining int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE id = $1`, wantID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("ordered row %q survived batch %d", wantID, i)
		}
	}
}

func TestRenewLeaseExtendsWhileClaimActive(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-heartbeat")
	itemID := createRefreshItem(t, store, sourceID, "effect-heartbeat", 3)

	claim, err := store.ClaimDue(context.Background(), "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// RenewLease with the correct token and a future time succeeds.
	extend := time.Now().UTC().Add(45 * time.Second)
	if err := store.RenewLease(context.Background(), claim, extend); err != nil {
		t.Fatalf("renew: %v", err)
	}
	var leaseUntil time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT lease_until FROM work_items WHERE id = $1`, itemID).Scan(&leaseUntil); err != nil {
		t.Fatal(err)
	}
	if leaseUntil.Before(extend.Add(-2 * time.Second)) {
		t.Fatalf("lease not extended: lease=%v", leaseUntil)
	}

	// A stale token must fail closed.
	stale := claim
	stale.FencingToken = claim.FencingToken + 1
	if err := store.RenewLease(context.Background(), stale, extend); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale token renew must fail with ErrStaleClaim, got %v", err)
	}

	// An expired lease must fail closed.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(context.Background(), claim, extend); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("expired lease renew must fail with ErrStaleClaim, got %v", err)
	}
}

func TestRequestCancelBlocksBusinessAndAllowsTypedOwnerRecovery(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	sourceID := createSourceOwner(t, db, "src-cancel")
	itemID := createRefreshItem(t, store, sourceID, "effect-cancel", 3)
	claim, err := store.ClaimDue(ctx, "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(ctx, claim, "maintenance"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(ctx, claim, time.Now().UTC().Add(45*time.Second)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("cancelled heartbeat: %v", err)
	}
	if err := store.Finish(ctx, claim, StateSucceeded, nil); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("cancelled generation committed success: %v", err)
	}
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.ClaimDue(ctx, "worker-2", []string{KindRSSRefresh}, 30*time.Second); !errors.Is(err, ErrNoDueWork) {
			t.Fatalf("cancelled business restarted: %v", err)
		}
	}
	recovery := claim
	var cancelled, blocked bool
	var recoveryReason string
	if err := db.QueryRowContext(ctx, `SELECT fencing_token,cancel_requested,recovery_blocked,recovery_reason FROM work_item_history WHERE id=$1`, itemID).Scan(&recovery.FencingToken, &cancelled, &blocked, &recoveryReason); err != nil {
		t.Fatal(err)
	}
	if !cancelled || !blocked || recoveryReason != "cancel_requested" || recovery.FencingToken != claim.FencingToken+1 {
		t.Fatalf("cancel evidence/fence lost: recovery=%+v cancelled=%v blocked=%v reason=%s", recovery, cancelled, blocked, recoveryReason)
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != claim.ExpectedOwnerVersion || health == "failed" {
		t.Fatalf("quarantine invented owner result: version=%d health=%s", version, health)
	}
	writer := runtimeOwnerWriter(sourceID, recovery.ExpectedOwnerVersion)
	if err := store.ResolveBlockedFailure(ctx, recovery, DispositionAmbiguous, writer); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("unknown effect accepted as no effect: %v", err)
	}
	if err := store.ResolveBlockedFailure(ctx, claim, DispositionNoExternalEffect, writer); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("old generation recovered owner: %v", err)
	}
	if err := store.ResolveBlockedFailure(ctx, recovery, DispositionNoExternalEffect, writer); err != nil {
		t.Fatal(err)
	}
	row := readWorkItem(t, db, itemID)
	version, health = readRuntimeVersion(t, db, sourceID)
	if row.state != StateFailed || row.terminalAt == nil || row.retentionUntil == nil || row.idempotencyKey != claim.IdempotencyKey || row.attempt != claim.Attempt || version != claim.ExpectedOwnerVersion+1 || health != "failed" {
		t.Fatalf("owner recovery not atomic: row=%+v version=%d health=%s", row, version, health)
	}
	if err := db.QueryRowContext(ctx, `SELECT cancel_requested FROM work_item_history WHERE id=$1`, itemID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("typed recovery cleared cancellation evidence")
	}
}

func TestCancelRequestsQuarantinesAvailableAndLeasedWithoutBusinessClaim(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	sourceID := createSourceOwner(t, db, "src-batch-cancel")
	leasedID := createRefreshItem(t, store, sourceID, "effect-batch-1", 3)
	claim, err := store.ClaimDue(ctx, "worker-1", []string{KindRSSRefresh}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	sourceID2 := createSourceOwner(t, db, "src-batch-cancel-2")
	availableID := createRefreshItem(t, store, sourceID2, "effect-batch-2", 3)
	count, err := store.CancelRequests(ctx, []string{KindRSSRefresh}, "drain")
	if err != nil || count != 2 {
		t.Fatalf("cancel both states: count=%d err=%v", count, err)
	}
	if err := store.RenewLease(ctx, claim, time.Now().UTC().Add(45*time.Second)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("leased cancel heartbeat: %v", err)
	}
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, leasedID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.ClaimDue(ctx, "worker-2", []string{KindRSSRefresh}, 30*time.Second); !errors.Is(err, ErrNoDueWork) {
			t.Fatalf("batch cancel ran business: %v", err)
		}
	}
	for _, fixture := range []struct {
		id, state, key string
		attempt        int
		fence          int64
	}{
		{leasedID, StateLeased, "effect-batch-1", 1, claim.FencingToken + 1},
		{availableID, StateAvailable, "effect-batch-2", 0, 1},
	} {
		row := readWorkItem(t, db, fixture.id)
		var cancelled, blocked bool
		var recoveryReason string
		if err := db.QueryRowContext(ctx, `SELECT cancel_requested,recovery_blocked,recovery_reason FROM work_item_history WHERE id=$1`, fixture.id).Scan(&cancelled, &blocked, &recoveryReason); err != nil {
			t.Fatal(err)
		}
		if !cancelled || !blocked || recoveryReason != "cancel_requested" || row.state != fixture.state || row.attempt != fixture.attempt || row.fencingToken != fixture.fence || row.idempotencyKey != fixture.key || row.terminalAt != nil {
			t.Fatalf("cancel evidence changed or business attempted: row=%+v cancelled=%v blocked=%v reason=%s", row, cancelled, blocked, recoveryReason)
		}
	}
	for _, id := range []string{sourceID, sourceID2} {
		version, health := readRuntimeVersion(t, db, id)
		if version != 1 || health == "failed" {
			t.Fatalf("batch cancel invented owner result: id=%s version=%d health=%s", id, version, health)
		}
	}
	count, err = store.CancelRequests(ctx, []string{KindRSSRefresh}, "repeat")
	if err != nil || count != 0 {
		t.Fatalf("cancel replay: count=%d err=%v", count, err)
	}
}
