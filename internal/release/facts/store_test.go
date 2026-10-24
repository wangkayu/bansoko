package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/testutil/disposabledb"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.ResetDisposableSchema(t)
}

func seedRelease(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-facts', 'Facts Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-facts', 'anime-facts', 'custom', 'Facts', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-facts', 'sub-facts', 'custom_rss', 'https://feeds.example.com/rss.xml', 'https://feeds.example.com/rss.xml', 'Group', 'Facts Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code, version)
VALUES ($1, 'src-facts', $2, '{"title":"Show - 12"}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'discovered', 'accepted', 1)`,
		id, "key-"+id); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	store := NewStoreWithRepository(persistence.NewRepository(db))
	seedRelease(t, db, "rel-1")

	resolver := episodeidentity.NewResolver()
	identity := resolver.Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    "tv",
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceReleaseParser, "release_title", "12"),
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.CreateReleaseSnapshotTx(ctx, txscope.Wrap(tx), Snapshot{
		ID:              "snap-1",
		ReleaseID:       "rel-1",
		ResolverVersion: identity.ResolverVersion,
		ParserKind:      "regex",
		InputDigest:     "digest-1",
		ReleaseIdentity: identity,
		ReleaseFacts: ReleaseFacts{
			Title: "Show - 12", GroupName: "Group", Resolution: "1080p", Languages: []string{"chs"},
			BGMWorkID: "bgm-42", TMDBWorkID: "tmdb-42",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.ReadSnapshot(ctx, "rel-1")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if loaded == nil || loaded.ID != "snap-1" {
		t.Fatalf("loaded=%+v, want snap-1", loaded)
	}
	if loaded.ReleaseIdentity.Episode == nil || *loaded.ReleaseIdentity.Episode != "12" {
		t.Fatalf("identity episode=%v, want 12", loaded.ReleaseIdentity.Episode)
	}
	if loaded.ReleaseFacts.GroupName != "Group" || loaded.ReleaseFacts.Resolution != "1080p" || loaded.ReleaseFacts.BGMWorkID != "bgm-42" || loaded.ReleaseFacts.TMDBWorkID != "tmdb-42" {
		t.Fatalf("release facts=%+v", loaded.ReleaseFacts)
	}
}

func TestFileFactsUpsertAndList(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	store := NewStoreWithRepository(persistence.NewRepository(db))
	seedRelease(t, db, "rel-2")

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.CreateReleaseSnapshotTx(ctx, txscope.Wrap(tx), Snapshot{
		ID: "snap-2", ReleaseID: "rel-2", ResolverVersion: "episode-identity-v1",
		ParserKind: "regex", InputDigest: "digest-2",
		ReleaseIdentity: episodeidentity.EpisodeIdentity{State: episodeidentity.StateResolved, ResolverVersion: "episode-identity-v1"},
		ReleaseFacts:    ReleaseFacts{Title: "Show - 12"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := FileRow{
		SnapshotID: "snap-2", FileRef: "Show - 12.mkv", MediaKind: "video", SizeBytes: 1024,
		SourceFingerprint: "fp-1",
		FileEvidence:      FileEvidence{Name: "Show - 12.mkv", MediaKind: "video", SizeBytes: 1024},
		EffectiveFacts: EffectiveFileFacts{
			Identity: episodeidentity.EpisodeIdentity{
				State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode,
				RawEpisode: ptr("12"), Offset: 0, Episode: ptr("12"),
				ResolverVersion: "episode-identity-v1",
			},
			MappingVersion: MappingVersion,
		},
	}
	if err := store.UpsertFileFactsTx(ctx, txscope.Wrap(tx), row); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListFileRows(ctx, "snap-2")
	if err != nil {
		t.Fatalf("list file rows: %v", err)
	}
	if len(rows) != 1 || rows[0].FileRef != "Show - 12.mkv" {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].EffectiveFacts.Identity.Episode == nil || *rows[0].EffectiveFacts.Identity.Episode != "12" {
		t.Fatalf("effective identity=%+v", rows[0].EffectiveFacts.Identity)
	}

	// The batched read returns the same rows, keyed by snapshot, and nothing for
	// a snapshot without rows.
	batched, err := store.ListFileRowsBySnapshots(ctx, []string{"snap-2", "snap-without-rows"})
	if err != nil {
		t.Fatalf("list file rows by snapshots: %v", err)
	}
	if len(batched) != 1 || len(batched["snap-2"]) != 1 || batched["snap-2"][0].FileRef != "Show - 12.mkv" ||
		batched["snap-2"][0].EffectiveFacts.Identity.Episode == nil || *batched["snap-2"][0].EffectiveFacts.Identity.Episode != "12" {
		t.Fatalf("batched rows=%+v", batched)
	}
	if none, err := store.ListFileRowsBySnapshots(ctx, nil); err != nil || len(none) != 0 {
		t.Fatalf("empty batch: %+v, %v", none, err)
	}

	// Upsert overwrites the same file_ref with a new facts generation.
	row.EffectiveFacts.Identity.Offset = 2
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFileFactsTx(ctx, txscope.Wrap(tx), row); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListFileRows(ctx, "snap-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("upsert must keep one row per file_ref, got %d", len(rows))
	}
}

func TestMarshalIdentityRoundTrip(t *testing.T) {
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindMovie,
		RawEpisode: ptr("0"), Offset: 0, Episode: ptr("0"),
		ResolverVersion: "episode-identity-v1",
	}
	raw, err := MarshalIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("identity must stay JSON-serializable: %v", err)
	}
	loaded, err := UnmarshalIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Kind != episodeidentity.KindMovie || loaded.Episode == nil || *loaded.Episode != "0" {
		t.Fatalf("round trip identity=%+v", loaded)
	}
}

func ptr(value string) *episodeidentity.EpisodeNumber {
	number := episodeidentity.EpisodeNumber(value)
	return &number
}
