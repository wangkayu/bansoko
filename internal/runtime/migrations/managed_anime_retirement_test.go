package migrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestManagedAnimeRetirementFence(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	// The owner graph: a ManagedAnime with a subscription, metadata, an RSS source
	// with a refresh work item, and an accepted download whose submit work item is
	// leased.
	seedAcceptableRelease(t, db, "retire")
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-retire');
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-retire', 'rss_source_runtime', 'src-retire', 'rss_refresh', 'available', 'rss_refresh:src-retire', 3, '{}', 1)`); err != nil {
		t.Fatalf("seed owner graph: %v", err)
	}
	accepted := validQueued(t, "acq-retire", "key-retire", "retire")
	if _, err := callQueuedAccept(t, db, accepted); err != nil {
		t.Fatalf("accept the download: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE work_items SET state='leased', lease_until=CURRENT_TIMESTAMP + INTERVAL '5 minutes', fencing_token=1
WHERE id='wi-sub-acq-retire'`); err != nil {
		t.Fatalf("lease the submit work item: %v", err)
	}
	// Admit the external effect while the owner is still active: this is the
	// row retirement must preserve.
	if _, err := db.ExecContext(ctx, `UPDATE work_items SET external_effect_admitted_at = CURRENT_TIMESTAMP WHERE id='wi-sub-acq-retire'`); err != nil {
		t.Fatalf("admit external effect: %v", err)
	}

	var lifecycleState, workAnimeID string
	var epoch, workEpoch int64
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state, fence_epoch FROM managed_anime WHERE id='anime-retire'`).Scan(&lifecycleState, &epoch); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT managed_anime_id, owner_lifecycle_epoch FROM work_item_history WHERE id='wi-retire'`).Scan(&workAnimeID, &workEpoch); err != nil {
		t.Fatal(err)
	}
	if lifecycleState != "active" || epoch != 1 || workAnimeID != "anime-retire" || workEpoch != 1 {
		t.Fatalf("backfill lifecycle=%s/%d work=%s/%d", lifecycleState, epoch, workAnimeID, workEpoch)
	}
	// Only the effect entries survive the removed row fences: the managed anime
	// lifecycle guard and the WorkItem create/update/delete fences.
	for _, trigger := range []string{
		"managed_anime_lifecycle_guard",
		"work_items_retirement_fence",
		"work_items_retirement_delete_fence",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname=$1 AND NOT tgisinternal)`, trigger).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing retirement trigger %s", trigger)
		}
	}

	// Retirement transaction order: lock the owner row, terminate un-admitted work
	// at the still-active epoch, then advance the lifecycle. No session GUC.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lockedEpoch int64
	if err := tx.QueryRowContext(ctx, `
SELECT fence_epoch FROM managed_anime WHERE id='anime-retire' FOR UPDATE`).Scan(&lockedEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE work_items
SET cancel_requested=TRUE,
    last_error_code='MANAGED_ANIME_RETIRED',
    last_error_reason='retired before admission',
    fencing_token=fencing_token+1, updated_at=CURRENT_TIMESTAMP
WHERE managed_anime_id='anime-retire' AND owner_lifecycle_epoch=$1
  AND state IN ('available','leased') AND external_effect_admitted_at IS NULL`, lockedEpoch); err != nil {
		t.Fatalf("retirement must isolate un-admitted work while active: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
SELECT public.work_item_close(id,'failed') FROM work_items
WHERE managed_anime_id='anime-retire' AND owner_lifecycle_epoch=$1
  AND state IN ('available','leased') AND external_effect_admitted_at IS NULL AND cancel_requested`, lockedEpoch); err != nil {
		t.Fatalf("retirement must terminate un-admitted work while active: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1, updated_at=CURRENT_TIMESTAMP
WHERE id='anime-retire' AND lifecycle_state='active' AND fence_epoch=$1`, lockedEpoch); err != nil {
		t.Fatalf("retire lifecycle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var retiredState string
	var retiredEpoch int64
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state, fence_epoch FROM managed_anime WHERE id='anime-retire'`).Scan(&retiredState, &retiredEpoch); err != nil {
		t.Fatal(err)
	}
	if retiredState != "retired" || retiredEpoch != 2 {
		t.Fatalf("lifecycle after retirement = %s/%d", retiredState, retiredEpoch)
	}
	var workState string
	var workToken int64
	var workTerminal *time.Time
	if err := db.QueryRowContext(ctx, `SELECT state, fencing_token, terminal_at FROM work_item_history WHERE id='wi-retire'`).Scan(&workState, &workToken, &workTerminal); err != nil {
		t.Fatal(err)
	}
	if workState != "failed" || workToken != 1 || workTerminal == nil {
		t.Fatalf("retirement isolate = %s/token=%d/terminal=%v", workState, workToken, workTerminal)
	}
	// The already-admitted external effect is preserved untouched.
	var admittedState string
	var admittedToken int64
	var admittedAt *time.Time
	if err := db.QueryRowContext(ctx, `
SELECT state, fencing_token, external_effect_admitted_at FROM work_item_history WHERE id='wi-sub-acq-retire'`).Scan(&admittedState, &admittedToken, &admittedAt); err != nil {
		t.Fatal(err)
	}
	if admittedState != "leased" || admittedToken != 1 || admittedAt == nil {
		t.Fatalf("retirement touched admitted work: %s/token=%d/admitted=%v", admittedState, admittedToken, admittedAt)
	}

	// A child-row write for a retired owner is now allowed at the DB level; the
	// effect entry is what refuses it. Creating a WorkItem for the retired owner
	// still fails closed.
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version) VALUES ('wi-retire-extra', 'rss_source_runtime', 'src-retire', 'rss_refresh', 'available', 'rss_refresh:src-retire:new', 3, '{}', 1)`)
		return err
	}())
	// The admitted row is retained evidence: it cannot be rewritten or removed.
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE id='wi-sub-acq-retire'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM work_items WHERE id='wi-sub-acq-retire'`)
		return err
	}())
	// A retired owner row is fully frozen and is only removable by cleanup.
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET title='mutated' WHERE id='anime-retire'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM managed_anime WHERE id='anime-retire'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='active', retired_at=NULL, retired_subscription_version=NULL WHERE id='anime-retire'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET fence_epoch=fence_epoch+1, lifecycle_version=lifecycle_version+1 WHERE id='anime-retire'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM managed_anime WHERE id='anime-retire'`)
		return err
	}())
	// Terminal rows stay retention-eligible: only live rows are pinned.
	if _, err := db.ExecContext(ctx, `DELETE FROM work_item_outcomes WHERE id='wi-retire'`); err != nil {
		t.Fatalf("terminal work item retention must stay possible: %v", err)
	}

	var animeCount, subscriptionCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id='anime-retire'`).Scan(&animeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id='anime-retire'`).Scan(&subscriptionCount); err != nil {
		t.Fatal(err)
	}
	if animeCount != 1 || subscriptionCount != 1 {
		t.Fatalf("retirement deleted owner graph: anime=%d subscription=%d", animeCount, subscriptionCount)
	}
}

// TestSessionCapabilityCannotAuthorizeRetirement proves the former
// `bansoko.retirement_owner` session capability no longer exists: any application
// session can set the GUC, and every fence decision still comes from committed
// rows and locks.
func TestSessionCapabilityCannotAuthorizeRetirement(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-cap', 'Capability');
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-cap', 'anime-cap', 'custom', 'Capability', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-cap', 'sub-cap', 'custom_rss', 'https://feeds.example.test/cap', 'https://feeds.example.test/cap', 'G', 'Capability');
INSERT INTO rss_source_runtime(rss_source_id, health) VALUES ('src-cap', 'unknown');
INSERT INTO work_items(id, owner_type, owner_id, kind, state, lease_until, fencing_token, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-cap', 'rss_source_runtime', 'src-cap', 'rss_refresh', 'leased', CURRENT_TIMESTAMP + INTERVAL '5 minutes', 1, 'rss_refresh:src-cap', 3, '{}', 1)`); err != nil {
		t.Fatal(err)
	}

	// Retirement is not capability-gated: a plain session may retire its own
	// owner (that is the reviewed application path).
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-cap'`); err != nil {
		t.Fatalf("direct retirement: %v", err)
	}
	// The forged capability must authorize nothing afterwards. It is asserted
	// with the decoy, with the owner's own id, and with the empty string, so the
	// fence provably ignores the GUC in every direction.
	for _, forged := range []string{"anime-cap", "some-other-anime", ""} {
		if _, err := db.ExecContext(ctx, `SELECT set_config('bansoko.retirement_owner', $1, false)`, forged); err != nil {
			t.Fatalf("set forged capability %q: %v", forged, err)
		}
		assertRetirementFenceError(t, func() error {
			_, err := db.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE id='wi-cap'`)
			return err
		}())
	}
}

// TestLifecycleTransitionGuards pins the legal lifecycle shapes:
// active/active is fence-neutral, retirement advances exactly one epoch with
// its facts, and retired is terminal.
func TestLifecycleTransitionGuards(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-trans', 'Transition')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_version=lifecycle_version+1 WHERE id='anime-trans'`); err != nil {
		t.Fatalf("fence-neutral active update: %v", err)
	}
	// An active row cannot gain retirement fields or an epoch without retiring.
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET fence_epoch=fence_epoch+1 WHERE id='anime-trans'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET retired_at=CURRENT_TIMESTAMP WHERE id='anime-trans'`)
		return err
	}())
	// A retirement that skips the epoch advance or the facts is invalid.
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired', retired_at=CURRENT_TIMESTAMP, retired_subscription_version=1 WHERE id='anime-trans'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired', fence_epoch=fence_epoch+2, retired_at=CURRENT_TIMESTAMP, retired_subscription_version=1, lifecycle_version=lifecycle_version+1 WHERE id='anime-trans'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP, retired_subscription_version=1 WHERE id='anime-trans'`)
		return err
	}())
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-trans'`); err != nil {
		t.Fatalf("legal retirement: %v", err)
	}
	// Version regression and identity rewrite are rejected even while retired.
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_version=lifecycle_version-1 WHERE id='anime-trans'`)
		return err
	}())
	assertRetirementFenceError(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE managed_anime SET created_at=CURRENT_TIMESTAMP WHERE id='anime-trans'`)
		return err
	}())
}

// TestWorkItemOwnerIsImmutable proves the retained WorkItem fence freezes the
// resolved managed anime and its lifecycle epoch, so a live WorkItem can never
// be moved onto another owner.
func TestWorkItemOwnerIsImmutable(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-a', 'A'), ('anime-b', 'B');
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-a', 'anime-a', 'custom', 'A', 'active'), ('sub-b', 'anime-b', 'custom', 'B', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-a', 'sub-a', 'custom_rss', 'https://feeds.example.test/a', 'https://feeds.example.test/a', 'G', 'A'),
       ('src-b', 'sub-b', 'custom_rss', 'https://feeds.example.test/b', 'https://feeds.example.test/b', 'G', 'B');
INSERT INTO rss_source_runtime(rss_source_id, health) VALUES ('src-a', 'unknown'), ('src-b', 'unknown');
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-a', 'rss_source_runtime', 'src-a', 'rss_refresh', 'available', 'rss_refresh:src-a', 3, '{}', 1)`); err != nil {
		t.Fatal(err)
	}

	assertMessage(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE work_items SET managed_anime_id='anime-b' WHERE id='wi-a'`)
		return err
	}(), "WORK_ITEM_OWNER_LIFECYCLE_IMMUTABLE")
	assertMessage(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE work_items SET owner_lifecycle_epoch=2 WHERE id='wi-a'`)
		return err
	}(), "WORK_ITEM_OWNER_LIFECYCLE_IMMUTABLE")
	// Active B still accepts its own work.
	if _, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-b', 'rss_source_runtime', 'src-b', 'rss_refresh', 'available', 'rss_refresh:src-b', 3, '{}', 1)`); err != nil {
		t.Fatalf("active owner B must keep admitting work: %v", err)
	}
}

// TestLifecycleDefaultsWithAnime proves a fresh ManagedAnime starts active at
// epoch 1 with lifecycle version 1, without a separate marker row.
func TestLifecycleDefaultsWithAnime(t *testing.T) {
	db := installedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-new-retire', 'New')`); err != nil {
		t.Fatal(err)
	}
	var state string
	var epoch, version int64
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state, fence_epoch, lifecycle_version FROM managed_anime WHERE id='anime-new-retire'`).Scan(&state, &epoch, &version); err != nil {
		t.Fatal(err)
	}
	if state != "active" || epoch != 1 || version != 1 {
		t.Fatalf("new lifecycle = %s/%d/%d", state, epoch, version)
	}
}

func assertRetirementFenceError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("retirement fence unexpectedly allowed mutation")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("retirement fence error = %v, want PostgreSQL 23514", err)
	}
}

func assertMessage(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got success", want)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PostgreSQL error %s", err, want)
	}
	if pgErr.Message != want {
		t.Fatalf("fence message = %s, want %s", pgErr.Message, want)
	}
}
