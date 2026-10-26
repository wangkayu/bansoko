package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/storedsections"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
	"bansoko/internal/namingvars"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/templatefacts"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/testutil/nativeinput"
	"bansoko/internal/workitems"
)

type testParentIdentityAuthorizer struct{}

func (testParentIdentityAuthorizer) ParentIdentities(string, string, string) (domain.FileIdentity, domain.FileIdentity, error) {
	return domain.FileIdentity{Device: 1, Inode: 2}, domain.FileIdentity{Device: 1, Inode: 3}, nil
}

// testExecutor implements only the planning half of the filesystem port; the
// typed publication chain fails closed (typed_publication_unsupported) with
// it. calls counts every adapter use.
type testExecutor struct {
	calls int
}

func (e *testExecutor) PreparePublication(context.Context, domain.FileOperation, string) (domain.PublicationPreparation, error) {
	e.calls++
	return domain.PublicationPreparation{}, errors.New("test executor has no publication port")
}

type testObserver struct {
	calls       int
	fingerprint string
	sizeBytes   int64
	err         error
}

func (o *testObserver) Observe(context.Context, string) (string, error) {
	o.calls++
	return o.fingerprint, o.err
}

func (o *testObserver) ObserveFile(ctx context.Context, path string) (domain.FileObservation, error) {
	o.calls++
	return domain.FileObservation{Fingerprint: o.fingerprint, SizeBytes: o.sizeBytes}, o.err
}

type legacyObserver struct {
	fingerprint string
	calls       int
}

func (o *legacyObserver) Observe(context.Context, string) (string, error) {
	o.calls++
	return o.fingerprint, nil
}

func TestObserveTargetUsesExplicitLegacyCompatibilityPath(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	observer := &legacyObserver{fingerprint: fingerprint}
	observation, err := observeTarget(context.Background(), observer, "/tmp/target.mkv", false)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Fingerprint != fingerprint || observation.SizeBytes != -1 || observer.calls != 1 {
		t.Fatalf("unexpected legacy observation: %+v calls=%d", observation, observer.calls)
	}
	if _, err := observeTarget(context.Background(), observer, "/tmp/target.mkv", true); !errors.Is(err, ErrSizeAwareObserverRequired) {
		t.Fatalf("strict observation accepted legacy observer: %v", err)
	}
}

func TestBuildNamingContextAndRenderTarget(t *testing.T) {
	digest := strings.Repeat("a", 64)
	resolved, err := buildNamingContext(artifactContext{
		managedAnimeID: "anime-builder-proof", animeTitle: "Anime title", titleOverride: sql.NullString{String: "Override", Valid: true},
		groupName: "Group", sourceKind: "provider_group", releaseID: "release-builder-proof", releaseVersion: 3,
		metadataBGM: sql.NullString{String: "bgm-1", Valid: true}, tmdbWorkID: sql.NullString{String: "tmdb-1", Valid: true},
		tmdbMediaKind: sql.NullString{String: "tv", Valid: true}, tmdbSeason: sql.NullInt64{Int64: 2, Valid: true},
		rawFacts:    json.RawMessage(`{"title":"[Group] Anime title - 01 [1080p HEVC][CHS]"}`),
		sourceFacts: []byte(`{"anibt":{"schema":"anibt-rss-1.0","tags":["resolution:1080p","subtitle:INTERNAL","language:CHS","format:MKV","tag:HEVC"]},"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@x","tags":["resolution:1080p","source_type:WEBRip","video_codec:HEVC","sub_lang:SC","sub_flag:soft"],"value_tags":["title:Anime title","group:Group","version:v2"]}}`),
	}, artifactFileContext{
		sourcePath: "/downloads/source.mkv", mediaKind: "video", sizeBytes: 42,
		fingerprint: fingerprintJSONForTest(digest, 42), facts: json.RawMessage(`{"resolution":"1080p","video_codec":"HEVC","container":"mkv"}`),
	}, &naming.FrozenEpisodeNamingFacts{Raw: "01", Number: "1", Display: "1"}, realFrozenVarsFixture())
	if err != nil {
		t.Fatal(err)
	}
	ctx := resolved.Context

	if ctx.Anime.Title != "Anime title" {
		t.Fatalf("unexpected title fact: %+v", ctx.Anime)
	}
	if naming.UnifiedFacts(ctx).Anime.Group != "Group" {
		t.Fatalf("unexpected group fact: %+v", naming.UnifiedFacts(ctx).Anime)
	}
	if ctx.Parsed.Episode.Number != 1 || ctx.Parsed.Episode.Display != "1" {
		t.Fatalf("unexpected episode fact: %+v", ctx.Parsed.Episode)
	}

	if ctx.Anime.ID != "anime-builder-proof" || ctx.Anime.TitleOverride != "Override" || ctx.Anime.TMDB.Season != 2 || ctx.Anime.BGM.WorkID != "bgm-1" || ctx.Anime.TMDB.WorkID != "tmdb-1" {
		t.Fatalf("acquisition facts were not populated: %+v", ctx.Anime)
	}
	if ctx.Release.ID != "release-builder-proof" || ctx.Release.Version != 3 || ctx.Release.FrozenEpisode == nil || ctx.Release.FrozenEpisode.Display != "1" {
		t.Fatalf("acquisition release facts = %+v", ctx.Release)
	}
	if ctx.File.Kind != "video" || ctx.File.SizeBytes != 42 || ctx.File.Media.Resolution != "1080p" || ctx.File.Media.VideoCodec != "HEVC" || ctx.File.Media.Container != "mkv" {
		t.Fatalf("acquisition file facts = %+v", ctx.File)
	}
	// The builder fills the typed naming context; the unified naming facts
	// project this struct, so its fields are the field-level check.
	for _, field := range templatefacts.ReltitleFields {
		if value, present := ctx.Rt.Get(field.Key); !present || value == "" {
			t.Errorf("reltitle field %q has no acquisition fixture value", field.Key)
		}
	}
	for _, field := range templatefacts.AniBTTagFields {
		if value, present := ctx.AniBTTags.Get(field.Key); !present || value == "" {
			t.Errorf("AniBT tag field %q has no acquisition fixture value", field.Key)
		}
	}
	contextPaths := []string{
		"Anime.ID",
		"Anime.Title",
		"Anime.TitleOverride",
		"Anime.BGM.WorkID",
		"Anime.TMDB.WorkID",
		"Anime.TMDB.MediaKind",
		"Anime.TMDB.Season",
		"Source.Kind",
		"Source.GroupName",
		"Release.ID",
		"Release.OriginalTitle",
		"Release.Version",
		"Release.FrozenEpisode.Display",
		"File.OriginalName",
		"File.Basename",
		"File.Extension",
		"File.SizeBytes",
		"File.Kind",
		"File.Media.Resolution",
		"File.Media.VideoCodec",
		"File.Media.Container",
		"Parsed.Episode.Raw",
		"Parsed.Episode.DetectedNumber",
		"Parsed.Episode.Display",
		"Parsed.Episode.Number",
		"Parsed.Episode.Offset",
		"Parsed.Episode.SpecialKind",
		"Parsed.Language.Code",
		"Parsed.Language.Languages",
		"Parsed.Language.Description",
		"Parsed.Language.BCP47",
		"Parsed.Subtitle.Format",
		"Parsed.Subtitle.OriginalSuffix",
		"Parsed.Subtitle.Languages",
		"Parsed.Subtitle.PrimaryLanguage",
		"Parsed.Subtitle.Flags.Forced",
		"Parsed.Subtitle.Flags.SDH",
		"Parsed.Subtitle.Flags.Commentary",
		"Parsed.Subtitle.Flags.Default",
		"Release.SubtitleMode",
		"Derived.EffectiveTitle",
		"Derived.EffectiveTitleSource",
		"Derived.EffectiveMediaKind",
		"Derived.SeasonFolder",
	}
	for _, path := range contextPaths {
		parts := strings.Split(path, ".")
		value := reflect.ValueOf(ctx)
		for _, part := range parts {
			if value.Kind() == reflect.Ptr {
				if value.IsNil() {
					value = reflect.Value{}
					break
				}
				value = value.Elem()
			}
			if value.Kind() != reflect.Struct {
				value = reflect.Value{}
				break
			}
			value = value.FieldByName(part)
			if !value.IsValid() {
				break
			}
		}
		if !value.IsValid() {
			t.Errorf("context field %q is absent from acquisition context", path)
			continue
		}
		if value.Kind() == reflect.String && value.String() == "" && path != "Parsed.Subtitle.Format" && path != "Parsed.Subtitle.OriginalSuffix" && path != "Parsed.Subtitle.Languages" && path != "Parsed.Subtitle.PrimaryLanguage" && path != "Parsed.Episode.SpecialKind" {
			t.Errorf("context field %q has no acquisition fixture value", path)
		}
	}
	engine := naming.NewEngine()
	compiled, err := engine.CompileRules(naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/${anime.title_base}/${anime.group}",
			VideoFilenameTemplate: "${file.stem} - E${pad(ep.display, 2)}.${file.ext}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	directory, filename, err := compiled.RenderVideo(engine, ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, err := renderedTarget(directory, filename)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join("/library", "Anime title", "Group", "source - E01.mkv") {
		t.Fatalf("unexpected target %q", target)
	}
}

type parityRulesSource struct{ rules naming.NamingRules }

func (p parityRulesSource) EffectiveNamingRules(context.Context, string) (naming.NamingRules, error) {
	return p.rules, nil
}

// parityRulesSourceUnder pins the maintenance-side rule source to the fixture
// root the frozen policy was re-rooted to. The acquisition render reads the
// rewritten naming section, so a parity test must hand the maintenance renderer
// the identical rules; otherwise the two renders differ by the directory prefix
// alone (the fixture root versus the deployment mount /library).
func parityRulesSourceUnder(fixture fileFixture, rules naming.NamingRules) parityRulesSource {
	return parityRulesSource{rules: fixtureAbsoluteNamingRules(rules, fixture.root)}
}

func TestRenderedTargetRejectsUnsafePaths(t *testing.T) {
	cases := []struct {
		name      string
		directory string
		filename  string
	}{
		{name: "parent directory", directory: "../escape", filename: "file.mkv"},
		{name: "home prefix", directory: "~/escape", filename: "file.mkv"},
		{name: "parent filename", directory: "/ok", filename: "../file.mkv"},
		{name: "control", directory: "/ok\n", filename: "file.mkv"},
		{name: "filename separator", directory: "/ok", filename: "nested/file.mkv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := renderedTarget(tc.directory, tc.filename); !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("expected invalid target, got %v", err)
			}
		})
	}
}

func TestRenderedTargetAllowsAbsoluteDirectory(t *testing.T) {
	target, err := renderedTarget("/media/anime", "file.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join("/media/anime", "file.mkv") {
		t.Fatalf("absolute directory must be the target as-is, got %q", target)
	}
	if _, err := renderedTarget("relative/dir", "file.mkv"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("relative target must be invalid, got %v", err)
	}
}

func TestSafeReasonRedactsCredentialsAndPaths(t *testing.T) {
	reason := safeReason(errors.New("request https://user:secret@example.test/feed password=topsecret /private/library/source.mkv"))
	if strings.Contains(reason, "secret") || strings.Contains(reason, "topsecret") || strings.Contains(reason, "user:") {
		t.Fatalf("reason contains sensitive data: %q", reason)
	}
	if strings.Contains(reason, "/private/library/source.mkv") {
		t.Fatalf("reason contains a private path: %q", reason)
	}
}

func TestCanonicalFileExecuteInputCarriesOnlyOverwriteMarkerWhenNeeded(t *testing.T) {
	base := operationRecord{id: "operation-1", artifactID: "artifact-1", artifactFileID: "file-1", collisionPolicy: string(domain.CollisionBlock)}
	for _, tc := range []struct {
		name      string
		operation operationRecord
		wantMode  string
	}{
		{name: "block has no recovery marker", operation: base},
		{name: "version has no recovery marker", operation: operationRecord{id: base.id, artifactID: base.artifactID, artifactFileID: base.artifactFileID, collisionPolicy: string(domain.CollisionVersion)}},
		{name: "overwrite declares recovery", operation: operationRecord{id: base.id, artifactID: base.artifactID, artifactFileID: base.artifactFileID, collisionPolicy: string(domain.CollisionOverwrite)}, wantMode: string(domain.CollisionOverwrite)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonicalFileExecuteInput(tc.operation)
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]string
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			if input["operation_id"] != "operation-1" || input["artifact_id"] != "artifact-1" || input["artifact_file_id"] != "file-1" {
				t.Fatalf("canonical identity fields are incomplete: %#v", input)
			}
			if input["recovery_mode"] != tc.wantMode {
				t.Fatalf("recovery_mode=%q, want %q", input["recovery_mode"], tc.wantMode)
			}
		})
	}
}

func TestBuildFileOperationNotificationPayloadBoundsFactsAndRelativeOutput(t *testing.T) {
	payload, err := buildFileOperationNotificationPayload(fileOperationNotificationFacts{
		managedAnimeID: "anime-1",
		animeTitle:     "Anime",
		releaseID:      "release-1",
		releaseTitle:   "[Group] Anime 01",
		filePath:       "source.mkv",
		fileSize:       42,
		fileMediaKind:  "video",
		fileFacts:      []byte(`{"container":"mkv","resolution":"1080p","url":"https://private.example"}`),
		locationRoot:   "/private/library",
		targetPath:     "/private/library/Anime/source.mkv",
		operation:      "copy",
		collision:      "block",
	}, plugintargetport.TriggerFileOperationCompleted, "success", "file_observed", "completed")
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["out.path"] != "Anime/source.mkv" || facts["out.dir"] != "Anime" || facts["out.name"] != "source.mkv" {
		t.Fatalf("unexpected relative output facts: %#v", facts)
	}
	if facts["media.container"] != "mkv" || facts["file.kind"] != "video" {
		t.Fatalf("unexpected file facts: %#v", facts)
	}
	if strings.Contains(string(payload), "private.example") || strings.Contains(string(payload), "/private/library") {
		t.Fatalf("payload contains private data: %s", payload)
	}
}

func TestBuildFileOperationNotificationPayloadFallsBackToFilenameOutsideRoot(t *testing.T) {
	payload, err := buildFileOperationNotificationPayload(fileOperationNotificationFacts{
		managedAnimeID: "anime-1",
		animeTitle:     "Anime",
		releaseID:      "release-1",
		releaseTitle:   "Release",
		filePath:       "source.mkv",
		fileSize:       42,
		locationRoot:   "/private/library",
		targetPath:     "/other/private/source.mkv",
		operation:      "move",
		collision:      "version",
	}, plugintargetport.TriggerFileOperationFailed, "failure", "target_invalid", "target was invalid")
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["out.path"] != nil || facts["out.dir"] != nil || facts["out.name"] != "source.mkv" {
		t.Fatalf("unsafe output path was not reduced to filename: %#v", facts)
	}
}

func TestInvalidKindDoesNotNeedDatabase(t *testing.T) {
	service := &Service{}
	_, err := service.Handle(context.Background(), workitems.Claim{Kind: workitems.KindArtifactInspect})
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("expected invalid kind, got %v", err)
	}
}

func TestOverwriteExecutionSnapshotRejectsInvalidForeignObservation(t *testing.T) {
	base := domain.OverwriteRecovery{
		Stage:         domain.OverwriteStageRestored,
		CanonicalRoot: "/library", CanonicalSource: "/source", CanonicalTarget: "/target", StagingPath: "/staging",
		SourceParentIdentity: domain.FileIdentity{Device: 1, Inode: 2},
		TargetParentIdentity: domain.FileIdentity{Device: 1, Inode: 3},
		SourceObservation:    domain.FileObservation{Fingerprint: strings.Repeat("a", 64), SizeBytes: 1, Identity: domain.FileIdentity{Device: 1, Inode: 1}},
		TargetObservation:    domain.FileObservation{Fingerprint: strings.Repeat("b", 64), SizeBytes: 1, Identity: domain.FileIdentity{Device: 1, Inode: 2}},
		StagedObservation:    domain.FileObservation{Fingerprint: strings.Repeat("a", 64), SizeBytes: 1, Identity: domain.FileIdentity{Device: 1, Inode: 3}},
		DisplacedObservation: domain.FileObservation{Fingerprint: strings.Repeat("b", 64), SizeBytes: 1, Identity: domain.FileIdentity{Device: 1, Inode: 2}},
		ForeignEvidencePath:  "/library/.bansoko-quarantine-aaaaaaaaaaaaaaaaaaaaaaaa",
		ForeignEvidence:      domain.FileObservation{Fingerprint: strings.Repeat("c", 64), SizeBytes: 1, Identity: domain.FileIdentity{Device: 1, Inode: 4}},
	}
	for name, mutate := range map[string]func(*domain.OverwriteRecovery){
		"negative size":       func(r *domain.OverwriteRecovery) { r.ForeignEvidence.SizeBytes = -1 },
		"invalid fingerprint": func(r *domain.OverwriteRecovery) { r.ForeignEvidence.Fingerprint = "invalid" },
		"partial identity":    func(r *domain.OverwriteRecovery) { r.ForeignEvidence.Identity = domain.FileIdentity{Device: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			recovery := base
			recovery.ForeignEvidencePath = overwriteCheckpointForeignEvidencePath(recovery.StagingPath)
			mutate(&recovery)
			if err := validateOverwriteRecovery(recovery); err == nil {
				t.Fatal("invalid foreign evidence observation was accepted")
			}
		})
	}
}

func TestFrozenPlanDigestCanonicalizesJSONWithoutLosingNumbers(t *testing.T) {
	left := frozenPlanDigest("operation-1", "move", "overwrite", "/source", "/target", []byte(`{"value":"source"}`), []byte(`{"b":2,"a":1}`), []byte(`{"large":9007199254740993123,"ratio":1e-7}`))
	right := frozenPlanDigest("operation-1", "move", "overwrite", "/source", "/target", []byte(`{"value":"source"}`), []byte("{\n  \"a\": 1, \"b\": 2\n}"), []byte(`{"ratio":0.0000001,"large":9007199254740993123}`))
	if left != right {
		t.Fatalf("equivalent JSON plans produced different digests: %s != %s", left, right)
	}
	canonical := string(canonicalDigestJSON([]byte(`{"large":9007199254740993123}`)))
	if !strings.Contains(canonical, "number:9007199254740993123/1") {
		t.Fatalf("canonical JSON lost large integer: %s", canonical)
	}
	if got := canonicalDigestJSON([]byte(`{"a":1} trailing`)); string(got) != `{"a":1} trailing` {
		t.Fatalf("trailing JSON should not be accepted: %s", got)
	}
}

func TestNewServiceRequiresAllPorts(t *testing.T) {
	if _, err := NewService(&sql.DB{}, nil, &testObserver{}); err == nil {
		t.Fatal("nil executor accepted")
	}
	if _, err := NewService(&sql.DB{}, &testExecutor{}, nil); err == nil {
		t.Fatal("nil observer accepted")
	}
	// Naming needs no deployment root: templates render absolute targets, so a
	// service built with nothing but its three ports is usable.
	if _, err := NewService(&sql.DB{}, &testExecutor{}, &testObserver{}); err != nil {
		t.Fatalf("a port-only service was rejected: %v", err)
	}
}

func TestPrepareForArtifactIgnoresPostAdmissionConfigDrift(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	// Rewrite every instance section that could change the file effect after the
	// download task was admitted. The frozen snapshot must win, so the prepared
	// operation keeps the admitted copy/fail plan instead of drifting.
	for section, value := range map[string]string{
		"file_effect":  `{"operation":"move","collision_policy":"overwrite"}`,
		"file_rules":   `{"must_contain":"nope","not_in":"Fixture"}`,
		"naming_rules": `{"language":"js-naming-1","tv":{"directory_template":"Drifted","video_filename_template":"drifted.mkv"},"movie":{"directory_template":"Drifted","video_filename_template":"drifted.mkv"},"subtitle_suffix_template":""}`,
	} {
		schema := map[string]string{
			"file_effect":  "file-effect-v1",
			"file_rules":   "file-rules-v1",
			"naming_rules": "naming-rules-v1",
		}[section]
		if _, err := db.ExecContext(context.Background(), `
UPDATE anime_config_overrides
SET schema_version = $3, value = $4::jsonb
WHERE managed_anime_id = $1 AND section_name = $2`, fixture.animeID, section, schema, value); err != nil {
			t.Fatal(err)
		}
	}
	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	var state, operation, collision string
	if err := db.QueryRow(`
SELECT state, operation, collision_policy FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).
		Scan(&state, &operation, &collision); err != nil {
		t.Fatal(err)
	}
	if state != ownerStatePending || operation != "copy" || collision != "block" {
		t.Fatalf("admitted operation drifted to state=%q operation=%q collision=%q, want pending/copy/block", state, operation, collision)
	}
}

func TestPrepareForArtifactPersistsInvalidNamingAndTerminalWorkItem(t *testing.T) {
	invalidNaming := map[string]any{
		"tv":                       map[string]any{"directory_template": "../escape", "video_filename_template": "${file.name}"},
		"movie":                    map[string]any{"directory_template": "../escape", "video_filename_template": "${file.name}"},
		"subtitle_suffix_template": "",
	}
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		sections := policy["effective_sections"].(map[string]any)
		sections[animedomain.SectionNamingRules] = invalidNaming
	})
	defer db.Close()

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)
	var operationCount, workCount int
	var reason string
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 0 {
		t.Fatalf("invalid naming created %d Operations", operationCount)
	}
	if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, fixture.artifactID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("invalid naming lacks terminal result diagnostic")
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind=$1`, workitems.KindFileExecute).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 0 {
		t.Fatalf("invalid naming scheduled %d effects", workCount)
	}
}

func TestPrepareForArtifactIsIdempotent(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "overwrite"}
	})
	defer db.Close()
	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)
	prepareFixture(t, db, service, fixture.artifactID)

	var operations, workItems int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type = 'file_operation' AND owner_id = (SELECT id FROM file_operations WHERE artifact_id = $1)`, fixture.artifactID).Scan(&workItems); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || workItems != 1 {
		t.Fatalf("prepare was not idempotent: operations=%d work_items=%d", operations, workItems)
	}
	var inputSnapshot []byte
	if err := db.QueryRow(`SELECT input_snapshot FROM work_item_history WHERE owner_type = 'file_operation' AND owner_id = (SELECT id FROM file_operations WHERE artifact_id = $1)`, fixture.artifactID).Scan(&inputSnapshot); err != nil {
		t.Fatal(err)
	}
	var input map[string]string
	if err := json.Unmarshal(inputSnapshot, &input); err != nil {
		t.Fatal(err)
	}
	if input["recovery_mode"] != string(domain.CollisionOverwrite) {
		t.Fatalf("overwrite recovery marker missing from input snapshot: %s", inputSnapshot)
	}
}

func TestPrepareForArtifactReplansBlockedPlaceholder(t *testing.T) {
	assertSealedNamingFailureCannotReprepare(t)
	if !retainedAcceptanceEnabled() {
		return
	} // Original retired repair assets.
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	setFrozenSectionValue(t, db, "download-file-test", animedomain.SectionNamingRules, map[string]any{
		"tv":                       map[string]any{"directory_template": "../escape", "video_filename_template": "${file.name}"},
		"movie":                    map[string]any{"directory_template": "../escape", "video_filename_template": "${file.name}"},
		"subtitle_suffix_template": "",
	})
	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	var blockedID string
	if err := db.QueryRow(`SELECT id FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&blockedID); err != nil {
		t.Fatal(err)
	}

	// Repair the environment (valid naming rules) and re-run prepare through
	// the artifact retry path: the blocked placeholder must be dropped and
	// re-planned instead of replaying the frozen failure.
	setFrozenSectionValue(t, db, "download-file-test", animedomain.SectionNamingRules, naming.DefaultNamingRules())
	prepareFixture(t, db, service, fixture.artifactID)

	var operationID, state, targetPath string
	if err := db.QueryRow(`SELECT id, state, target_path FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operationID, &state, &targetPath); err != nil {
		t.Fatal(err)
	}
	if operationID == blockedID {
		t.Fatalf("blocked placeholder was not replaced: %q", operationID)
	}
	if state != ownerStatePending {
		t.Fatalf("replanned operation is not pending: %q", state)
	}
	canonicalRoot, err := filepath.EvalSymlinks(fixture.root)
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	if !strings.HasPrefix(targetPath, canonicalRoot+string(filepath.Separator)) || strings.Contains(targetPath, invalidPlaceholderDir) {
		t.Fatalf("replanned target is not a real library target: %q", targetPath)
	}
	var staleWorkItems int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type = 'file_operation' AND owner_id = $1`, blockedID).Scan(&staleWorkItems); err != nil {
		t.Fatal(err)
	}
	var liveWorkItems int
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE owner_type = 'file_operation' AND owner_id = $1 AND state = 'available'`, operationID).Scan(&liveWorkItems); err != nil {
		t.Fatal(err)
	}
	if staleWorkItems != 0 || liveWorkItems != 1 {
		t.Fatalf("work items not rebuilt for the replanned operation: stale=%d live=%d", staleWorkItems, liveWorkItems)
	}
}

func TestPrepareForArtifactPlansFromTemplateWhenRouteRootUnavailable(t *testing.T) {
	assertAcceptedUnavailableRootDoesNotReachThePlan(t)
}

func TestPrepareForArtifactFreezesResolverEvidence(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	var snapshot []byte
	if err := db.QueryRow(`SELECT frozen_render_snapshot FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		RendererVersion string           `json:"renderer_version"`
		Resolved        []map[string]any `json:"resolved"`
	}
	if err := json.Unmarshal(snapshot, &frozen); err != nil {
		t.Fatalf("decode frozen render snapshot: %v (%s)", err, snapshot)
	}
	if frozen.RendererVersion == "" {
		t.Fatal("frozen render snapshot misses renderer_version")
	}
	if len(frozen.Resolved) == 0 {
		t.Fatal("frozen render snapshot misses resolved values")
	}
	byKey := map[string]map[string]any{}
	for _, entry := range frozen.Resolved {
		key, _ := entry["key"].(string)
		byKey[key] = entry
	}
	episode, ok := byKey["parsed.episode"]
	if !ok {
		t.Fatalf("resolved values miss parsed.episode: %s", snapshot)
	}
	if episode["state"] != "value" {
		t.Errorf("episode state = %v, want value", episode["state"])
	}
	valueSummary, _ := episode["value"].(string)
	if !strings.Contains(valueSummary, "number=1") {
		t.Errorf("episode value summary = %q", valueSummary)
	}
	// The episode comes from the frozen per-file identity; naming never
	// parses the release title.
	evidenceSummary, _ := episode["evidence"].(string)
	if !strings.Contains(evidenceSummary, "frozen_episode_identity") || !strings.Contains(evidenceSummary, "Release.FrozenEpisode") {
		t.Errorf("episode evidence summary = %q", evidenceSummary)
	}
	var withFacts struct {
		RenderFacts map[string]string `json:"render_facts"`
	}
	if err := json.Unmarshal(snapshot, &withFacts); err != nil {
		t.Fatalf("decode frozen render snapshot: %v (%s)", err, snapshot)
	}
	if withFacts.RenderFacts["Source.GroupName"] != "Test Group" {
		t.Fatalf("frozen render facts keep the source group: %v", withFacts.RenderFacts)
	}
}

func mustOperationID(t *testing.T, db *sql.DB, artifactID string) string {
	t.Helper()
	var operationID string
	if err := db.QueryRow(`SELECT id FROM file_operations WHERE artifact_id = $1`, artifactID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	return operationID
}

type fileFixture struct {
	animeID     string
	artifactID  string
	fileID      string
	root        string
	sourcePath  string
	fingerprint string
	size        int64
}

func openFileOperationDB(t *testing.T) (*sql.DB, fileFixture) {
	t.Helper()
	return openFileOperationDBWithPolicy(t, nil)
}

// ownerTestDB returns the schema-owner handle for a test-only DDL path (for
// example installing an injected failure trigger). Application tests otherwise
// run as the segregated runtime role.
var ownerTestPools sync.Map // keyed by *testing.T; never shared across schema-reset tests.

func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if cached, ok := ownerTestPools.Load(t); ok {
		return cached.(*sql.DB)
	}
	db := disposabledb.Owner(t)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	actual, loaded := ownerTestPools.LoadOrStore(t, db)
	if loaded {
		db.Close()
		return actual.(*sql.DB)
	}
	t.Cleanup(func() { ownerTestPools.Delete(t); db.Close() })
	return db
}

// openFileOperationDBWithPolicy seeds the owner chain with a frozen policy the
// caller may mutate before it is written. A non-nil mutate receives the
// complete fixture map, so a test can substitute one resolved section.
// renderFixtureOverwriteTarget runs the PRODUCTION naming render path over the
// facts this fixture already writes, so the accepted replacement binding names
// exactly the path the planner will render for an overwrite operation.
//
// No naming domain logic is duplicated here. The naming rules and the
// frozen vars are unmarshalled from the fixture's own frozen policy, and the
// rendering is performed by the same engine, compiled rules and context builder
// the planner uses (prepare.go:384-436): naming.NewEngine ->
// CompileRulesWithVars -> buildNamingContext -> RenderVideo -> renderedTarget.
// The artifact context is decoded from the media evidence the ACCEPTED input
// carries (fixtureAcceptedArtifact), never from fixture constants: a default
// template that names an accepted media fact (anime.year, the TMDB season, the
// release's title candidates) would otherwise render a name the planner never
// renders.
func renderFixtureOverwriteTarget(t *testing.T, policy map[string]any, fixture fileFixture) (string, error) {
	t.Helper()
	sections, ok := policy["effective_sections"].(map[string]any)
	if !ok {
		return "", errors.New("fixture policy carries no effective sections")
	}
	rulesRaw, err := json.Marshal(sections[animedomain.SectionNamingRules])
	if err != nil {
		return "", err
	}
	var rules naming.NamingRules
	if err := json.Unmarshal(rulesRaw, &rules); err != nil {
		return "", fmt.Errorf("decode fixture naming rules: %w", err)
	}
	varsRaw, err := json.Marshal(policy["vars"])
	if err != nil {
		return "", err
	}
	var vars namingvars.FrozenEvaluation
	if err := json.Unmarshal(varsRaw, &vars); err != nil {
		return "", fmt.Errorf("decode fixture frozen vars: %w", err)
	}
	// The accept freezes the owner year and season into the Vars input facts
	// (acquisitionfixture, fixture_anime_year / fixture_anime_season); the
	// incumbent must sit at the target the planner renders from that same input.
	if vars.Input.Facts == nil {
		vars.Input.Facts = map[string]any{}
	}
	anime := map[string]any{"season": nil}
	if year, ok := policy["fixture_anime_year"]; ok && year != nil {
		anime["year"] = fmt.Sprint(year)
	}
	if season, ok := policy["fixture_anime_season"]; ok && season != nil {
		anime["season"] = season
	}
	vars.Input.Facts["anime"] = anime
	resolved, err := buildNamingContext(
		fixtureAcceptedArtifact(t, policy, fixture),
		artifactFileContext{
			// sourcePath drives File.Extension (config.go:199-201); omitting it
			// renders a filename ending in a bare "." instead of ".mkv", which
			// then fails the accepted-binding comparison against the planner's
			// rendered path.
			relativePath: filepath.Base(fixture.sourcePath),
			sourcePath:   fixture.sourcePath,
			mediaKind:    "video",
			sizeBytes:    fixture.size,
			facts:        json.RawMessage(`{"resolution":"1080p"}`),
		},
		&naming.FrozenEpisodeNamingFacts{Raw: "01", Number: "1", Display: "1"},
		&vars,
	)
	if err != nil {
		return "", err
	}
	engine := naming.NewEngine()
	compiled, err := engine.CompileRulesWithVars(rules, vars.Output)
	if err != nil {
		return "", err
	}
	directory, filename, err := compiled.RenderVideo(engine, resolved.Context)
	if err != nil {
		return "", err
	}
	return renderedTarget(directory, filename)
}

// fixtureAcceptedMedia returns the media evidence the ACCEPTED input will
// carry: the golden input vector's media object with the fixture's own
// fixture_media_values overlaid, the same two steps the seeding helper performs
// (acquisitionfixture.SeedObservedDownloadManifest). A render that runs before
// acceptance therefore resolves exactly the facts the planner resolves after it.
func fixtureAcceptedMedia(t *testing.T, policy map[string]any) map[string]any {
	t.Helper()
	media := nativeinput.Media(t)
	values, _ := policy["fixture_media_values"].(map[string]any)
	for key, value := range values {
		media[key] = value
	}
	return media
}

// fixtureAcceptedArtifact decodes that evidence through the same field names,
// states and defaults typed_plan_input.go:135-206 reads from the accepted input,
// so the context this fixture renders with carries the planner's own facts.
// Decoding accepted values is fixture serialization, not naming domain logic
// The known media keys mirror seedFileFixture's inserts: the
// source group and kind, the release's raw/parsed facts and the artifact file.
func fixtureAcceptedArtifact(t *testing.T, policy map[string]any, fixture fileFixture) artifactContext {
	t.Helper()
	raw, err := json.Marshal(fixtureAcceptedMedia(t, policy))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Title         frozenPlanValue `json:"title"`
		TitleOverride frozenPlanValue `json:"title_override"`
		ProviderTitle frozenPlanValue `json:"provider_title"`
		BGM           frozenPlanValue `json:"bgm"`
		TMDB          frozenPlanValue `json:"tmdb"`
		BGMTitles     frozenPlanValue `json:"bgm_titles"`
		TMDBTitles    frozenPlanValue `json:"tmdb_titles"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	artifact := artifactContext{
		managedAnimeID: fixture.animeID,
		groupName:      "Test Group",
		sourceKind:     "custom_rss",
		releaseID:      "release-file-test",
		releaseVersion: 1,
		rawFacts:       json.RawMessage(`{"title":"[Test Group] Fixture Anime - 01"}`),
		parsedFacts:    json.RawMessage(`{"episode":1,"group_name":"Test Group"}`),
	}
	if in.Title.State == "known" {
		if err := json.Unmarshal(in.Title.Value, &artifact.animeTitle); err != nil {
			t.Fatal(err)
		}
	}
	if in.TitleOverride.State == "known" {
		artifact.titleOverride.Valid = true
		if err := json.Unmarshal(in.TitleOverride.Value, &artifact.titleOverride.String); err != nil {
			t.Fatal(err)
		}
	}
	if in.ProviderTitle.State == "known" {
		if err := json.Unmarshal(in.ProviderTitle.Value, &artifact.providerTitle); err != nil {
			t.Fatal(err)
		}
	}
	if in.BGM.State == "known" {
		var value struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(in.BGM.Value, &value); err != nil {
			t.Fatal(err)
		}
		artifact.metadataBGM = sql.NullString{String: value.Ref, Valid: true}
	}
	if in.TMDB.State == "known" {
		var value struct {
			WorkRef string `json:"work_ref"`
			Kind    string `json:"kind"`
			Season  int64  `json:"season"`
		}
		if err := json.Unmarshal(in.TMDB.Value, &value); err != nil {
			t.Fatal(err)
		}
		artifact.tmdbWorkID = sql.NullString{String: value.WorkRef, Valid: true}
		artifact.tmdbMediaKind = sql.NullString{String: value.Kind, Valid: true}
		artifact.tmdbSeason = sql.NullInt64{Int64: value.Season, Valid: true}
	}
	if in.BGMTitles.State == "known" {
		var value struct {
			Title   *string `json:"title"`
			TitleCN *string `json:"title_cn"`
			Year    *int64  `json:"year"`
		}
		if err := json.Unmarshal(in.BGMTitles.Value, &value); err != nil {
			t.Fatal(err)
		}
		artifact.bindingTitles.bgmTitle, artifact.bindingTitles.bgmTitleCN = derefString(value.Title), derefString(value.TitleCN)
		artifact.bindingTitles.bgmYear = value.Year
	}
	if in.TMDBTitles.State == "known" {
		var value struct {
			TitleCN       *string `json:"title_cn"`
			OriginalTitle *string `json:"original_title"`
			Year          *int64  `json:"year"`
		}
		if err := json.Unmarshal(in.TMDBTitles.Value, &value); err != nil {
			t.Fatal(err)
		}
		artifact.bindingTitles.tmdbTitleCN, artifact.bindingTitles.tmdbOriginalTitle = derefString(value.TitleCN), derefString(value.OriginalTitle)
		artifact.bindingTitles.tmdbYear = value.Year
	}
	return artifact
}

// fixtureOverwriteIncumbent renders the planner's target (production naming
// path), creates the incumbent object there, and freezes it as the ACCEPTED
// ReplacementV1 binding: the admission SQL compares the accepted binding's
// expected_object location/size/identity/content.sha256 against the frozen
// prepare-time observation of that same file, so the binding must carry the
// full observed object shape -- a thin {canonical_path} stub fails
// FILE_PREPARE_OVERWRITE_PROOF_REQUIRED by construction. The observation uses
// the production path observer, and the object shape mirrors what the
// production accept path freezes (replacement_capture.go replacementObject /
// preparedPublicationObjects expected_target): same field names, same FV
// envelopes. The incumbent bytes are fixed so the test can assert them.
func fixtureOverwriteIncumbent(t *testing.T, policy map[string]any, fixture fileFixture) (string, error) {
	t.Helper()
	target, err := renderFixtureOverwriteTarget(t, policy, fixture)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(fixtureOverwriteIncumbentBytes), 0o644); err != nil {
		return "", err
	}
	observation, err := (filesystem.OSFileObserver{}).ObservePath(context.Background(), target)
	if err != nil {
		return "", err
	}
	// The location namespace must equal the one the prepare path freezes:
	// publicationLocation (file_publication_prepare.go) uses root=Dir(target),
	// relative_path=Base(target) and digests the same two-key namespace. A
	// library-root namespace here is exactly what the admission's location equality
	// rejects.
	locationRoot := filepath.Dir(target)
	namespace, err := canonicalPlanValue(map[string]any{"root": locationRoot, "adapter_version": "local-stable-read-v1"})
	if err != nil {
		return "", err
	}
	location := map[string]any{
		"namespace_digest": protocol.Digest("local-filesystem-namespace:v1", []byte(namespace)),
		"root":             locationRoot,
		"relative_path":    filepath.Base(target),
		"canonical_path":   target,
	}
	expectedObject := map[string]any{
		"location":    location,
		"file_type":   "regular",
		"size":        observation.SizeBytes,
		"identity":    pathIdentity(observation.Identity),
		"observed_at": protocol.FormatTime(observation.ObservedAt),
	}
	binding := map[string]any{
		"state": "known",
		"value": map[string]any{
			// RefVersion + ComparisonV1 members complete the ReplacementV1 shape;
			// Admission consumes only the expected_object fields, but the binding must
			// stay structurally identical to what the production accept freezes.
			"placement":       map[string]any{"ref": "fixture-placement-1", "version": 1},
			"expected_object": expectedObject,
			"comparison": map[string]any{
				"policy":              map[string]any{"ref": fixture.animeID, "version": 1},
				"candidate_quality":   fixtureAbsentQuality(),
				"existing_quality":    fixtureAbsentQuality(),
				"compared_dimensions": []string{},
				"verdict":             "replace",
				"evaluated_at":        protocol.FormatTime(observation.ObservedAt),
			},
		},
		"reason_code": nil,
	}
	canonical, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	policy["fixture_replacement_binding"] = json.RawMessage(canonical)
	policy["fixture_replacement_target"] = target
	return target, nil
}

// QualityV1 with every dimension in the missing state, the shape ComparisonV1
// carries when the fixture expresses no quality preference.
func fixtureAbsentQuality() map[string]any {
	missing := map[string]any{"state": "missing", "value": nil, "reason_code": nil, "raw_digest": nil}
	quality := map[string]any{
		"group": missing, "resolution": missing, "source": missing,
		"revision": missing, "languages": missing, "size": missing,
	}
	for _, key := range []string{"mapping_version", "parser_provenance", "profile", "rules_version"} {
		quality[key] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
	}
	return quality
}

// fixtureOverwriteIncumbentBytes is the fixed incumbent payload the fixture
// freezes into the accepted replacement binding and the chain test asserts
// against after the exchange.
const fixtureOverwriteIncumbentBytes = "occupied previous object"

// fixtureUnmanagedOverwriteKey marks a fixture policy whose overwrite
// incumbent is unmanaged: the file is created but no approval is frozen.
const fixtureUnmanagedOverwriteKey = "fixture_unmanaged_overwrite"

// rewriteFixtureNamingRoot pins the frozen directory templates to the fixture
// root. Naming targets must render absolute paths, so a fixture that wants the
// deployment's /library mount or an old relative template is re-rooted under
// the test directory instead of inheriting an implicit anchor.
func rewriteFixtureNamingRoot(t *testing.T, policy map[string]any, root string) {
	t.Helper()
	sections, ok := policy["effective_sections"].(map[string]any)
	if !ok {
		return
	}
	raw, err := json.Marshal(sections[animedomain.SectionNamingRules])
	if err != nil {
		t.Fatalf("encode fixture naming rules: %v", err)
	}
	var rules naming.NamingRules
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatalf("decode fixture naming rules: %v", err)
	}
	sections[animedomain.SectionNamingRules] = fixtureAbsoluteNamingRules(rules, root)
}

// fixtureAbsoluteNamingRules returns rules whose directory templates name root.
// Naming targets are absolute by construction, so a test double that supplies
// rules outside the frozen policy must supply the same re-rooted bytes the
// policy carries, never the deployment mount /library.
func fixtureAbsoluteNamingRules(rules naming.NamingRules, root string) naming.NamingRules {
	rules.TV.DirectoryTemplate = fixtureAbsoluteTemplate(rules.TV.DirectoryTemplate, root)
	rules.Movie.DirectoryTemplate = fixtureAbsoluteTemplate(rules.Movie.DirectoryTemplate, root)
	rules.Unknown.DirectoryTemplate = fixtureAbsoluteTemplate(rules.Unknown.DirectoryTemplate, root)
	return rules
}

// fixtureAbsoluteTemplate re-roots one stored directory template body under
// root. A body that does not open with an absolute path is rooted under the
// fixture root: naming targets are absolute, so leaving it unprefixed would
// render the deployment mount /library instead of the test's own tree.
func fixtureAbsoluteTemplate(body, root string) string {
	prefix := strings.TrimRight(filepath.ToSlash(root), "/")
	switch {
	case body == "/library" || body == "/media":
		return prefix
	case strings.HasPrefix(body, "/library/"):
		return prefix + strings.TrimPrefix(body, "/library")
	case strings.HasPrefix(body, "/media/"):
		return prefix + strings.TrimPrefix(body, "/media")
	case !strings.HasPrefix(body, "/"):
		return prefix + "/" + body
	}
	return body
}

func openFileOperationDBWithPolicy(t *testing.T, mutate func(map[string]any)) (*sql.DB, fileFixture) {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "source.mkv")
	content := []byte("source-content")
	if err := os.WriteFile(sourcePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	fixture := fileFixture{
		animeID:     "anime-file-test",
		artifactID:  "artifact-file-test",
		fileID:      "artifact-file-primary",
		root:        libraryRoot,
		sourcePath:  sourcePath,
		fingerprint: hex.EncodeToString(digest[:]),
		size:        int64(len(content)),
	}
	policy := frozenpolicy.CompleteMap()
	policy["route_root"] = libraryRoot
	// The fixture anime is "Fixture Anime" in every title candidate the
	// effective title reads (override > provider > TMDB Chinese > BGM Chinese >
	// BGM > the managed record title), so the name the naming rules render is
	// the fixture's own. The golden vector's media titles belong to another
	// anime ("进击的巨人"): leaving them in place while overriding only media.title
	// would let the render take a title from a work the fixture never seeds.
	// anime.year reads only the owner-edited year, frozen into the Vars input
	// (fixture_anime_year); the provider years in the media values never reach
	// naming.
	policy["fixture_anime_year"] = 2025
	// anime.season likewise reads only the owner-edited season (the fixture's
	// frozen Vars input, fixture_anime_season). The shipped TV directory
	// template keeps anime.season_folder as a folder level of its own, so a TV
	// fixture must carry one: the default is season 1, and a test that needs
	// the season-less refusal overrides this to nil.
	policy["fixture_anime_season"] = 1
	policy["fixture_media_values"] = map[string]any{
		"title": map[string]any{"state": "known", "value": "Fixture Anime", "reason_code": nil},
		"bgm_titles": map[string]any{"state": "known", "value": map[string]any{
			"title": "Fixture Anime", "title_cn": "Fixture Anime", "year": 2025,
		}, "reason_code": nil},
		"tmdb_titles": map[string]any{"state": "known", "value": map[string]any{
			"title_cn": "Fixture Anime", "original_title": "Fixture Anime", "year": nil,
		}, "reason_code": nil},
	}
	if mutate != nil {
		mutate(policy)
	}
	// Naming targets are absolute by construction: fixtures stand the test root
	// in for the deployment mount, so the frozen rules must name it explicitly.
	fixtureRoot := libraryRoot
	if root, ok := policy["route_root"].(string); ok && root != "" {
		fixtureRoot = root
	}
	rewriteFixtureNamingRoot(t, policy, fixtureRoot)
	// An overwrite delivery
	// needs the ACCEPTED ReplacementV1 binding in the plan input, and it must
	// name the very path the planner renders AND carry the full observed
	// expected_object (admission compares location/size/identity/content.sha256
	// against the frozen prepare observation). The fixture owns libraryRoot,
	// so it is the only place that can create the incumbent and freeze that
	// binding before the accept function computes admission_digest and the
	// vars evidence over these bytes -- neither can be rewritten afterwards
	// (the digest CHECK, the acquisition FK, the vars consistency guard and
	// the immutable vars record all bind them).
	if effect, ok := policy["effective_sections"].(map[string]any)[animedomain.SectionFileEffect].(map[string]any); ok && fmt.Sprint(effect["collision_policy"]) == "overwrite" {
		if _, incumbentErr := fixtureOverwriteIncumbent(t, policy, fixture); incumbentErr != nil {
			t.Fatalf("freeze the fixture overwrite incumbent: %v", incumbentErr)
		}
		if unmanaged, _ := policy[fixtureUnmanagedOverwriteKey].(bool); unmanaged {
			// The incumbent stays on disk but no approval names it: the
			// delivery is ensure_present/overwrite (unmanaged overwrite).
			delete(policy, "fixture_replacement_binding")
			delete(policy, "fixture_replacement_target")
		}
	}
	delete(policy, fixtureUnmanagedOverwriteKey)
	if name, ok := policy["fixture_primary_name"].(string); ok {
		newPath := filepath.Join(filepath.Dir(fixture.sourcePath), name)
		if err := os.Rename(fixture.sourcePath, newPath); err != nil {
			t.Fatal(err)
		}
		fixture.sourcePath = newPath
	}
	policyJSON, err := frozenpolicy.Encode(policy)
	if err != nil {
		t.Fatal(err)
	}
	seedFileFixture(t, db, fixture, policyJSON)
	return db, fixture
}

func seedFileFixture(t *testing.T, db *sql.DB, fixture fileFixture, policyJSON string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Fixture Anime')`, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ($1)`, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	for range []string{"subgroups", "rules"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ($1) ON CONFLICT (managed_anime_id) DO NOTHING`, fixture.animeID); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, fixture.animeID)
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-file-test', $1, 'custom', 'File test', 'active')`, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-file-test', 'subscription-file-test', 'custom_rss', 'https://feed.example.test/rss', 'https://feed.example.test/rss', 'Test Group', 'Fixture Anime')`); err != nil {
		t.Fatal(err)
	}
	// The release's frozen source facts carry the reltitle tag set a real
	// admission freezes: the default subtitle suffix template renders
	// rt.sub_lang, so a fixture that plans a bound subtitle must supply the
	// language the release was parsed with. Tests that need the absent-value
	// case overwrite source_facts themselves.
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-file-test', 'source-file-test', 'guid:file-test', '{"title":"[Test Group] Fixture Anime - 01"}', '{"group_name":"Test Group","title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@ca671af","tags":["resolution:1080p","sub_lang:SC"],"value_tags":["title:Fixture Anime","group:Test Group"]}}', '{"episode":1,"group_name":"Test Group"}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}

	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	manifest, _ := json.Marshal([]map[string]any{{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": fixture.size, "media_kind": "video", "identity_evidence": map[string]any{}}})
	var policyFixture struct {
		Manifest json.RawMessage `json:"fixture_manifest"`
	}
	if err := json.Unmarshal([]byte(policyJSON), &policyFixture); err != nil {
		t.Fatal(err)
	}
	if len(policyFixture.Manifest) > 0 {
		manifest = policyFixture.Manifest
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "file-fixture-root", "download-file-test", "release-file-test", "download:file-test", `{"release_stable_item_key":"guid:file-test","source_locator_display":"https://feed.example.test/rss"}`, policyJSON, "completed", `null`, string(manifest))
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'download-file-test', verdict = 'submitted' WHERE id = 'release-file-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-file-test', 'download-file-test', 'file-fixture-root', '{}', '{}', 'verified', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ($1, 'artifact-file-test', 'file-fixture-root', 'primary', $5, $2, 'video', $3, $4, '{"resolution":"1080p"}', 'not_applicable')`, fixture.fileID, fixture.sourcePath, fixture.size, fingerprintJSONForTest(fixture.fingerprint, fixture.size), filepath.Base(fixture.sourcePath)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedFixtureEpisodeSnapshot(t, db, manifest)
}

func newFixtureService(t *testing.T, db *sql.DB, fixture fileFixture) *Service {
	t.Helper()
	return newFixtureServiceWithPorts(t, db, fixture, &testExecutor{}, &testObserver{fingerprint: fixture.fingerprint, sizeBytes: fixture.size})
}

func newFixtureServiceWithPorts(t *testing.T, db *sql.DB, fixture fileFixture, executor FileExecutor, observer FileObserver) *Service {
	t.Helper()
	service, err := NewService(db, executor, observer, func() time.Time { return time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	// These fakes intentionally model the checkpoint port only. Keep the
	// production authorizer requirement intact while giving overwrite planning
	// the same typed identity facts as a real filesystem executor.
	service.parentAuthorizer = testParentIdentityAuthorizer{}
	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetActivityWriter(writer)
	return service
}

func prepareFixture(t *testing.T, db *sql.DB, service *Service, artifactID string) {
	t.Helper()
	ctx := context.Background()
	var planID string
	err := db.QueryRow(`SELECT id FROM postprocessing_plans WHERE artifact_id=$1`, artifactID).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		tx, err := ownerTestDB(t).BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		planID, err = seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: artifactID}, nil)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM postprocessing_plans WHERE id=$1`, planID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "succeeded" {
		return
	}
	claim := claimPlanWorkItem(t, db, "typed-fixture-planner")
	if claim.Owner.ID != planID {
		t.Fatal("fixture claimed a different Plan")
	}
	out, err := service.PostProcessingPlanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != worker.OutcomeCommitted || out.WriteOwner != nil || out.PostTransition != nil {
		t.Fatalf("fixture planning did not commit: %+v", out)
	}
}

// finishOutcome applies one terminal outcome through the store, honoring the
// optional post-transition hook (the durable placement obligation commits in
// the same transaction as the completion).
func finishOutcome(t *testing.T, store *workitems.Store, claim workitems.Claim, outcome worker.Outcome) {
	t.Helper()
	if outcome.PostTransition != nil {
		if err := store.FinishWithResultPostTransition(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
}

func runClaimExpectFinishError(t *testing.T, db *sql.DB, service *Service, _ fileFixture) worker.Outcome {
	t.Helper()
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim, err := store.ClaimDue(context.Background(), "file-operation-test-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("expected terminal outcome, got %+v", outcome)
	}
	if outcome.PostTransition != nil {
		if err := store.FinishWithResultPostTransition(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition); err == nil {
			t.Fatal("nil Activity writer must reject terminal owner mutation")
		}
		return outcome
	}
	if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err == nil {
		t.Fatal("nil Activity writer must reject terminal owner mutation")
	}
	return outcome
}
func runClaim(t *testing.T, db *sql.DB, service *Service, _ fileFixture) worker.Outcome {
	t.Helper()
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim, err := store.ClaimDue(context.Background(), "file-operation-test-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	switch outcome.Kind {
	case worker.OutcomeFinish:
		finishOutcome(t, store, claim, outcome)
	case worker.OutcomeRetry:
		if outcome.RetryUntilResolved {
			if _, err := store.RetryWithoutAttemptBudget(context.Background(), claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
		} else if _, err := store.Retry(context.Background(), claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	return outcome
}

func fingerprintJSONForTest(value string, size int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"algorithm":"sha256","value":"%s","size_bytes":%d}`, value, size))
}

type recordingPlacementSink struct {
	calls []string
	err   error
}

func (r *recordingPlacementSink) AdmitFromFileOperation(_ context.Context, fileOperationID string) error {
	r.calls = append(r.calls, fileOperationID)
	return r.err
}

func TestBuildOverwriteExecutionSnapshotTargetExists(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("source bytes for overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old target bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := operationPlan{id: "op-overwrite-1", sourcePath: source, targetPath: target, sourceFingerprint: fingerprintJSONForTest(digestForTest(t, source), int64(len("source bytes for overwrite")))}
	snapshot, err := buildOverwriteExecutionSnapshot(plan, testParentIdentityAuthorizer{})
	if err != nil {
		t.Fatalf("build overwrite snapshot: %v", err)
	}
	if len(snapshot) == 0 {
		t.Fatal("existing target must produce an overwrite snapshot")
	}
	var envelope overwriteExecutionSnapshot
	if err := json.Unmarshal(snapshot, &envelope); err != nil {
		t.Fatalf("decode overwrite snapshot: %v", err)
	}
	if envelope.OperationID != plan.id || envelope.PlanDigest != operationPlanDigestForPlan(plan) {
		t.Fatalf("snapshot binding=%s/%s", envelope.OperationID, envelope.PlanDigest)
	}
	decoded := envelope.Recovery
	if decoded.Stage != domain.OverwriteStagePrepared {
		t.Fatalf("stage=%q want prepared", decoded.Stage)
	}
	if decoded.SourceObservation.Fingerprint == "" || decoded.TargetObservation.Fingerprint == "" {
		t.Fatalf("observations must carry fingerprints: source=%+v target=%+v", decoded.SourceObservation, decoded.TargetObservation)
	}
	if decoded.SourceObservation.Identity == (domain.FileIdentity{}) || decoded.TargetObservation.Identity == (domain.FileIdentity{}) {
		t.Fatalf("observations must carry device/inode identity: %+v", decoded)
	}
	if runtime.GOOS == "linux" {
		marker, err := fileidentity.ChangeNanosFromPath(source)
		if err != nil {
			t.Fatalf("probe source mutation marker: %v", err)
		}
		if marker != 0 && (decoded.SourceObservation.ChangeNanos == 0 || decoded.TargetObservation.ChangeNanos == 0) {
			t.Fatalf("available Linux overwrite observations must carry mutation markers: source=%+v target=%+v", decoded.SourceObservation, decoded.TargetObservation)
		}
	}
	handle, handleErr := fileidentity.HandleEvidenceFromPath(source)
	if handleErr != nil && !errors.Is(handleErr, fileidentity.ErrEvidenceUnavailable) {
		t.Fatalf("probe source handle evidence: %v", handleErr)
	}
	if handleErr == nil && handle == nil {
		t.Fatal("available handle evidence must be retained")
	}
	if handleErr == nil && (decoded.SourceObservation.HandleEvidence == nil || decoded.TargetObservation.HandleEvidence == nil) {
		t.Fatalf("available overwrite observations must carry handle evidence: source=%+v target=%+v", decoded.SourceObservation, decoded.TargetObservation)
	}
	if decoded.SourceParentIdentity == (domain.FileIdentity{}) || decoded.TargetParentIdentity == (domain.FileIdentity{}) {
		t.Fatalf("recovery must carry durable parent identities: %+v", decoded)
	}
	if decoded.CanonicalTarget != filepath.Clean(target) || decoded.StagingPath == "" {
		t.Fatalf("recovery paths are wrong: %+v", decoded)
	}
}

func TestBuildOverwriteExecutionSnapshotRejectsSourceDrift(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("actual source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old target"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := operationPlan{
		id:                "op-overwrite-drift",
		sourcePath:        source,
		targetPath:        target,
		sourceFingerprint: fingerprintJSONForTest(strings.Repeat("0", 64), int64(len("actual source"))),
	}
	if _, err := buildOverwriteExecutionSnapshot(plan, testParentIdentityAuthorizer{}); !errors.Is(err, ErrArtifactFingerprintMismatch) {
		t.Fatalf("source drift was not rejected with artifact fingerprint mismatch: %v", err)
	}
}

func TestBuildOverwriteExecutionSnapshotTargetAbsent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mkv")
	if err := os.WriteFile(source, []byte("source bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := operationPlan{id: "op-overwrite-2", sourcePath: source, targetPath: filepath.Join(root, "absent.mkv"), sourceFingerprint: fingerprintJSONForTest(digestForTest(t, source), int64(len("source bytes")))}
	snapshot, err := buildOverwriteExecutionSnapshot(plan, testParentIdentityAuthorizer{})
	if err != nil {
		t.Fatalf("absent target must not error: %v", err)
	}
	if len(snapshot) != 0 {
		t.Fatalf("absent target must produce no snapshot, got %d bytes", len(snapshot))
	}
}

func TestObserveFileObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obs.mkv")
	content := []byte("observation bytes")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	observation, err := observeFileObservation(path)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.SizeBytes != int64(len(content)) || observation.Fingerprint == "" {
		t.Fatalf("observation is incomplete: %+v", observation)
	}
	if observation.Identity.Device == 0 || observation.Identity.Inode == 0 {
		t.Fatalf("observation identity is missing: %+v", observation.Identity)
	}
	if runtime.GOOS == "linux" {
		marker, err := fileidentity.ChangeNanosFromPath(path)
		if err != nil {
			t.Fatalf("probe mutation marker: %v", err)
		}
		if marker != 0 && observation.ChangeNanos == 0 {
			t.Fatalf("available Linux observation mutation marker is missing: %+v", observation)
		}
	}
	handle, handleErr := fileidentity.HandleEvidenceFromPath(path)
	if handleErr != nil && !errors.Is(handleErr, fileidentity.ErrEvidenceUnavailable) {
		t.Fatalf("probe handle evidence: %v", handleErr)
	}
	if handleErr == nil && handle == nil {
		t.Fatal("available handle evidence must be retained")
	}
	if handleErr == nil && observation.HandleEvidence == nil {
		t.Fatalf("available handle evidence is missing: %+v", observation)
	}
}

func digestForTest(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
