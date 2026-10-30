package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/disposabledb"
)

func TestJackettSearchSnapshotPersistsClosedSourceSite(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	now := time.Now().UTC()
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed("jackett-seed")}
	seeds[0].SourceSite = &acquisitiondomain.SeedSourceSite{ID: "example-indexer", Name: "Example Tracker", Type: "private"}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	search, err := repository.PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{
		Provider: "jackett", ProviderEntryRef: "search-scope:" + strings.Repeat("1", 64),
		ProviderGroupRef: "search-query:" + strings.Repeat("2", 64), SnapshotKind: catalogdomain.FeedSnapshotKindSearch,
		SearchParserVersion: "jackett-torznab-search-v1", FetchedAt: now, FreshUntil: now.Add(time.Hour),
		ContentHash: contentHash, HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema,
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema, Seeds: seeds,
	})
	if err != nil {
		t.Fatalf("persist Jackett search snapshot: %v", err)
	}
	read, err := repository.ReadProviderFeedSnapshotByID(ctx, search.ID)
	if err != nil {
		t.Fatalf("read Jackett search snapshot: %v", err)
	}
	if read.Provider != "jackett" || read.SnapshotKind != catalogdomain.FeedSnapshotKindSearch || len(read.Seeds) != 1 {
		t.Fatalf("unexpected Jackett snapshot: %+v", read)
	}
	site := read.Seeds[0].SourceSite
	if site == nil || site.ID != "example-indexer" || site.Name != "Example Tracker" || site.Type != "private" {
		t.Fatalf("source site did not round trip: %+v", site)
	}
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshot_seeds SET source_site = '{"id":"example","name":"Example","type":"unknown"}' WHERE snapshot_id = $1`, search.ID); err == nil {
		t.Fatal("source_site accepted a type outside the closed set")
	}
	feed := persistSnapshotKind(t, repository, "anibt", "entry-jackett", "group-jackett", catalogdomain.FeedSnapshotKindProviderFeed, now)
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshots SET provider = 'jackett' WHERE id = $1`, feed.ID); err == nil {
		t.Fatal("a provider feed snapshot accepted Jackett")
	}
}
