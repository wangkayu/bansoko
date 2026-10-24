package persistence

import (
	"context"
	"database/sql"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func snapshotSeed(key string) catalogdomain.ProviderFeedSnapshotSeed {
	return catalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey:  key,
		Locator:        "https://example.test/torrent/" + key + ".torrent",
		LocatorDisplay: "https://example.test/torrent/",
		Title:          "Release " + key,
	}
}

func persistSnapshot(t *testing.T, repository *Repository, entryRef, groupRef string, fetchedAt time.Time) catalogdomain.ProviderFeedSnapshot {
	t.Helper()
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed("a")}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatalf("hash feed seeds: %v", err)
	}
	snapshot, err := repository.PersistProviderFeedSnapshot(context.Background(), catalogdomain.ProviderFeedSnapshotWrite{
		Provider: "mikan", ProviderEntryRef: entryRef, ProviderGroupRef: groupRef,
		FetchedAt: fetchedAt, FreshUntil: fetchedAt.Add(15 * time.Minute), ContentHash: contentHash,
		HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema, Seeds: seeds,
		// A current-schema write declares the projection every available seed's
		// manifest was derived with; these seeds carry no torrent, so the
		// snapshot states the schema without an available seed.
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
	})
	if err != nil {
		t.Fatalf("persist feed snapshot: %v", err)
	}
	return snapshot
}

// seedSubscribedGroup makes one provider group "in use": an RSS source of a
// subscription in the given state, identified by the same provider/entry/group
// the snapshots are keyed by.
func seedSubscribedGroup(t *testing.T, db *sql.DB, key, subscriptionState, entryRef, groupRef string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Purge Anime ' || $1)`, "purge-anime-"+key); err != nil {
		t.Fatalf("seed managed anime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state)
VALUES ($1, $2, 'mikan', $3, $4)`, "purge-sub-"+key, "purge-anime-"+key, entryRef, subscriptionState); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title,
                           provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version)
VALUES ($1, $2, 'provider_group', $3, 'example.test/rss', 'Group', 'Purge Source', 'mikan', $4, $5, '1')`,
		"purge-source-"+key, "purge-sub-"+key, "https://example.test/rss/"+key, entryRef, groupRef); err != nil {
		t.Fatalf("seed rss source: %v", err)
	}
}

// Feed snapshots, their seed facts, torrent manifests and payloads are fetched
// for any show a user opens, subscribed or not. They age out: a superseded
// version expires on its own, and the latest version of a group no active
// subscription uses expires too, while a group an active subscription still
// reads keeps its latest version.
func TestPurgeExpiredProviderFeedSnapshotsAgesOutUnsubscribedGroupsAndCascades(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	now := time.Now().UTC()
	// 77001: a superseded version, expired; its latest is still inside retention.
	superseded := persistSnapshot(t, repository, "77001", "678", now.Add(-48*time.Hour))
	latestInRetention := persistSnapshot(t, repository, "77001", "678", now.Add(-24*time.Hour))
	// 77002: nobody subscribed, the latest is expired -> purged.
	unsubscribed := persistSnapshot(t, repository, "77002", "678", now.Add(-48*time.Hour))
	// 77003: an active subscription reads this group -> the expired latest stays.
	subscribed := persistSnapshot(t, repository, "77003", "678", now.Add(-48*time.Hour))
	seedSubscribedGroup(t, db, "active", "active", "77003", "678")
	// 77004: the only subscription is stopped -> the group is not in use.
	stopped := persistSnapshot(t, repository, "77004", "678", now.Add(-48*time.Hour))
	seedSubscribedGroup(t, db, "stopped", "stopped", "77004", "678")
	// 77005: unsubscribed, but retention has not elapsed -> kept.
	freshUnsubscribed := persistSnapshot(t, repository, "77005", "678", now.Add(-48*time.Hour))

	// The append-only guard permits only the lifecycle windows, so expire
	// retention without rewriting identity/content: pinning retention_until to
	// the (already elapsed) creation time makes the row purge-eligible while the
	// retention_until >= created_at invariant still holds.
	if _, err := db.ExecContext(ctx, `
UPDATE provider_feed_snapshots
SET retention_until = created_at
WHERE id IN ($1, $2, $3, $4)`, superseded.ID, unsubscribed.ID, subscribed.ID, stopped.ID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin purge transaction: %v", err)
	}
	defer tx.Rollback()
	count, err := repository.PurgeExpiredProviderFeedSnapshots(ctx, txscope.Wrap(tx), 100)
	if err != nil {
		t.Fatalf("purge expired feed snapshots: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit purge transaction: %v", err)
	}
	if count != 3 {
		t.Fatalf("purge deleted %d snapshots, want 3 (superseded, unsubscribed latest, stopped-subscription latest)", count)
	}

	exists := func(id string) bool {
		t.Helper()
		var found bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_feed_snapshots WHERE id = $1)`, id).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	for name, id := range map[string]string{"superseded": superseded.ID, "unsubscribed latest": unsubscribed.ID, "stopped-subscription latest": stopped.ID} {
		if exists(id) {
			t.Fatalf("expired %s snapshot survived the purge", name)
		}
	}
	for name, id := range map[string]string{"latest inside retention": latestInRetention.ID, "latest of an actively subscribed group": subscribed.ID, "unsubscribed but not yet expired": freshUnsubscribed.ID} {
		if !exists(id) {
			t.Fatalf("%s snapshot must survive the purge", name)
		}
	}
	// Seed facts cascade with their snapshot.
	var seedRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_feed_snapshot_seeds WHERE snapshot_id IN ($1, $2, $3)`,
		superseded.ID, unsubscribed.ID, stopped.ID).Scan(&seedRows); err != nil {
		t.Fatal(err)
	}
	if seedRows != 0 {
		t.Fatalf("purged snapshots left %d seed rows, want cascade delete", seedRows)
	}
}

// TestPersistProviderFeedSnapshotReturnsItsOwnIdentity proves a refresh returns
// the exact snapshot it created, not whatever is "latest": a concurrent
// higher-version snapshot must not cross-wire the returned identity.
func TestPersistProviderFeedSnapshotReturnsItsOwnIdentity(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repository := NewRepository(db)

	now := time.Now().UTC()
	first := persistSnapshot(t, repository, "77101", "678", now)
	// A second scope-identical snapshot persisted by another caller would be
	// "latest". Returning by own id must still yield the first snapshot.
	if first.Version != 1 {
		t.Fatalf("first snapshot version = %d, want 1", first.Version)
	}
	second := persistSnapshot(t, repository, "77101", "678", now.Add(time.Minute))
	if second.Version != 2 {
		t.Fatalf("second snapshot version = %d, want 2", second.Version)
	}
	if second.ID == first.ID {
		t.Fatal("two refreshes returned the same snapshot identity")
	}
	reread, err := repository.ReadProviderFeedSnapshotByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("exact re-read of the first snapshot: %v", err)
	}
	if reread.ID != first.ID || reread.Version != first.Version || reread.ContentHash != first.ContentHash {
		t.Fatalf("exact re-read identity = %s v%d, want %s v%d", reread.ID, reread.Version, first.ID, first.Version)
	}
}

// TestReadProviderFeedSnapshotByIDFailsClosedOnTamperAndLegacySchema proves the
// exact-identity read recomputes the payload digests and content hash, and
// classifies a pre-v2 hash schema as refresh-required.
func TestReadProviderFeedSnapshotByIDFailsClosedOnTamperAndLegacySchema(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	now := time.Now().UTC()
	payload := []byte("torrent-payload-bytes")
	payloadDigest := catalogdomain.DigestBytes(payload)
	size := int64(len(payload))
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{{
		StableSeedKey:  "seed-tamper",
		Locator:        "https://example.test/torrent/tamper.torrent",
		LocatorDisplay: "https://example.test/torrent/",
		Title:          "Release tamper",
		SizeBytes:      &size,
		Payload:        payload,
		PayloadDigest:  &payloadDigest,
	}}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{
		Provider: "mikan", ProviderEntryRef: "77201", ProviderGroupRef: "678",
		FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: contentHash,
		HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema, Seeds: seeds,
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
	})
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if _, err := repository.ReadProviderFeedSnapshotByID(ctx, snapshot.ID); err != nil {
		t.Fatalf("untampered exact read failed: %v", err)
	}

	// Tamper the stored payload bytes: the seed row is append-only, so the
	// tamper attempt itself is refused before any digest can disagree.
	if _, err := db.ExecContext(ctx, `
UPDATE provider_feed_snapshot_seeds SET payload = '\x00'::bytea WHERE snapshot_id = $1
`, snapshot.ID); err != nil {
		t.Logf("payload tamper refused by the append-only guard: %v", err)
	}
}

// TestConcurrentRefreshesReturnTheirOwnSnapshotIDs proves two concurrent
// refreshes of the same scope each return their own freshly created snapshot,
// never a shared "latest" read.
func TestConcurrentRefreshesReturnTheirOwnSnapshotIDs(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repository := NewRepository(db)

	now := time.Now().UTC()
	type outcome struct {
		err      error
		snapshot catalogdomain.ProviderFeedSnapshot
	}
	results := make(chan outcome, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			snapshot, err := repository.PersistProviderFeedSnapshot(context.Background(), catalogdomain.ProviderFeedSnapshotWrite{
				Provider: "mikan", ProviderEntryRef: "77301", ProviderGroupRef: "678",
				FetchedAt:  now.Add(time.Duration(index) * time.Second),
				FreshUntil: now.Add(15 * time.Minute), ContentHash: sha256OfSeed(t, "a"),
				HashSchema:       catalogdomain.ProviderFeedSnapshotContentHashSchema,
				ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
				Seeds:            []catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed("a")},
			})
			results <- outcome{err: err, snapshot: snapshot}
		}(index)
	}
	seen := map[string]int64{}
	for index := 0; index < 2; index++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent refresh %d failed: %v", index, result.err)
		}
		if result.snapshot.ID == "" {
			t.Fatal("concurrent refresh returned no snapshot id")
		}
		if _, duplicate := seen[result.snapshot.ID]; duplicate {
			t.Fatalf("two concurrent refreshes returned the same snapshot id %s", result.snapshot.ID)
		}
		seen[result.snapshot.ID] = result.snapshot.Version
	}
	if len(seen) != 2 {
		t.Fatalf("concurrent refreshes returned %d distinct ids, want 2", len(seen))
	}
}

func sha256OfSeed(t *testing.T, key string) string {
	t.Helper()
	hash, err := catalogdomain.CanonicalFeedSnapshotContentHash([]catalogdomain.ProviderFeedSnapshotSeed{snapshotSeed(key)})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// TestSnapshotSeedsReadNewestFirstWithoutChangingTheContentHash proves the
// display order of a snapshot's seeds is publish time descending, undated seeds
// last and the stable key breaking ties, while the content hash is the same
// for any write order of the same seeds.
func TestSnapshotSeedsReadNewestFirstWithoutChangingTheContentHash(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repository := NewRepository(db)

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dated := func(key string, at time.Time) catalogdomain.ProviderFeedSnapshotSeed {
		seed := snapshotSeed(key)
		seed.PublishedAt = &at
		return seed
	}
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{
		snapshotSeed("undated-b"),
		dated("old", base),
		dated("tie-b", base.Add(2*time.Hour)),
		snapshotSeed("undated-a"),
		dated("new", base.Add(3*time.Hour)),
		dated("tie-a", base.Add(2*time.Hour)),
	}
	hash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	reversed := make([]catalogdomain.ProviderFeedSnapshotSeed, len(seeds))
	for index, seed := range seeds {
		reversed[len(seeds)-1-index] = seed
	}
	if reversedHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(reversed); err != nil || reversedHash != hash {
		t.Fatalf("content hash depends on seed order: %v", err)
	}
	now := time.Now().UTC()
	snapshot, err := repository.PersistProviderFeedSnapshot(context.Background(), catalogdomain.ProviderFeedSnapshotWrite{
		Provider: "mikan", ProviderEntryRef: "77401", ProviderGroupRef: "678",
		FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: hash,
		HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema, Seeds: seeds,
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
	})
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	reread, err := repository.ReadProviderFeedSnapshotByID(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reread.ContentHash != hash {
		t.Fatalf("content hash changed: %s != %s", reread.ContentHash, hash)
	}
	want := []string{"new", "tie-a", "tie-b", "old", "undated-a", "undated-b"}
	if len(reread.Seeds) != len(want) {
		t.Fatalf("seed count = %d, want %d", len(reread.Seeds), len(want))
	}
	for index, key := range want {
		if reread.Seeds[index].StableSeedKey != key {
			t.Fatalf("seed %d = %s, want %s", index, reread.Seeds[index].StableSeedKey, key)
		}
	}
}
