package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
)

// seedOneShotMarkerForUnsubscribe inserts one acquisition marker anchored on
// the given anime/subscription. Anchors are opaque references without FKs, so
// the ids only need to satisfy the CHECK bounds.
func seedOneShotMarkerForUnsubscribe(t *testing.T, db *sql.DB, id, animeID, subscriptionID, state string) {
	t.Helper()
	ctx := context.Background()
	if state == "running" {
		if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(
  id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
  correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb, $10)`,
			id, animeID, subscriptionID, "src-"+id, "rel-"+id, "task-"+id,
			"corr-"+id, "req-key-"+id, "sha256:"+strings.Repeat("0", 64), state); err != nil {
			t.Fatalf("seed running marker %s: %v", id, err)
		}
		return
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(
  id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
  correlation_id, request_idempotency_key, request_hash, result_snapshot, state,
  terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb, $10,
        'succeeded', 'one_shot_completed', 'one-shot acquisition completed',
        CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day')`,
		id, animeID, subscriptionID, "src-"+id, "rel-"+id, "task-"+id,
		"corr-"+id, "req-key-"+id, "sha256:"+strings.Repeat("0", 64), state); err != nil {
		t.Fatalf("seed marker %s (state %s): %v", id, state, err)
	}
}

// Any non-cleaned marker state owns a live temporary owner graph and must
// block the unsubscribe with the typed conflict while deleting nothing.
func TestDeleteAnimeSubscriptionConflictOnActiveOneShotMarker(t *testing.T) {
	for _, state := range []string{"running", "retention_pending", "cleanup_blocked", "reclaiming"} {
		t.Run(state, func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
			ctx := context.Background()
			subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-oneshot-"+state)
			animeID := subscribe.ManagedAnime.Id
			var subscriptionID string
			if err := db.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subscriptionID); err != nil {
				t.Fatal(err)
			}
			seedOneShotMarkerForUnsubscribe(t, db, "osa-unsub-"+state, animeID, subscriptionID, state)

			err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1})
			var business *apperrors.Business
			if err == nil || !errors.As(err, &business) || business.Status != 409 || business.Code != "RETIREMENT_ONE_SHOT_ACTIVE" {
				t.Fatalf("state %s must produce the typed conflict, got %v", state, err)
			}
			// Transaction rollback preserves every owner row.
			var subs, animes, metadata, markers int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subs); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, animeID).Scan(&animes); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_acquisitions WHERE managed_anime_id = $1 AND state <> 'cleaned'`, animeID).Scan(&markers); err != nil {
				t.Fatal(err)
			}
			if subs != 1 || animes != 1 || metadata != 1 || markers != 1 {
				t.Fatalf("conflict must retain all rows: subs=%d animes=%d metadata=%d markers=%d",
					subs, animes, metadata, markers)
			}
		})
	}
}

// A cleaned marker has no live one-shot ownership: retirement proceeds and the
// complete managed owner graph remains inspectable.
func TestDeleteAnimeSubscriptionAllowsCleanedOneShotMarker(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-oneshot-cleaned")
	animeID := subscribe.ManagedAnime.Id
	var subscriptionID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	const markerID = "osa-unsub-cleaned"
	seedOneShotMarkerForUnsubscribe(t, db, markerID, animeID, subscriptionID, "cleaned")

	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1}); err != nil {
		t.Fatalf("cleaned marker must not block unsubscribe: %v", err)
	}
	var subs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subs); err != nil {
		t.Fatal(err)
	}
	if subs != 1 {
		t.Fatalf("retirement must preserve the subscription, got %d", subs)
	}
	state, epoch := lifecycleForTest(t, db, animeID)
	if state != "retired" || epoch != 2 {
		t.Fatalf("retirement lifecycle = %s/%d", state, epoch)
	}
	// The cleaned marker row survives as the bounded replay record.
	var markers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_acquisitions WHERE id = $1 AND state = 'cleaned'`, markerID).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("cleaned marker replay row must remain, got %d", markers)
	}
}

// A marker anchor must not change the existing missing-subscription result.
// This is important because marker locking intentionally happens before the
// normal subscription/anime lock.
func TestDeleteAnimeSubscriptionMarkerDoesNotMaskMissingSubscription(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	const animeID = "anime-oneshot-missing-subscription"
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ($1, 'Missing Subscription')`, animeID); err != nil {
		t.Fatal(err)
	}
	seedOneShotMarkerForUnsubscribe(t, db, "osa-unsub-missing-subscription", animeID, "missing-subscription", "running")

	err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1})
	var notFound *apperrors.NotFound
	if err == nil || !errors.As(err, &notFound) {
		t.Fatalf("missing subscription must remain NotFound with an active marker, got %v", err)
	}
}

// The delete must wait on an existing marker lock before it can lock the
// subscription/anime rows. An ordinary COUNT (the previous implementation)
// would return the active-marker conflict immediately while this lock is held.
func TestDeleteAnimeSubscriptionLocksMarkerBeforeOwnerRows(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-oneshot-lock-order")
	animeID := subscribe.ManagedAnime.Id
	var subscriptionID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	seedOneShotMarkerForUnsubscribe(t, db, "osa-unsub-lock-order", animeID, subscriptionID, "running")

	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	var state string
	if err := holder.QueryRowContext(ctx, `
SELECT state
FROM one_shot_acquisitions
WHERE managed_anime_id = $1
FOR UPDATE`, animeID).Scan(&state); err != nil {
		t.Fatalf("hold marker lock: %v", err)
	}
	if state != "running" {
		t.Fatalf("held marker state = %q, want running", state)
	}

	deleteCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	err = service.DeleteAnimeSubscription(deleteCtx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1})
	if err == nil {
		t.Fatal("delete completed while marker lock was held")
	}
	var business *apperrors.Business
	if errors.As(err, &business) && business.Code == "RETIREMENT_ONE_SHOT_ACTIVE" {
		t.Fatalf("delete bypassed the marker lock and returned the active conflict: %v", err)
	}
	// The context deadline is expected: the marker FOR UPDATE is the first
	// database lock in the delete transaction.

	if err := holder.Rollback(); err != nil {
		t.Fatalf("release marker lock: %v", err)
	}
	// Once the marker lock is released, a fresh request still returns the
	// typed conflict and leaves the owner graph intact.
	err = service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1})
	var releasedBusiness *apperrors.Business
	if err == nil || !errors.As(err, &releasedBusiness) || releasedBusiness.Code != "RETIREMENT_ONE_SHOT_ACTIVE" {
		t.Fatalf("active marker after lock release must produce typed conflict, got %v", err)
	}
}
