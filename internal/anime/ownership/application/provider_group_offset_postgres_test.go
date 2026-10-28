package application

import (
	"context"
	"errors"
	"testing"

	apperrors "bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/subscription/groupsource"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// ownershipNoConnections lists no provider connection, so the provider reader
// endpoint falls back to the built-in default.
type ownershipNoConnections struct{}

func (ownershipNoConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return nil, nil
}

// pendingProviderGroupAnime subscribes a not-started Mikan anime that lists one
// group in its subgroups section and has no RSS link yet.
func pendingProviderGroupAnime(t *testing.T, service *ownershipTestServices, key string) generated.SubscribeResult {
	t.Helper()
	draft := ownershipProviderDraft(t, "3000", "Pending Offset Show")
	draft.Subgroups.Entries = []generated.SubgroupScoreEntry{
		{Provider: generated.ProviderKindMikan, GroupRef: "202", DisplayName: "Group 202", SelectionMode: generated.SelectionModeAuto, EffectiveSelected: true, Score: 50},
	}
	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(false)}, key)
	if err != nil {
		t.Fatalf("subscribe pending provider anime: %v", err)
	}
	if len(result.RssSources) != 0 {
		t.Fatalf("a pending provider anime must have no link yet: %+v", result.RssSources)
	}
	return result
}

func TestSetProviderGroupOffsetCreatesTheLinkBeforeStartAndDiscoveryKeepsTheOffset(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribed := pendingProviderGroupAnime(t, service, "group-offset-create-0001")
	ctx := context.Background()
	animeID := subscribed.ManagedAnime.Id

	created, err := service.SetProviderGroupOffset(ctx, animeID, "mikan", "202", ProviderGroupOffsetInput{ExpectedVersion: 0, EpisodeOffset: -48})
	if err != nil {
		t.Fatalf("set offset without a link: %v", err)
	}
	if created.EpisodeOffset != -48 {
		t.Fatalf("created link offset = %d, want -48", created.EpisodeOffset)
	}
	var rows, runtimes, work int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources WHERE subscription_id = $1`, subscribed.Subscription.Id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_source_runtime WHERE rss_source_id = $1`, created.ID).Scan(&runtimes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_id = $1`, created.ID).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || runtimes != 1 || work != 0 {
		t.Fatalf("link rows=%d runtimes=%d work items=%d, want 1/1/0: a pending link is created without any work", rows, runtimes, work)
	}

	// A later save goes through the existing row with its version.
	updated, err := service.SetProviderGroupOffset(ctx, animeID, "mikan", "202", ProviderGroupOffsetInput{ExpectedVersion: created.Version, EpisodeOffset: 3})
	if err != nil {
		t.Fatalf("update through the existing link: %v", err)
	}
	if updated.ID != created.ID || updated.EpisodeOffset != 3 || updated.Version <= created.Version {
		t.Fatalf("update = %+v, want the same row at offset 3 with a newer version", updated)
	}
	_, err = service.SetProviderGroupOffset(ctx, animeID, "mikan", "202", ProviderGroupOffsetInput{ExpectedVersion: created.Version, EpisodeOffset: 4})
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale version error = %v, want a version conflict", err)
	}
	// expected_version 0 means "no link yet": it must not overwrite an existing link.
	_, err = service.SetProviderGroupOffset(ctx, animeID, "mikan", "202", ProviderGroupOffsetInput{ExpectedVersion: 0, EpisodeOffset: 4})
	if !errors.As(err, &conflict) {
		t.Fatalf("version 0 over an existing link error = %v, want a version conflict", err)
	}

	// Discovery hits the same row: it refreshes locator and provenance and keeps the offset.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	sourceID, inserted, err := groupsource.UpsertTx(ctx, tx, groupsource.Source{
		SubscriptionID: subscribed.Subscription.Id, Provider: "mikan", CatalogEntryRef: "3000", GroupExternalID: "202",
		DisplayName: "Group 202", AnimeTitle: "Pending Offset Show",
		RSSURL: "https://mikanani.me/RSS/Bangumi?bangumiId=3000&subgroupid=202", SnapshotVersion: "v7",
	})
	if err != nil {
		t.Fatalf("discovery upsert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if inserted || sourceID != created.ID {
		t.Fatalf("discovery inserted=%t id=%s, want the pending link %s", inserted, sourceID, created.ID)
	}
	var offset int
	var snapshotVersion string
	if err := db.QueryRowContext(ctx, `SELECT episode_offset, discovery_snapshot_version FROM rss_sources WHERE id = $1`, created.ID).Scan(&offset, &snapshotVersion); err != nil {
		t.Fatal(err)
	}
	if offset != 3 || snapshotVersion != "v7" {
		t.Fatalf("after discovery offset=%d snapshot=%s, want the kept offset 3 and the discovery snapshot v7", offset, snapshotVersion)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources WHERE subscription_id = $1`, subscribed.Subscription.Id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("discovery left %d link rows, want the single pending one", rows)
	}
}

func TestSetProviderGroupOffsetThroughADuplicatedPendingCopy(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	first := pendingProviderGroupAnime(t, service, "group-offset-copy-0001")
	// A second instance of the same entry is a copy with its own subgroups section and its own link.
	draft := ownershipProviderDraft(t, "3000", "Pending Offset Show")
	draft.InstanceLabel = new("copy")
	draft.Subgroups.Entries = []generated.SubgroupScoreEntry{
		{Provider: generated.ProviderKindMikan, GroupRef: "202", DisplayName: "Group 202", SelectionMode: generated.SelectionModeAuto, EffectiveSelected: true, Score: 50},
	}
	second, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(false)}, "group-offset-copy-0002")
	if err != nil {
		t.Fatalf("subscribe copy: %v", err)
	}
	ctx := context.Background()
	firstLink, err := service.SetProviderGroupOffset(ctx, first.ManagedAnime.Id, "mikan", "202", ProviderGroupOffsetInput{EpisodeOffset: 5})
	if err != nil {
		t.Fatal(err)
	}
	secondLink, err := service.SetProviderGroupOffset(ctx, second.ManagedAnime.Id, "mikan", "202", ProviderGroupOffsetInput{EpisodeOffset: -2})
	if err != nil {
		t.Fatal(err)
	}
	if firstLink.ID == secondLink.ID || firstLink.EpisodeOffset != 5 || secondLink.EpisodeOffset != -2 {
		t.Fatalf("copies must keep independent links: first=%+v second=%+v", firstLink, secondLink)
	}
}

func TestSetProviderGroupOffsetRefusesUnlistedGroupsAndRetiredInstances(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribed := pendingProviderGroupAnime(t, service, "group-offset-refuse-0001")
	ctx := context.Background()
	animeID := subscribed.ManagedAnime.Id

	for name, call := range map[string]func() error{
		"group not listed": func() error {
			_, err := service.SetProviderGroupOffset(ctx, animeID, "mikan", "999", ProviderGroupOffsetInput{EpisodeOffset: 1})
			return err
		},
		"other provider": func() error {
			_, err := service.SetProviderGroupOffset(ctx, animeID, "anibt", "202", ProviderGroupOffsetInput{EpisodeOffset: 1})
			return err
		},
	} {
		var business *apperrors.Business
		if err := call(); !errors.As(err, &business) || business.Code != "PROVIDER_GROUP_NOT_LISTED" {
			t.Fatalf("%s error = %v, want PROVIDER_GROUP_NOT_LISTED", name, err)
		}
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources WHERE subscription_id = $1`, subscribed.Subscription.Id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a refused save created %d link rows", rows)
	}

	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	_, err := service.SetProviderGroupOffset(ctx, animeID, "mikan", "202", ProviderGroupOffsetInput{EpisodeOffset: 1})
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "MANAGED_ANIME_RETIRED" {
		t.Fatalf("retired instance error = %v, want MANAGED_ANIME_RETIRED", err)
	}
}
