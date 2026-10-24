package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/disposabledb"
)

// The lazy expansion cache is the only place a torrent payload outlives the
// request that fetched it, so its read must be exactly the write: same bytes,
// same digest, same file projection. A miss is an expected outcome (the caller
// then fetches); a corrupt row must fail instead of feeding a downloader bytes
// the database can no longer attest.

func cachedSeedTorrentEntry(t *testing.T, locator string, payload []byte) catalogdomain.SeedTorrentCache {
	t.Helper()
	digest := catalogdomain.DigestBytes(payload)
	file := catalogdomain.TorrentFile{
		Ordinal: 0, SourcePath: "目录/<组>&剧集.mkv", Filename: "<组>&剧集.mkv",
		SizeBytes: 1000, MediaKind: "video", Language: "zh-Hans",
	}
	file.FileRef = catalogdomain.TorrentFileRef(digest, file.SourcePath)
	subtitle := catalogdomain.TorrentFile{
		Ordinal: 1, SourcePath: "Subs/ep1.jpn.ass", Filename: "ep1.jpn.ass",
		SizeBytes: 20, MediaKind: "subtitle",
	}
	subtitle.FileRef = catalogdomain.TorrentFileRef(digest, subtitle.SourcePath)
	files := []catalogdomain.TorrentFile{file, subtitle}
	projection, err := catalogdomain.CanonicalTorrentProjectionDigest(digest, "parser-v1", files)
	if err != nil {
		t.Fatal(err)
	}
	return catalogdomain.SeedTorrentCache{
		Locator: locator, Payload: payload, PayloadDigest: digest,
		FetchedAt: time.Now().UTC().Truncate(time.Microsecond),
		Manifest: catalogdomain.TorrentManifest{
			PayloadDigest: digest, ParserVersion: "parser-v1", TorrentVersion: "v1",
			TorrentName: "Show S1", InfoHashV1: strings.Repeat("a", 40),
			TotalSizeBytes: 1020, ProjectionDigest: projection, Files: files,
		},
	}
}

func TestSeedTorrentCacheReadMissIsNotAnError(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()

	entry, found, err := NewRepository(db).ReadSeedTorrentCache(context.Background(), "https://example.test/never-fetched.torrent")
	if err != nil {
		t.Fatalf("a cache miss must not be an error: %v", err)
	}
	if found {
		t.Fatalf("an unwritten locator reported a row: %+v", entry)
	}
}

func TestSeedTorrentCacheRoundTripsTheExactPayloadAndProjection(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	payload := []byte("d4:infod4:name8:show.mkv6:lengthi12345eee")
	written := cachedSeedTorrentEntry(t, "https://mikanani.me/Download/20261017/"+strings.Repeat("a", 40)+".torrent", payload)
	if err := repository.UpsertSeedTorrentCache(ctx, written); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	read, found, err := repository.ReadSeedTorrentCache(ctx, written.Locator)
	if err != nil || !found {
		t.Fatalf("read: found=%v err=%v", found, err)
	}
	if string(read.Payload) != string(payload) || read.PayloadDigest != written.PayloadDigest {
		t.Fatalf("payload drifted: %q vs %q", read.Payload, payload)
	}
	if read.Manifest.TorrentName != "Show S1" || read.Manifest.ProjectionDigest != written.Manifest.ProjectionDigest {
		t.Fatalf("manifest identity drifted: %+v", read.Manifest)
	}
	if read.Manifest.InfoHashV1 != written.Manifest.InfoHashV1 || read.Manifest.InfoHashV2 != "" {
		t.Fatalf("info hashes drifted: %+v", read.Manifest)
	}
	if read.Manifest.FileCount() != 2 {
		t.Fatalf("file count = %d, want 2", read.Manifest.FileCount())
	}
	if read.Manifest.Files[0].Language != "zh-Hans" || read.Manifest.Files[1].Language != "" {
		t.Fatalf("subtitle language drifted: %+v", read.Manifest.Files)
	}
	if read.Manifest.Files[0].FileRef != written.Manifest.Files[0].FileRef {
		t.Fatalf("file reference drifted: %+v", read.Manifest.Files[0])
	}
}

func TestSeedTorrentCacheUpsertReplacesTheSameLocator(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	locator := "https://mikanani.me/Download/20261017/" + strings.Repeat("b", 40) + ".torrent"
	if err := repository.UpsertSeedTorrentCache(ctx, cachedSeedTorrentEntry(t, locator, []byte("first payload"))); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := repository.UpsertSeedTorrentCache(ctx, cachedSeedTorrentEntry(t, locator, []byte("second payload"))); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM seed_torrent_cache WHERE locator = $1`, locator).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("locator has %d rows, want 1", rows)
	}
	read, _, err := repository.ReadSeedTorrentCache(ctx, locator)
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Payload) != "second payload" {
		t.Fatalf("payload = %q, want the latest write", read.Payload)
	}
}

func TestSeedTorrentCacheRefusesADigestThatDoesNotCoverItsBytes(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	// The domain guard already rejects this, so write the row directly to prove
	// the database itself keeps the invariant.
	// The row violates exactly one invariant. `files` must hold as many entries
	// as `file_count` (the table also enforces 1..20000), so a one-element array
	// keeps the file-count check satisfied and leaves the payload digest as the
	// only violated constraint: PostgreSQL reports the first violated CHECK in
	// constraint-name order, and an empty array would name the other one.
	_, err := db.ExecContext(ctx, `
INSERT INTO seed_torrent_cache(locator, payload, payload_digest, payload_size_bytes, torrent_version,
	info_hash_v1, torrent_name, total_size_bytes, file_count, projection_digest, parser_version, files, fetched_at)
VALUES ($1, $2, $3, $4, 'v1', $5, 'Show', 1, 1, $6, 'parser-v1', $7, CURRENT_TIMESTAMP)`,
		"https://example.test/tampered.torrent", []byte("payload"), catalogdomain.DigestBytes([]byte("other")), 7,
		strings.Repeat("a", 40), catalogdomain.DigestBytes([]byte("projection")), []byte(`[{}]`))
	if err == nil {
		t.Fatal("a digest that does not cover the payload was accepted")
	}
	if !strings.Contains(err.Error(), "seed_torrent_cache_payload_check") {
		t.Fatalf("unexpected rejection: %v", err)
	}
}
