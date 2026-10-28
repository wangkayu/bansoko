package filemaintenance

import (
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/episodeidentity"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type panicAfterOffsetExecutor struct {
	inner  filesystem.OSFileExecutor
	before bool
}

func (e panicAfterOffsetExecutor) Execute(ctx context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	if e.before {
		panic("injected crash before offset file effect")
	}
	executed, err := e.inner.Execute(ctx, operation)
	if err != nil {
		return executed, err
	}
	panic("injected crash after offset file effect")
}

func (e panicAfterOffsetExecutor) ExecuteMaintenance(ctx context.Context, op domain.FileOperation, i executorpkg.MaintenanceInvocation) (domain.FileOperation, error) {
	if e.before {
		panic("injected crash before maintenance effect")
	}
	executed, err := e.inner.ExecuteMaintenance(ctx, op, i)
	if err != nil {
		return executed, err
	}
	panic("injected crash after actual maintenance receipt")
}
func (e panicAfterOffsetExecutor) ReadMaintenanceReceipt(ctx context.Context, i executorpkg.MaintenanceInvocation) (executorpkg.MaintenanceReceipt, error) {
	return e.inner.ReadMaintenanceReceipt(ctx, i)
}

// seedOffsetMigrationRealChain seeds the full owner chain with one real file
// on disk and returns its verified fingerprint hex. The placement
// physical_path points at the real file so the durable execute lane moves a
// real filesystem object.
func seedOffsetMigrationRealChain(t *testing.T, db *sql.DB, root, rawEpisode string) (sourcePath, fingerprintHex string) {
	t.Helper()
	ctx := context.Background()
	sourcePath = filepath.Join(root, "Offset Anime", "Season 01", "Offset Anime - 01 [Group].mkv")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("real offset migration content " + rawEpisode)
	if err := os.WriteFile(sourcePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	fingerprintHex = hex.EncodeToString(sum[:])
	fingerprintJSON := `{"algorithm":"sha256","value":"` + fingerprintHex + `"}`

	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	owners := []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-real', 'Offset Anime')`,
		`INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, season)
		 VALUES ('anime-real', 'bgm-1', 'tmdb-1', 'tv', 1, 1)`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		 VALUES ('sub-real', 'anime-real', 'custom', 'Offset', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
		 VALUES ('src-real', 'sub-real', 'custom_rss', 'https://feeds.example.com/rss.xml', 'https://feeds.example.com/rss.xml', 'Group', 'Offset Anime')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code, download_task_id, version)
		 VALUES ('rel-real', 'src-real', 'guid:real', '{"title":"Offset Anime"}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'submitted', 'submitted', 'task-real', 1)`,
	}
	for _, statement := range owners {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement[:60], err)
		}
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-offset-real", "task-real", "rel-real", "qb:real", `{}`, `{}`, "completed", `null`, `[{"file_ref":"primary","ordinal":0,"relative_path":"Offset Anime - 01.mkv","exact_size":1024,"media_kind":"video","identity_evidence":{}}]`)
	seed := []string{
		`UPDATE download_tasks SET correlation_id = 'corr-real', causation_type = 'release', causation_id = 'rel-real' WHERE id = 'task-real'`,
		`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at, correlation_id, causation_type, causation_id)
		 VALUES ('art-real', 'task-real', 'acq-offset-real', '{}'::jsonb, '{}'::jsonb, 'verified', CURRENT_TIMESTAMP, 'corr-real', 'download_task', 'task-real')`,
		`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
		 VALUES ('af-real', 'art-real', 'acq-offset-real', 'primary', 'Offset Anime - 01.mkv', '` + sourcePath + `', 'video', 1024,
		         '` + fingerprintJSON + `'::jsonb, '{}'::jsonb, 'not_applicable')`,
	}
	for _, statement := range seed {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement[:60], err)
		}
	}
	// The naming rules render this test's own disposable root as the directory
	// prefix, so the execute lane moves the file inside the temp tree and never
	// touches a real /library.
	rules := offsetRealChainRules(root)
	acquisitionfixture.CommitOperations(t, tx, "art-real", []acquisitionfixture.PlanOperation{{ID: "op-real", FileID: "af-real", FileRef: "primary", Source: sourcePath, Target: "/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv", Operation: "move", Key: "op-real", Size: 1024, Fingerprint: json.RawMessage(fingerprintJSON), Render: json.RawMessage(`{}`), Config: frozenConfigSnapshotForTest(t, rules)}})
	if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint,correlation_id='corr-real',causation_type='artifact',causation_id='art-real' WHERE id='op-real'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id='op-real'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,quality_facts_ref,facts_version,correlation_id,causation_type,causation_id,replacement_eligible,verification_state,state,version) VALUES('plc-real',$1,'anime-real','file_operation','op-real',$2::jsonb,$2::jsonb,'rel-real','facts-v1','corr-real','file_operation','op-real',true,'verified','active',1)`, sourcePath, fingerprintJSON); err != nil {
		t.Fatal(err)
	}
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode,
		RawEpisode: ptrEp(rawEpisode), Offset: 0, Episode: ptrEp(rawEpisode),
	}
	source := episodeidentity.EpisodeSourceFileParser
	identity.SelectedSource = &source
	coverage := "tv:e" + rawEpisode
	identity.CoverageKey = &coverage
	identityRaw, _ := json.Marshal(identity)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-real', 'rel-real', 'media-facts-v1', 'episode-identity-v1', 'regex', 'digest-real', $1::jsonb, '{"title":"Offset Anime"}'::jsonb)`,
		identityRaw); err != nil {
		t.Fatal(err)
	}
	effective, _ := json.Marshal(map[string]any{"identity": identity})
	evidence, _ := json.Marshal(map[string]any{"name": "Offset Anime - 01.mkv", "media_kind": "video", "size_bytes": 1024})
	if _, err := tx.ExecContext(ctx, `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-real', 'primary', 'video', 1024, '`+fingerprintHex+`', $1::jsonb, $2::jsonb)`,
		evidence, effective); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	freezeRenderFixtureBusiness(t, db, "op-real")
	return sourcePath, fingerprintHex
}

// offsetRealChainRules renders this test's own disposable root as the
// directory prefix. The renderer reads the anime's current rules, so the test's
// rules source serves the same value that the operation froze.
func offsetRealChainRules(root string) naming.NamingRules {
	rules := frozenRulesForTest()
	rules.TV.DirectoryTemplate = filepath.Join(root, "${sanitize(anime.title)}/${defaultValue('Season 01', anime.season_folder)}")
	rules.Movie.DirectoryTemplate = filepath.Join(root, "${sanitize(anime.title)}")
	return rules
}

// TestOffsetMigrationRealEffectChain is the real-effect end-to-end evidence:
// an offset edit admits a generation, the durable planner freezes the
// operation, and the execute lane moves the real file, verifies its
// fingerprint, and commits placement/coverage/applied with consistent final
// state — on a real filesystem, not fakes.
func TestOffsetMigrationRealEffectChain(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	root := t.TempDir()
	workStore := newOffsetMigrationWorkStore(t, db)
	sourcePath, fingerprintHex := seedOffsetMigrationRealChain(t, db, root, "1")

	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-real", "src-real", 1, 0, 1)
	if err != nil || !created {
		t.Fatalf("admit generation id=%q created=%v err=%v", generationID, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: offsetRealChainRules(root)}))
	planner.SetWorkItems(workStore)
	generation, err := planner.PlanGeneration(ctx, generationID)
	if err != nil {
		t.Fatalf("plan generation: %v", err)
	}
	if generation.State != OffsetMigrationGenerationReady {
		t.Fatalf("generation state=%s, want ready", generation.State)
	}

	claim, err := workStore.ClaimDue(ctx, "real-worker", []string{workitems.KindOffsetMigrationExecute}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var plannedTarget string
	if err := db.QueryRowContext(ctx, `SELECT new_target_path FROM offset_migration_plans WHERE id=$1`, claim.Owner.ID).Scan(&plannedTarget); err != nil {
		t.Fatal(err)
	}
	expectedTarget := resolveOffsetTargetPath(plannedTarget)
	execute := NewOffsetMigrationExecuteService(db)
	execute.SetWorkItems(workStore)
	execute.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	outcome, err := execute.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("outcome kind=%s terminal=%s, want finish/succeeded", outcome.Kind, outcome.TerminalState)
	}
	if err := workStore.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// The old file is gone and the new file exists with the same content.
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("old source still exists at %q (err=%v)", sourcePath, err)
	}
	var newPath string
	if err := db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = 'plc-real'`).Scan(&newPath); err != nil {
		t.Fatal(err)
	}
	if newPath != expectedTarget {
		t.Fatalf("placement path=%q, want executed target %q", newPath, expectedTarget)
	}
	content, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read moved file %q: %v", newPath, err)
	}
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != fingerprintHex {
		t.Fatalf("moved file fingerprint=%s, want %s", got, fingerprintHex)
	}

	var coverage, opState, itemState string
	if err := db.QueryRowContext(ctx, `SELECT observation_key FROM placement_coverage WHERE placement_id = 'plc-real'`).Scan(&coverage); err != nil {
		t.Fatal(err)
	}
	if coverage != "tv:e2" {
		t.Fatalf("coverage=%s, want tv:e2", coverage)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM offset_migration_plans WHERE id = $1`, claim.Owner.ID).Scan(&opState); err != nil {
		t.Fatal(err)
	}
	if opState != string(OffsetMigrationOperationApplied) {
		t.Fatalf("operation state=%s, want applied", opState)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id = $1`, claim.ID).Scan(&itemState); err != nil {
		t.Fatal(err)
	}
	if itemState != workitems.StateSucceeded {
		t.Fatalf("work item state=%s, want succeeded", itemState)
	}
}
