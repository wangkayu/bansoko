package workitems

import (
	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// insertOneShotMarker seeds one minimal marker row. Anchors are opaque
// references without cascading FKs, so batch reads need no owner graph.
func insertOneShotMarker(t *testing.T, db *sql.DB, id, state string, retentionUntil *time.Time) {
	t.Helper()
	ctx := context.Background()
	if state == OneShotStateRunning {
		if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb, $10)`,
			id, "anime-"+id, "sub-"+id, "src-"+id, "rel-"+id, "task-"+id,
			"corr-"+id, "req-key-"+id, "sha256:"+strings.Repeat("0", 64), state); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state,
    terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb, $10,
        'succeeded', 'one_shot_completed', 'one-shot acquisition completed', CURRENT_TIMESTAMP, $11)`,
		id, "anime-"+id, "sub-"+id, "src-"+id, "rel-"+id, "task-"+id,
		"corr-"+id, "req-key-"+id, "sha256:"+strings.Repeat("0", 64), state, retentionUntil); err != nil {
		t.Fatal(err)
	}
}

func oneShotReclaimAt(value time.Time) *time.Time { return &value }

// Regression: the batch query ends in LIMIT $2, so the bind args must carry
// the batch size or every retention pass fails at PostgreSQL bind time.
func TestOneShotDueIDsBindsLimitAndFilters(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	insertOneShotMarker(t, db, "m-run-a", OneShotStateRunning, nil)
	insertOneShotMarker(t, db, "m-pend-due", OneShotStateReclaimPending, oneShotReclaimAt(time.Now().Add(-time.Hour)))
	insertOneShotMarker(t, db, "m-pend-future", OneShotStateReclaimPending, oneShotReclaimAt(time.Now().Add(24*time.Hour)))

	drainIDs, err := service.oneShotDueIDs(ctx, []string{OneShotStateRunning}, false)
	if err != nil {
		t.Fatalf("drain batch: %v", err)
	}
	if len(drainIDs) != 1 || drainIDs[0] != "m-run-a" {
		t.Fatalf("drain batch = %v, want [m-run-a]", drainIDs)
	}

	reclaimIDs, err := service.oneShotDueIDs(ctx, []string{OneShotStateReclaimPending}, true)
	if err != nil {
		t.Fatalf("reclaim batch: %v", err)
	}
	if len(reclaimIDs) != 1 || reclaimIDs[0] != "m-pend-due" {
		t.Fatalf("reclaim batch = %v, want [m-pend-due] (deadline filter failed)", reclaimIDs)
	}
}

func TestOneShotDueIDSBoundedByBatchSize(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.BatchSize = 2
	for _, id := range []string{"m-batch-1", "m-batch-2", "m-batch-3"} {
		insertOneShotMarker(t, db, id, OneShotStateRunning, nil)
	}
	ids, err := service.oneShotDueIDs(context.Background(), []string{OneShotStateRunning}, false)
	if err != nil {
		t.Fatal(err)
	}
	// All running rows share a NULL retention_until, so ties order by id.
	if len(ids) != 2 || ids[0] != "m-batch-1" || ids[1] != "m-batch-2" {
		t.Fatalf("bounded batch = %v, want [m-batch-1 m-batch-2]", ids)
	}
}

// oneShotFixtureFile is one seeded artifact file of a fixture chain.
type oneShotFixtureFile struct {
	id  string
	ref string
}

// createOneShotChain seeds one complete temporary owner graph for markerID:
// managed anime -> subscription -> RSS source -> release -> download task ->
// one verified artifact with len(fileRefs) artifact files. Anchor ids follow
// the insertOneShotMarker convention ("anime-"+markerID, ...). All rows insert
// in one transaction because the release/download-task foreign-key pair is
// deferred to commit time.
func createOneShotChain(t *testing.T, db *sql.DB, markerID string, fileRefs []string) []oneShotFixtureFile {
	t.Helper()
	ctx := context.Background()
	animeID, subID, srcID := "anime-"+markerID, "sub-"+markerID, "src-"+markerID
	relID, taskID, artifactID := "rel-"+markerID, "task-"+markerID, "art-"+markerID
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(name, query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed one-shot chain %s (%s): %v", markerID, name, err)
		}
	}
	exec("managed_anime", `INSERT INTO managed_anime(id, title) VALUES ($1, 'One Shot Fixture')`, animeID)
	exec("subscription", `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, 'custom', 'one-shot retention fixture', 'active')`, subID, animeID)
	exec("rss_source", `INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', 'https://feeds.example.com/one-shot.xml', 'https://feeds.example.com/one-shot.xml', 'One Shot', 'One Shot Fixture')`, srcID, subID)
	exec("release", `INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code, download_task_id)
VALUES ($1, $2, $3, '{}'::jsonb, '{}'::jsonb, 'submitted', 'one_shot_fixture_submitted', $4)`, relID, srcID, "one-shot:"+markerID, taskID)
	manifestFiles := []any{}
	for ordinal, ref := range fileRefs {
		manifestFiles = append(manifestFiles, map[string]any{"file_ref": ref, "ordinal": ordinal, "relative_path": ref, "exact_size": 1, "media_kind": "video", "identity_evidence": map[string]any{}})
	}
	manifest, err := json.Marshal(manifestFiles)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-"+markerID, taskID, relID, "idem-task-"+markerID, `{"one_shot":true}`, `{}`, "completed", `null`, string(manifest))
	exec("artifact", `INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, (SELECT acquisition_id FROM download_tasks WHERE id=$2), '{}'::jsonb, '{}'::jsonb, 'verified', CURRENT_TIMESTAMP)`, artifactID, taskID)
	files := make([]oneShotFixtureFile, 0, len(fileRefs))
	for _, ref := range fileRefs {
		fileID := "af-" + ref
		exec("artifact_file", `INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), $3, $3, $4, 'video', 1, '{}'::jsonb, 'not_applicable')`, fileID, artifactID, ref, "/downloads/"+ref)
		files = append(files, oneShotFixtureFile{id: fileID, ref: ref})
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit one-shot chain %s: %v", markerID, err)
	}
	return files
}

// insertOneShotFileOperation seeds one completed file operation over a fixture
// artifact file, stamped with the given media correlation id.
func insertOneShotFileOperation(t *testing.T, db *sql.DB, foID, artifactID string, file oneShotFixtureFile, correlationID string) {
	t.Helper()
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	var exists bool
	if err := owner.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM file_operations WHERE id=$1)`, foID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		return
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,file_ref,source_path,size_bytes,fingerprint FROM artifact_files WHERE artifact_id=$1 ORDER BY file_ref COLLATE "C"`, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	ops := []acquisitionfixture.PlanOperation{}
	for rows.Next() {
		var id, ref, source string
		var size int64
		var fp json.RawMessage
		if err := rows.Scan(&id, &ref, &source, &size, &fp); err != nil {
			t.Fatal(err)
		}
		opID := foID
		if id != file.id {
			opID = "fo-a-" + strings.TrimPrefix(artifactID, "art-")
		}
		ops = append(ops, acquisitionfixture.PlanOperation{ID: opID, FileID: id, FileRef: ref, Source: source, Target: "/library/" + ref, Operation: "copy", Key: "idem-fo-" + opID, Size: size, Fingerprint: fp, Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	acquisitionfixture.CommitOperations(t, tx, artifactID, ops)
	if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint,correlation_id=$2 WHERE artifact_id=$1`, artifactID, correlationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id IN(SELECT id FROM file_operations WHERE artifact_id=$1)`, artifactID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func oneShotMarkerTerminalState(t *testing.T, db *sql.DB, markerID string) (state, outcome, reasonCode string) {
	t.Helper()
	ctx := context.Background()
	if err := db.QueryRowContext(ctx, `
SELECT state, terminal_outcome, terminal_reason_code
FROM one_shot_acquisitions WHERE id = $1`, markerID).Scan(&state, &outcome, &reasonCode); err != nil {
		t.Fatal(err)
	}
	return state, outcome, reasonCode
}

// Regression (rc.9 production failure): loadAnchorsTx scanned PostgreSQL
// array_agg values straight into []string, but the pgx database/sql driver
// delivers arrays as strings — every complete chain failed with
// "unsupported Scan, storing driver.Value type string into type *[]string"
// and landed in cleanup_blocked/anchor_validation_failed. The enumeration now
// round-trips through jsonb text; this test pins exact anchor ids and the
// ORDER BY id ordering across reversed insertion order.
func TestLoadAnchorsTxEnumeratesAnchorsWithOrdering(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-anchor-scan"
	files := createOneShotChain(t, db, markerID, []string{"bbb-scan.mkv", "aaa-scan.mkv"})
	insertOneShotMarker(t, db, markerID, OneShotStateRunning, nil)
	insertOneShotFileOperation(t, db, "fo-b-"+markerID, "art-"+markerID, files[0], "corr-"+markerID)
	insertOneShotFileOperation(t, db, "fo-a-"+markerID, "art-"+markerID, files[1], "corr-"+markerID)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	graph, err := service.loadAnchorsTx(ctx, tx, markerID)
	if err != nil {
		t.Fatalf("loadAnchorsTx: %v", err)
	}
	wantAnime := "anime-" + markerID
	wantSub := "sub-" + markerID
	wantSource := "src-" + markerID
	wantRelease := "rel-" + markerID
	wantTask := "task-" + markerID
	if graph.animeID != wantAnime || graph.subscriptionID != wantSub ||
		graph.rssSourceID != wantSource || graph.releaseID != wantRelease ||
		graph.downloadTaskID != wantTask {
		t.Fatalf("owner anchors = (%s, %s, %s, %s, %s), want (%s, %s, %s, %s, %s)",
			graph.animeID, graph.subscriptionID, graph.rssSourceID, graph.releaseID, graph.downloadTaskID,
			wantAnime, wantSub, wantSource, wantRelease, wantTask)
	}
	wantArtifacts := []string{"art-" + markerID}
	if !reflect.DeepEqual(graph.artifactIDs, wantArtifacts) {
		t.Fatalf("artifact anchors = %v, want %v", graph.artifactIDs, wantArtifacts)
	}
	wantFileOps := []string{"fo-a-" + markerID, "fo-b-" + markerID}
	if !reflect.DeepEqual(graph.fileOpIDs, wantFileOps) {
		t.Fatalf("file operation anchors = %v, want %v (ordering broken)", graph.fileOpIDs, wantFileOps)
	}
}

// Regression: a complete terminal one-shot chain must drain through RunPass to
// reclaim_pending with terminal outcome succeeded instead of blocking on the
// anchor scan conversion.
func TestRunPassDrainsCompleteTerminalChainToReclaimPending(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-drain-full"
	files := createOneShotChain(t, db, markerID, []string{"full.mkv"})
	failOneShotFileOperation(t, db, "fo-"+markerID, "art-"+markerID, files[0], "/library/full.mkv")
	insertOneShotMarker(t, db, markerID, OneShotStateRunning, nil)
	settleOneShotRoot(t, db, markerID)

	result, err := service.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if result.Drained != 1 || result.Blocked != 0 {
		t.Fatalf("RunPass result = %+v, want drained=1 blocked=0", result)
	}
	state, outcome, reasonCode := oneShotMarkerTerminalState(t, db, markerID)
	if state != OneShotStateReclaimPending || outcome != "failed" || reasonCode != "one_shot_failed" {
		t.Fatalf("marker = (%s, %s, %s), want (reclaim_pending, failed, one_shot_failed)", state, outcome, reasonCode)
	}
}

// The attempts model: the drain terminalizes the current attempt in the
// same transaction as the marker projection, so the immutable per-attempt
// history cannot drift from the marker's compatibility projection.
func TestRunPassTerminalizesCurrentAttemptWithMarkerProjection(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-drain-attempt"
	files := createOneShotChain(t, db, markerID, []string{"attempt.mkv"})
	failOneShotFileOperation(t, db, "fo-"+markerID, "art-"+markerID, files[0], "/library/attempt.mkv")
	insertOneShotMarker(t, db, markerID, OneShotStateRunning, nil)
	settleOneShotRoot(t, db, markerID)
	// The admission path creates the attempt row with the marker; direct test
	// seeding reproduces exactly that pair.
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO one_shot_execution_attempts(id, acquisition_id, sequence, mode, input_snapshot, input_digest, state)
VALUES ('attempt-'||$1, $1, 1, 'initial', '{}'::jsonb, 'sha256:'||repeat('a',64), 'running')`, markerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
UPDATE one_shot_acquisitions SET current_attempt_id = 'attempt-'||$1 WHERE id = $1`, markerID); err != nil {
		t.Fatal(err)
	}

	result, err := service.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if result.Drained != 1 || result.Blocked != 0 {
		t.Fatalf("RunPass result = %+v, want drained=1 blocked=0", result)
	}
	state, outcome, reasonCode := oneShotMarkerTerminalState(t, db, markerID)
	if state != OneShotStateReclaimPending || outcome != "failed" || reasonCode != "one_shot_failed" {
		t.Fatalf("marker = (%s, %s, %s), want (reclaim_pending, failed, one_shot_failed)", state, outcome, reasonCode)
	}
	var attemptState, outcomeCode string
	var terminalAt *time.Time
	if err := db.QueryRowContext(context.Background(), `
SELECT a.state, a.outcome_code, a.terminal_at
FROM one_shot_execution_attempts a
JOIN one_shot_acquisitions m ON m.current_attempt_id = a.id
WHERE m.id = $1 AND a.sequence = 1`, markerID).Scan(&attemptState, &outcomeCode, &terminalAt); err != nil {
		t.Fatalf("read current attempt: %v", err)
	}
	if attemptState != "failed" || outcomeCode != "one_shot_failed" || terminalAt == nil {
		t.Fatalf("attempt = (%s, %s, %v), want terminal failed evidence", attemptState, outcomeCode, terminalAt)
	}
}

// The empty anchor set must COALESCE to an empty JSON list and still drain;
// before the jsonb rewrite this hit the same pgx array scan failure.
func TestRunPassDrainsChainWithoutFileOperations(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-drain-nofileops"
	createOneShotChain(t, db, markerID, []string{"solo.mkv"})
	insertOneShotMarker(t, db, markerID, OneShotStateRunning, nil)
	if _, err := workItemFixtureDB(t).Exec(`
INSERT INTO postprocessing_plans(id, artifact_id, acquisition_id, artifact_version, state, outcome_code, outcome_reason, input_digest)
VALUES ('plan-'||$1, 'art-'||$1, 'acq-'||$1, 1, 'failed', 'plan_failed', 'fixture planning failure', 'sha256:'||repeat('b',64))`, markerID); err != nil {
		t.Fatal(err)
	}
	settleOneShotRoot(t, db, markerID)

	result, err := service.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if result.Drained != 1 || result.Blocked != 0 {
		t.Fatalf("RunPass result = %+v, want drained=1 blocked=0", result)
	}
	state, outcome, reasonCode := oneShotMarkerTerminalState(t, db, markerID)
	if state != OneShotStateReclaimPending || outcome != "failed" || reasonCode != "one_shot_failed" {
		t.Fatalf("marker = (%s, %s, %s), want (reclaim_pending, failed, one_shot_failed)", state, outcome, reasonCode)
	}
}

// Fail-closed guard: a correlation-matched file operation anchored to a
// foreign chain's artifact must still block with anchor_validation_failed
// after the jsonb rewrite (the enumeration disagreement check survives).
func TestDrainOneBlocksWhenCorrelationAndArtifactAnchorsDisagree(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerA = "m-disagree-a"
	const markerB = "m-disagree-b"
	createOneShotChain(t, db, markerA, []string{"a.mkv"})
	insertOneShotMarker(t, db, markerA, OneShotStateRunning, nil)
	filesB := createOneShotChain(t, db, markerB, []string{"b.mkv"})
	insertOneShotMarker(t, db, markerB, OneShotStateRunning, nil)
	// Chain B's file operation carries chain A's media correlation but hangs
	// off chain B's artifact file only.
	insertOneShotFileOperation(t, db, "fo-foreign", "art-"+markerB, filesB[0], "corr-"+markerA)

	processed, err := service.drainOne(context.Background(), markerA)
	if err != nil {
		t.Fatalf("drainOne: %v", err)
	}
	if processed != "blocked" {
		t.Fatalf("drainOne = %q, want blocked", processed)
	}
	state, _, reasonCode := oneShotMarkerTerminalState(t, db, markerA)
	if state != OneShotStateCleanupBlocked || reasonCode != "anchor_validation_failed" {
		t.Fatalf("marker = (%s, %s), want (cleanup_blocked, anchor_validation_failed)", state, reasonCode)
	}
}

// Equal cardinality is not enough for the two anchor paths: a foreign file
// operation may replace the real one while both lists still contain one row.
func TestDrainOneBlocksWhenEqualSizedFileOperationAnchorsDiffer(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerA = "m-equal-size-a"
	const markerB = "m-equal-size-b"
	filesA := createOneShotChain(t, db, markerA, []string{"a-equal.mkv"})
	insertOneShotMarker(t, db, markerA, OneShotStateRunning, nil)
	filesB := createOneShotChain(t, db, markerB, []string{"b-equal.mkv"})
	insertOneShotMarker(t, db, markerB, OneShotStateRunning, nil)

	// The marker A correlation selects this row, while its artifact-file anchor
	// belongs only to chain B. Both SQL queries therefore return one ID, but not
	// the same ID.
	insertOneShotFileOperation(t, db, "fo-equal-foreign", "art-"+markerB, filesB[0], "corr-"+markerA)
	insertOneShotFileOperation(t, db, "fo-equal-real", "art-"+markerA, filesA[0], "corr-unrelated")

	processed, err := service.drainOne(context.Background(), markerA)
	if err != nil {
		t.Fatalf("drainOne: %v", err)
	}
	if processed != "blocked" {
		t.Fatalf("drainOne = %q, want blocked", processed)
	}
	state, _, reasonCode := oneShotMarkerTerminalState(t, db, markerA)
	if state != OneShotStateCleanupBlocked || reasonCode != "anchor_validation_failed" {
		t.Fatalf("marker = (%s, %s), want (cleanup_blocked, anchor_validation_failed)", state, reasonCode)
	}
}

// demoteAnchoredArtifact moves the chain's single artifact out of its terminal
// verified state (and clears verified_at per the table CHECK) so
// assertOwnersSettledTx must fail while anchors still resolve exactly.
func demoteAnchoredArtifact(t *testing.T, db *sql.DB, markerID string) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE artifacts SET state = 'pending', verified_at = NULL WHERE id = $1`, "art-"+markerID); err != nil {
		t.Fatalf("demote anchored artifact %s: %v", markerID, err)
	}
}

// demoteAnchoredFileOperation moves the chain's single file operation back to
// a non-terminal state. Updating state alone does not re-run the
// verified-artifact trigger (it watches artifact identity columns only).
func demoteAnchoredFileOperation(t *testing.T, db *sql.DB, foID string) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE file_operations SET state = 'running' WHERE id = $1`, foID); err != nil {
		t.Fatalf("demote file operation %s: %v", foID, err)
	}
}

// seedOneShotPlacementSubgraph seeds the placement subgraph admission writes
// for a verified one-shot video: one placement keyed by its file operation,
// one coverage row, and one bound-subtitle companion. The reclaim package
// cannot call filemaintenance (that package imports workitems), so the shape
// is written directly; the columns mirror AdmitFromFileOperationResult plus
// RegisterCompanions.
func seedOneShotPlacementSubgraph(t *testing.T, db *sql.DB, markerID, foID, targetPath string) {
	t.Helper()
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	placementID := "pl-" + markerID
	if _, err := tx.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
    source_fingerprint, target_fingerprint, facts_version, verification_state, state)
VALUES ($1, $2, $3, 'file_operation', $4, $5, $5, 'fixture-v1', 'verified', 'active')`,
		placementID, targetPath, "anime-"+markerID, foID, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("seed placement: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, 'tv:e01', 0, 'fixture-v1', $2)`, placementID, foID); err != nil {
		t.Fatalf("seed placement coverage: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration,
    media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ($1, $2, $3, $4, 'intake', 'subtitle', '.zh', 'ass', $5, 42, $6)`,
		"pc-"+markerID, placementID, "anime-"+markerID, "companion:"+markerID,
		filepath.Join(filepath.Dir(targetPath), "preserve.zh.ass"), "meta:size=42"); err != nil {
		t.Fatalf("seed placement companion: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countOneShotOwnerGraphRows(t *testing.T, db *sql.DB, markerID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `
SELECT
    (SELECT COUNT(*) FROM managed_anime WHERE id = $1)
  + (SELECT COUNT(*) FROM subscriptions WHERE id = $2)
  + (SELECT COUNT(*) FROM rss_sources WHERE id = $3)
  + (SELECT COUNT(*) FROM releases WHERE id = $4)
  + (SELECT COUNT(*) FROM download_tasks WHERE id = $5)
  + (SELECT COUNT(*) FROM artifacts WHERE download_task_id = $5)
  + (SELECT COUNT(*) FROM artifact_files WHERE artifact_id = $6)
  + (SELECT COUNT(*) FROM file_operations WHERE artifact_id = $6)
  + (SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1)
  + (SELECT COUNT(*) FROM placement_coverage WHERE placement_id IN (SELECT id FROM placements WHERE managed_anime_id = $1))
  + (SELECT COUNT(*) FROM placement_companions WHERE managed_anime_id = $1)`,
		"anime-"+markerID, "sub-"+markerID, "src-"+markerID, "rel-"+markerID,
		"task-"+markerID, "art-"+markerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func readOneShotDeadline(t *testing.T, db *sql.DB, markerID string) time.Time {
	t.Helper()
	var deadline time.Time
	if err := db.QueryRowContext(context.Background(), `
SELECT retention_until FROM one_shot_acquisitions WHERE id = $1`, markerID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	return deadline
}

// Contract: terminal one-shot reclamation deletes ONLY the temporary database
// owner graph, and only after the root settled and its detail retention ended.
// The media-library target and the external download data keep their exact
// bytes; the reclamation owner has no filesystem or qBittorrent capability at
// all, so "preserved" is structural, not a business delete.
//
// G1: the root settles through the real T1 settlement (its one required file
// fails through the formal file failure command). While detail_until has not
// passed the reclaim is retained and changes nothing. The reclaim then runs
// with its evaluation instant at detail_until + 1s (the clock seam; no row is
// back-dated) and compacts the root with trigger detail_expired before
// deleting the temporary owner graph.
func TestReclaimDeletesTemporaryGraphAndPreservesMediaAndDownloadData(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-reclaim-preserve"
	ctx := context.Background()

	// A real media-library target and an external download-data file on disk.
	libraryRoot := t.TempDir()
	downloadRoot := t.TempDir()
	targetPath := filepath.Join(libraryRoot, "Show", "preserve.mkv")
	downloadPath := filepath.Join(downloadRoot, "preserve.mkv")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	targetBytes := []byte("verified-media-target-bytes")
	downloadBytes := []byte("external-qb-download-data-bytes")
	if err := os.WriteFile(targetPath, targetBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(downloadPath, downloadBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	files := createOneShotChain(t, db, markerID, []string{"preserve.mkv"})
	foID := "fo-" + markerID
	failOneShotFileOperation(t, db, foID, "art-"+markerID, files[0], targetPath)
	// The placement admission of that video wrote its Placement with coverage
	// and registered the bound subtitle as a companion of the temporary owner.
	// None of these rows may block the reclaim; all of them belong to the
	// temporary owner graph and must be gone afterwards.
	seedOneShotPlacementSubgraph(t, db, markerID, foID, targetPath)
	root := "acq-" + markerID
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle one-shot root: status=%q err=%v", status, err)
	}
	var state string
	var detailUntil time.Time
	if err := db.QueryRowContext(ctx, `SELECT state, detail_until FROM acquisitions WHERE id=$1`, root).Scan(&state, &detailUntil); err != nil || state != "failed" {
		t.Fatalf("settled root state=%q err=%v", state, err)
	}

	insertOneShotMarker(t, db, markerID, OneShotStateReclaimPending, oneShotReclaimAt(time.Now().Add(-time.Hour)))
	if got := countOneShotOwnerGraphRows(t, db, markerID); got != 11 {
		t.Fatalf("seed owner graph rows = %d, want 11", got)
	}

	// Detail retention has not ended: retained, nothing written.
	before := acquisitionfixture.Snapshot(t, disposabledb.Owner(t))
	for attempt := 0; attempt < 2; attempt++ {
		reclaimed, err := service.reclaimOne(ctx, markerID)
		if err != nil || reclaimed {
			t.Fatalf("retained root reclaim=%v err=%v", reclaimed, err)
		}
		if acquisitionfixture.Snapshot(t, disposabledb.Owner(t)) != before {
			t.Fatal("retained reclaim changed root/marker/epoch/retention/ledger")
		}
	}
	assertOneShotFileBytes(t, targetPath, targetBytes, downloadPath, downloadBytes)

	service.Now = func() time.Time { return detailUntil.Add(time.Second) }
	reclaimed, err := service.reclaimOne(ctx, markerID)
	if err != nil {
		t.Fatalf("reclaimOne: %v", err)
	}
	if !reclaimed {
		t.Fatal("a complete terminal chain must reclaim once its detail retention ended")
	}
	if state, _, _ := oneShotMarkerTerminalState(t, db, markerID); state != OneShotStateCleaned {
		t.Fatalf("marker state = %s, want cleaned", state)
	}
	if got := countOneShotOwnerGraphRows(t, db, markerID); got != 0 {
		t.Fatalf("temporary owner graph rows = %d, want 0 after leaf-first reclamation", got)
	}
	for _, check := range []struct{ table, query string }{
		{"placements", `SELECT count(*) FROM placements WHERE managed_anime_id=$1`},
		{"placement_coverage", `SELECT count(*) FROM placement_coverage WHERE placement_id IN (SELECT id FROM placements WHERE managed_anime_id=$1)`},
		{"placement_companions", `SELECT count(*) FROM placement_companions WHERE managed_anime_id=$1`},
	} {
		var rows int
		if err := db.QueryRowContext(ctx, check.query, "anime-"+markerID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s rows of the temporary owner = %d, want 0", check.table, rows)
		}
	}
	var record, trigger string
	if err := db.QueryRowContext(ctx, `SELECT state, record_state, compact_trigger FROM acquisitions WHERE id=$1`, root).Scan(&state, &record, &trigger); err != nil {
		t.Fatalf("root must outlive the reclaim: %v", err)
	}
	if state != "failed" || record != "compact" || trigger != "detail_expired" {
		t.Fatalf("root after reclaim = %s/%s/%s, want failed/compact/detail_expired", state, record, trigger)
	}
	assertOneShotFileBytes(t, targetPath, targetBytes, downloadPath, downloadBytes)
}

// TestOneShotOwnerTableClassificationMatchesSchema keeps the reclaim's table
// classification honest as a closed set: every public base table carrying a
// managed_anime_id column belongs to exactly one of three lists — reclaim
// deletes it, it can never hold a temporary owner row, or it is known to be
// able to hold one and deliberately blocks the reclaim. A new owner table in
// none of them is a classification defect, because the reclaim would block on
// it forever without anyone having decided that.
func TestOneShotOwnerTableClassificationMatchesSchema(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	column := func(table string) (exists, hasAnime bool) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT
  EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1 AND table_type='BASE TABLE'),
  EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name='managed_anime_id')`,
			table).Scan(&exists, &hasAnime); err != nil {
			t.Fatal(err)
		}
		return exists, hasAnime
	}
	category := map[string]string{}
	for name, tables := range map[string][]string{
		"classified":     oneShotClassifiedOwnerTables,
		"never-owner":    oneShotNeverOwnerTables,
		"known-blocking": oneShotKnownBlockingTables,
	} {
		for _, table := range tables {
			exists, hasAnime := column(table)
			if !exists || !hasAnime {
				t.Fatalf("%s owner table %s: exists=%v managed_anime_id=%v", name, table, exists, hasAnime)
			}
			if previous, ok := category[table]; ok {
				t.Fatalf("owner table %s appears in both %s and %s; a table has exactly one disposition", table, previous, name)
			}
			category[table] = name
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT c.table_name FROM information_schema.columns c
JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name AND t.table_type='BASE TABLE'
WHERE c.table_schema='public' AND c.column_name='managed_anime_id'
ORDER BY c.table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		seen++
		if _, ok := category[table]; !ok {
			t.Fatalf("表 %s 带 managed_anime_id 却未被分类：要么加进 oneShotClassifiedOwnerTables 并在 deleteTemporaryOwnerGraphTx 删除，要么证明它永不持有临时 owner 行", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("schema introspection found no managed_anime_id owner table; the closed-set check would be vacuous")
	}
	for _, table := range oneShotIndependentTables {
		if exists, hasAnime := column(table); !exists || hasAnime {
			t.Fatalf("anime-independent table %s: exists=%v managed_anime_id=%v", table, exists, hasAnime)
		}
	}
}

// assertOneShotFileBytes is the never-delete-media guard of the reclaim.
func assertOneShotFileBytes(t *testing.T, targetPath string, targetBytes []byte, downloadPath string, downloadBytes []byte) {
	t.Helper()
	preservedTarget, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("media-library target must remain: %v", err)
	}
	if !bytes.Equal(preservedTarget, targetBytes) {
		t.Fatalf("media-library target bytes changed: got=%q want=%q", preservedTarget, targetBytes)
	}
	preservedDownload, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("external download data must remain: %v", err)
	}
	if !bytes.Equal(preservedDownload, downloadBytes) {
		t.Fatalf("external download data bytes changed: got=%q want=%q", preservedDownload, downloadBytes)
	}
}

// failOneShotFileOperation commits the fixture chain's single operation and
// fails it through the formal file failure command: the file_execute work is
// claimed as the runtime and finished failed, which records the required
// file's typed failed delivery result.
func failOneShotFileOperation(t *testing.T, db *sql.DB, foID, artifactID string, file oneShotFixtureFile, target string) {
	t.Helper()
	ctx := context.Background()
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var source string
	var size int64
	var fp json.RawMessage
	if err := tx.QueryRowContext(ctx, `SELECT source_path,size_bytes,fingerprint FROM artifact_files WHERE id=$1`, file.id).Scan(&source, &size, &fp); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.CommitOperations(t, tx, artifactID, []acquisitionfixture.PlanOperation{{ID: foID, FileID: file.id, FileRef: file.ref, Source: source, Target: target, Operation: "copy", Key: "idem-fo-" + foID, Size: size, Fingerprint: fp, Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var work string
	var token int64
	if err := db.QueryRowContext(ctx, `SELECT id,fencing_token FROM work_item_claim_due(60::double precision,ARRAY['file_execute']::text[])`).Scan(&work, &token); err != nil {
		t.Fatalf("claim file_execute: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT file_finish_failed($1,$2,$3,'FIXTURE_FILE_FAILED','fixture file failure')`, foID, work, token); err != nil {
		t.Fatalf("finish file failed: %v", err)
	}
}

func settleOneShotRoot(t *testing.T, db *sql.DB, markerID string) {
	t.Helper()
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(context.Background(), "acq-"+markerID); err != nil || status != "settled" {
		t.Fatalf("settle one-shot root %s: status=%q err=%v", markerID, status, err)
	}
}

// Contract: a cleanup_blocked marker whose anchored Artifact or FileOperation
// is still non-terminal must never pass a recheck — even with an empty ledger
// and no live WorkItem — and the owner graph must survive. Only after every
// owner is terminal again does the next recheck return the marker to
// reclaim_pending.
func TestRecheckOneBlocksOnNonTerminalOwnersAndRecovers(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-recheck-owners"
	files := createOneShotChain(t, db, markerID, []string{"recheck.mkv"})
	foID := "fo-" + markerID
	failOneShotFileOperation(t, db, foID, "art-"+markerID, files[0], "/library/recheck.mkv")
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(context.Background(), "acq-"+markerID); err != nil || status != "settled" {
		t.Fatalf("settle recheck root: status=%q err=%v", status, err)
	}
	insertOneShotMarker(t, db, markerID, OneShotStateCleanupBlocked, oneShotReclaimAt(time.Now().Add(-time.Hour)))
	if _, err := db.Exec(`UPDATE one_shot_acquisitions
SET terminal_outcome='failed',terminal_reason_code='chain_unsettled',terminal_reason='fixture owner still settling'
WHERE id=$1`, markerID); err != nil {
		t.Fatal(err)
	}

	before := readOneShotDeadline(t, db, markerID)

	for _, tc := range []struct {
		name   string
		demote func()
	}{
		{name: "artifact_pending", demote: func() { demoteAnchoredArtifact(t, db, markerID) }},
		{name: "file_operation_running", demote: func() { demoteAnchoredFileOperation(t, db, foID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE one_shot_acquisitions SET retention_until=CURRENT_TIMESTAMP-INTERVAL '1 hour' WHERE id=$1`, markerID); err != nil {
				t.Fatal(err)
			}
			tc.demote()

			status, err := service.recheckOne(context.Background(), markerID)
			if err != nil {
				t.Fatalf("recheckOne: %v", err)
			}
			if status != "extended" {
				t.Fatalf("recheck status = %q, want extended while an anchored owner is non-terminal", status)
			}
			state, _, _ := oneShotMarkerTerminalState(t, db, markerID)
			if state != OneShotStateCleanupBlocked {
				t.Fatalf("marker state = %s, want cleanup_blocked", state)
			}
			if got := countOneShotOwnerGraphRows(t, db, markerID); got != 8 {
				t.Fatalf("owner graph rows = %d, want 8 (owner graph must not be deleted)", got)
			}
			if after := readOneShotDeadline(t, db, markerID); !after.After(before) {
				t.Fatalf("failed recheck must extend the deadline: before=%v after=%v", before, after)
			}
		})
	}

	// Repair both owners to their terminal states; only then may the recheck
	// pass the complete terminal-chain proof.
	if _, err := db.Exec(`UPDATE artifacts SET state = 'verified', verified_at = CURRENT_TIMESTAMP WHERE id = $1`, "art-"+markerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE file_operations SET state = 'failed' WHERE id = $1`, foID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE one_shot_acquisitions SET retention_until=CURRENT_TIMESTAMP-INTERVAL '1 hour' WHERE id=$1`, markerID); err != nil {
		t.Fatal(err)
	}
	status, err := service.recheckOne(context.Background(), markerID)
	if err != nil {
		t.Fatalf("recheckOne after repair: %v", err)
	}
	if status != "passed" {
		t.Fatalf("recheck status = %q, want passed with all owners terminal", status)
	}
	state, outcome, reasonCode := oneShotMarkerTerminalState(t, db, markerID)
	if state != OneShotStateReclaimPending || outcome != "failed" || reasonCode != "chain_unsettled" {
		t.Fatalf("marker = (%s, %s, %s), want (reclaim_pending, failed, chain_unsettled)", state, outcome, reasonCode)
	}
}

// Contract: reclaim_pending reclamation with a non-terminal anchored owner and
// an independently running root is retained before the terminal-chain proof;
// the marker and whole temporary owner graph survive untouched.
func TestReclaimOneBlocksOnNonTerminalOwners(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const markerID = "m-reclaim-owners"
	files := createOneShotChain(t, db, markerID, []string{"reclaim.mkv"})
	foID := "fo-" + markerID
	insertOneShotFileOperation(t, db, foID, "art-"+markerID, files[0], "corr-"+markerID)
	insertOneShotMarker(t, db, markerID, OneShotStateReclaimPending, oneShotReclaimAt(time.Now().Add(-time.Hour)))

	for _, tc := range []struct {
		name   string
		demote func()
	}{
		{name: "artifact_pending", demote: func() { demoteAnchoredArtifact(t, db, markerID) }},
		{name: "file_operation_running", demote: func() { demoteAnchoredFileOperation(t, db, foID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The previous subtest may have blocked this shared marker; reset it
			// to a due reclaim_pending row for each scenario.
			if _, err := db.Exec(`
UPDATE one_shot_acquisitions
SET state = $2, terminal_outcome = 'failed', terminal_reason_code = 'test_transition',
    terminal_reason = 'test transition', terminal_at = CURRENT_TIMESTAMP,
    retention_until = CURRENT_TIMESTAMP - INTERVAL '1 hour',
    active_effect_leases = '[]'::jsonb, active_effect_count = 0
WHERE id = $1`, markerID, OneShotStateReclaimPending); err != nil {
				t.Fatalf("reset marker to %s: %v", OneShotStateReclaimPending, err)
			}
			tc.demote()
			before := acquisitionfixture.Snapshot(t, disposabledb.Owner(t))

			reclaimed, err := service.reclaimOne(context.Background(), markerID)
			if err != nil {
				t.Fatalf("reclaimOne: %v", err)
			}
			if reclaimed {
				t.Fatal("reclamation must not proceed while an anchored owner is non-terminal")
			}
			// Root detail retention precedes the owner-terminal proof, so this
			// running-root shape remains pending without even a marker write.
			state, _, _ := oneShotMarkerTerminalState(t, db, markerID)
			if acquisitionfixture.Snapshot(t, disposabledb.Owner(t)) != before {
				t.Fatal("running-root waiting changed graph")
			}
			if state != OneShotStateReclaimPending {
				t.Fatalf("marker state = %s, want reclaim_pending for independent running root", state)
			}
			if got := countOneShotOwnerGraphRows(t, db, markerID); got != 8 {
				t.Fatalf("owner graph rows = %d, want 8 (owner graph must not be deleted)", got)
			}
		})
	}
}

// Reclaim must never turn live work into cleanup_blocked: either an owner that
// still looks non-terminal or a terminal chain with live work retains the
// reclaim_pending marker and the complete graph until normal work settles.
func TestReclaimOneKeepsLiveChainPending(t *testing.T) {
	for _, tc := range []struct {
		name           string
		demoteArtifact bool
	}{
		{name: "nonterminal_owner", demoteArtifact: true},
		{name: "terminal_chain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			service, err := NewOneShotReclaimService(db, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			markerID := "m-reclaim-live-" + tc.name
			files := createOneShotChain(t, db, markerID, []string{"live.mkv"})
			failOneShotFileOperation(t, db, "fo-"+markerID, "art-"+markerID, files[0], "/library/live.mkv")
			insertOneShotMarker(t, db, markerID, OneShotStateReclaimPending, oneShotReclaimAt(time.Now().Add(-time.Hour)))
			settleOneShotRoot(t, db, markerID)
			service.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
			if tc.demoteArtifact {
				demoteAnchoredArtifact(t, db, markerID)
			}
			store := &Store{DB: db, RetentionDays: DefaultRetentionDays}
			if err := store.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := store.Create(ctx, tx, WorkItemSpec{
					Owner:                OwnerRef{Type: OwnerRelease, ID: "rel-" + markerID},
					Kind:                 KindReleaseEvaluate,
					IdempotencyKey:       "live-reclaim-guard-" + markerID,
					MaxAttempts:          3,
					ExpectedOwnerVersion: 1,
				})
				return err
			}); err != nil {
				t.Fatalf("seed live chain work: %v", err)
			}
			before := acquisitionfixture.Snapshot(t, disposabledb.Owner(t))
			reclaimed, err := service.reclaimOne(ctx, markerID)
			if err != nil {
				t.Fatalf("reclaimOne with live work: %v", err)
			}
			if reclaimed {
				t.Fatal("live chain was reclaimed")
			}
			if after := acquisitionfixture.Snapshot(t, disposabledb.Owner(t)); after != before {
				t.Fatal("live-chain reclamation guard mutated the graph")
			}
			state, _, _ := oneShotMarkerTerminalState(t, db, markerID)
			if state != OneShotStateReclaimPending {
				t.Fatalf("marker state = %s, want reclaim_pending", state)
			}
		})
	}
}

// Outcome aggregation derives the failed stage from the anchored chain
// in the read projection's order (file_execution -> planning -> artifact ->
// download), and the drained attempt carries that stage as immutable evidence.
func TestRunPassAggregatesFailedStageFromAnchoredChain(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	type scenario struct {
		markerID string
		mutate   func(markerID, artifactID string, files []oneShotFixtureFile)
		want     string
	}
	scenarios := []scenario{
		{
			markerID: "m-stage-download",
			mutate: func(markerID, _ string, _ []oneShotFixtureFile) {
				if _, err := db.ExecContext(ctx, `
UPDATE download_tasks SET state = 'failed', failure_code = 'qB_SUBMIT_FAILED', failure_reason = 'download failed'
WHERE id = $1`, "task-"+markerID); err != nil {
					t.Fatal(err)
				}
			},
			want: "download",
		},
		{
			markerID: "m-stage-artifact",
			mutate: func(markerID, artifactID string, _ []oneShotFixtureFile) {
				if _, err := db.ExecContext(ctx, `
UPDATE artifacts SET state = 'failed', verified_at = NULL, failure_code = 'ARTIFACT_FAILED', failure_reason = 'verify failed'
WHERE id = $1`, artifactID); err != nil {
					t.Fatal(err)
				}
			},
			want: "artifact",
		},
		{
			markerID: "m-stage-planning",
			mutate: func(markerID, artifactID string, _ []oneShotFixtureFile) {
				if _, err := workItemFixtureDB(t).ExecContext(ctx, `
INSERT INTO postprocessing_plans(id, artifact_id, acquisition_id, artifact_version, state, outcome_code, outcome_reason, input_digest)
VALUES ('plan-'||$1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), 1, 'failed', 'plan_failed', 'naming identity missing', 'sha256:'||repeat('c',64))`, markerID, artifactID); err != nil {
					t.Fatal(err)
				}
			},
			want: "planning",
		},
		{
			markerID: "m-stage-fileexec",
			mutate: func(markerID, artifactID string, files []oneShotFixtureFile) {
				failOneShotFileOperation(t, db, "fo-"+markerID, artifactID, files[0], "/library/"+markerID+".mkv")
			},
			want: "file_execution",
		},
	}

	for _, testCase := range scenarios {
		files := createOneShotChain(t, db, testCase.markerID, []string{testCase.markerID + ".mkv"})
		insertOneShotMarker(t, db, testCase.markerID, OneShotStateRunning, nil)
		if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_execution_attempts(id, acquisition_id, sequence, mode, input_snapshot, input_digest, state)
VALUES ('attempt-'||$1, $1, 1, 'initial', '{}'::jsonb, 'sha256:'||repeat('a',64), 'running')`, testCase.markerID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
UPDATE one_shot_acquisitions SET current_attempt_id = 'attempt-'||$1 WHERE id = $1`, testCase.markerID); err != nil {
			t.Fatal(err)
		}
		testCase.mutate(testCase.markerID, "art-"+testCase.markerID, files)
		settleOneShotRoot(t, db, testCase.markerID)
	}

	if _, err := service.RunPass(ctx); err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	for _, testCase := range scenarios {
		state, outcome, _ := oneShotMarkerTerminalState(t, db, testCase.markerID)
		if state != OneShotStateReclaimPending || outcome != "failed" {
			t.Fatalf("%s marker = (%s, %s), want (reclaim_pending, failed)", testCase.markerID, state, outcome)
		}
		var failedStage sql.NullString
		if err := db.QueryRowContext(ctx, `
SELECT a.failed_stage
FROM one_shot_execution_attempts a
JOIN one_shot_acquisitions m ON m.current_attempt_id = a.id
WHERE m.id = $1 AND a.sequence = 1`, testCase.markerID).Scan(&failedStage); err != nil {
			t.Fatalf("read %s attempt: %v", testCase.markerID, err)
		}
		if !failedStage.Valid || failedStage.String != testCase.want {
			t.Fatalf("%s failed_stage = %v, want %s", testCase.markerID, failedStage, testCase.want)
		}
	}
}

// Marker purge removes the acquisition's attempt summaries in the same
// statement: the bounded attempt evidence is retained exactly until purge and
// would otherwise outlive its acquisition forever (no cascading FK).
func TestPurgeCleanedOneShotMarkerRemovesItsAttempts(t *testing.T) {
	db := openTestDB(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const markerID = "m-purge-attempts"
	insertOneShotMarker(t, db, markerID, OneShotStateCleaned, oneShotReclaimAt(time.Now().Add(-time.Hour)))
	if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_execution_attempts(id, acquisition_id, sequence, mode, input_snapshot, input_digest, state, outcome_code, outcome_reason, terminal_at)
VALUES ('attempt-'||$1, $1, 1, 'initial', '{}'::jsonb, 'sha256:'||repeat('a',64), 'failed',
        'one_shot_failed', 'failed', CURRENT_TIMESTAMP)`, markerID); err != nil {
		t.Fatal(err)
	}

	result, err := service.RunPass(ctx)
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if result.Purged != 1 {
		t.Fatalf("purged = %d, want 1", result.Purged)
	}
	var markers, attempts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_acquisitions WHERE id = $1`, markerID).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_execution_attempts WHERE acquisition_id = $1`, markerID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if markers != 0 || attempts != 0 {
		t.Fatalf("after purge markers=%d attempts=%d, want both removed", markers, attempts)
	}
}
