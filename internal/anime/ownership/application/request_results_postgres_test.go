package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/workitems"
)

type ownershipResultLogger struct {
	observabilityport.NopRuntimeLogger
	infos []observabilityport.RuntimeEvent
}

func (l *ownershipResultLogger) Info(_ context.Context, e observabilityport.RuntimeEvent) {
	l.infos = append(l.infos, e)
}

func TestRetirementAndLocalCleanupLogOnlyFirstCommittedResult(t *testing.T) {
	db := animetest.OpenTestDB(t)
	s := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	sub := ownershipSubscribeTestDraft(t, s, "subscribe-key-request-results")
	animeID := sub.ManagedAnime.Id
	logger := &ownershipResultLogger{}
	s.SetLogger(logger)
	for i := 0; i < 2; i++ {
		if err := s.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: sub.Subscription.Version}); err != nil {
			t.Fatal(err)
		}
	}
	_, epoch := lifecycleForTest(t, db, animeID)
	for i := 0; i < 2; i++ {
		if err := s.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-request-result"); err != nil {
			t.Fatal(err)
		}
	}
	if len(logger.infos) != 2 || logger.infos[0].ReasonCode != "subscription_retired" || logger.infos[1].ReasonCode != "local_cleanup_performed" ||
		logger.infos[0].OwnerID != animeID || logger.infos[1].OwnerID != animeID {
		t.Fatalf("result logs = %+v", logger.infos)
	}
}

func TestMetadataRefreshPublishesWhenOnlyBangumiCacheWasRefreshed(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	s, err := NewService(db, Dependencies{WorkItems: store, EventHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh-partial', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season)
VALUES ('anime-refresh-partial', '10', '42', 'tv', 1)`); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("metadata refresh failed")
	for _, test := range []struct {
		name      string
		refresher countingMetadataRefresher
	}{
		{"previous provider wrote", countingMetadataRefresher{err: failure, failProvider: "tmdb"}},
		{"failing provider wrote", countingMetadataRefresher{err: failure, failProvider: "bgm", partial: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			published, cancel := hub.Subscribe()
			defer cancel()
			s.SetMetadataWorkRefresher(&test.refresher)
			if _, err := s.RefreshAnimeMetadataApplication(ctx, "anime-refresh-partial"); !errors.Is(err, failure) {
				t.Fatalf("refresh returned %v, want %v", err, failure)
			}
			select {
			case event := <-published:
				if event.Target != events.TargetRSS || event.ManagedAnimeID != "anime-refresh-partial" {
					t.Fatalf("invalidation = %+v", event)
				}
			default:
				t.Fatal("Bangumi cache refreshed without invalidating dependent views")
			}
		})
	}
}
