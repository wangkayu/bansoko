package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type fakeInspector struct {
	facts  domain.ArtifactFacts
	err    error
	byPath map[string]domain.ArtifactFacts
	paths  []string
}

func (f *fakeInspector) Inspect(_ context.Context, path string) (domain.ArtifactFacts, error) {
	f.paths = append(f.paths, path)
	if f.err != nil {
		return domain.ArtifactFacts{}, f.err
	}
	facts, ok := f.byPath[path]
	if !ok {
		facts = f.facts
	}
	if facts.Path == "" {
		facts.Path = path
	}
	return facts, nil
}

func TestHandleRejectsInvalidKindBeforeDatabaseAccess(t *testing.T) {
	service := &Service{}
	_, err := service.Handle(context.Background(), workitems.Claim{Kind: workitems.KindQBSubmit})
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("invalid kind error = %v", err)
	}
}

func TestApplyProviderPathSnapshotUsesFrozenApplicationRoot(t *testing.T) {
	input, err := applyProviderPathSnapshot(inspectInput{LocalRoot: "/configured/downloads"}, providerCorrelation{
		PathSnapshot: &pathSnapshot{
			RemoteRoot:      "/downloads",
			ApplicationRoot: "/frozen/downloads",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.LocalRoot != "/frozen/downloads" {
		t.Fatalf("frozen application root was not applied: %q", input.LocalRoot)
	}
	path, err := resolveArtifactPathValue(input, "Show/Show.mkv")
	if err != nil || path != "/frozen/downloads/Show/Show.mkv" {
		t.Fatalf("relative artifact path = %q, err=%v", path, err)
	}
	if _, err := resolveArtifactPathValue(input, "/configured/downloads/Show/Show.mkv"); !errors.Is(err, ErrArtifactPathUnsafe) {
		t.Fatalf("absolute path outside frozen root was accepted: %v", err)
	}
}

func TestSanitizeInventoryDropsCredentialBearingValuesAndBoundsPayload(t *testing.T) {
	sanitized := sanitizeInventory([]byte(`{
		"files":[{"name":"show.mkv","url":"https://example.test/show.mkv"}],
		"passkey_url":"https://example.test/?passkey=secret",
		"provider_task_id":"tag:task-1",
		"nested":{"token":"secret","keep":true}
	}`))
	if sanitized == nil {
		t.Fatal("sanitized inventory is nil")
	}
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	if strings.Contains(value, "passkey") || strings.Contains(value, "secret") || strings.Contains(value, "token") {
		t.Fatalf("sanitized inventory leaked credential material: %s", value)
	}
	if !strings.Contains(value, "provider_task_id") || !strings.Contains(value, "show.mkv") {
		t.Fatalf("sanitized inventory dropped safe facts: %s", value)
	}
}

func TestBuildInspectionPlanRecordsMetadataFingerprint(t *testing.T) {
	// Hermetic: a real host /downloads (e.g. a harness symlink) must not
	// change the pure plan result.
	downloads := hermeticDownloadsRoot(t)
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	plan, err := buildInspectionPlan(artifactSnapshot{id: "artifact-1", providerCorrelation: []byte(`{"external_id":"tag:task-1","file_count":1}`)}, inspectInput{LocalRoot: downloads}, domain.ArtifactFacts{
		Path:       downloads + "/show/show.mkv",
		SizeBytes:  42,
		Device:     7,
		Inode:      99,
		ObservedAt: at,
		MediaKind:  "video",
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	var fingerprint map[string]any
	if err := json.Unmarshal(plan.file.fingerprint, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if len(fingerprint) != 3 || fingerprint["algorithm"] != "metadata" || fingerprint["value"] != "meta:size=42;dev=7;ino=99" || fingerprint["size_bytes"] != float64(42) {
		t.Fatalf("file fingerprint = %s", plan.file.fingerprint)
	}
	if plan.file.relativePath != "show/show.mkv" || plan.file.mediaKind != "video" {
		t.Fatalf("unexpected artifact file plan: %+v", plan.file)
	}
	for _, key := range inventoryListKeys {
		if _, ok := plan.inventoryBase[key]; ok {
			t.Fatalf("inventory base kept file list %q: %+v", key, plan.inventoryBase)
		}
	}
	if plan.inventoryBase["provider_task_id"] != "tag:task-1" {
		t.Fatalf("inventory base dropped provider facts: %+v", plan.inventoryBase)
	}
	// A device without an inode is an incomplete identity, never a size-only
	// fingerprint.
	if _, err := buildInspectionPlan(artifactSnapshot{id: "artifact-1"}, inspectInput{LocalRoot: downloads}, domain.ArtifactFacts{Path: downloads + "/show.mkv", SizeBytes: 1, Device: 3}, at); !errors.Is(err, ErrArtifactFactsInvalid) {
		t.Fatalf("incomplete identity error = %v", err)
	}
}

func TestBuildInspectionPlanRejectsFrozenExpectedSizeMismatch(t *testing.T) {
	// Hermetic: a real host /downloads (e.g. a harness symlink) must not
	// change the pure plan result.
	downloads := hermeticDownloadsRoot(t)
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	path := downloads + "/show/show.mkv"
	expectedSize := int64(42)
	_, err := buildInspectionPlanForFiles(
		artifactSnapshot{id: "artifact-size-mismatch"},
		inspectInput{LocalRoot: downloads},
		[]artifactInspectionTarget{{path: path, expectedSize: &expectedSize, sourceHash: "torrent-infohash", primary: true}},
		[]domain.ArtifactFacts{{Path: path, SizeBytes: 41, MediaKind: "video", ObservedAt: at}},
		at,
	)
	if !errors.Is(err, ErrArtifactFactsInvalid) || !errors.Is(err, ErrArtifactSizeMismatch) {
		t.Fatalf("expected frozen size mismatch error, got %v", err)
	}

	negativeExpectedSize := int64(-1)
	plan, err := buildInspectionPlanForFiles(
		artifactSnapshot{id: "artifact-size-unknown"},
		inspectInput{LocalRoot: downloads},
		[]artifactInspectionTarget{{path: path, expectedSize: &negativeExpectedSize, primary: true}},
		[]domain.ArtifactFacts{{Path: path, SizeBytes: 41, MediaKind: "video", ObservedAt: at}},
		at,
	)
	if err != nil {
		t.Fatalf("negative expected size should not constrain inspection: %v", err)
	}
	if strings.Contains(string(plan.file.facts), "provider_size_bytes") {
		t.Fatalf("negative expected size was persisted as a provider constraint: %s", plan.file.facts)
	}
}

func TestBuildInspectionPlanForFilesUsesStableRefsAndRecordsEveryFile(t *testing.T) {
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	root := hermeticDownloadsRoot(t)
	primaryPath := filepath.Join(root, "show", "show.mkv")
	subtitlePath := filepath.Join(root, "show", "show.zh.ass")
	plan, err := buildInspectionPlanForFiles(
		artifactSnapshot{id: "artifact-multi"},
		inspectInput{LocalRoot: root},
		[]artifactInspectionTarget{
			{path: primaryPath, mediaKind: "video", primary: true},
			{path: subtitlePath, mediaKind: "subtitle", language: "zh"},
		},
		[]domain.ArtifactFacts{
			{Path: primaryPath, SizeBytes: 100, MediaKind: "video", ObservedAt: at},
			{Path: subtitlePath, SizeBytes: 20, MediaKind: "subtitle", ObservedAt: at},
		},
		at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.files) != 2 || plan.file.fileRef != "primary" {
		t.Fatalf("unexpected multi-file plan: %+v", plan)
	}
	if plan.files[1].fileRef != stableArtifactFileRef(filepath.ToSlash(filepath.Clean(subtitlePath))) {
		t.Fatalf("secondary file ref is not stable: %+v", plan.files)
	}
	inventoryRaw, fingerprintsRaw, err := inspectionSnapshots(plan.inventoryBase, plan.files, "primary")
	if err != nil {
		t.Fatal(err)
	}
	var inventory map[string]any
	if err := json.Unmarshal(inventoryRaw, &inventory); err != nil {
		t.Fatal(err)
	}
	digest, _ := inventory["files_digest"].(string)
	if inventory["schema_version"] != inventorySchemaVersion || inventory["primary_file_ref"] != "primary" ||
		inventory["file_count"] != float64(2) || inventory["video_count"] != float64(1) || inventory["subtitle_count"] != float64(1) ||
		inventory["other_count"] != float64(0) || inventory["total_size_bytes"] != float64(120) || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("inventory summary = %s", inventoryRaw)
	}
	for _, key := range inventoryListKeys {
		if _, ok := inventory[key]; ok {
			t.Fatalf("inventory summary carries the file list %q: %s", key, inventoryRaw)
		}
	}
	var fingerprints map[string]any
	if err := json.Unmarshal(fingerprintsRaw, &fingerprints); err != nil {
		t.Fatal(err)
	}
	if len(fingerprints) != 4 || fingerprints["schema_version"] != fingerprintSchemaVersion || fingerprints["files_digest"] != digest || fingerprints["file_count"] != float64(2) {
		t.Fatalf("fingerprint summary = %s", fingerprintsRaw)
	}
	// The digest binds every file's reference, path, kind, size and
	// fingerprint, independent of the order the files were inspected in.
	reversed := []artifactFilePlan{plan.files[1], plan.files[0]}
	again, err := inspectionFilesDigest(reversed)
	if err != nil || again != digest {
		t.Fatalf("digest depends on order: %q vs %q (%v)", again, digest, err)
	}
	changed := append([]artifactFilePlan(nil), plan.files...)
	changed[1].sizeBytes++
	if other, err := inspectionFilesDigest(changed); err != nil || other == digest {
		t.Fatalf("digest ignores a size change: %q (%v)", other, err)
	}
}

func openArtifactTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

// ownerTestDB returns the schema-owner handle for a test-only DDL path.
func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.Owner(t)
}

func seedArtifactOwner(t *testing.T, db *sql.DB, artifactID, taskID, taskState, artifactState string, providerCorrelation string, inventory string) {
	t.Helper()
	seedArtifactOwnerBudget(t, db, artifactID, taskID, taskState, artifactState, providerCorrelation, inventory, nil)
}

// seedArtifactOwnerBudget optionally freezes an explicit inspect
// max_attempts at INSERT (nil lets the migration default apply).
func seedArtifactOwnerBudget(t *testing.T, db *sql.DB, artifactID, taskID, taskState, artifactState string, providerCorrelation string, inventory string, inspectMaxAttempts any) {
	t.Helper()
	ctx := context.Background()
	var correlation struct {
		ArtifactPath  string `json:"artifact_path"`
		FilesComplete *bool  `json:"files_complete"`
		Files         []struct {
			Path      string         `json:"path"`
			Name      string         `json:"name"`
			SizeBytes *int64         `json:"size_bytes"`
			MediaKind string         `json:"media_kind"`
			Language  string         `json:"language"`
			Facts     map[string]any `json:"facts"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(providerCorrelation), &correlation); err != nil {
		t.Fatalf("decode provider correlation for frozen input: %v", err)
	}
	frozen := frozenDownloadInput{}
	for _, file := range correlation.Files {
		sourcePath := strings.TrimSpace(file.Path)
		filename := strings.TrimSpace(file.Name)
		if sourcePath == "" {
			sourcePath = filename
		}
		if filename == "" && sourcePath != "" {
			filename = filepath.Base(sourcePath)
		}
		facts := file.Facts
		if strings.TrimSpace(file.Language) != "" {
			if facts == nil {
				facts = make(map[string]any)
			}
			facts["language"] = file.Language
		}
		frozen.ReleaseFiles = append(frozen.ReleaseFiles, frozenReleaseFile{
			SourcePath: sourcePath,
			Filename:   filename,
			SizeBytes:  file.SizeBytes,
			MediaKind:  file.MediaKind,
			Facts:      facts,
		})
	}
	if len(correlation.Files) == 0 && strings.TrimSpace(correlation.ArtifactPath) != "" {
		artifactPath := strings.TrimSpace(correlation.ArtifactPath)
		frozen.ReleaseFiles = []frozenReleaseFile{{
			SourcePath: artifactPath,
			Filename:   filepath.Base(artifactPath),
		}}
	}
	if correlation.FilesComplete != nil && !*correlation.FilesComplete {
		// Keep the explicit incomplete fixture on the production no-files branch.
		frozen.ReleaseFiles = nil
	}
	frozenInput, err := json.Marshal(frozen)
	if err != nil {
		t.Fatalf("encode frozen input: %v", err)
	}
	queries := []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-artifact-test', 'Artifact Test')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-artifact-test', 'anime-artifact-test', 'custom', 'Artifact Test', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('source-artifact-test', 'sub-artifact-test', 'custom_rss', 'https://example.test/rss', 'https://example.test/rss', 'Test', 'Artifact Test')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-artifact-test')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('release-artifact-test', 'source-artifact-test', 'guid:artifact-test', '{}'::jsonb, '{}'::jsonb, 'accepted_pending_download', 'accepted_pending_download')`,
	}
	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed artifact owner: %v", err)
		}
	}
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin owner seed transaction: %v", err)
	}
	defer tx.Rollback()
	manifest := make([]map[string]any, 0, len(frozen.ReleaseFiles))
	for i, file := range frozen.ReleaseFiles {
		size := int64(1024)
		if file.SizeBytes != nil {
			size = *file.SizeBytes
		}
		kind := file.MediaKind
		if kind == "" {
			kind = "video"
		}
		path := filepath.ToSlash(filepath.Base(file.SourcePath))
		if filepath.Base(filepath.Dir(file.SourcePath)) == "Show" {
			path = "Show/" + path
		}
		manifest = append(manifest, map[string]any{"file_ref": stableArtifactFileRef(path), "ordinal": i, "relative_path": path, "exact_size": size, "media_kind": kind, "identity_evidence": map[string]any{}})
	}
	if len(manifest) == 0 {
		manifest = append(manifest, map[string]any{"file_ref": "unused", "ordinal": 0, "relative_path": "unused.mkv", "exact_size": 1, "media_kind": "video", "identity_evidence": map[string]any{}})
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedDownloadManifest(t, tx, "acq-"+taskID, taskID, "release-artifact-test", "anime-artifact-test", "sub-artifact-test", "source-artifact-test", "qb:artifact-test", []byte("fixture:"+taskID), strings.Repeat("a", 40), string(frozenInput), string(manifestRaw))
	if _, err := tx.ExecContext(ctx, `UPDATE download_tasks SET state=$2,provider_correlation=$3::jsonb WHERE id=$1`, taskID, taskState, providerCorrelation); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = $1, verdict = 'submitted', reason_code = 'submitted' WHERE id = 'release-artifact-test'`, taskID); err != nil {
		t.Fatalf("attach download task: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at, inspect_max_attempts)
VALUES ($1, $2, 'acq-'||$2, $3::jsonb, '{}'::jsonb, $4,
        CASE WHEN $4 = 'verified' THEN CURRENT_TIMESTAMP ELSE NULL END, $5::integer)`, artifactID, taskID, inventory, artifactState, inspectMaxAttempts); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit owner seed transaction: %v", err)
	}
}

func claimArtifactWorkItem(t *testing.T, db *sql.DB, artifactID string, input json.RawMessage) (workitems.Claim, *workitems.Store) {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerArtifact, ID: artifactID},
		Kind:                 workitems.KindArtifactInspect,
		IdempotencyKey:       "artifact_inspect:" + artifactID,
		InputSnapshot:        input,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create artifact work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "artifact-test-worker", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatalf("claim artifact work item: %v", err)
	}
	return claim, store
}

func finishArtifactOutcome(t *testing.T, db *sql.DB, store *workitems.Store, claim workitems.Claim, outcome worker.Outcome) {
	t.Helper()
	if outcome.Kind == worker.OutcomeCommitted {
		if outcome.CommittedClaim == nil || outcome.CommittedClaim.WorkItemID != claim.ID || outcome.CommittedClaim.Token != claim.FencingToken || outcome.WriteOwner != nil {
			t.Fatalf("invalid committed outcome %+v", outcome)
		}
		return
	}
	ctx := context.Background()
	if outcome.Kind == worker.OutcomeFinish {
		if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatalf("finish artifact outcome: %v", err)
		}
		return
	}
	if outcome.Kind == worker.OutcomeRetry {
		if _, err := store.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
			t.Fatalf("retry artifact outcome: %v", err)
		}
		return
	}
	t.Fatalf("unexpected artifact outcome: %+v", outcome)
}

func TestHandleSuccessfulInspectionPersistsArtifactFileAndSanitizedJSON(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The service resolves an existing regular file into canonical space (the
	// OS inspector's view); assert the plan works end to end with the canonical
	// path the inspector will report.
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:artifact-task","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-success", "task-success", "completed", "pending", correlation, `{"files":[{"name":"Show - 01.mkv"}],"passkey_url":"https://example.test/?passkey=secret"}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-success", []byte(`{"download_task_id":"task-success","provider_task_id":"tag:artifact-task","local_root":"/stale/configured-root"}`))
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 1024, ObservedAt: at, MediaKind: "video"}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	// The settle re-inspection is exercised by the dedicated unstable test;
	// success-path tests disable it so they assert a single pass.
	service.SetSettleDelay(0)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" || len(inspector.paths) != 1 || inspector.paths[0] != canonicalPath {
		t.Fatalf("successful inspection outcome=%+v paths=%v", outcome, inspector.paths)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)
	var state string
	var inventory, fingerprints []byte
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT state, inventory_snapshot, fingerprint_snapshot, version FROM artifacts WHERE id = 'artifact-success'`).Scan(&state, &inventory, &fingerprints, &version); err != nil {
		t.Fatal(err)
	}
	if state != "verified" || version != 2 {
		t.Fatalf("artifact state=%q version=%d", state, version)
	}
	for name, raw := range map[string][]byte{"inventory": inventory, "fingerprints": fingerprints} {
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			t.Fatalf("%s is not a JSON object: %s (%v)", name, raw, err)
		}
		if strings.Contains(string(raw), "passkey") || strings.Contains(string(raw), "secret") {
			t.Fatalf("%s leaked credentials: %s", name, raw)
		}
	}
	var fileRef, relativePath, sourcePath, mediaKind string
	var size int64
	var fingerprint []byte
	if err := db.QueryRowContext(context.Background(), `SELECT file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint FROM artifact_files WHERE artifact_id = 'artifact-success'`).Scan(&fileRef, &relativePath, &sourcePath, &mediaKind, &size, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if fileRef != stableArtifactFileRef("Show/Show - 01.mkv") || relativePath != "Show/Show - 01.mkv" || sourcePath != canonicalPath || mediaKind != "video" || size != 1024 || !strings.Contains(string(fingerprint), `"meta:size=1024"`) {
		t.Fatalf("unexpected artifact file ref=%q relative=%q source=%q media=%q size=%d fingerprint=%s", fileRef, relativePath, sourcePath, mediaKind, size, fingerprint)
	}
}

func TestHandleMultiFileInspectionPersistsAllArtifactFiles(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	primaryPath := filepath.Join(root, "Show", "Show.mkv")
	subtitlePath := filepath.Join(root, "Show", "Show.zh.ass")
	if err := os.MkdirAll(filepath.Dir(primaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{primaryPath, subtitlePath} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Existing files resolve into canonical space (the OS inspector's view);
	// the plan and persisted rows use the canonical paths the inspector sees.
	canonicalPrimary, err := filepath.EvalSymlinks(primaryPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSubtitle, err := filepath.EvalSymlinks(subtitlePath)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:multi","artifact_path":"` + canonicalPrimary + `","file_count":2,"files_complete":true,"files":[{"path":"` + canonicalPrimary + `","size_bytes":100,"media_kind":"video","progress":1},{"path":"` + canonicalSubtitle + `","size_bytes":20,"media_kind":"subtitle","language":"zh","progress":1}]}`
	seedArtifactOwner(t, db, "artifact-multi", "task-multi", "completed", "pending", correlation, `{}`)
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('stale-file', 'artifact-multi', 'acq-task-multi', 'stale', 'old.mkv', '/old/old.mkv', 'video', 1, '{"algorithm":"metadata","value":"meta:size=1","size_bytes":1}', '{}', 'not_applicable')`); err != nil {
		t.Fatal(err)
	}
	claim, store := claimArtifactWorkItem(t, db, "artifact-multi", []byte(`{"download_task_id":"task-multi","provider_task_id":"tag:multi","local_root":"`+root+`"}`))
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{byPath: map[string]domain.ArtifactFacts{
		canonicalPrimary:  {Path: canonicalPrimary, SizeBytes: 100, ObservedAt: at, MediaKind: "video"},
		canonicalSubtitle: {Path: canonicalSubtitle, SizeBytes: 20, ObservedAt: at, MediaKind: "subtitle"},
	}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("multi-file inspection outcome=%+v", outcome)
	}
	if len(inspector.paths) != 2 || inspector.paths[0] != canonicalPrimary || inspector.paths[1] != canonicalSubtitle {
		t.Fatalf("multi-file inspection paths=%v", inspector.paths)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	var state string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM artifacts WHERE id = 'artifact-multi'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "verified" {
		t.Fatalf("artifact state=%q", state)
	}
	var fileCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM artifact_files WHERE artifact_id = 'artifact-multi'`).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 2 {
		t.Fatalf("artifact file count=%d, want 2", fileCount)
	}
	rows, err := db.QueryContext(context.Background(), `SELECT file_ref, source_path, media_kind, size_bytes FROM artifact_files WHERE artifact_id = 'artifact-multi' ORDER BY file_ref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var fileRef, sourcePath, mediaKind string
		var size int64
		if err := rows.Scan(&fileRef, &sourcePath, &mediaKind, &size); err != nil {
			t.Fatal(err)
		}
		seen[fileRef] = true
		switch fileRef {
		case stableArtifactFileRef("Show/Show.mkv"):
			if sourcePath != canonicalPrimary || mediaKind != "video" || size != 100 {
				t.Fatalf("unexpected primary artifact file: ref=%q path=%q kind=%q size=%d", fileRef, sourcePath, mediaKind, size)
			}
		default:
			if fileRef != stableArtifactFileRef("Show/Show.zh.ass") || sourcePath != canonicalSubtitle || mediaKind != "subtitle" || size != 20 {
				t.Fatalf("unexpected secondary artifact file: ref=%q path=%q kind=%q size=%d", fileRef, sourcePath, mediaKind, size)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !seen[stableArtifactFileRef("Show/Show.mkv")] || len(seen) != 2 {
		t.Fatalf("artifact files=%v", seen)
	}
}

func TestHandleIncompleteProviderFileSnapshotCannotVerifyArtifact(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "incomplete.mkv")
	correlation := `{"external_id":"tag:incomplete","artifact_path":"` + path + `","file_count":2,"files_complete":false,"files":[{"path":"` + path + `","size_bytes":10,"media_kind":"video"}]}`
	seedArtifactOwner(t, db, "artifact-incomplete", "task-incomplete", "completed", "pending", correlation, `{}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-incomplete", []byte(`{"download_task_id":"task-incomplete","provider_task_id":"tag:incomplete","local_root":"`+root+`"}`))
	inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: path, SizeBytes: 10, MediaKind: "video"}}
	service, err := NewService(db, inspector)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != "provider_files_incomplete" || len(inspector.paths) != 0 {
		t.Fatalf("incomplete provider snapshot outcome=%+v paths=%v", outcome, inspector.paths)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)
	var state string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM artifacts WHERE id = 'artifact-incomplete'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("artifact state=%q", state)
	}
}

func TestHandleAlreadyVerifiedReplaySkipsInspector(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "already.mkv")
	seedArtifactOwner(t, db, "artifact-verified", "task-verified", "completed", "verified", `{"external_id":"tag:verified","artifact_path":"`+path+`"}`, `{}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-verified", []byte(`{"download_task_id":"task-verified","artifact_id":"artifact-verified"}`))
	inspector := &fakeInspector{err: errors.New("must not inspect replay")}
	service, err := NewService(db, inspector)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OwnerResult.State != "" || outcome.ReasonCode != "ARTIFACT_VERIFIED_WORK_LIVE" || outcome.Disposition != workitems.DispositionAmbiguous || outcome.WriteOwner != nil || len(inspector.paths) != 0 {
		t.Fatalf("verified replay outcome=%+v paths=%v", outcome, inspector.paths)
	}
	var workState string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM work_item_history WHERE id=$1`, claim.ID).Scan(&workState); err != nil {
		t.Fatal(err)
	}
	if workState != "leased" {
		t.Fatalf("verified live work mutated: %s", workState)
	}
	_ = store
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM artifacts WHERE id = 'artifact-verified'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("verified replay changed artifact version to %d", version)
	}
}

func TestHandleInspectorFailureMarksArtifactWaitingRetry(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "failure.mkv")
	seedArtifactOwner(t, db, "artifact-failure", "task-failure", "completed", "pending", `{"external_id":"tag:failure","artifact_path":"`+path+`"}`, `{}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-failure", []byte(`{"download_task_id":"task-failure","artifact_id":"artifact-failure"}`))
	inspector := &fakeInspector{err: errors.New("permission denied: /private/secret")}
	service, err := NewService(db, inspector)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "artifact_inspect_failed" || strings.Contains(outcome.OwnerResult.Reason, "private") || strings.Contains(outcome.OwnerResult.Reason, "secret") {
		t.Fatalf("inspector failure outcome=%+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)
	var state, code, reason string
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code, failure_reason FROM artifacts WHERE id = 'artifact-failure'`).Scan(&state, &code, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_retry" || code != "artifact_inspect_failed" || reason == "" {
		t.Fatalf("artifact retry state=%q code=%q reason=%q", state, code, reason)
	}
}

func TestHandleMissingArtifactReturnsNotFound(t *testing.T) {
	db := openArtifactTestDB(t)
	service, err := NewService(db, &fakeInspector{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Handle(context.Background(), workitems.Claim{
		Kind:                 workitems.KindArtifactInspect,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerArtifact, ID: "missing-artifact"},
		ExpectedOwnerVersion: 1,
	})
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("missing artifact error = %v", err)
	}
}

func TestHandleRejectsMismatchedInputOwnerWithoutInspecting(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "mismatch.mkv")
	seedArtifactOwner(t, db, "artifact-mismatch", "task-mismatch", "completed", "pending", `{"external_id":"tag:mismatch","artifact_path":"`+path+`"}`, `{}`)
	claim, _ := claimArtifactWorkItem(t, db, "artifact-mismatch", []byte(`{"download_task_id":"different-task","artifact_id":"artifact-mismatch"}`))
	inspector := &fakeInspector{}
	service, err := NewService(db, inspector)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "failed" || len(inspector.paths) != 0 {
		t.Fatalf("mismatched input outcome=%+v paths=%v", outcome, inspector.paths)
	}
}

func TestResolveArtifactPathValueCanonicalizesExistingFileUnderSymlinkedRoot(t *testing.T) {
	// t.TempDir sits under /var -> /private/var on macOS, the same shape as
	// the /tmp -> /private/tmp mount symlink that broke inspection in the
	// live fixture. The canonical space of the OS inspector must match the
	// resolved plan target.
	root := t.TempDir()
	file := filepath.Join(root, "show.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := inspectInput{LocalRoot: root}
	got, err := resolveArtifactPathValue(input, "show.mkv")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved path = %q, want canonical %q", got, want)
	}
	// A path outside the root must still be rejected in canonical space.
	if _, err := resolveArtifactPathValue(input, "/etc/hostname"); !errors.Is(err, ErrArtifactPathUnsafe) {
		t.Fatalf("outside-root absolute path was accepted: %v", err)
	}
	// A missing file keeps the raw resolved path so the inspector reports the
	// normal missing-file error (retryable) instead of a terminal unsafe path.
	missing, err := resolveArtifactPathValue(input, "missing.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(missing) != filepath.Join(root, "missing.mkv") {
		t.Fatalf("missing file resolved = %q, want raw %q", missing, filepath.Join(root, "missing.mkv"))
	}
}

func TestResolveArtifactPathValueDoesNotFollowArtifactFileSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.mkv")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.mkv")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	got, err := resolveArtifactPathValue(inspectInput{LocalRoot: root}, "link.mkv")
	if err != nil {
		t.Fatal(err)
	}
	// The raw path is retained so the OS inspector's own Lstat rejects the
	// symlinked artifact instead of silently following it.
	if got != link {
		t.Fatalf("symlinked artifact was canonicalized to %q, want raw %q", got, link)
	}
}

func TestRelativeArtifactPathCanonicalizesExistingRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "show.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := relativeArtifactPath(root, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "show.mkv" {
		t.Fatalf("relative = %q, want show.mkv", rel)
	}
}

func TestBuildInspectionPlanAcceptsCanonicalInspectorPathUnderSymlinkedRoot(t *testing.T) {
	root := t.TempDir()
	name := "[Sub] Show - 01.mkv"
	content := []byte("test content")
	if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
	size := int64(len(content))
	canonicalPath, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolveArtifactPathValue(inspectInput{LocalRoot: root}, name)
	if err != nil {
		t.Fatal(err)
	}
	facts := domain.ArtifactFacts{
		Path:       canonicalPath,
		SizeBytes:  size,
		ObservedAt: time.Now().UTC(),
		MediaKind:  "video",
	}
	plan, err := buildInspectionPlanForFiles(artifactSnapshot{id: "artifact-1", providerCorrelation: []byte("{}")},
		inspectInput{LocalRoot: root},
		[]artifactInspectionTarget{{path: target, mediaKind: "video", primary: true}},
		[]domain.ArtifactFacts{facts}, time.Now().UTC())
	if err != nil {
		t.Fatalf("plan rejected canonical inspector path: %v", err)
	}
	if len(plan.files) != 1 || plan.files[0].relativePath != name {
		t.Fatalf("plan files = %+v", plan.files)
	}
}

// sequenceInspector returns one fact set per Inspect call (in order) so tests
// can simulate a file that is still settling while the download client has
// already reported completion.
type sequenceInspector struct {
	facts []domain.ArtifactFacts
	calls int
	paths []string
}

func (s *sequenceInspector) Inspect(_ context.Context, path string) (domain.ArtifactFacts, error) {
	s.paths = append(s.paths, path)
	if s.calls >= len(s.facts) {
		return s.facts[len(s.facts)-1], nil
	}
	facts := s.facts[s.calls]
	s.calls++
	if facts.Path == "" {
		facts.Path = path
	}
	return facts, nil
}

func TestHandleUnstablePrimaryFileRetriesAfterSettle(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:unstable","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-unstable", "task-unstable", "completed", "pending", correlation, `{"files":[{"name":"Show - 01.mkv"}]}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-unstable", []byte(`{"download_task_id":"task-unstable","provider_task_id":"tag:unstable","local_root":"`+root+`"}`))
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	// First read observes a file still being written (same size, an older
	// modification time); the settle re-read observes a later modification
	// time: the handler must retry with artifact_unstable instead of freezing
	// an unsettled observation.
	inspector := &sequenceInspector{facts: []domain.ArtifactFacts{
		{Path: canonicalPath, SizeBytes: 1024, ModifiedAt: at, ObservedAt: at, MediaKind: "video"},
		{Path: canonicalPath, SizeBytes: 1024, ModifiedAt: at.Add(time.Second), ObservedAt: at, MediaKind: "video"},
	}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(5 * time.Millisecond)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "artifact_unstable" {
		t.Fatalf("unstable inspection outcome=%+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)
	var state, code string
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM artifacts WHERE id = 'artifact-unstable'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_retry" || code != "artifact_unstable" {
		t.Fatalf("unstable artifact state=%q code=%q", state, code)
	}
}

func TestHandleStablePrimaryFileVerifiesAfterSettle(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:stable","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-stable", "task-stable", "completed", "pending", correlation, `{"files":[{"name":"Show - 01.mkv"}]}`)
	claim, _ := claimArtifactWorkItem(t, db, "artifact-stable", []byte(`{"download_task_id":"task-stable","provider_task_id":"tag:stable","local_root":"`+root+`"}`))
	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	stable := domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 1024, ObservedAt: at, MediaKind: "video"}
	inspector := &sequenceInspector{facts: []domain.ArtifactFacts{stable, stable}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(5 * time.Millisecond)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("stable inspection outcome=%+v", outcome)
	}
	if len(inspector.paths) != 2 || inspector.paths[0] != canonicalPath || inspector.paths[1] != canonicalPath {
		t.Fatalf("stable inspection paths=%v", inspector.paths)
	}
}

func TestResolveArtifactFilesFallsBackToProviderCorrelationForV1(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "[Sub] Show - 79 [1080P].mp4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	correlation := providerCorrelation{
		ArtifactPath: "/downloads/[Sub] Show - 79 [1080P].mp4",
		Files: []providerFileSnapshot{
			{Path: "/downloads/[Sub] Show - 79 [1080P].mp4", SizeBytes: 1024, MediaKind: "video", Progress: 1},
		},
		PathSnapshot: &pathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: root},
	}
	// Empty frozen release_files with a v1 torrent falls back to the provider
	// correlation snapshot, translating remote-root paths onto the app root.
	targets, err := resolveArtifactFilesFromFrozenInput(
		inspectInput{LocalRoot: root},
		frozenDownloadInput{TorrentVersion: "v1"},
		correlation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if targets[0].path != canonical {
		t.Fatalf("target path = %q, want canonical %q", targets[0].path, canonical)
	}
	if targets[0].expectedSize == nil || *targets[0].expectedSize != 1024 {
		t.Fatalf("target size = %v, want 1024", targets[0].expectedSize)
	}
	if !targets[0].primary {
		t.Fatalf("single fallback target should be primary")
	}
	if !targets[0].firstLevel {
		t.Fatalf("a single file directly under the download root is first level")
	}
}

// The correlation fallback has no manifest and the provider reports absolute
// paths, so the level is the depth below the download root: the torrent's own
// folder holds the first-level files and anything deeper is nested.
func TestResolveArtifactFilesFallbackLevelIsDepthBelowDownloadRoot(t *testing.T) {
	root := t.TempDir()
	correlation := providerCorrelation{
		Files: []providerFileSnapshot{
			{Path: "/downloads/Show/Show - 01.mkv", SizeBytes: 1024, MediaKind: "video", Progress: 1},
			{Path: "/downloads/Show/Subs/Show - 01.ass", SizeBytes: 10, MediaKind: "subtitle", Progress: 1},
		},
		PathSnapshot: &pathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: root},
	}
	targets, err := resolveArtifactFilesFromFrozenInput(
		inspectInput{LocalRoot: root},
		frozenDownloadInput{TorrentVersion: "v1"},
		correlation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || !targets[0].firstLevel || targets[1].firstLevel {
		t.Fatalf("levels = %+v, want [first level, nested]", targets)
	}
}

func TestResolveArtifactFilesFallbackGatedByV1AndCorrelationFiles(t *testing.T) {
	root := t.TempDir()
	correlation := providerCorrelation{
		Files: []providerFileSnapshot{
			{Path: "/downloads/[Sub] Show - 79 [1080P].mp4", SizeBytes: 1024, MediaKind: "video", Progress: 1},
		},
		PathSnapshot: &pathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: root},
	}
	// v2 torrents are not eligible for the qB snapshot fallback.
	targets, err := resolveArtifactFilesFromFrozenInput(
		inspectInput{LocalRoot: root},
		frozenDownloadInput{TorrentVersion: "v2"},
		correlation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("v2 torrent unexpectedly fell back: %d targets", len(targets))
	}
	// Empty correlation files keep the terminal provider_files_incomplete path.
	targets, err = resolveArtifactFilesFromFrozenInput(
		inspectInput{LocalRoot: root},
		frozenDownloadInput{TorrentVersion: "v1"},
		providerCorrelation{PathSnapshot: &pathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: root}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("empty correlation files unexpectedly fell back: %d targets", len(targets))
	}
	// Frozen release_files still take the normal path even for v1.
	releaseFile := frozenReleaseFile{
		SourcePath: "[Sub] Show - 78 [1080P].mp4",
		Filename:   "[Sub] Show - 78 [1080P].mp4",
		MediaKind:  "video",
	}
	targets, err = resolveArtifactFilesFromFrozenInput(
		inspectInput{LocalRoot: root},
		frozenDownloadInput{TorrentVersion: "v1", ReleaseFiles: []frozenReleaseFile{releaseFile}},
		correlation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].path != filepath.Join(root, "[Sub] Show - 78 [1080P].mp4") {
		t.Fatalf("frozen release_files path targets = %+v", targets)
	}
}

// hermeticDownloadsRoot is an absolute local root that does not exist, so the
// pure inspection-plan tests never resolve through whatever the host has at
// /downloads (a concurrent harness may symlink it).
func hermeticDownloadsRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "downloads")
}
