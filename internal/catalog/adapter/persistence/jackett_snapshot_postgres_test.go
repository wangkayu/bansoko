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
	seeds[0].SourceGUID = "tracker-entry-123"
	seeds[0].DetailsURL = "https://tracker.example.test/details/123"
	seeds[0].SourceSite = &acquisitiondomain.SeedSourceSite{ID: "example-indexer", Name: "Example Tracker", Type: "semi-private"}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	search, err := repository.PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{
		Provider: "jackett", ProviderEntryRef: "search-scope:" + strings.Repeat("1", 64),
		ProviderGroupRef: "search-query:" + strings.Repeat("2", 64), SnapshotKind: catalogdomain.FeedSnapshotKindSearch,
		SearchParserVersion: "jackett-torznab-search-v2", FetchedAt: now, FreshUntil: now.Add(time.Hour),
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
	if read.Seeds[0].SourceGUID != seeds[0].SourceGUID || read.Seeds[0].DetailsURL != seeds[0].DetailsURL {
		t.Fatalf("source page did not round trip: %+v", read.Seeds[0])
	}
	site := read.Seeds[0].SourceSite
	if site == nil || site.ID != "example-indexer" || site.Name != "Example Tracker" || site.Type != "semi-private" {
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

func TestJackettSearchSnapshotPersistsPTFactsOutsideContentHash(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	now := time.Now().UTC()

	// The hash is blind to the PT display facts: the same seed with and
	// without them hashes identically, so a snapshot published before the
	// columns existed still verifies.
	seeders, leechers := int64(17), int64(3)
	downloadFactor, uploadFactor := 0.0, 2.0
	plain := []catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed("jackett-pt")}
	enriched := []catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed("jackett-pt")}
	enriched[0].Seeders = &seeders
	enriched[0].Leechers = &leechers
	enriched[0].DownloadFactor = &downloadFactor
	enriched[0].UploadFactor = &uploadFactor
	enriched[0].Categories = []acquisitiondomain.SeedCategory{{ID: "5071", Name: "Anime/RAW"}}
	plainHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(plain)
	if err != nil {
		t.Fatal(err)
	}
	enrichedHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(enriched)
	if err != nil {
		t.Fatal(err)
	}
	if plainHash != enrichedHash {
		t.Fatal("PT facts leaked into the canonical content hash")
	}

	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(enriched)
	if err != nil {
		t.Fatal(err)
	}
	search, err := repository.PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{
		Provider: "jackett", ProviderEntryRef: "search-scope:" + strings.Repeat("3", 64),
		ProviderGroupRef: "search-query:" + strings.Repeat("4", 64), SnapshotKind: catalogdomain.FeedSnapshotKindSearch,
		SearchParserVersion: "jackett-torznab-search-v2", FetchedAt: now, FreshUntil: now.Add(time.Hour),
		ContentHash: contentHash, HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema,
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema, Seeds: enriched,
	})
	if err != nil {
		t.Fatalf("persist Jackett PT snapshot: %v", err)
	}
	read, err := repository.ReadProviderFeedSnapshotByID(ctx, search.ID)
	if err != nil {
		t.Fatalf("read Jackett PT snapshot: %v", err)
	}
	fact := read.Seeds[0]
	if fact.Seeders == nil || *fact.Seeders != 17 || fact.Leechers == nil || *fact.Leechers != 3 {
		t.Fatalf("swarm counts did not round trip: %+v", fact)
	}
	if fact.DownloadFactor == nil || *fact.DownloadFactor != 0.0 || fact.UploadFactor == nil || *fact.UploadFactor != 2.0 {
		t.Fatalf("billing factors did not round trip: %+v", fact)
	}
	if len(fact.Categories) != 1 || fact.Categories[0].ID != "5071" || fact.Categories[0].Name != "Anime/RAW" {
		t.Fatalf("categories did not round trip: %+v", fact.Categories)
	}

	// Absent stays absent: a seed without PT facts reads back with nil facts,
	// never a zero or a 1.
	plainRead := persistSnapshot(t, repository, "entry-pt-absent", "group-pt-absent", now)
	found, err := repository.ReadProviderFeedSnapshotByID(ctx, plainRead.ID)
	if err != nil {
		t.Fatalf("read plain snapshot: %v", err)
	}
	absent := found.Seeds[0]
	if absent.Seeders != nil || absent.Leechers != nil || absent.DownloadFactor != nil || absent.UploadFactor != nil || len(absent.Categories) != 0 {
		t.Fatalf("absent PT facts were normalized: %+v", absent)
	}

	// The owner function and the column guard both refuse a negative factor.
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshot_seeds SET download_factor = -1 WHERE snapshot_id = $1`, search.ID); err == nil {
		t.Fatal("a negative download factor was accepted")
	}
}
