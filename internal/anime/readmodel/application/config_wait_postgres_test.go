package application

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/storedsections"
)

// seedConfigWaitingImport arranges a complete anime workspace with one
// running subscription download whose import waits for configuration: the
// real wait owner function parked its plan with the given hold.
func seedConfigWaitingImport(t *testing.T, hold string, files int) (*Service, *sql.DB, string) {
	t.Helper()
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-wait', 'Wait Show')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('subscription-wait', 'anime-wait', 'custom', 'Wait', 'active')`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-wait') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-wait')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-wait', 'subscription-wait', 'custom_rss', 'https://feed.example.test/wait.xml', 'https://feed.example.test/wait.xml', 'Group', 'Wait Show')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, "anime-wait")

	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-wait', 'source-wait', 'guid:wait', '{"title":"[Group] Wait Show - 01"}', '{}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "wait-root", "download-wait", "release-wait", "download:wait", `{}`, `{}`, "completed", `null`,
		`[{"file_ref":"primary","ordinal":0,"relative_path":"Wait Show - 01.mkv","exact_size":7,"media_kind":"video","identity_evidence":{}}]`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'download-wait' WHERE id = 'release-wait'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-wait', 'download-wait', 'wait-root', '{}', '{"fingerprints":[]}', 'verified', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ('file-wait', 'artifact-wait', 'wait-root', 'primary', 'Wait Show - 01.mkv', '/downloads/Wait Show - 01.mkv', 'video', 7, '{}', 'not_applicable')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.ParkWaitingPlan(t, tx, "artifact-wait", hold, files)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return readmodelNewTestService(t, db, nil), db, "wait-root"
}

// A running record whose import waits for configuration is at the plan stage
// in waiting_config with what the configuration lacks, in the stored order,
// and offers no import retry; the anime workspace counts the files it blocks.
func TestAcquisitionRecordShowsAnImportWaitingForConfiguration(t *testing.T) {
	service, _, root := seedConfigWaitingImport(t,
		`[{"item":"binding_missing:tmdb","files":1},{"item":"template_variable_undefined:anibt.language,rt.title","files":1}]`, 1)
	ctx := context.Background()
	want := []AcquisitionConfigHoldItem{
		{Missing: AcquisitionConfigMissingTMDB, Files: 1},
		{Missing: AcquisitionConfigMissingVariable, Variables: []string{"anibt.language", "rt.title"}, Files: 1},
	}

	detail, err := service.GetAcquisitionRecord(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "running" || detail.Stage == nil || *detail.Stage != AcquisitionStagePlan ||
		detail.StageState == nil || *detail.StageState != AcquisitionStageStateWaitingConfig || detail.ReasonCode != nil {
		t.Fatalf("position = %v/%v/%v (%s)", detail.Stage, detail.StageState, detail.ReasonCode, detail.State)
	}
	if !reflect.DeepEqual(detail.ConfigHold, want) {
		t.Fatalf("config hold = %+v, want %+v", detail.ConfigHold, want)
	}
	if detail.ImportRetryAvailable {
		t.Fatal("a waiting import offers no retry")
	}
	plan := stageByName(t, detail.Stages, AcquisitionStagePlan)
	if stateOf(plan) != AcquisitionStageStateWaitingConfig || plan.AttemptsStarted != nil {
		t.Fatalf("plan stage = %s, attempts %v", stateOf(plan), plan.AttemptsStarted)
	}

	page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].ConfigHold, want) {
		t.Fatalf("list items = %+v", page.Items)
	}

	workspace, err := service.GetAnimeWorkspaceApplication(ctx, "anime-wait")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.ConfigWaitingFiles != 1 {
		t.Fatalf("config waiting files = %d, want 1", workspace.ConfigWaitingFiles)
	}
}

// Retiring the instance while its import waits settles the root cancelled:
// the record shows the plan stage cancelled, no hold and no retry, and the
// workspace counts nothing.
func TestRetiredInstanceCancelsAWaitingImport(t *testing.T) {
	service, db, root := seedConfigWaitingImport(t, `[{"item":"binding_missing:bgm","files":1}]`, 1)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT fence_epoch FROM managed_anime WHERE id='anime-wait' FOR UPDATE`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT work_item_retire_unadmitted('anime-wait', $1)`, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE managed_anime SET lifecycle_state='retired', fence_epoch=$1+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=(SELECT version FROM subscriptions WHERE managed_anime_id='anime-wait'),
    lifecycle_version=lifecycle_version+1, updated_at=CURRENT_TIMESTAMP
WHERE id='anime-wait'`, epoch); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.DisposeRoot(ctx, root); err != nil || status != "cancelled" {
		t.Fatalf("dispose retired root: status=%q err=%v", status, err)
	}

	detail, err := service.GetAcquisitionRecord(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "cancelled" || detail.ConfigHold != nil || detail.ImportRetryAvailable {
		t.Fatalf("retired record = %s hold=%+v retry=%v", detail.State, detail.ConfigHold, detail.ImportRetryAvailable)
	}
	if plan := stageByName(t, detail.Stages, AcquisitionStagePlan); stateOf(plan) != AcquisitionStageStateCancelled {
		t.Fatalf("plan stage = %s, want cancelled", stateOf(plan))
	}
	var workState string
	if err := owner.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE owner_id='fixture-plan-artifact-wait'`).Scan(&workState); err != nil {
		t.Fatal(err)
	}
	if workState == "available" || workState == "leased" {
		t.Fatalf("the waiting plan's work item stayed live: %s", workState)
	}
	workspace, err := service.GetAnimeWorkspaceApplication(ctx, "anime-wait")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.ConfigWaitingFiles != 0 {
		t.Fatalf("a cancelled import still counts %d waiting files", workspace.ConfigWaitingFiles)
	}
}
