package application

import (
	"database/sql"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/persistence/txscope"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/testutil/animetest"
	"context"
)

func TestDecodeArtifactFileBinding(t *testing.T) {
	// A bound subtitle exposes the frozen method, confidence, and ref.
	bound, err := decodeArtifactFileBindingApplication("bound", sql.NullString{String: "primary", Valid: true},
		[]byte(`{"version":"subtitle-binding-1","method":"basename_match","confidence":1,"diagnostics":[],"locked":{"directory":"Show","basename":"Show - E01"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != "bound" || bound.BoundVideoFileRef == nil || *bound.BoundVideoFileRef != "primary" {
		t.Fatalf("unexpected bound projection: %+v", bound)
	}
	if bound.Method == nil || *bound.Method != "basename_match" {
		t.Fatalf("method = %+v", bound.Method)
	}
	if bound.Confidence == nil || *bound.Confidence != 1.0 {
		t.Fatalf("confidence = %+v", bound.Confidence)
	}
	if bound.Diagnostics == nil || len(bound.Diagnostics) != 0 {
		t.Fatalf("diagnostics must be an empty array: %+v", bound.Diagnostics)
	}

	// An explicit missing decision carries typed diagnostics.
	missing, err := decodeArtifactFileBindingApplication("missing", sql.NullString{},
		[]byte(`{"version":"subtitle-binding-1","diagnostics":[{"code":"subtitle_binding_missing","message":"no video","path":"File.Ref"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if missing.State != "missing" || missing.BoundVideoFileRef != nil || missing.Method != nil {
		t.Fatalf("unexpected missing projection: %+v", missing)
	}
	if len(missing.Diagnostics) != 1 || missing.Diagnostics[0].Code != "subtitle_binding_missing" {
		t.Fatalf("diagnostics = %+v", missing.Diagnostics)
	}
	if missing.Diagnostics[0].Path == nil || *missing.Diagnostics[0].Path != "File.Ref" {
		t.Fatalf("diagnostic path = %+v", missing.Diagnostics[0].Path)
	}

	// A non-subtitle row has no snapshot but still reports its state.
	plain, err := decodeArtifactFileBindingApplication("not_applicable", sql.NullString{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.State != "not_applicable" || plain.Diagnostics == nil {
		t.Fatalf("unexpected plain projection: %+v", plain)
	}

	if _, err := decodeArtifactFileBindingApplication("bound", sql.NullString{String: "primary", Valid: true}, []byte(`{broken`)); err == nil {
		t.Fatal("expected a decode error for a corrupt snapshot")
	}
}

func TestReleaseFilesFromInternalProjectsTargetProjection(t *testing.T) {
	targetPath := "/library/Show/Show - 01.mkv"
	files := (&Service{}).releaseFilesFromInternalApplication([]releaseInternalFile{{
		SourcePath: "Show/Show - 01.mkv",
		Filename:   "Show - 01.mkv",
		MediaKind:  "video",
		TargetPath: &targetPath,
		TargetDiagnostic: &acquisitiondomain.TargetDiagnostic{
			Code: "target_render_failed", Message: "目标路径生成失败，未执行重命名", SourcePath: "Show/Show - 01.mkv",
		},
	}}, sql.NullString{}, nil)
	if files == nil || len(*files) != 1 || (*files)[0].TargetPath == nil || *(*files)[0].TargetPath != targetPath {
		t.Fatalf("target path was not projected: %+v", files)
	}
	if (*files)[0].TargetDiagnostic == nil || (*files)[0].TargetDiagnostic.Code != "target_render_failed" {
		t.Fatalf("target diagnostic was not projected: %+v", files)
	}
}

func TestReleaseFilesFromInternalProjectsDisplayBadges(t *testing.T) {
	animetest.InstallRegistries()
	size := int64(1024)
	service := &Service{}
	files := service.releaseFilesFromInternalApplication([]releaseInternalFile{
		{SourcePath: "Show/EP01.mkv", Filename: "[Nekomoe kissaten][Sakamoto Days][01v2][1080p][CHS].mkv", SizeBytes: &size, MediaKind: "video"},
		{SourcePath: "Show/EP01.ass", Filename: "[Nekomoe kissaten][Sakamoto Days][01][CHT].ass", MediaKind: "subtitle"},
		{SourcePath: "Show/menu.png", Filename: "menu.png", MediaKind: "other"},
	}, sql.NullString{}, frozenEpisodeParser("regex", nil))
	if files == nil || len(*files) != 3 {
		t.Fatalf("expected three projected files: %+v", files)
	}
	video := (*files)[0]
	if video.Episode == nil || *video.Episode != "1" || video.Revision == nil || *video.Revision != "v2" ||
		video.Resolution == nil || *video.Resolution != "1080p" || video.Language == nil || *video.Language != "chs" {
		t.Fatalf("video badges must project episode/revision/resolution/language: %+v", video)
	}
	subtitle := (*files)[1]
	if subtitle.Episode == nil || *subtitle.Episode != "1" || subtitle.Language == nil || *subtitle.Language != "cht" {
		t.Fatalf("subtitle badges must project episode/language: %+v", subtitle)
	}
	if subtitle.Resolution != nil || subtitle.Revision != nil {
		t.Fatalf("subtitle must not invent resolution/revision badges: %+v", subtitle)
	}
	plain := (*files)[2]
	if plain.Episode != nil || plain.Resolution != nil || plain.Revision != nil || plain.Language != nil {
		t.Fatalf("unparseable filename must carry no badges: %+v", plain)
	}
	if got := service.releaseFilesFromInternalApplication(nil, sql.NullString{}, nil); got != nil {
		t.Fatalf("empty input must stay nil: %+v", got)
	}
	// A release that froze no parser shows no episode badge, and a filename
	// that is a range has no scalar episode for the frozen parser.
	none := service.releaseFilesFromInternalApplication([]releaseInternalFile{{SourcePath: "Show/EP01.mkv", Filename: "[Group] Show - 01 [1080p].mkv", MediaKind: "video"}}, sql.NullString{}, nil)
	if (*none)[0].Episode != nil {
		t.Fatalf("no frozen parser must not invent an episode: %+v", (*none)[0])
	}
	ranged := service.releaseFilesFromInternalApplication([]releaseInternalFile{{SourcePath: "Show/Show.mkv", Filename: "[Group] Show 第01-12话 [1080p].mkv", MediaKind: "video"}}, sql.NullString{}, frozenEpisodeParser("regex", nil))
	if (*ranged)[0].Episode != nil {
		t.Fatalf("a range filename must not show its first number: %+v", (*ranged)[0])
	}
}

func TestReleaseFilesFromInternalPrefersFrozenBadges(t *testing.T) {
	db := animetest.OpenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	size := int64(1024)
	store := facts.NewStoreWithRepository(mediapersistence.NewRepository(db))
	service := &Service{mediaFacts: store}

	// A release-level snapshot with one frozen file row: the file was
	// inspected and resolved once, so badges come from the snapshot, never
	// from re-parsing the filename.
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-badges', 'Badge Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-badges', 'anime-badges', 'custom', 'Badges', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-badges', 'sub-badges', 'custom_rss', 'https://feeds.example.com/rss.xml', 'https://feeds.example.com/rss.xml', 'Group', 'Badge Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code, version)
VALUES ('rel-badges', 'src-badges', 'guid:badges', '{"title":"Badge Anime"}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'discovered', 'discovered', 1)`); err != nil {
		t.Fatal(err)
	}
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode,
		RawEpisode: ptrEpisode("7"), Offset: 0, Episode: ptrEpisode("7"),
	}
	snapshot := facts.Snapshot{
		ID: "snap-badges", ReleaseID: "rel-badges", SchemaVersion: facts.SchemaVersion,
		ResolverVersion: episodeidentity.ResolverVersion, ParserKind: "regex",
		InputDigest: "digest-badges", ReleaseIdentity: identity,
		ReleaseFacts: facts.ReleaseFacts{Title: "Badge Anime"},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateReleaseSnapshotTx(ctx, txscope.Wrap(tx), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFileFactsTx(ctx, txscope.Wrap(tx), facts.FileRow{
		SnapshotID: "snap-badges", FileRef: "primary", MediaKind: "video", SizeBytes: size,
		SourceFingerprint: "fp-badges",
		FileEvidence: facts.FileEvidence{
			Name: "Show EP07.5.mkv", MediaKind: "video", SizeBytes: size,
			Resolution: "1080p", Languages: []string{"chs"}, Revision: "v3",
		},
		EffectiveFacts: facts.EffectiveFileFacts{
			Identity: identity, MappingVersion: facts.MappingVersion,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	files := service.releaseFilesFromInternalApplication([]releaseInternalFile{
		{SourcePath: "Show/EP07.5.mkv", Filename: "Show EP07.5.mkv", SizeBytes: &size, MediaKind: "video"},
	}, sql.NullString{String: "snap-badges", Valid: true}, nil)
	if files == nil || len(*files) != 1 {
		t.Fatalf("expected one projected file: %+v", files)
	}
	video := (*files)[0]
	if video.Episode == nil || *video.Episode != "7" {
		t.Fatalf("frozen episode badge=%v, want 7", video.Episode)
	}
	if video.Resolution == nil || *video.Resolution != "1080p" || video.Revision == nil || *video.Revision != "v3" ||
		video.Language == nil || *video.Language != "chs" {
		t.Fatalf("frozen quality badges=%+v, want 1080p/v3/chs", video)
	}
}

func ptrEpisode(value string) *episodeidentity.EpisodeNumber {
	number := episodeidentity.EpisodeNumber(value)
	return &number
}
