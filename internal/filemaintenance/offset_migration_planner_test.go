package filemaintenance

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// TestPlanGenerationExpandsAdmittedGeneration drives the durable planner end
// to end: an admitted pending generation is expanded into one ready per-placement
// operation with the re-rendered target and corrected coverage keys, and a
// crashed-then-replayed planning pass stays idempotent.
func TestPlanGenerationExpandsAdmittedGeneration(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")

	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created || generationID == "" {
		t.Fatalf("admit link generation id=%q created=%v err=%v", generationID, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	renderer := NewNamingTargetRenderer(db, fakeRulesSource{})
	service := NewOffsetMigrationService(db, renderer)
	generation, err := service.PlanGeneration(ctx, generationID)
	if err != nil {
		t.Fatalf("plan generation: %v", err)
	}
	if generation.State != OffsetMigrationGenerationReady {
		t.Fatalf("generation state=%s, want ready", generation.State)
	}
	if generation.PlannedCount != 1 {
		t.Fatalf("planned_count=%d, want 1", generation.PlannedCount)
	}

	var (
		opState, newEpisode, oldTarget, newTarget string
		oldCoverage, newCoverage                  sql.NullString
	)
	if err := db.QueryRowContext(ctx, `
SELECT state, new_episode, old_target_path, new_target_path, old_coverage_key, new_coverage_key
FROM offset_migration_plans WHERE generation_id = $1`, generationID).Scan(
		&opState, &newEpisode, &oldTarget, &newTarget, &oldCoverage, &newCoverage); err != nil {
		t.Fatal(err)
	}
	if opState != string(OffsetMigrationOperationReady) {
		t.Fatalf("operation state=%s, want ready", opState)
	}
	if newEpisode != "2" {
		t.Fatalf("new_episode=%s, want 2 (raw 1 + offset 1)", newEpisode)
	}
	if oldTarget != "/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv" {
		t.Fatalf("old_target=%q, want the frozen placement path", oldTarget)
	}
	if newTarget == "" || newTarget == oldTarget {
		t.Fatalf("new_target=%q, want a re-rendered different target", newTarget)
	}
	if !oldCoverage.Valid || oldCoverage.String != "tv:s1:e1" {
		t.Fatalf("old_coverage=%v, want tv:s1:e1", oldCoverage)
	}
	if !newCoverage.Valid || newCoverage.String != "tv:s1:e2" {
		t.Fatalf("new_coverage=%v, want tv:s1:e2", newCoverage)
	}

	// Simulate a planner crash after the generation was marked planning but
	// before it settled: replay must converge without duplicating operations.
	if _, err := db.ExecContext(ctx, `
UPDATE offset_migration_generations SET state = 'planning', version = version + 1 WHERE id = $1`, generationID); err != nil {
		t.Fatal(err)
	}
	generation, err = service.PlanGeneration(ctx, generationID)
	if err != nil {
		t.Fatalf("replay plan generation: %v", err)
	}
	if generation.State != OffsetMigrationGenerationReady {
		t.Fatalf("replay generation state=%s, want ready", generation.State)
	}
	var opCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM offset_migration_plans WHERE generation_id = $1`, generationID).Scan(&opCount); err != nil {
		t.Fatal(err)
	}
	if opCount != 1 {
		t.Fatalf("replay operation count=%d, want 1 (idempotent)", opCount)
	}
}

// TestListOffsetMigrationGenerationsOwnerTruth verifies the durable
// generation owner-truth surface: an admitted and planned generation exposes
// its state, offsets, scope and aggregate counts; superseded generations keep
// their terminal state visible.
func TestListOffsetMigrationGenerationsOwnerTruth(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")

	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		t.Fatalf("admit generation id=%q created=%v err=%v", generationID, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	service := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	if _, err := service.PlanGeneration(ctx, generationID); err != nil {
		t.Fatalf("plan generation: %v", err)
	}

	views, err := service.ListGenerations(ctx, "anime-offmig", 100, 0)
	if err != nil {
		t.Fatalf("list generations: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("generations=%d, want 1", len(views))
	}
	view := views[0]
	if view.Id != generationID || view.ScopeKind != "rss_source" || view.ScopeOwnerId != "src-offmig" {
		t.Fatalf("generation view identity: %+v", view)
	}
	if view.PolicyVersion != 1 || view.OldOffset != 0 || view.NewOffset != 1 {
		t.Fatalf("generation offsets: %+v", view)
	}
	plannedCount := 0
	if view.PlannedCount != nil {
		plannedCount = *view.PlannedCount
	}
	blockedCount := 0
	if view.BlockedCount != nil {
		blockedCount = *view.BlockedCount
	}
	if string(view.State) != "ready" || plannedCount != 1 || blockedCount != 0 {
		t.Fatalf("generation owner truth: %+v", view)
	}
	if view.CompletedAt != nil {
		t.Fatalf("ready generation must not be completed: %+v", view)
	}

	// A newer generation supersedes the old one; both stay visible.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	newGeneration, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 2, 1, 2)
	if err != nil || !created {
		t.Fatalf("admit v2 generation id=%q created=%v err=%v", newGeneration, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlanGeneration(ctx, newGeneration); err != nil {
		t.Fatalf("plan v2 generation: %v", err)
	}
	views, err = service.ListGenerations(ctx, "anime-offmig", 100, 0)
	if err != nil {
		t.Fatalf("list generations after supersede: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("generations=%d, want 2", len(views))
	}
	states := map[string]string{}
	for _, v := range views {
		states[v.Id] = string(v.State)
	}
	if states[generationID] != "superseded" || states[newGeneration] != "ready" {
		t.Fatalf("generation states after supersede: %+v", states)
	}
}

// TestPlanGenerationSupersedesOlderGenerations proves that a newer policy
// version wins the scope: the older generation and its ready operations are
// superseded, and only the newest generation owns ready work.
func TestPlanGenerationSupersedesOlderGenerations(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")

	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		t.Fatalf("admit v1 generation id=%q created=%v err=%v", oldGeneration, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	renderer := NewNamingTargetRenderer(db, fakeRulesSource{})
	service := NewOffsetMigrationService(db, renderer)
	if _, err := service.PlanGeneration(ctx, oldGeneration); err != nil {
		t.Fatalf("plan v1 generation: %v", err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	newGeneration, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 2, 1, 2)
	if err != nil || !created {
		t.Fatalf("admit v2 generation id=%q created=%v err=%v", newGeneration, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	generation, err := service.PlanGeneration(ctx, newGeneration)
	if err != nil {
		t.Fatalf("plan v2 generation: %v", err)
	}
	if generation.State != OffsetMigrationGenerationReady {
		t.Fatalf("v2 generation state=%s, want ready", generation.State)
	}

	var (
		oldGenState, newGenState string
		oldOpState, newOpEpisode string
		oldOpCount               int
	)
	if err := db.QueryRowContext(ctx, `
SELECT state FROM offset_migration_generations WHERE id = $1`, oldGeneration).Scan(&oldGenState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT state FROM offset_migration_generations WHERE id = $1`, newGeneration).Scan(&newGenState); err != nil {
		t.Fatal(err)
	}
	if oldGenState != string(OffsetMigrationGenerationSuperseded) || newGenState != string(OffsetMigrationGenerationReady) {
		t.Fatalf("generation states old=%s new=%s, want superseded/ready", oldGenState, newGenState)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM offset_migration_plans WHERE generation_id = $1 AND state <> 'superseded'`, oldGeneration).Scan(&oldOpCount); err != nil {
		t.Fatal(err)
	}
	if oldOpCount != 0 {
		t.Fatalf("older generation live operations=%d, want 0 (all superseded)", oldOpCount)
	}
	if err := db.QueryRowContext(ctx, `
SELECT state FROM offset_migration_plans WHERE generation_id = $1`, oldGeneration).Scan(&oldOpState); err != nil {
		t.Fatal(err)
	}
	if oldOpState != string(OffsetMigrationOperationSuperseded) {
		t.Fatalf("older operation state=%s, want superseded", oldOpState)
	}
	if err := db.QueryRowContext(ctx, `
SELECT new_episode FROM offset_migration_plans WHERE generation_id = $1`, newGeneration).Scan(&newOpEpisode); err != nil {
		t.Fatal(err)
	}
	if newOpEpisode != "3" {
		t.Fatalf("v2 operation new_episode=%s, want 3 (raw 1 + offset 2)", newOpEpisode)
	}
}

func TestDuplicateOffsetTargetsCountsEveryReadyOperation(t *testing.T) {
	target := "/library/same.mkv"
	blocked := plannedOffsetOperation{state: OffsetMigrationOperationBlocked, newTargetPath: &target}
	ready := plannedOffsetOperation{state: OffsetMigrationOperationReady, newTargetPath: &target}
	counts := duplicateOffsetTargets([]plannedOffsetOperation{ready, ready, blocked})
	if counts[target] != 2 {
		t.Fatalf("duplicate target count=%d, want 2", counts[target])
	}
}

func TestDuplicateOffsetTargetsIgnoreBlockedOperations(t *testing.T) {
	target := "/library/same.mkv"
	blocked := plannedOffsetOperation{state: OffsetMigrationOperationBlocked, newTargetPath: &target}
	counts := duplicateOffsetTargets([]plannedOffsetOperation{blocked, blocked})
	if counts[target] != 0 {
		t.Fatalf("blocked operations counted as duplicate targets: %d", counts[target])
	}
}

// seedSecondOffsetMigrationPlacement adds a second verified placement whose
// release came from secondSourceID, reusing the seedOffsetMigrationChain rows.
func seedSecondOffsetMigrationPlacement(t *testing.T, secondSourceID, secondPath string) {
	t.Helper()
	ctx := context.Background()
	seedTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer seedTx.Rollback()
	if _, err := seedTx.ExecContext(ctx, `
		INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,parsed_facts,internal_facts,verdict,reason_code,download_task_id,version)
		SELECT 'rel-offmig-2',$1,'guid:offmig-2',raw_facts,source_facts,parsed_facts,internal_facts,verdict,reason_code,'task-offmig-2',version FROM releases WHERE id='rel-offmig'`, secondSourceID); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, seedTx, "acq-offmig-2", "task-offmig-2", "rel-offmig-2", "qb:offmig-2", `{}`, `{}`, "completed", `null`, `[{"file_ref":"primary","ordinal":0,"relative_path":"Offset Anime - 01.mkv","exact_size":1024,"media_kind":"video","identity_evidence":{}}]`)
	if _, err := seedTx.ExecContext(ctx, `
		UPDATE download_tasks AS t SET frozen_input=o.frozen_input,frozen_policy=o.frozen_policy,state=o.state,correlation_id='corr-offmig-2',causation_type=o.causation_type,causation_id='rel-offmig-2' FROM download_tasks AS o WHERE t.id='task-offmig-2' AND o.id='task-offmig';
		INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at,correlation_id,causation_type,causation_id)
		SELECT 'art-offmig-2','task-offmig-2','acq-offmig-2',inventory_snapshot,fingerprint_snapshot,state,verified_at,'corr-offmig-2','download_task','task-offmig-2' FROM artifacts WHERE id='art-offmig';
		INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state)
		SELECT 'af-offmig-2','art-offmig-2','acq-offmig-2',file_ref,relative_path,'/downloads/Offset Anime - 02.mkv',media_kind,size_bytes,fingerprint,facts,subtitle_binding_state FROM artifact_files WHERE id='af-offmig';
		INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,input_digest,release_identity,release_facts)
		SELECT 'snap-offmig-2','rel-offmig-2',schema_version,resolver_version,parser_kind,'digest-offmig-2',release_identity,release_facts FROM media_fact_snapshots WHERE id='snap-offmig';
		INSERT INTO media_fact_files(snapshot_id,file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts)
		SELECT 'snap-offmig-2',file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts FROM media_fact_files WHERE snapshot_id='snap-offmig';`); err != nil {
		t.Fatal(err)
	}
	var fingerprint, config json.RawMessage
	var target string
	if err := seedTx.QueryRowContext(ctx, `SELECT source_fingerprint,frozen_config_snapshot,target_path FROM file_operations WHERE id='op-offmig'`).Scan(&fingerprint, &config, &target); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.CommitOperations(t, seedTx, "art-offmig-2", []acquisitionfixture.PlanOperation{{ID: "op-offmig-2", FileID: "af-offmig-2", FileRef: "primary", Source: "/downloads/Offset Anime - 02.mkv", Target: target, Operation: "move", Key: "op-offmig-2", Size: 1024, Fingerprint: fingerprint, Render: json.RawMessage(`{}`), Config: config}})
	if _, err := seedTx.ExecContext(ctx, `UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint,correlation_id='corr-offmig-2',causation_type='artifact',causation_id='art-offmig-2' WHERE id='op-offmig-2'; SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id='op-offmig-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,quality_facts_ref,facts_version,correlation_id,causation_type,causation_id,replacement_eligible,verification_state,state,version) SELECT 'plc-offmig-2',$1,managed_anime_id,'file_operation','op-offmig-2',source_fingerprint,target_fingerprint,quality_facts_ref,facts_version,'corr-offmig-2','file_operation','op-offmig-2',replacement_eligible,verification_state,state,version FROM placements WHERE id='plc-offmig'`, secondPath); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOffsetMigrationDuplicateTargetsArePersistedBlockedWithoutWorkItems(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")
	seedSecondOffsetMigrationPlacement(t, "src-offmig", "/library/Other/Offset Anime - 02 [Group].mkv")
	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("admit generation created=%v err=%v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := newOffsetMigrationWorkStore(t, db)
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	planner.SetWorkItems(store)
	if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
		t.Fatal(err)
	}
	var total, blocked, ready, items int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER (WHERE state='blocked' AND reason_code='target_conflict'),COUNT(*) FILTER (WHERE state='ready') FROM offset_migration_plans WHERE generation_id=$1`, generationID).Scan(&total, &blocked, &ready); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE kind=$1 AND owner_type='offset_migration_operation' AND owner_id IN (SELECT id FROM offset_migration_plans WHERE generation_id=$2)`, workitems.KindOffsetMigrationExecute, generationID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if total != 2 || blocked != 2 || ready != 0 || items != 0 {
		t.Fatalf("total=%d blocked=%d ready=%d workitems=%d", total, blocked, ready, items)
	}
}

// An RSS link offset edit plans only the placements whose release came from
// that link; another link of the same anime keeps its files.
func TestPlanGenerationMigratesOnlyTheEditedLink(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, episode_offset)
VALUES ('src-offmig-other', 'sub-offmig', 'custom_rss', 'https://feeds.example.com/other.xml', 'https://feeds.example.com/other.xml', 'Group', 'Offset Anime', 0)`); err != nil {
		t.Fatal(err)
	}
	seedSecondOffsetMigrationPlacement(t, "src-offmig-other", "/library/Other/Offset Anime - 01 [Other].mkv")

	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 2, 0, 1)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("admit generation created=%v err=%v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	planner.SetWorkItems(newOffsetMigrationWorkStore(t, db))
	if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT placement_id, rss_source_id FROM offset_migration_plans WHERE generation_id=$1 ORDER BY placement_id`, generationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var planned []string
	for rows.Next() {
		var placement, source string
		if err := rows.Scan(&placement, &source); err != nil {
			t.Fatal(err)
		}
		planned = append(planned, placement+"@"+source)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(planned) != 1 || planned[0] != "plc-offmig@src-offmig" {
		t.Fatalf("planned=%v, want only the edited link's placement plc-offmig", planned)
	}
}

// TestPlanGenerationRendersWithCurrentNamingRules proves offset migration
// renders the corrected target with the anime's CURRENT naming rules (the same
// renderer as alignment), while the episode identity stays the frozen one plus
// the new offset.
func TestPlanGenerationRendersWithCurrentNamingRules(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := NewOffsetMigrationAdmissionService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		t.Fatalf("admit generation id=%q created=%v err=%v", generationID, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	current := frozenRulesForTest()
	current.TV.DirectoryTemplate = "/library/Renamed/${sanitize(anime.title)}"
	current.TV.VideoFilenameTemplate = "E${pad(ep.display, 2)}"
	service := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: current}))
	if _, err := service.PlanGeneration(ctx, generationID); err != nil {
		t.Fatalf("plan generation: %v", err)
	}
	var newTarget string
	if err := db.QueryRowContext(ctx, `SELECT new_target_path FROM offset_migration_plans WHERE generation_id = $1`, generationID).Scan(&newTarget); err != nil {
		t.Fatal(err)
	}
	if newTarget != "/library/Renamed/Offset Anime/E02" {
		t.Fatalf("new_target=%q, want the current template with episode 2", newTarget)
	}
}
