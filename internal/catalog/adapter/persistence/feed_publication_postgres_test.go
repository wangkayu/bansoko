package persistence

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	generated "bansoko/internal/catalog/adapter/persistence/generated"
	domain "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/disposabledb"
)

func wholeFeedInput(t *testing.T) domain.ProviderFeedSnapshotWrite {
	t.Helper()
	payload := []byte("exact torrent payload")
	digest := domain.DigestBytes(payload)
	size := int64(len(payload))
	file := domain.TorrentFile{Ordinal: 0, SourcePath: "目录/<组>&剧集.mkv", Filename: "<组>&剧集.mkv", SizeBytes: 1000, MediaKind: "video", Language: "jpn"}
	file.FileRef = domain.TorrentFileRef(digest, file.SourcePath)
	projection, err := domain.CanonicalTorrentProjectionDigest(digest, "parser-v1", []domain.TorrentFile{file})
	if err != nil {
		t.Fatal(err)
	}
	filename := "  show.torrent  "
	published := time.Date(2026, 10, 5, 10, 0, 0, 123000000, time.UTC)
	code, reason := "torrent_unavailable", "upstream failed"
	seeds := []domain.ProviderFeedSnapshotSeed{
		{StableSeedKey: "<key>& ", Locator: "https://example.test/a?x=1&y=2", LocatorDisplay: "example.test/a", Title: "<组> & 剧集", PublishedAt: &published, SizeBytes: &size,
			Payload: payload, PayloadDigest: &digest, TorrentFilename: &filename, TorrentState: "available", Manifest: &domain.TorrentManifest{PayloadDigest: digest, ParserVersion: "parser-v1", TorrentVersion: "v1", TorrentName: "show", InfoHashV1: strings.Repeat("a", 40), TotalSizeBytes: 1000, ProjectionDigest: projection, Files: []domain.TorrentFile{file}}},
		{StableSeedKey: "unavailable", Locator: "https://example.test/missing", LocatorDisplay: "example.test/missing", Title: "Unavailable", TorrentState: "unavailable", TorrentErrorCode: &code, TorrentErrorReason: &reason},
	}
	hash, err := domain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return domain.ProviderFeedSnapshotWrite{Provider: "mikan", ProviderEntryRef: "123", ProviderGroupRef: "678", FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: hash, HashSchema: domain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: domain.TorrentManifestProjectionSchema, Seeds: seeds}
}
func mutateSeedDeclaration(t *testing.T, raw json.RawMessage, fn func(map[string]any)) json.RawMessage {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	fn(rows[0])
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mutateNestedDeclaration mutates a seed's nested manifest object or the first
// element of its file projection, the leaves that used to live in their own
// arrays. The manifest is one object; the files are a list.
func mutateNestedDeclaration(t *testing.T, seed map[string]any, key string, fn func(map[string]any)) {
	t.Helper()
	leaf, ok := seed[key].(map[string]any)
	if !ok {
		rows, isList := seed[key].([]any)
		if !isList || len(rows) == 0 {
			t.Fatalf("seed declaration has no %s leaf: %+v", key, seed)
		}
		if leaf, ok = rows[0].(map[string]any); !ok {
			t.Fatalf("seed %s element is not an object: %+v", key, rows[0])
		}
	}
	fn(leaf)
}
func TestFeedPublicationRoundTripPreservesWholeCanonicalGraph(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repo := NewRepository(db)
	input := wholeFeedInput(t)
	snapshot, err := repo.PersistProviderFeedSnapshot(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ContentHash != input.ContentHash || len(snapshot.Seeds) != 2 {
		t.Fatalf("whole graph lost: %+v", snapshot)
	}
	if err := snapshot.VerifyContentHash(); err != nil {
		t.Fatal(err)
	}
	var available bool
	for _, seed := range snapshot.Seeds {
		if seed.Manifest != nil {
			available = true
			if string(seed.Payload) != string(input.Seeds[0].Payload) || seed.Manifest.ProjectionDigest != input.Seeds[0].Manifest.ProjectionDigest {
				t.Fatal("blob/projection changed")
			}
		}
	}
	if !available {
		t.Fatal("available manifest missing")
	}
}
func TestFeedPublicationRejectsBrokenSetsAtomically(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*generated.PublishProviderFeedSnapshotParams)
	}{
		{"bad content hash", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.ContentHash = "sha256:" + strings.Repeat("b", 64)
		}},
		{"payload mismatch", func(p *generated.PublishProviderFeedSnapshotParams) { p.Payloads[0] = []byte("other bytes") }},
		{"missing payload", func(p *generated.PublishProviderFeedSnapshotParams) { p.Payloads = [][]byte{} }},
		{"unused payload", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Payloads = append(p.Payloads, []byte("unused"))
		}},
		{"wrong payload index", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["payload_index"] = 0 })
		}},
		{"missing manifest", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["manifest"] = nil })
		}},
		{"missing files", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["files"] = []any{} })
		}},
		{"bad manifest digest", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) {
				mutateNestedDeclaration(t, m, "manifest", func(n map[string]any) { n["projection_digest"] = "sha256:" + strings.Repeat("b", 64) })
			})
		}},
		{"wrong size total", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) {
				mutateNestedDeclaration(t, m, "manifest", func(n map[string]any) { n["total_size_bytes"] = 999 })
			})
		}},
		{"ordinal gap", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) {
				mutateNestedDeclaration(t, m, "files", func(n map[string]any) { n["ordinal"] = 1 })
			})
		}},
		{"unsafe path", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) {
				mutateNestedDeclaration(t, m, "files", func(n map[string]any) { n["source_path"] = "../escape.mkv" })
			})
		}},
		{"wrong file identity", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) {
				mutateNestedDeclaration(t, m, "files", func(n map[string]any) { n["file_ref"] = "sha256:" + strings.Repeat("b", 64) })
			})
		}},
		{"cross root manifest digest", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) {
				mutateNestedDeclaration(t, m, "manifest", func(n map[string]any) { n["payload_digest"] = "sha256:" + strings.Repeat("b", 64) })
			})
		}},
		{"unknown key", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["skip_guard"] = true })
		}},
		{"missing key", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { delete(m, "title") })
		}},
		{"coerced string", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["title"] = 123 })
		}},
		{"coerced manifest", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["manifest"] = "not-an-object" })
		}},
		{"coerced files", func(p *generated.PublishProviderFeedSnapshotParams) {
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, func(m map[string]any) { m["files"] = "not-an-array" })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := feedPublication(wholeFeedInput(t), 1)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&p)
			if _, err := generated.New(db).PublishProviderFeedSnapshot(ctx, p); err == nil {
				t.Fatal("broken set committed")
			}
			var count int
			if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM provider_feed_snapshots)+(SELECT count(*) FROM provider_feed_snapshot_seeds)`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial graph left: %d %v", count, err)
			}
		})
	}
}

func TestFeedPublicationKeepsPayloadBoundAndEmptySnapshot(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	payload := make([]byte, domain.MaxProviderFeedPayloadBytes)
	digest := domain.DigestBytes(payload)
	size := int64(len(payload))
	seeds := []domain.ProviderFeedSnapshotSeed{{StableSeedKey: "bound", Locator: "https://example.test/bound", LocatorDisplay: "example.test/bound", Title: "Bound", Payload: payload, PayloadDigest: &digest, SizeBytes: &size}}
	hash, err := domain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.ProviderFeedSnapshotWrite{Provider: "mikan", ProviderEntryRef: "bound", ProviderGroupRef: "payload", FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: hash, HashSchema: domain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: domain.TorrentManifestProjectionSchema, Seeds: seeds}
	if snapshot, err := NewRepository(db).PersistProviderFeedSnapshot(ctx, input); err != nil || len(snapshot.Seeds) != 1 || len(snapshot.Seeds[0].Payload) != domain.MaxProviderFeedPayloadBytes {
		t.Fatalf("20MiB bound changed: %v", err)
	}
	p, err := feedPublication(input, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.Payloads[0] = make([]byte, domain.MaxProviderFeedPayloadBytes+1)
	if _, err := generated.New(db).PublishProviderFeedSnapshot(ctx, p); err == nil {
		t.Fatal("oversized blob committed")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM provider_feed_snapshots WHERE id=$1`, p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("oversized publication left root: %d %v", count, err)
	}
	input.ProviderGroupRef = "empty"
	input.Seeds = nil
	input.ContentHash, err = domain.CanonicalFeedSnapshotContentHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := NewRepository(db).PersistProviderFeedSnapshot(ctx, input); err != nil || len(snapshot.Seeds) != 0 {
		t.Fatalf("empty snapshot rejected: %v", err)
	}
}

// The AniBT tag set is provider XML, not title text: it can never be recovered
// from a persisted seed, so the snapshot must carry it. It is deliberately
// outside the content hash (every already-published snapshot has none, so the
// hash schema cannot fold it in), which makes the column's own closed shape the
// only guard.
func TestFeedPublicationPersistsTheFrozenAniBTTagSet(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	settled := acquisitiondomain.StoredAniBTTags{
		Schema: acquisitiondomain.AniBTTagSchemaV1,
		// The last tag fills the extractor's bound exactly: 128 bytes of value
		// after the colon. The owner function must compare the value, not the
		// whole "dimension:value" string, or a tag the extractor produced would
		// be refused at publish.
		Tags: []string{"resolution:1080p", "language:CHS", "subtitle:INTERNAL", "tag:" + strings.Repeat("x", acquisitiondomain.MaxAniBTTagValueBytes)},
	}
	empty := acquisitiondomain.StoredAniBTTags{Tags: []string{}}
	seeds := []domain.ProviderFeedSnapshotSeed{
		{StableSeedKey: "tagged", Locator: "https://example.test/tagged", LocatorDisplay: "example.test/tagged", Title: "Tagged", AniBTTags: &settled},
		{StableSeedKey: "tagless", Locator: "https://example.test/tagless", LocatorDisplay: "example.test/tagless", Title: "Tagless", AniBTTags: &empty},
		{StableSeedKey: "plain", Locator: "https://example.test/plain", LocatorDisplay: "example.test/plain", Title: "Plain"},
	}
	hash, err := domain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.ProviderFeedSnapshotWrite{Provider: "anibt", ProviderEntryRef: "entry", ProviderGroupRef: "tags", FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: hash, HashSchema: domain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: domain.TorrentManifestProjectionSchema, Seeds: seeds}
	snapshot, err := NewRepository(db).PersistProviderFeedSnapshot(ctx, input)
	if err != nil {
		t.Fatalf("publish a snapshot carrying tag sets: %v", err)
	}
	// The tags stay outside the content identity: a snapshot published with
	// them still reproduces the hash computed without folding them in.
	if err := snapshot.VerifyContentHash(); err != nil {
		t.Fatalf("content hash drifted with the tag column: %v", err)
	}
	byKey := map[string]domain.ProviderFeedSnapshotSeed{}
	for _, seed := range snapshot.Seeds {
		byKey[seed.StableSeedKey] = seed
	}
	tagged := byKey["tagged"].AniBTTags
	if tagged == nil || tagged.Schema != settled.Schema || !reflect.DeepEqual(tagged.Tags, settled.Tags) {
		t.Fatalf("tagged seed round trip = %+v, want %+v", tagged, settled)
	}
	// A recorded empty set is a fact distinct from "never observed with tags".
	if tagless := byKey["tagless"].AniBTTags; tagless == nil || tagless.Tags == nil || len(tagless.Tags) != 0 {
		t.Fatalf("tag-less AniBT seed = %+v, want a non-nil empty set", tagless)
	}
	if plain := byKey["plain"].AniBTTags; plain != nil {
		t.Fatalf("a seed with no tag block persisted one: %+v", plain)
	}
}

func TestFeedPublicationRejectsMalformedAniBTTagBlocks(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	tooMany := make([]any, 65)
	for index := range tooMany {
		tooMany[index] = "tag:" + string(rune('a'+index%26))
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"not an object", func(m map[string]any) { m["anibt_tags"] = "resolution:1080p" }},
		{"missing tags array", func(m map[string]any) { m["anibt_tags"] = map[string]any{"schema": "anibt-tags-v1"} }},
		{"tags not an array", func(m map[string]any) { m["anibt_tags"] = map[string]any{"tags": "resolution:1080p"} }},
		{"over the tag bound", func(m map[string]any) { m["anibt_tags"] = map[string]any{"tags": tooMany} }},
		{"value over the extractor bound", func(m map[string]any) {
			// One byte past the value bound, with the dimension itself fine: the
			// check is on the value, so this must be the reason it is refused.
			m["anibt_tags"] = map[string]any{"tags": []any{"custom:raw:" + strings.Repeat("x", acquisitiondomain.MaxAniBTTagValueBytes)}}
		}},
		{"tag without a colon", func(m map[string]any) {
			m["anibt_tags"] = map[string]any{"tags": []any{"resolution"}}
		}},
		{"tag entry is not a string", func(m map[string]any) {
			// A nested object yields text (with a colon) from the text-array
			// walk, but the read path decodes the column into []string.
			m["anibt_tags"] = map[string]any{"tags": []any{map[string]any{"resolution": "1080p"}}}
		}},
		{"schema is not a string", func(m map[string]any) {
			m["anibt_tags"] = map[string]any{"tags": []any{}, "schema": 1}
		}},
		{"unknown key", func(m map[string]any) {
			m["anibt_tags"] = map[string]any{"tags": []any{}, "extra": true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := feedPublication(wholeFeedInput(t), 1)
			if err != nil {
				t.Fatal(err)
			}
			p.Seeds = mutateSeedDeclaration(t, p.Seeds, tc.mutate)
			if _, err := generated.New(db).PublishProviderFeedSnapshot(ctx, p); err == nil {
				t.Fatal("malformed tag block committed")
			}
			var count int
			if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM provider_feed_snapshots)+(SELECT count(*) FROM provider_feed_snapshot_seeds)`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial graph left: %d %v", count, err)
			}
		})
	}
}
