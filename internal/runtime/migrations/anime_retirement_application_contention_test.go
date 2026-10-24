package migrations

import (
	"context"
	"errors"
	"testing"
	"time"

	animepersistence "bansoko/internal/anime/adapter/persistence"
	ownership "bansoko/internal/anime/ownership/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/workitems"
)

func TestAnimeRetirementApplicationBoundedContention(t *testing.T) {
	owner, db, p := progressProbe(t, "retirement-app", nil, nil)
	ctx := context.Background()
	var anime string
	var version int64
	if err := owner.QueryRowContext(ctx, `SELECT sub.managed_anime_id,sub.version FROM subscriptions sub JOIN rss_sources src ON src.subscription_id=sub.id JOIN releases r ON r.rss_source_id=src.id WHERE r.id=$1`, p.SourceReleaseID).Scan(&anime, &version); err != nil {
		t.Fatal(err)
	}
	service, err := ownership.NewService(db, ownership.Dependencies{WorkItems: &workitems.Store{DB: db}, OneShotMarkers: animepersistence.NewRepository(db)})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `SELECT id FROM work_items WHERE id=$1 FOR UPDATE`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = service.DeleteAnimeSubscription(ctx, anime, ownership.SubscriptionDeleteInput{ExpectedVersion: version})
	// A contended retirement set is retried as ONE whole transaction (5
	// attempts, 20/40/80/160ms) and exhaustion yields the zero-write 409
	// MANAGED_ANIME_RETIREMENT_BUSY. Below: the bounded elapsed window, the
	// untouched lifecycle, and the release of every lock across all five
	// attempts.
	var busy *apperrors.Business
	if !errors.As(err, &busy) {
		t.Fatalf("persistent work conflict=%v", err)
	}
	if busy.Status != 409 || busy.Code != "MANAGED_ANIME_RETIREMENT_BUSY" {
		t.Fatalf("retirement exhaustion must be 409 MANAGED_ANIME_RETIREMENT_BUSY, got %d %s", busy.Status, busy.Code)
	}
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("bounded retries elapsed=%s", elapsed)
	}
	var state string
	var epoch int64
	if err := owner.QueryRowContext(ctx, `SELECT lifecycle_state,fence_epoch FROM managed_anime WHERE id=$1`, anime).Scan(&state, &epoch); err != nil {
		t.Fatal(err)
	}
	if state != "active" || epoch != 1 {
		t.Fatalf("failed command committed lifecycle=%s/%d", state, epoch)
	}
	// All five attempts must release lifecycle locks, even on final failure.
	probe, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id=$1 FOR UPDATE NOWAIT`, anime); err != nil {
		probe.Rollback()
		t.Fatal(err)
	}
	if err := probe.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, anime, ownership.SubscriptionDeleteInput{ExpectedVersion: version}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, anime, ownership.SubscriptionDeleteInput{ExpectedVersion: version}); err != nil {
		t.Fatalf("idempotent retirement=%v", err)
	}
	if err := owner.QueryRowContext(ctx, `SELECT lifecycle_state,fence_epoch FROM managed_anime WHERE id=$1`, anime).Scan(&state, &epoch); err != nil {
		t.Fatal(err)
	}
	if state != "retired" || epoch != 2 {
		t.Fatalf("successful retirement=%s/%d", state, epoch)
	}
	var live int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE managed_anime_id=$1 AND state IN ('available','leased') AND external_effect_admitted_at IS NULL`, anime).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("missed unadmitted work=%d", live)
	}
}
