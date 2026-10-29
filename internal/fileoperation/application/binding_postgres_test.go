package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/postprocessing/naming"
)

// addArtifactFile inserts one extra artifact file into the shared fixture.

type freshTargetObserver struct{}

func (freshTargetObserver) Observe(context.Context, string) (string, error) {
	return "", nil
}

func (freshTargetObserver) ObserveFile(_ context.Context, path string) (domain.FileObservation, error) {
	info, err := os.Stat(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	return domain.FileObservation{SizeBytes: info.Size()}, nil
}

// pathReadingTargetObserver adds the plan-stage PathObserver face to the
// freshTargetObserver stub, so the plan's own target observation reads the real
// filesystem — the shipped composition root registers one observer carrying
// both faces, and only its absence stays out of the product.
type pathReadingTargetObserver struct {
	freshTargetObserver
}

func (pathReadingTargetObserver) ObservePath(ctx context.Context, path string) (domain.PathObservation, error) {
	return filesystem.OSFileObserver{}.ObservePath(ctx, path)
}

func addArtifactFile(t *testing.T, db *sql.DB, fixture fileFixture, id, ref, relativePath, sourcePath, mediaKind string, size int64) {
	t.Helper()
	bindingState := bindingStateNotApplicable
	if mediaKind == "subtitle" {
		bindingState = bindingStateMissing
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), $3, $4, $5, $6, $7, $8, '{}', $9)`,
		id, fixture.artifactID, ref, relativePath, sourcePath, mediaKind, size,
		fingerprintJSONForTest(metadataFingerprintForTest(t, sourcePath), size), bindingState); err != nil {
		t.Fatal(err)
	}
}

// setSubtitleSuffixTemplate rewrites the fixture naming override, keeping the
// default tv/movie variants and replacing only the suffix template.
func setSubtitleSuffixTemplate(t *testing.T, db *sql.DB, fixture fileFixture, suffixTemplate string) {
	t.Helper()
	value, err := json.Marshal(map[string]any{
		"language": naming.Language,
		"tv": map[string]string{
			"directory_template":      "${sanitize(anime.title)}/${defaultValue('Season 01', anime.season_folder)}",
			"video_filename_template": "${sanitize(anime.title)} - E${pad(ep.display, 2)}${media.resolution ? ` [${media.resolution}]` : ''}${media.video_codec ? ` [${media.video_codec}]` : ''}${release.lang ? ` [${release.lang}]` : ''}${anime.group ? `@${anime.group}` : ''}.${file.ext}",
		},
		"movie": map[string]string{
			"directory_template":      "${sanitize(anime.title)}",
			"video_filename_template": "${sanitize(anime.title)}${media.resolution ? ` [${media.resolution}]` : ''}${media.video_codec ? ` [${media.video_codec}]` : ''}${release.lang ? ` [${release.lang}]` : ''}${anime.group ? `@${anime.group}` : ''}.${file.ext}",
		},
		"subtitle_suffix_template": suffixTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	var namingValue map[string]any
	if err := json.Unmarshal(value, &namingValue); err != nil {
		t.Fatal(err)
	}
	setFrozenSectionValue(t, db, "download-file-test", animedomain.SectionNamingRules, namingValue)
}

type operationRow struct {
	id                    string
	artifactFileID        string
	state                 string
	targetPath            string
	failureCode           string
	boundVideoOperationID sql.NullString
	renderSnapshot        []byte
}

func loadOperations(t *testing.T, db *sql.DB, artifactID string) []operationRow {
	t.Helper()
	rows, err := db.Query(`
SELECT id, artifact_file_id, state, target_path, COALESCE(failure_code, ''), bound_video_operation_id, frozen_render_snapshot
FROM file_operations WHERE artifact_id = $1 ORDER BY created_at, id`, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []operationRow
	for rows.Next() {
		var row operationRow
		if err := rows.Scan(&row.id, &row.artifactFileID, &row.state, &row.targetPath, &row.failureCode, &row.boundVideoOperationID, &row.renderSnapshot); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func operationForFile(t *testing.T, operations []operationRow, fileID string) operationRow {
	t.Helper()
	for _, operation := range operations {
		if operation.artifactFileID == fileID {
			return operation
		}
	}
	t.Fatalf("no operation for artifact file %q", fileID)
	return operationRow{}
}

func canonicalFixtureRoot(t *testing.T, fixture fileFixture) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// fixtureDefaultVideoTarget is the path the shipped default naming rules render
// for the fixture's main video — the kind word, the "Title (Year)" name, the
// owner season folder, then <title> <SxxEyy>.<ext>
// (internal/postprocessing/naming/naming.go) — with the fixture's own title and
// the owner year and season its frozen Vars input carries. The fixture media
// binds TMDB kind tv, season 1, the owner year is 2025, and the frozen episode
// is 1, so the expected target is stable by construction.
func fixtureDefaultVideoTarget(t *testing.T, fixture fileFixture) string {
	t.Helper()
	return filepath.Join(canonicalFixtureRoot(t, fixture), "TV", "Fixture Anime (2025)", "Season 01", "Fixture Anime S01E01.mkv")
}

// TestPrepareBindsSubtitleToLockedVideoTarget pins the current contract: a
// subtitle overwrite next to the video is refused — only a video target with
// an approved replacement decision is ever an approved overwrite path (one
// approval per video file), and persistOperationPlan's binding backpressure
// fails the whole typed plan with FILE_PLAN_REPLACEMENT_BINDING_INVALID and
// zero operations. The positive control below keeps the policy-independent
// semantics (subtitle composed from the locked video target) alive.
func TestPrepareBindsSubtitleToLockedVideoTarget(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "sub-1", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		p["fixture_media_values"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tv-1", "kind": "tv", "season": 1, "version": 1}, "reason_code": nil}
		p["fixture_anime_season"] = 1
		sections := p["effective_sections"].(map[string]any)
		rules := naming.DefaultNamingRules()
		rules.SubtitleSuffixTemplate = "${defaultValue('und', file.sub.lang)}"
		sections[animedomain.SectionNamingRules] = rules
		sections[animedomain.SectionFileEffect] = map[string]any{"operation": "move", "collision_policy": "overwrite"}
	})
	defer db.Close()
	subtitlePath := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(subtitlePath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-1", "sub-1", "source.chs.ass", subtitlePath, "subtitle", 8)
	videoTarget := fixtureDefaultVideoTarget(t, fixture)
	// The suffix here comes from the caller's own suffix template
	// (file.sub.lang), not from the default rules: only the directory and the
	// locked basename are the shipped default layout.
	subtitleTarget := strings.TrimSuffix(videoTarget, ".mkv") + ".zh-Hans.ass"
	if err := os.MkdirAll(filepath.Dir(videoTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoTarget, []byte("existing video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitleTarget, []byte("old subs"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	// The approved replacement names the video; the subtitle target holds an
	// unmanaged file no approval names, which the overwrite setting replaces
	// (the overwrite setting replaces it).
	operations := loadOperations(t, db, fixture.artifactID)
	if len(operations) != 2 {
		t.Fatalf("approved video + unmanaged subtitle overwrite must plan 2 operations, got %d", len(operations))
	}
	subtitle := operationForFile(t, operations, "artifact-file-sub-1")
	if subtitle.targetPath != subtitleTarget || subtitle.boundVideoOperationID.String == "" {
		t.Fatalf("subtitle must compose from the locked video target: %+v", subtitle)
	}
	var intent, collision string
	if err := db.QueryRow(`SELECT intent,collision_policy FROM file_operations WHERE id=$1`, subtitle.id).Scan(&intent, &collision); err != nil {
		t.Fatal(err)
	}
	if intent != "ensure_present" || collision != "overwrite" {
		t.Fatalf("unmanaged subtitle overwrite intent=%s collision=%s", intent, collision)
	}
}

// TestPrepareAdoptsExistingSidecarUnderReplacement pins the replacement ruling:
// under the same overwrite delivery, a subtitle target that already holds the
// subtitle's exact file (same size and content) is adopted without a write
// instead of failing the whole typed plan; the video stays the only
// replacement incumbent.
func TestPrepareAdoptsExistingSidecarUnderReplacement(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "sub-1", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		p["fixture_media_values"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tv-1", "kind": "tv", "season": 1, "version": 1}, "reason_code": nil}
		p["fixture_anime_season"] = 1
		sections := p["effective_sections"].(map[string]any)
		rules := naming.DefaultNamingRules()
		rules.SubtitleSuffixTemplate = "${defaultValue('und', file.sub.lang)}"
		sections[animedomain.SectionNamingRules] = rules
		sections[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "overwrite"}
	})
	defer db.Close()
	subtitlePath := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(subtitlePath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-1", "sub-1", "source.chs.ass", subtitlePath, "subtitle", 8)
	videoTarget := fixtureDefaultVideoTarget(t, fixture)
	subtitleTarget := strings.TrimSuffix(videoTarget, ".mkv") + ".zh-Hans.ass"
	if err := os.MkdirAll(filepath.Dir(videoTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitleTarget, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(subtitleTarget)
	if err != nil {
		t.Fatal(err)
	}

	// The adopt proof is a path observation of real files.
	service := newFixtureServiceWithPorts(t, db, fixture, &testExecutor{}, filesystem.OSFileObserver{})
	prepareFixture(t, db, service, fixture.artifactID)

	var planState, lastErrorReason string
	if err := db.QueryRow(`SELECT p.state,COALESCE(w.last_error_reason,'') FROM postprocessing_plans p JOIN work_item_history w ON w.kind='postprocess_plan' AND w.owner_id=p.id WHERE p.artifact_id=$1`, fixture.artifactID).Scan(&planState, &lastErrorReason); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lastErrorReason, "FILE_PLAN_REPLACEMENT_BINDING_INVALID") {
		t.Fatalf("an identical existing subtitle must be adopted, got plan=%s reason=%q", planState, lastErrorReason)
	}
	for _, op := range loadOperations(t, db, fixture.artifactID) {
		if op.targetPath == subtitleTarget {
			t.Fatalf("the adopted subtitle needs no file operation: %+v", op)
		}
	}
	after, err := os.Stat(subtitleTarget)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("the adopted subtitle must not be written: err=%v", err)
	}
	var kind string
	if err := db.QueryRow(`SELECT f.plan_kind FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.artifact_id=$1 AND f.file_ref='sub-1'`, fixture.artifactID).Scan(&kind); err != nil || kind != "adopt" {
		t.Fatalf("the existing subtitle must be an adopt item: kind=%q err=%v plan=%s", kind, err, planState)
	}
}

// TestPrepareBindsSubtitleToLockedVideoTargetVersionControl is the
// positive control: the same dual-file graph under a version collision keeps
// every policy-independent assertion of the historical test alive — two
// operations, the subtitle bound to the video through the production binder,
// and the subtitle target composed from the locked video directory, basename,
// suffix and its own extension.
func TestPrepareBindsSubtitleToLockedVideoTargetVersionControl(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "sub-1", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		p["fixture_media_values"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tv-1", "kind": "tv", "season": 1, "version": 1}, "reason_code": nil}
		p["fixture_anime_season"] = 1
		sections := p["effective_sections"].(map[string]any)
		rules := naming.DefaultNamingRules()
		rules.SubtitleSuffixTemplate = "${defaultValue('und', file.sub.lang)}"
		sections[animedomain.SectionNamingRules] = rules
		// "versioned" is the policy literal that maps to the DB's version
		// collision (collisionPolicyToDB); the bare "version" is not a valid
		// fileOperationCollisionPolicy and fails closed to block.
		sections[animedomain.SectionFileEffect] = map[string]any{"operation": "move", "collision_policy": "versioned"}
	})
	defer db.Close()
	subtitlePath := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(subtitlePath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-1", "sub-1", "source.chs.ass", subtitlePath, "subtitle", 8)
	videoTarget := fixtureDefaultVideoTarget(t, fixture)
	if err := os.MkdirAll(filepath.Dir(videoTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoTarget, []byte("existing video"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	if len(operations) != 2 {
		t.Fatalf("expected video and subtitle operations, got %d", len(operations))
	}
	video := operationForFile(t, operations, fixture.fileID)
	subtitle := operationForFile(t, operations, "artifact-file-sub-1")
	if video.state != ownerStatePending {
		t.Fatalf("video operation state = %q", video.state)
	}
	if subtitle.state != ownerStatePending {
		t.Fatalf("subtitle operation state = %q failure=%q", subtitle.state, subtitle.failureCode)
	}
	if !subtitle.boundVideoOperationID.Valid || subtitle.boundVideoOperationID.String != video.id {
		t.Fatalf("subtitle bound_video_operation_id = %+v, want %q", subtitle.boundVideoOperationID, video.id)
	}
	// The subtitle target is composed from the locked video directory and
	// basename plus only the suffix and its own extension (policy-independent).
	wantTarget := strings.TrimSuffix(videoTarget, ".mkv") + ".zh-Hans.ass"
	if subtitle.targetPath != wantTarget {
		t.Fatalf("subtitle target = %q, want %q", subtitle.targetPath, wantTarget)
	}
	var videoSnapshot struct {
		Directory string `json:"directory"`
		Filename  string `json:"filename"`
	}
	if err := json.Unmarshal(video.renderSnapshot, &videoSnapshot); err != nil {
		t.Fatal(err)
	}
	var subtitleSnapshot struct {
		Suffix  string `json:"suffix"`
		Binding struct {
			State                 string  `json:"state"`
			BoundVideoFileRef     string  `json:"bound_video_file_ref"`
			BoundVideoOperationID string  `json:"bound_video_operation_id"`
			LockedDirectory       string  `json:"locked_directory"`
			LockedBasename        string  `json:"locked_basename"`
			LockedExtension       string  `json:"locked_extension"`
			Method                string  `json:"method"`
			Confidence            float64 `json:"confidence"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(subtitle.renderSnapshot, &subtitleSnapshot); err != nil {
		t.Fatal(err)
	}
	if subtitleSnapshot.Suffix != "zh-Hans" {
		t.Fatalf("suffix = %q, want zh-Hans", subtitleSnapshot.Suffix)
	}
	binding := subtitleSnapshot.Binding
	if binding.State != bindingStateBound || binding.BoundVideoFileRef != "primary" || binding.BoundVideoOperationID != video.id {
		t.Fatalf("frozen binding = %+v", binding)
	}
	if binding.LockedDirectory != videoSnapshot.Directory || binding.LockedBasename != strings.TrimSuffix(videoSnapshot.Filename, ".mkv") || binding.LockedExtension != "mkv" {
		t.Fatalf("locked segments = %+v, video = %+v", binding, videoSnapshot)
	}
	if binding.Method != bindingMethodBasenameMatch || binding.Confidence != 1.0 {
		t.Fatalf("binding method/confidence = %+v", binding)
	}

	// The binder decision is persisted on the artifact file row with the
	// locked target segments.
	var state, boundRef string
	var rawSnapshot []byte
	if err := db.QueryRow(`
SELECT subtitle_binding_state, bound_video_file_ref, subtitle_binding_snapshot
FROM artifact_files WHERE id = 'artifact-file-sub-1'`).Scan(&state, &boundRef, &rawSnapshot); err != nil {
		t.Fatal(err)
	}
	if state != bindingStateBound || boundRef != "primary" {
		t.Fatalf("artifact file binding = %q/%q", state, boundRef)
	}
	persisted, err := decodeBindingSnapshot(rawSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Method != bindingMethodBasenameMatch || persisted.Confidence != 1.0 {
		t.Fatalf("persisted snapshot = %+v", persisted)
	}
	if persisted.Locked == nil || persisted.Locked.Directory != videoSnapshot.Directory || persisted.Locked.VideoOperationID != video.id {
		t.Fatalf("persisted locked target = %+v", persisted.Locked)
	}

	// Replanning is idempotent and never rebinds.
	prepareFixture(t, db, service, fixture.artifactID)
	if again := loadOperations(t, db, fixture.artifactID); len(again) != 2 {
		t.Fatalf("reprepare created operations: %d", len(again))
	}
}

func TestPrepareSubtitleBindingMissingBlocksSubtitleOnly(t *testing.T) {
	// A first-level subtitle whose name starts with no video name is unbound.
	db, fixture := openFileOperationDBWithPolicy(t, bindingManifestFixture(t, map[string]any{"file_ref": "sub-missing", "relative_path": "notes.ass", "exact_size": 5, "media_kind": "subtitle"}))
	defer db.Close()
	subtitlePath := filepath.Join(t.TempDir(), "notes.ass")
	if err := os.WriteFile(subtitlePath, []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-missing", "sub-missing", "notes.ass", subtitlePath, "subtitle", 5)

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	video := operationForFile(t, operations, fixture.fileID)
	assertTypedFailedFileResult(t, db, fixture.artifactID, "sub-missing", "subtitle_binding_missing")
	if video.state != ownerStatePending {
		t.Fatalf("video operation must stay pending: %q", video.state)
	}
	var state string
	var rawSnapshot []byte
	if err := db.QueryRow(`
SELECT subtitle_binding_state, subtitle_binding_snapshot
FROM artifact_files WHERE id = 'artifact-file-sub-missing'`).Scan(&state, &rawSnapshot); err != nil {
		t.Fatal(err)
	}
	if state != bindingStateMissing {
		t.Fatalf("artifact file binding state = %q", state)
	}
	persisted, err := decodeBindingSnapshot(rawSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Diagnostics) != 1 || persisted.Diagnostics[0].Code != "subtitle_binding_missing" {
		t.Fatalf("persisted diagnostics = %+v", persisted.Diagnostics)
	}
	// That the pending video then really completes through the typed lane
	// while the failed subtitle declaration stays failed is proven by
	// TestPGPartialNamingFailureKeepsFailedOwnerAndCompletesVideo.
}

func TestPrepareSubtitleSuffixInvalidBlocksSubtitleOnly(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "sub-suffix", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		rules := naming.DefaultNamingRules()
		rules.SubtitleSuffixTemplate = "../escape"
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
	})
	defer db.Close()
	subtitlePath := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(subtitlePath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-suffix", "sub-suffix", "source.chs.ass", subtitlePath, "subtitle", 8)
	setSubtitleSuffixTemplate(t, db, fixture, `../escape`)

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	video := operationForFile(t, operations, fixture.fileID)
	assertTypedFailedFileResult(t, db, fixture.artifactID, "sub-suffix", "subtitle_suffix_invalid")
	if video.state != ownerStatePending {
		t.Fatalf("video operation must stay pending: %q", video.state)
	}
	// The binding itself succeeded; only the suffix render failed.
	var state string
	if err := db.QueryRow(`SELECT subtitle_binding_state FROM artifact_files WHERE id = 'artifact-file-sub-suffix'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != bindingStateBound {
		t.Fatalf("artifact file binding state = %q", state)
	}
}

func TestPrepareBoundTargetMismatchFailsExplicitly(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, bindingManifestFixture(t, map[string]any{"file_ref": "sub-drift", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"}))
	defer db.Close()
	subtitlePath := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(subtitlePath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-drift", "sub-drift", "source.chs.ass", subtitlePath, "subtitle", 8)

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	video := operationForFile(t, operations, fixture.fileID)
	subtitle := operationForFile(t, operations, "artifact-file-sub-drift")
	if subtitle.state != ownerStatePending {
		t.Fatalf("initial subtitle operation = %q/%q", subtitle.state, subtitle.failureCode)
	}

	// Simulate frozen-target drift and a subtitle replan: the video
	// operation's frozen directory changed while the binding lock persisted.
	if _, err := db.Exec(`
UPDATE file_operations
SET frozen_render_snapshot = jsonb_set(frozen_render_snapshot, '{directory}', '"Other/Dir"')
WHERE id = $1`, video.id); err != nil {
		t.Fatal(err)
	}
	// A sealed prepared graph is observed, never deleted/replanned to hide drift.
	before := planGraphSnapshot(t, db)
	assertCommittedReentry(t, service, fixture.artifactID, nil, "blocked", "")
	if after := planGraphSnapshot(t, db); after != before {
		t.Fatal("bound-target rejection rewrote sealed graph")
	}
	// The binding decision was never silently rebound.
	var state, boundRef string
	if err := db.QueryRow(`
SELECT subtitle_binding_state, bound_video_file_ref
FROM artifact_files WHERE id = 'artifact-file-sub-drift'`).Scan(&state, &boundRef); err != nil {
		t.Fatal(err)
	}
	if state != bindingStateBound || boundRef != "primary" {
		t.Fatalf("binding was silently rebound: %q/%q", state, boundRef)
	}
}

// Each first-level video plans from its own filename, and a subtitle binds to
// the video whose name is a strict prefix of the subtitle name.
func TestPrepareMultiVideoBindsOwnEpisode(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		policy["fixture_manifest"] = []map[string]any{
			{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "video-ep2", "ordinal": 1, "relative_path": "Show - 02.mkv", "exact_size": 5, "media_kind": "video", "episode": "2", "identity_evidence": map[string]any{}},
			{"file_ref": "sub-ep2", "ordinal": 2, "relative_path": "Show - 02.chs.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
		}
	})
	defer db.Close()
	videoDir := t.TempDir()
	videoPath := filepath.Join(videoDir, "Show - 02.mkv")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-video-ep2", "video-ep2", "Show - 02.mkv", videoPath, "video", 5)
	subtitlePath := filepath.Join(videoDir, "Show - 02.chs.ass")
	if err := os.WriteFile(subtitlePath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-ep2", "sub-ep2", "Show - 02.chs.ass", subtitlePath, "subtitle", 8)

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	if len(operations) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(operations))
	}
	video2 := operationForFile(t, operations, "artifact-file-video-ep2")
	subtitle := operationForFile(t, operations, "artifact-file-sub-ep2")
	if subtitle.state != ownerStatePending {
		t.Fatalf("subtitle operation = %q/%q", subtitle.state, subtitle.failureCode)
	}
	if !subtitle.boundVideoOperationID.Valid || subtitle.boundVideoOperationID.String != video2.id {
		t.Fatalf("subtitle must bind the video its name starts with: %+v", subtitle.boundVideoOperationID)
	}
	var method string
	if err := db.QueryRow(`
SELECT subtitle_binding_snapshot->>'method' FROM artifact_files WHERE id = 'artifact-file-sub-ep2'`).Scan(&method); err != nil {
		t.Fatal(err)
	}
	if method != bindingMethodBasenameMatch {
		t.Fatalf("binding method = %q, want basename_match", method)
	}
	// The subtitle target locks the bound video's rendered directory.
	if filepath.Dir(subtitle.targetPath) != filepath.Dir(video2.targetPath) {
		t.Fatalf("subtitle target %q does not share the bound video directory %q", subtitle.targetPath, video2.targetPath)
	}
}

func TestSubtitleBindingDatabaseConstraints(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	ctx := context.Background()

	expectViolation := func(name, statement string, args ...any) {
		t.Helper()
		// Supply the legitimate root so the intended binding constraint is tested.
		statement = strings.Replace(statement, "artifact_id, file_ref", "artifact_id, acquisition_id, file_ref", 1)
		statement = strings.Replace(statement, "$1, 'f-", "$1, (SELECT acquisition_id FROM artifacts WHERE id=$1), 'f-", 1)
		statement = strings.Replace(statement, "'artifact-other', 'f-e'", "'artifact-other', 'fileop-other-root', 'f-e'", 1)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, statement, args...); err == nil {
			t.Fatalf("%s: expected a constraint violation", name)
		}
	}

	// The state column is NOT NULL without a default after migration 013.
	expectViolation("binding state is required", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts)
VALUES ('f-a', $1, 'f-a', 'a.ass', '/tmp/a.ass', 'subtitle', 1, '{}', '{}')`, fixture.artifactID)

	// Non-subtitle rows must stay not_applicable without ref or snapshot.
	expectViolation("non-subtitle rows stay not_applicable", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('f-b', $1, 'f-b', 'b.mkv', '/tmp/b.mkv', 'video', 1, '{}', '{}', 'missing')`, fixture.artifactID)

	// bound iff the ref is present.
	expectViolation("bound requires a ref", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('f-c', $1, 'f-c', 'c.ass', '/tmp/c.ass', 'subtitle', 1, '{}', '{}', 'bound')`, fixture.artifactID)
	expectViolation("unbound states reject a ref", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state, bound_video_file_ref)
VALUES ('f-d', $1, 'f-d', 'd.ass', '/tmp/d.ass', 'subtitle', 1, '{}', '{}', 'missing', 'primary')`, fixture.artifactID)

	// The bound ref must point into the same artifact. The release/task pair
	// has a deferred circular FK, so seed it in one transaction like the
	// fixture does.
	seedTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer seedTx.Rollback()
	if _, err := seedTx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-other', 'source-file-test', 'guid:other', '{"title":"[Test Group] Fixture Anime - 02"}', '{}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, seedTx, "fileop-other-root", "download-other", "release-other", "download:other", `{}`, completeFrozenPolicyFixture(t), "completed", `null`)
	if _, err := seedTx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'download-other' WHERE id = 'release-other'`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-other', 'download-other', 'fileop-other-root', '{}', '{}', 'verified', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	expectViolation("cross-artifact binding is rejected", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state, bound_video_file_ref)
VALUES ('f-e', 'artifact-other', 'f-e', 'e.ass', '/tmp/e.ass', 'subtitle', 1, '{}', '{}', 'bound', 'primary')`)

	// The bound ref must point at a video.
	if _, err := db.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('f-other', $1, (SELECT acquisition_id FROM artifacts WHERE id=$1), 'f-other', 'note.txt', '/tmp/note.txt', 'other', 1, '{}', '{}', 'not_applicable')`, fixture.artifactID); err != nil {
		t.Fatal(err)
	}
	expectViolation("binding to a non-video is rejected", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state, bound_video_file_ref)
VALUES ('f-f', $1, 'f-f', 'f.ass', '/tmp/f.ass', 'subtitle', 1, '{}', '{}', 'bound', 'f-other')`, fixture.artifactID)

	// A referenced video cannot change its media kind.
	if _, err := db.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state, bound_video_file_ref)
VALUES ('f-g', $1, (SELECT acquisition_id FROM artifacts WHERE id=$1), 'f-g', 'g.ass', '/tmp/g.ass', 'subtitle', 1, '{}', '{}', 'bound', 'primary')`, fixture.artifactID); err != nil {
		t.Fatal(err)
	}
	expectViolation("referenced video kind is frozen", `
UPDATE artifact_files SET media_kind = 'other' WHERE id = $1`, fixture.fileID)

	// Cleanup the stray artifact so later tests in this file stay isolated.
	if _, err := db.ExecContext(ctx, `DELETE FROM artifacts WHERE id = 'artifact-other'`); err != nil {
		t.Fatal(err)
	}
}

// A stale present-matched observation never authorizes skipping the write:
// planning re-observes the target (path-level verification) before it
// sidelines the delivery or plans the rewrite. The acquisition target records
// the file as found at the exact required size; each subtest then changes the
// real filesystem behind that record. A disappeared target leaves the copy
// planned (execution recreates it); a changed target is an unmanaged file
// occupying a block target, so the plan item fails TARGET_OCCUPIED and no
// FileOperation is created — the stale observation is neither adopted nor
// retained, and the requirement's delivery closes failed with the same code.
func TestFrozenPresentObservationRequiresFreshExactSizeTarget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mutatePath   func(t *testing.T, path string)
		wantOps      int
		wantKind     string
		wantPlanCode string
	}{
		{name: "target disappeared", mutatePath: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, wantOps: 1, wantKind: "execute"},
		{name: "target size changed", mutatePath: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("changed-size"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, wantOps: 0, wantKind: "failed", wantPlanCode: "TARGET_OCCUPIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openFileOperationDB(t)
			defer db.Close()
			service, err := NewService(db, &testExecutor{}, pathReadingTargetObserver{})
			if err != nil {
				t.Fatal(err)
			}
			service.parentAuthorizer = testParentIdentityAuthorizer{}
			targetPath := fixtureDefaultVideoTarget(t, fixture)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(targetPath, []byte(strings.Repeat("x", int(fixture.size))), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ownerTestDB(t).Exec(`
				INSERT INTO acquisition_targets(
					id, release_id, target_ref, source_path, media_kind, target_path,
					expected_size_bytes, intent, collision_policy, render_snapshot,
					config_snapshot, spec_digest
				) VALUES ('target-fresh-file-test', 'release-file-test', 'source.mkv', 'source.mkv', 'video', $1,
					$2, 'ensure_present', 'block', '{}', '{}', 'sha256:fresh-target')`, targetPath, fixture.size); err != nil {
				t.Fatal(err)
			}
			if _, err := ownerTestDB(t).Exec(`
				UPDATE acquisition_targets
				SET observation_state = 'present_matched', observed_size_bytes = $1, observation_reason_code = 'target_size_matched',
				    observed_at = CURRENT_TIMESTAMP
				WHERE id = 'target-fresh-file-test'`, fixture.size); err != nil {
				t.Fatal(err)
			}
			tc.mutatePath(t, targetPath)
			prepareFixture(t, db, service, fixture.artifactID)
			var operationCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operationCount); err != nil {
				t.Fatal(err)
			}
			if operationCount != tc.wantOps {
				t.Fatalf("stale present observation must be re-verified, not skipped (%s), got %d file operations, want %d", tc.name, operationCount, tc.wantOps)
			}
			var kind, planCode, deliveryCode, outcome string
			if err := db.QueryRow(`SELECT COALESCE(rf.plan_kind,''), COALESCE(rf.plan_reason_code,''), COALESCE(rf.delivery_reason_code,''), COALESCE(rf.outcome,'') FROM acquisition_files rf JOIN artifacts a ON a.acquisition_id=rf.acquisition_id WHERE a.id=$1 AND rf.file_ref='primary'`, fixture.artifactID).Scan(&kind, &planCode, &deliveryCode, &outcome); err != nil {
				t.Fatal(err)
			}
			if kind != tc.wantKind {
				t.Fatalf("the plan item must be declared %q, got %q (code=%q)", tc.wantKind, kind, planCode)
			}
			if tc.wantPlanCode != "" {
				if planCode != tc.wantPlanCode || deliveryCode != tc.wantPlanCode || outcome != "failed" {
					t.Fatalf("the refused plan must record %q (got plan_code=%q delivery=%q outcome=%q)", tc.wantPlanCode, planCode, deliveryCode, outcome)
				}
				var works int
				if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type='file_operation' AND kind='file_execute'`).Scan(&works); err != nil {
					t.Fatal(err)
				}
				if works != 0 {
					t.Fatalf("the refused plan must not queue execution, got %d execute work items", works)
				}
			}
		})
	}
}
