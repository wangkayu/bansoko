package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// seedFailedRecordRoot drives one download root through the real plan commit,
// a failed file execution and the real settler, so it settles failed with a
// sealed requirement set, one failed delivery and a terminal receipt.
func seedFailedRecordRoot(t *testing.T, db *sql.DB, root string) {
	t.Helper()
	ctx := context.Background()
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed failed root: %v", err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-failed', 'Failed Show')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-failed', 'anime-failed', 'custom', 'Failed', 'active')`)
	exec(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-failed', 'sub-failed', 'custom_rss', 'https://feed.example.test/failed.xml', 'https://feed.example.test/failed.xml', 'Group', 'Failed Show')`)
	exec(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code, download_task_id)
VALUES ('rel-failed', 'src-failed', 'guid:failed', '{"title":"[Group] Failed Show - 02"}', '{}', 'submitted', 'fixture_submitted', 'task-failed')`)
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, root, "task-failed", "rel-failed", "idem-task-failed", `{"one_shot":true}`, `{}`, "completed", `null`,
		`[{"file_ref":"file-a","ordinal":0,"relative_path":"file-a.mkv","exact_size":1,"media_kind":"video","identity_evidence":{}}]`)
	exec(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('art-failed', 'task-failed', $1, '{}', '{}', 'verified', CURRENT_TIMESTAMP)`, root)
	exec(`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ('af-failed', 'art-failed', $1, 'file-a', 'file-a.mkv', '/downloads/file-a.mkv', 'video', 1, '{}', 'not_applicable')`, root)
	acquisitionfixture.CommitOperations(t, tx, "art-failed", []acquisitionfixture.PlanOperation{{ID: "fo-failed", FileID: "af-failed", FileRef: "file-a",
		Source: "/downloads/file-a.mkv", Target: "/library/Failed Show/file-a.mkv", Operation: "copy", Key: "idem-fo-failed", Size: 1,
		Fingerprint: json.RawMessage(`{}`), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var work string
	var token int64
	if err := db.QueryRowContext(ctx, `SELECT id,fencing_token FROM work_item_claim_due(60::double precision,ARRAY['file_execute']::text[])`).Scan(&work, &token); err != nil {
		t.Fatalf("claim file_execute: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT file_finish_failed($1,$2,$3,'FIXTURE_FILE_FAILED','fixture file failure')`, "fo-failed", work, token); err != nil {
		t.Fatalf("finish file failed: %v", err)
	}
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle root: status=%q err=%v", status, err)
	}
}

// seedRecordRoots arranges two roots: a settled failed root (older) and a
// running download root accepted from a subscription release (task completed,
// artifact verified, no plan yet).
func seedRecordRoots(t *testing.T) (*Service, *sql.DB, string, string) {
	t.Helper()
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	seedFailedRecordRoot(t, db, "record-failed")
	animetest.SeedReleaseOwnerChain(t, db, "source-record")

	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-record', 'source-record', 'guid:record', '{"title":"[Group] Record Show - 01"}', '{}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "record-download", "download-record", "release-record", "download:record", `{}`, `{}`, "completed", `null`,
		`[{"file_ref":"primary","ordinal":0,"relative_path":"Record Show - 01.mkv","exact_size":7,"media_kind":"video","identity_evidence":{}}]`)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-record', 'download-record', 'record-download', '{}', '{"fingerprints":[]}', 'verified', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return readmodelNewTestService(t, db, nil), db, "record-download", "record-failed"
}

func stageByName(t *testing.T, stages []AcquisitionStage, name string) AcquisitionStage {
	t.Helper()
	for _, stage := range stages {
		if stage.Stage == name {
			return stage
		}
	}
	t.Fatalf("stage %s missing from %+v", name, stages)
	return AcquisitionStage{}
}

func stateOf(stage AcquisitionStage) string {
	if stage.State == nil {
		return "<" + stage.Availability + ">"
	}
	return *stage.State
}

// TestAcquisitionRecordsListAndDetail proves the list order, the anime and
// state filters, the cursor and the fixed-stage detail of a running download
// root and a settled failed root.
func TestAcquisitionRecordsListAndDetail(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()

	page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].AcquisitionID != downloadID || page.Items[1].AcquisitionID != failedID || page.NextCursor != nil {
		t.Fatalf("list must hold both roots newest first: %+v", page.Items)
	}
	running := page.Items[0]
	if running.State != "running" || running.EntryKind != "one_shot" || running.Stage == nil || *running.Stage != AcquisitionStagePlan || *running.StageState != AcquisitionStageStatePending {
		t.Fatalf("running root must report the plan stage it waits at: %+v", running)
	}
	if running.Anime == nil || running.Anime.ManagedAnimeID != "anime-trace" || running.Anime.Title != "Trace Show" {
		t.Fatalf("running root must link its anime: %+v", running.Anime)
	}
	if settled := page.Items[1]; settled.State != "failed" || settled.Stage == nil || *settled.Stage != AcquisitionStageFileExecution ||
		settled.ReasonCode == nil || *settled.ReasonCode != "FIXTURE_FILE_FAILED" || settled.Counts == nil || settled.Counts.Failed != 1 ||
		settled.TerminalAt == nil || settled.Anime == nil || settled.Anime.ManagedAnimeID != "anime-failed" {
		t.Fatalf("a failed root must report its receipt failure and counts: %+v", settled)
	}

	first, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("limit 1 must page with a cursor: %+v %v", first, err)
	}
	second, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Limit: 1, Cursor: *first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].AcquisitionID != failedID || second.NextCursor != nil {
		t.Fatalf("the cursor must continue after the last returned row: %+v %v", second, err)
	}
	var bad *apperrors.BadRequest
	if _, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Limit: 1, Cursor: *first.NextCursor, State: "failed"}); !errors.As(err, &bad) || bad.Code != "CURSOR_FILTER_MISMATCH" {
		t.Fatalf("a cursor reused under other filters must be CURSOR_FILTER_MISMATCH, got %v", err)
	}
	if _, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Cursor: "bm90LWEtY3Vyc29y"}); !errors.As(err, &bad) || bad.Code != "INVALID_CURSOR" {
		t.Fatalf("a foreign cursor must be INVALID_CURSOR, got %v", err)
	}
	if _, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{State: "done"}); !errors.As(err, &bad) || bad.Code != "QUERY_INVALID" {
		t.Fatalf("an unknown state must be QUERY_INVALID, got %v", err)
	}

	byAnime, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{ManagedAnimeID: "anime-trace"})
	if err != nil || len(byAnime.Items) != 1 || byAnime.Items[0].AcquisitionID != downloadID {
		t.Fatalf("anime filter must keep only that anime's root: %+v %v", byAnime.Items, err)
	}
	byState, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{State: "failed"})
	if err != nil || len(byState.Items) != 1 || byState.Items[0].AcquisitionID != failedID {
		t.Fatalf("state filter must keep only settled roots: %+v %v", byState.Items, err)
	}

	detail, err := service.GetAcquisitionRecord(ctx, downloadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Stages) != 7 || detail.ExecutionBranch != "download" || detail.Outcome != nil {
		t.Fatalf("running download detail must have seven stages and no receipt: %+v", detail)
	}
	for name, want := range map[string]string{
		AcquisitionStageSubmit: AcquisitionStageStateSucceeded, AcquisitionStageDownloadWait: AcquisitionStageStateSucceeded,
		AcquisitionStageInspect: AcquisitionStageStateSucceeded, AcquisitionStagePlan: "<not_reached>",
		AcquisitionStageFileExecution: "<not_reached>", AcquisitionStageSettlement: AcquisitionStageStatePending,
		AcquisitionStageCompletion: "<not_reached>",
	} {
		if got := stateOf(stageByName(t, detail.Stages, name)); got != want {
			t.Fatalf("stage %s = %s, want %s", name, got, want)
		}
	}
	if detail.Release == nil || detail.Release.ReleaseID != "release-record" || detail.Release.Title.Value == nil || *detail.Release.Title.Value != "[Group] Record Show - 01" {
		t.Fatalf("detail must link the source release: %+v", detail.Release)
	}

	failed, err := service.GetAcquisitionRecord(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Outcome == nil || failed.Outcome.Outcome != "failed" || failed.Outcome.PrimaryFailure == nil || !failed.FilesAvailable ||
		failed.Requirements == nil || failed.Requirements.Status != "sealed" {
		t.Fatalf("failed detail must carry its receipt and its sealed file set: %+v", failed)
	}
	for name, want := range map[string]string{
		AcquisitionStageSubmit: AcquisitionStageStateSucceeded, AcquisitionStagePlan: AcquisitionStageStateSucceeded,
		AcquisitionStageFileExecution: AcquisitionStageStateFailed, AcquisitionStageSettlement: AcquisitionStageStateFailed,
		AcquisitionStageCompletion: "<not_applicable>",
	} {
		if got := stateOf(stageByName(t, failed.Stages, name)); got != want {
			t.Fatalf("failed stage %s = %s, want %s", name, got, want)
		}
	}
	files, err := service.ListAcquisitionRecordFiles(ctx, failedID, AcquisitionFileQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Items) != 1 || files.Items[0].FileRef != "file-a" || files.Items[0].Operation == nil || files.Items[0].Operation.State != "failed" ||
		files.Items[0].RelativePath.Value == nil || *files.Items[0].RelativePath.Value != "file-a.mkv" {
		t.Fatalf("failed files must list the failed file: %+v", files.Items)
	}

	var notFound *apperrors.NotFound
	if _, err := service.GetAcquisitionRecord(ctx, "record-missing"); !errors.As(err, &notFound) {
		t.Fatalf("an unknown root must be NotFound, got %v", err)
	}
}

// TestAcquisitionRecordsNameTheirEpisodes proves a record row names its
// source release and the episodes of the newest frozen batch plan of that
// release, that a release without a snapshot names none, that a finished
// download carries no transfer, and that a failed delivery has no placed path.
func TestAcquisitionRecordsNameTheirEpisodes(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	for _, snapshot := range []struct{ id, coverage, at string }{
		{"snapshot-record-old", `["tv:e9"]`, "2026-10-01T00:00:00Z"},
		{"snapshot-record-new", `["tv:e2","tv:e1"]`, "2026-10-02T00:00:00Z"},
	} {
		if _, err := owner.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts, created_at)
VALUES ($1, 'release-record', 'v1', 'resolver-v1', 'regex', 'digest', '{}', jsonb_build_object('batch_plan', jsonb_build_object('coverage_keys', $2::jsonb)), $3::timestamptz)`,
			snapshot.id, snapshot.coverage, snapshot.at); err != nil {
			t.Fatalf("seed media snapshot: %v", err)
		}
	}

	page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("list: %+v %v", page.Items, err)
	}
	running, settled := page.Items[0], page.Items[1]
	if running.AcquisitionID != downloadID || len(running.Episodes) != 2 || running.Episodes[0] != 1 || running.Episodes[1] != 2 {
		t.Fatalf("the row must carry the newest frozen plan's episodes: %+v", running.Episodes)
	}
	if running.Release == nil || running.Release.ReleaseID != "release-record" || running.Release.Title.Value == nil || *running.Release.Title.Value != "[Group] Record Show - 01" {
		t.Fatalf("the row must name its source release: %+v", running.Release)
	}
	if running.Transfer != nil {
		t.Fatalf("a finished download carries no transfer: %+v", running.Transfer)
	}
	if settled.AcquisitionID != failedID || settled.Episodes != nil || settled.Release == nil || settled.Release.ReleaseID != "rel-failed" {
		t.Fatalf("a release without a frozen plan names no episodes but keeps its release: %+v", settled)
	}

	detail, err := service.GetAcquisitionRecord(ctx, downloadID)
	if err != nil || len(detail.Episodes) != 2 || detail.Release == nil {
		t.Fatalf("detail must carry the same episodes and release: %+v %v", detail, err)
	}

	files, err := service.ListAcquisitionRecordFiles(ctx, failedID, AcquisitionFileQuery{})
	if err != nil || len(files.Items) != 1 || files.Items[0].PlacedPath != nil || files.Items[0].CollisionOutcome != nil {
		t.Fatalf("a failed delivery has no placed path: %+v %v", files.Items, err)
	}
}

// TestAcquisitionRecordsCarrySeedIdentity proves a row reads the seed's size
// from the root's frozen accepted input and its tags, release version and
// publish time from the source release's frozen facts; that a release without
// frozen tags leaves them out; and that a compacted root whose release is gone
// keeps the frozen size but no tags.
func TestAcquisitionRecordsCarrySeedIdentity(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()
	owner := disposabledb.Owner(t)

	page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("list: %+v %v", page.Items, err)
	}
	for _, item := range page.Items {
		if item.AcquisitionID == downloadID && item.Seed != nil && (len(item.Seed.TitleTags) != 0 || item.Seed.PublishedAt != nil) {
			t.Fatalf("a release without frozen facts must not invent tags or a publish time: %+v", item.Seed)
		}
		if item.ReplacesExisting {
			t.Fatalf("an ensure_present acceptance is not a replacement: %s", item.AcquisitionID)
		}
	}

	if _, err := owner.ExecContext(ctx, `
UPDATE releases SET raw_facts = '{"title":"[Group] Record Show - 01","published_at":"2026-10-01T02:03:04Z"}',
  source_facts = '{"group_name":"Group","identity_kind":"guid","identity_value":"record","title_tags":{"schema":"reltitle-attrs-v1","implementation":"x","tags":["resolution:1080p","sub_lang:SC"],"value_tags":["title:Record Show","version:v2"]},"anibt":{"tags":["resolution:1080p"]}}'
WHERE id = 'release-record'`); err != nil {
		t.Fatal(err)
	}
	detail, err := service.GetAcquisitionRecord(ctx, downloadID)
	if err != nil || detail.Seed == nil {
		t.Fatalf("detail must carry the seed identity: %+v %v", detail.Seed, err)
	}
	seed := detail.Seed
	if seed.SizeBytes == nil || *seed.SizeBytes != 7 {
		t.Fatalf("size must come from the frozen torrent total: %+v", seed.SizeBytes)
	}
	if strings.Join(seed.TitleTags, ",") != "resolution:1080p,sub_lang:SC" || seed.ReleaseVersion == nil || *seed.ReleaseVersion != "v2" ||
		strings.Join(seed.AniBTTags, ",") != "resolution:1080p" {
		t.Fatalf("tags and version must come from the frozen release facts: %+v", seed)
	}
	if seed.PublishedAt == nil || seed.PublishedAt.UTC().Format("2006-01-02T15:04:05Z") != "2026-10-01T02:03:04Z" {
		t.Fatalf("publish time must come from the release raw facts: %+v", seed.PublishedAt)
	}

	// Owner cleanup deletes the failed root's release and compacts the root.
	if _, err := owner.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
    retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1 WHERE id = 'anime-failed'`); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := owner.QueryRowContext(ctx, `SELECT fence_epoch FROM managed_anime WHERE id = 'anime-failed'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `SELECT public.cleanup_managed_anime_local_data('anime-failed', 'record-cleanup', $1, $2)`,
		"sha256:"+strings.Repeat("0", 64), epoch); err != nil {
		t.Fatal(err)
	}
	compact, err := service.GetAcquisitionRecord(ctx, failedID)
	if err != nil || compact.RecordDetail != "compact" {
		t.Fatalf("root must be compact: %+v %v", compact, err)
	}
	if compact.Seed == nil || compact.Seed.SizeBytes == nil || len(compact.Seed.TitleTags) != 0 || compact.Seed.PublishedAt != nil {
		t.Fatalf("a compact root keeps the frozen input facts but not the deleted release's tags: %+v", compact.Seed)
	}
}

// TestAcquisitionRecordsCompactAndScope proves a compact root keeps its title
// and outcome only, that hiding is gone (PRUNE-046 S4), and that a root of
// another access scope reads as not found.
func TestAcquisitionRecordsCompactAndScope(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()
	owner := disposabledb.Owner(t)

	if _, err := service.ListAcquisitionRecordFiles(ctx, downloadID, AcquisitionFileQuery{}); !errors.Is(err, ErrAcquisitionFilesNotReady) {
		t.Fatalf("an unsealed root has no file page yet, got %v", err)
	}
	// The release still names the root's download task: compact refuses with
	// the typed release_bound status and writes nothing (it never detaches the
	// pointer, and the commit does not fail on the deferred foreign key).
	const compactSQL = `SELECT public.acquisition_compact($1, 'detail_expired', clock_timestamp() + INTERVAL '400 days')->>'status'`
	var status string
	if err := owner.QueryRowContext(ctx, compactSQL, failedID).Scan(&status); err != nil || status != "release_bound" {
		t.Fatalf("a release-bound root must be refused as release_bound, got %q %v", status, err)
	}
	if refused, err := service.GetAcquisitionRecord(ctx, failedID); err != nil || refused.RecordDetail != "full" || len(refused.Stages) != 7 {
		t.Fatalf("a refused compact must leave the root full: %+v %v", refused, err)
	}
	// The owner's local cleanup deletes that release in the same transaction,
	// so it compacts the root (trigger owner_cleanup).
	if _, err := owner.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
    retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1 WHERE id = 'anime-failed'`); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := owner.QueryRowContext(ctx, `SELECT fence_epoch FROM managed_anime WHERE id = 'anime-failed'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	var cleanup string
	if err := owner.QueryRowContext(ctx, `SELECT public.cleanup_managed_anime_local_data('anime-failed', 'record-cleanup', $1, $2)::text`,
		"sha256:"+strings.Repeat("0", 64), epoch).Scan(&cleanup); err != nil || !strings.Contains(cleanup, `"acquisitions_compacted": 1`) {
		t.Fatalf("owner cleanup must compact the release-bound root: %s %v", cleanup, err)
	}
	compact, err := service.GetAcquisitionRecord(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if compact.RecordDetail != "compact" || len(compact.Stages) != 0 || compact.Requirements != nil || compact.FilesAvailable ||
		compact.Outcome == nil || compact.Outcome.Outcome != "failed" || compact.SourceTitle.State != AcquisitionDisplayVisible ||
		compact.CompactReason == nil || *compact.CompactReason != "owner_cleanup" {
		t.Fatalf("compact root must keep title and outcome only: %+v", compact)
	}
	if _, err := service.ListAcquisitionRecordFiles(ctx, failedID, AcquisitionFileQuery{}); !errors.Is(err, ErrAcquisitionDetailUnavailable) {
		t.Fatalf("a compact root's files must be unavailable, got %v", err)
	}

	// PRUNE-046 S4 removed hide_acquisition and hidden_at: nothing leaves the
	// list, and the detail no longer carries a hidden flag at all.
	page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("both roots stay listed: %+v %v", page.Items, err)
	}

	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET access_scope = 'other_scope_v1' WHERE id = $1`, downloadID); err != nil {
		t.Fatal(err)
	}
	var notFound *apperrors.NotFound
	if _, err := service.GetAcquisitionRecord(ctx, downloadID); !errors.As(err, &notFound) {
		t.Fatalf("a foreign-scope root must read as not found, got %v", err)
	}
	if _, err := service.ListAcquisitionRecordFiles(ctx, downloadID, AcquisitionFileQuery{}); !errors.As(err, &notFound) {
		t.Fatalf("a foreign-scope root's files must read as not found, got %v", err)
	}
	if page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{}); err != nil || len(page.Items) != 1 || page.Items[0].AcquisitionID != failedID {
		t.Fatalf("only the in-scope root is listed: %+v %v", page.Items, err)
	}
}

// TestAcquisitionRecordFilesPlacementFailure proves the file page shows a
// terminally failed placement handoff of a completed operation from the
// durable obligation alone (its failed continuation may have been pruned), and
// that a running root never offers the import retry.
func TestAcquisitionRecordFilesPlacementFailure(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if detail, err := service.GetAcquisitionRecord(ctx, downloadID); err != nil || detail.ImportRetryAvailable || detail.Version < 1 {
		t.Fatalf("a running root offers no import retry: %+v %v", detail, err)
	}
	failureOf := func() *AcquisitionFilePlacementFailure {
		t.Helper()
		files, err := service.ListAcquisitionRecordFiles(ctx, failedID, AcquisitionFileQuery{})
		if err != nil || len(files.Items) != 1 {
			t.Fatalf("files: %+v %v", files, err)
		}
		return files.Items[0].PlacementFailure
	}
	if failure := failureOf(); failure != nil {
		t.Fatalf("a failed file effect is not a placement failure: %+v", failure)
	}

	// Projection fixture: the file effect completed and its placement handoff
	// then failed terminally. The completed state is written directly (the
	// read model is under test, not the file executor).
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`SET LOCAL session_replication_role = replica`,
		`UPDATE file_operations SET state = 'completed', failure_code = NULL, failure_reason = NULL,
    observed_at = CURRENT_TIMESTAMP, target_fingerprint = COALESCE(target_fingerprint, '{}'::jsonb) WHERE id = 'fo-failed'`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	animetest.SeedPlacementHandoff(t, owner, "fo-failed", "failed", 3, 3, "")
	failure := failureOf()
	if failure == nil || failure.Attempts != 3 || failure.ReasonCode == nil || *failure.ReasonCode != "placement_admission_failed" {
		t.Fatalf("a failed placement handoff of a completed operation must show: %+v", failure)
	}

	if _, err := owner.ExecContext(ctx, `UPDATE placement_obligations SET state = 'pending', attempts = 0, reason_code = NULL, reason = NULL WHERE file_operation_id = 'fo-failed'`); err != nil {
		t.Fatal(err)
	}
	if failure := failureOf(); failure != nil {
		t.Fatalf("a re-opened obligation shows no failure: %+v", failure)
	}
}

// TestAcquisitionRecordsExposeOneShotMarkerHandle proves the record surface
// resolves the one-shot marker handle of a one-shot root (the marker owns the
// same request idempotency key the root was admitted under) and leaves it null
// for every other entry kind.
func TestAcquisitionRecordsExposeOneShotMarkerHandle(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()

	// The root re-label and the marker seed are owner writes: the readmodel
	// tests otherwise run under the segregated runtime role.
	owner, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Rollback() }()

	// The failed fixture root is re-labelled as an automatic acquisition, the
	// way the real accept path would have admitted it.
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET entry_kind='automatic', request_namespace='auto_acquire_v1' WHERE id=$1`, failedID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(
  id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
  correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES (
  'osa-record-download', 'anime-trace', 'subscription-trace', 'source-record',
  'release-record', 'download-record', 'corr-record-download',
  $1, $2, '{}', 'running')`, downloadID, "sha256:"+strings.Repeat("1", 64)); err != nil {
		t.Fatalf("seed one-shot marker: %v", err)
	}
	if err := owner.Commit(); err != nil {
		t.Fatal(err)
	}

	page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AcquisitionRecordSummary{}
	for _, item := range page.Items {
		byID[item.AcquisitionID] = item
	}
	running, ok := byID[downloadID]
	if !ok || running.EntryKind != "one_shot" {
		t.Fatalf("one-shot root missing from the list: %+v", page.Items)
	}
	if running.OneShotID == nil || *running.OneShotID != "osa-record-download" {
		t.Fatalf("one-shot root one_shot_id = %v, want osa-record-download", running.OneShotID)
	}
	if automatic := byID[failedID]; automatic.EntryKind != "automatic" || automatic.OneShotID != nil {
		t.Fatalf("automatic root must not carry a marker handle: %+v", automatic)
	}

	detail, err := service.GetAcquisitionRecord(ctx, downloadID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.OneShotID == nil || *detail.OneShotID != "osa-record-download" {
		t.Fatalf("one-shot detail one_shot_id = %v, want osa-record-download", detail.OneShotID)
	}
	automaticDetail, err := service.GetAcquisitionRecord(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if automaticDetail.OneShotID != nil {
		t.Fatalf("automatic detail one_shot_id = %v, want nil", *automaticDetail.OneShotID)
	}
}

// TestAcquisitionRecordsSplitByOrigin proves the origin filter (subscription =
// automatic + subscription_add, one_shot = one_shot), the origin counts under
// the other filters, and that a cursor is bound to the origin it was issued
// under.
func TestAcquisitionRecordsSplitByOrigin(t *testing.T) {
	service, _, downloadID, failedID := seedRecordRoots(t)
	ctx := context.Background()
	// The fixture accepts both roots as one-shots; make the failed one a
	// subscription add (same request namespace, so the row stays valid).
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `UPDATE acquisitions SET entry_kind='subscription_add' WHERE id=$1`, failedID); err != nil {
		t.Fatal(err)
	}

	all, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{})
	if err != nil || len(all.Items) != 2 || all.OriginCounts != (AcquisitionRecordOriginCounts{Subscription: 1, OneShot: 1}) {
		t.Fatalf("unfiltered page must count both origins: %+v %v", all, err)
	}
	subs, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Origin: "subscription"})
	if err != nil || len(subs.Items) != 1 || subs.Items[0].AcquisitionID != failedID || subs.OriginCounts != all.OriginCounts {
		t.Fatalf("origin=subscription must keep the subscription record and keep both counts: %+v %v", subs, err)
	}
	oneShot, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Origin: "one_shot"})
	if err != nil || len(oneShot.Items) != 1 || oneShot.Items[0].AcquisitionID != downloadID {
		t.Fatalf("origin=one_shot must keep the one-shot record: %+v %v", oneShot, err)
	}
	// Counts follow the state filter but never the origin.
	failedOnly, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Origin: "one_shot", State: "failed"})
	if err != nil || len(failedOnly.Items) != 0 || failedOnly.OriginCounts != (AcquisitionRecordOriginCounts{Subscription: 1, OneShot: 0}) {
		t.Fatalf("counts must follow the state filter: %+v %v", failedOnly, err)
	}

	var bad *apperrors.BadRequest
	if _, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Origin: "agent"}); !errors.As(err, &bad) || bad.Code != "QUERY_INVALID" {
		t.Fatalf("an unknown origin must be QUERY_INVALID, got %v", err)
	}
	first, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Limit: 1})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("limit 1 must page: %+v %v", first, err)
	}
	if _, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{Limit: 1, Cursor: *first.NextCursor, Origin: "one_shot"}); !errors.As(err, &bad) || bad.Code != "CURSOR_FILTER_MISMATCH" {
		t.Fatalf("a cursor reused under another origin must be CURSOR_FILTER_MISMATCH, got %v", err)
	}
}
