package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/events"
	observabilityapplication "bansoko/internal/observability/application"
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

	reader, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{"business"}
	q, view := "Test Show", "primary"
	page, err := reader.ListRecords(ctx, observabilityapplication.RecordQuery{Kind: &kinds, Q: &q, EventView: &view})
	if err != nil || len(page.Records) != 2 {
		t.Fatalf("retained operation records = %+v, err = %v", page.Records, err)
	}
	seen := map[string]bool{}
	for _, rec := range page.Records {
		var wire struct {
			Entry struct {
				SubscriptionID *string `json:"subscription_id"`
				ManagedAnimeID *string `json:"managed_anime_id"`
				SubjectID      string  `json:"subject_id"`
				SubjectDisplay string  `json:"subject_display"`
				MessageCode    string  `json:"message_code"`
				Source         string  `json:"source"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(rec.Raw, &wire); err != nil {
			t.Fatal(err)
		}
		e := wire.Entry
		if e.SubscriptionID != nil || e.ManagedAnimeID != nil || e.SubjectID != animeID || e.SubjectDisplay != q || e.Source != "manual" {
			t.Fatalf("wrong operation projection: %+v", e)
		}
		seen[e.MessageCode] = true
	}
	if !seen["subscription_retired"] || !seen["local_records_cleaned"] {
		t.Fatalf("operation message codes = %v", seen)
	}
}

func TestManualOperationBatchCountsOnlyCommittedTransitions(t *testing.T) {
	db := animetest.OpenTestDB(t)
	s := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	first := ownershipSubscribeTestDraft(t, s, "subscribe-manual-batch-first")
	second := ownershipSubscribeTestDraft(t, s, "subscribe-manual-batch-second")
	batchID := new("manual-operation-batch-test")
	for _, target := range []struct {
		id      string
		version int64
	}{
		{first.ManagedAnime.Id, first.Subscription.Version},
		{second.ManagedAnime.Id, second.Subscription.Version},
	} {
		input := SubscriptionDeleteInput{ExpectedVersion: target.version, OperationBatchID: batchID}
		for range 2 {
			if err := s.DeleteAnimeSubscription(ctx, target.id, input); err != nil {
				t.Fatal(err)
			}
		}
	}
	animeID := first.ManagedAnime.Id
	_, epoch := lifecycleForTest(t, db, animeID)
	input := localCleanupInput(epoch)
	input.OperationBatchID = batchID
	for range 2 {
		if err := s.CleanupAnimeLocalData(ctx, animeID, input, "cleanup-manual-batch-first"); err != nil {
			t.Fatal(err)
		}
	}
	secondID := second.ManagedAnime.Id
	_, secondEpoch := lifecycleForTest(t, db, secondID)
	stale := localCleanupInput(secondEpoch + 1)
	stale.OperationBatchID = batchID
	if err := s.CleanupAnimeLocalData(ctx, secondID, stale, "cleanup-manual-batch-stale"); err == nil {
		t.Fatal("cleanup with the wrong lifecycle epoch committed")
	}
	for kind, want := range map[string]int{"subscription_retired": 2, "local_records_cleaned": 1} {
		var rows, count int
		if err := db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum((details->>'item_count')::integer), 0)
FROM subscription_log_entries WHERE subject_type = 'operation_batch' AND subject_id = $1 AND kind = $2`, *batchID, kind).
			Scan(&rows, &count); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || count != want {
			t.Fatalf("%s: rows=%d, committed=%d; want 1, %d", kind, rows, count, want)
		}
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
