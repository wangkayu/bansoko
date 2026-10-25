package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func persistSnapshotKind(t *testing.T, repository *Repository, provider, entryRef, groupRef string, kind catalogdomain.FeedSnapshotKind, fetchedAt time.Time) catalogdomain.ProviderFeedSnapshot {
	t.Helper()
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed(string(kind) + "-seed")}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatalf("hash search seeds: %v", err)
	}
	write := catalogdomain.ProviderFeedSnapshotWrite{
		Provider: provider, ProviderEntryRef: entryRef, ProviderGroupRef: groupRef, SnapshotKind: kind,
		SearchParserVersion: map[catalogdomain.FeedSnapshotKind]string{catalogdomain.FeedSnapshotKindSearch: "search-parser-v1"}[kind],
		FetchedAt:           fetchedAt, FreshUntil: fetchedAt.Add(time.Hour), ContentHash: contentHash,
		HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
		Seeds: seeds,
	}
	var snapshot catalogdomain.ProviderFeedSnapshot
	if kind == catalogdomain.FeedSnapshotKindCustom {
		snapshot, err = repository.PersistCustomFeedSnapshot(context.Background(), write, func(context.Context, txscope.Tx) error { return nil })
	} else {
		snapshot, err = repository.PersistProviderFeedSnapshot(context.Background(), write)
	}
	if err != nil {
		t.Fatalf("persist %s snapshot: %v", kind, err)
	}
	return snapshot
}

// Migration 056 is exercised through the real publication function and read
// queries: search survives as its own kind, the repository's post-publication
// fresh-window update really produces one hour, and SQL owns the 24-hour
// retention. Purging an expired latest search does not purge an expired latest
// custom snapshot, whose superseded-only rule remains unchanged.
func TestSearchSnapshotRoundTripAndLatestRetention(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	now := time.Now().UTC()
	search := persistSnapshotKind(t, repository, "mikan",
		"search-scope:"+strings.Repeat("a", 64), "search-query:"+strings.Repeat("b", 64),
		catalogdomain.FeedSnapshotKindSearch, now)
	if search.SnapshotKind != catalogdomain.FeedSnapshotKindSearch {
		t.Fatalf("round-trip kind = %q, want search", search.SnapshotKind)
	}
	read, err := repository.ReadProviderFeedSnapshotByID(ctx, search.ID)
	if err != nil {
		t.Fatalf("read search snapshot: %v", err)
	}
	if err := read.VerifyKind(catalogdomain.FeedSnapshotKindSearch); err != nil {
		t.Fatalf("read kind: %v", err)
	}
	if read.SearchParserVersion != "search-parser-v1" {
		t.Fatalf("round-trip parser = %q", read.SearchParserVersion)
	}
	if got := read.RetentionUntil.Sub(read.FetchedAt); got < 24*time.Hour-time.Second || got > 24*time.Hour+time.Second {
		t.Fatalf("round-trip retention = %v", got)
	}
	if len(read.Seeds) != 1 || read.Seeds[0].StableSeedKey != "search-seed" {
		t.Fatalf("read seeds = %+v", read.Seeds)
	}
	var freshSeconds, retentionSeconds float64
	if err := db.QueryRowContext(ctx, `
SELECT EXTRACT(EPOCH FROM fresh_until - fetched_at),
       EXTRACT(EPOCH FROM retention_until - fetched_at)
FROM provider_feed_snapshots WHERE id = $1`, search.ID).Scan(&freshSeconds, &retentionSeconds); err != nil {
		t.Fatal(err)
	}
	if freshSeconds < 3599 || freshSeconds > 3601 {
		t.Fatalf("persisted fresh window = %.3f seconds, want 3600", freshSeconds)
	}
	if retentionSeconds < 86399 || retentionSeconds > 86401 {
		t.Fatalf("persisted retention = %.3f seconds, want 86400", retentionSeconds)
	}

	retainedSearch := persistSnapshotKind(t, repository, "anibt",
		"search-scope:"+strings.Repeat("c", 64), "search-query:"+strings.Repeat("d", 64),
		catalogdomain.FeedSnapshotKindSearch, now)
	custom := persistSnapshotKind(t, repository, "custom",
		"custom-source:"+strings.Repeat("e", 64), "custom-group:"+strings.Repeat("f", 64),
		catalogdomain.FeedSnapshotKindCustom, now)
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshots SET retention_until = created_at WHERE id IN ($1, $2)`, search.ID, custom.ID); err != nil {
		t.Fatalf("expire lifecycle fixtures: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	purged, err := repository.PurgeExpiredProviderFeedSnapshots(ctx, txscope.Wrap(tx), 100)
	if err != nil {
		tx.Rollback()
		t.Fatalf("purge search snapshots: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("purged %d snapshots, want only the expired search", purged)
	}
	exists := func(id string) bool {
		t.Helper()
		var found bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_feed_snapshots WHERE id = $1)`, id).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	if exists(search.ID) {
		t.Fatal("expired latest search snapshot survived purge")
	}
	if !exists(retainedSearch.ID) || !exists(custom.ID) {
		t.Fatalf("retention drifted: unexpired search=%t, expired latest custom=%t", exists(retainedSearch.ID), exists(custom.ID))
	}
}
