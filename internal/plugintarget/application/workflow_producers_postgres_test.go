package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
)

// NF2 database-side producers (migration 331): download_failed fires once per
// task on its transition to failed; unknown_files_imported fires once per
// acquisition on settlement when at least one unknown file was released to
// the library. Both admissions resolve into deliveries through the normal
// resolver, which checks the payload against the trigger's codec.

func createWorkflowTarget(t *testing.T, service *Service, key string, triggers ...TriggerKind) {
	t.Helper()
	input := validCreateInput(t)
	input.Name = "workflow-" + key
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfig(t, "none")
	input.Enabled = true
	input.TriggerKinds = triggers
	input.Secret = SecretInput{Mode: "set", Value: json.RawMessage(workflowWebhookSecret(false))}
	if _, err := service.CreateTarget(context.Background(), input, "workflow-target-"+key); err != nil {
		t.Fatal(err)
	}
}

// seedWorkflowOwner inserts one active anime with a custom RSS source and a
// release, the chain a download needs.
func seedWorkflowOwner(t *testing.T, tx *sql.Tx, suffix string) (animeID, releaseID string) {
	t.Helper()
	ctx := context.Background()
	animeID, releaseID = "anime-"+suffix, "release-"+suffix
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO managed_anime(id, title) VALUES ($1, 'Workflow Show')`, []any{animeID}},
		{`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ($1, $2, 'custom', $1, 'active')`, []any{"sub-" + suffix, animeID}},
		{`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ($1, $2, 'custom_rss', 'https://feeds.example.test/'||$1, 'feeds.example.test', 'Workflow Group', 'Workflow Show')`, []any{"source-" + suffix, "sub-" + suffix}},
		{`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ($1, $2, $1, '{}', '{}', 'accepted_pending_download', 'accepted_pending_download')`, []any{releaseID, "source-" + suffix}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", statement.query, err)
		}
	}
	return animeID, releaseID
}

func admissionFacts(t *testing.T, db *sql.DB, trigger string) (count int, ownerID, resultKey string, facts map[string]any) {
	t.Helper()
	var payload []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*), COALESCE(MIN(source_owner_id), ''), COALESCE(MIN(result_key), ''), COALESCE((array_agg(payload))[1], '{}'::jsonb)
FROM notification_admissions WHERE trigger_kind = $1`, trigger).Scan(&count, &ownerID, &resultKey, &payload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &facts); err != nil {
		t.Fatal(err)
	}
	return count, ownerID, resultKey, facts
}

func resolveWorkflowAdmissions(t *testing.T, service *Service, trigger string) int {
	t.Helper()
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	if _, err := resolver.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var deliveries int
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugin_deliveries WHERE trigger_kind = $1 AND delivery_kind = 'business'`, trigger).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	return deliveries
}

func TestDownloadFailedNoticeOncePerTask(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	createWorkflowTarget(t, service, "download-failed", TriggerDownloadFailed)

	owner := disposabledb.Owner(t)
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseID := seedWorkflowOwner(t, tx, "dlfail")
	acquisitionfixture.SeedObservedDownload(t, tx, "root-dlfail", "task-dlfail", releaseID, "dlfail-task-key", `{}`, `{}`, "downloading", `null`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'task-dlfail', verdict = 'submitted', reason_code = 'submitted' WHERE id = $1`, releaseID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count, _, _, _ := admissionFacts(t, db, "download_failed"); count != 0 {
		t.Fatalf("a running download admitted %d notices", count)
	}

	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='failed', failure_code='DOWNLOAD_STALLED', failure_reason='fixture', version=version+1 WHERE id='task-dlfail'`); err != nil {
		t.Fatal(err)
	}
	// A later write to the failed task is not a second failure.
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET failure_reason='fixture again', version=version+1 WHERE id='task-dlfail'`); err != nil {
		t.Fatal(err)
	}
	count, ownerID, resultKey, facts := admissionFacts(t, db, "download_failed")
	if count != 1 || ownerID != "task-dlfail" || resultKey == "" {
		t.Fatalf("admissions=%d owner=%s key=%q, want one for the task", count, ownerID, resultKey)
	}
	// The fixture root is a one-shot acquisition, which the RSS page cannot
	// retry, so the notice predicts no retry.
	if facts["result.outcome"] != "failure" || facts["result.category"] != "stalled" || facts["result.retryable"] != false ||
		facts["release.src"] != "custom_rss" || facts["release.id"] != releaseID {
		t.Fatalf("download_failed facts=%v", facts)
	}
	for key := range facts {
		if key == "failure_code" || key == "failure_reason" {
			t.Fatalf("raw failure field %s leaked into the notice: %v", key, facts)
		}
	}
	if deliveries := resolveWorkflowAdmissions(t, service, "download_failed"); deliveries != 1 {
		t.Fatalf("download_failed deliveries=%d, want 1", deliveries)
	}
}

func TestDownloadFailureCategoryMapsTypedCodes(t *testing.T) {
	db := openTestDB(t)
	for code, want := range map[string]string{
		"DOWNLOAD_STALLED":                 "stalled",
		"DOWNLOAD_PROGRESS_UNVERIFIABLE":   "stalled",
		"SYNC_BUDGET_EXHAUSTED":            "timeout",
		"SUBMIT_BUDGET_EXHAUSTED":          "timeout",
		"qb_provider_failed":               "client",
		"SYNC_ERROR_BUDGET_EXHAUSTED":      "unobservable",
		"DOWNLOAD_OBSERVATION_UNAVAILABLE": "unobservable",
		"QB_SUBMIT_REJECTED":               "submit",
	} {
		var got string
		if err := disposabledb.Owner(t).QueryRowContext(context.Background(), `SELECT download_failure_category($1)`, code).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s -> %s, want %s", code, got, want)
		}
	}
	_ = db
}

// seedUnknownFilesAcquisition seeds one completed download whose plan copied
// three files into the library; the first `unknown` were rendered with the
// unknown route. handoff is the placement obligation state of every file:
// "admitted" also writes the placements the sink would have written (unknown
// ones released at import), "failed" models an exhausted handoff that wrote
// none.
func seedUnknownFilesAcquisition(t *testing.T, owner *sql.DB, suffix string, unknown int, handoff string) (root string) {
	t.Helper()
	ctx := context.Background()
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	animeID, releaseID := seedWorkflowOwner(t, tx, suffix)
	root, task, artifact := "root-"+suffix, "task-"+suffix, "artifact-"+suffix
	// The fixture injects the accepted input's frozen values from its
	// frozen-policy argument, so the delivery route root — the work's tree top,
	// which every unrecognized file's reported path is relativized against at
	// settle time — is stated there.
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, root, task, releaseID, suffix+"-task-key", `{}`, `{"route_root":"/library"}`, "completed", `null`,
		`[{"file_ref":"f1","ordinal":0,"relative_path":"a.mkv","exact_size":100,"media_kind":"video","identity_evidence":{}},`+
			`{"file_ref":"f2","ordinal":1,"relative_path":"b.mkv","exact_size":222,"media_kind":"video","identity_evidence":{}},`+
			`{"file_ref":"f3","ordinal":2,"relative_path":"c.mkv","exact_size":300,"media_kind":"video","identity_evidence":{}}]`)
	exec(`UPDATE releases SET download_task_id = $1, verdict = 'submitted', reason_code = 'submitted' WHERE id = $2`, task, releaseID)
	exec(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, $3, '{}', '{}', 'verified', CURRENT_TIMESTAMP)`, artifact, task, root)
	type file struct{ ref, name, target string }
	files := []file{
		{"f1", "a.mkv", "/library/Workflow Show/extras/a.mkv"},
		{"f2", "b.mkv", "/library/Workflow Show/extras/b c.mkv"},
		{"f3", "c.mkv", "/library/Workflow Show/Season 01/c.mkv"},
	}
	sizes := map[string]int64{"f1": 100, "f2": 222, "f3": 300}
	ops := make([]acquisitionfixture.PlanOperation, 0, len(files))
	for i, f := range files {
		render := json.RawMessage(`{}`)
		if i < unknown {
			render = json.RawMessage(`{"unknown_file":true}`)
		}
		exec(`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ($1, $2, $3, $4, $5, $6, 'video', $7, '{}', 'not_applicable')`, artifact+"-"+f.ref, artifact, root, f.ref, f.name, "/downloads/"+f.name, sizes[f.ref])
		ops = append(ops, acquisitionfixture.PlanOperation{
			ID: "fo-" + suffix + "-" + f.ref, FileID: artifact + "-" + f.ref, FileRef: f.ref, Source: "/downloads/" + f.name, Target: f.target,
			Operation: "copy", Key: suffix + ":" + f.ref, Size: sizes[f.ref],
			Fingerprint: json.RawMessage(`{}`), Render: render, Config: json.RawMessage(`{}`),
		})
	}
	acquisitionfixture.CommitOperations(t, tx, artifact, ops)
	for i, f := range files {
		fingerprint := `{}`
		if f.ref == "f1" {
			fingerprint = `{"size_bytes":111}`
		}
		fo := "fo-" + suffix + "-" + f.ref
		exec(`UPDATE file_operations SET state='completed', observed_at=CURRENT_TIMESTAMP, target_fingerprint=$2::jsonb, observed_target_size_bytes=$3 WHERE id=$1`, fo, fingerprint, sizes[f.ref])
		exec(`SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id=$1`, fo)
		// The obligation fence admits only a pending start; the handoff
		// outcome is a transition from it.
		exec(`INSERT INTO placement_obligations(file_operation_id) VALUES ($1)`, fo)
		switch handoff {
		case "admitted":
			exec(`UPDATE placement_obligations SET state='admitted', attempts=1, settled_at=CURRENT_TIMESTAMP WHERE file_operation_id=$1`, fo)
			insertPlacement(t, tx, fo, f.target, animeID, i < unknown)
		case "failed":
			exec(`UPDATE placement_obligations SET state='failed', attempts=3, reason_code='placement_unknown_admission_failed', reason='placement admission failed' WHERE file_operation_id=$1`, fo)
		default:
			t.Fatalf("unknown handoff %q", handoff)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return root
}

// insertPlacement writes the placement the sink admits for one completed file
// operation; an unknown file's placement is released at import.
func insertPlacement(t *testing.T, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, fo, target, animeID string, unknown bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := tx.ExecContext(ctx, `INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, verification_state, state, replacement_eligible)
VALUES ($1, $2, $3, 'file_operation', $4, '{}', '{}', 'v1', 'verified', 'active', FALSE)`, "placement-"+fo, target, animeID, fo); err != nil {
		t.Fatal(err)
	}
	if unknown {
		if _, err := tx.ExecContext(ctx, `UPDATE placements SET state='released', released_at=CURRENT_TIMESTAMP, released_by='system:unknown_file_import', release_request_key='unknown-file:'||id WHERE id=$1`, "placement-"+fo); err != nil {
			t.Fatal(err)
		}
	}
}

func settleAcquisition(t *testing.T, owner *sql.DB, root, state string) {
	t.Helper()
	if _, err := owner.ExecContext(context.Background(), `UPDATE acquisitions SET state=$2,terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, root, state); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownFilesImportedNoticeOncePerAcquisition(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	createWorkflowTarget(t, service, "unknown-files", TriggerUnknownFilesImported)
	owner := disposabledb.Owner(t)
	root := seedUnknownFilesAcquisition(t, owner, "unknown", 2, "admitted")
	if count, _, _, _ := admissionFacts(t, db, "unknown_files_imported"); count != 0 {
		t.Fatalf("a running acquisition admitted %d notices", count)
	}
	settleAcquisition(t, owner, root, "succeeded")
	// A later write to the settled acquisition is not a second settlement.
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state=state WHERE id=$1`, root); err != nil {
		t.Fatal(err)
	}
	count, ownerID, resultKey, facts := admissionFacts(t, db, "unknown_files_imported")
	if count != 1 || ownerID != root || resultKey != "unknown_files" {
		t.Fatalf("admissions=%d owner=%s key=%s, want one for the acquisition", count, ownerID, resultKey)
	}
	files, _ := facts["out.files"].([]any)
	if facts["out.count"] != float64(2) || len(files) != 2 {
		t.Fatalf("unknown files facts=%v", facts)
	}
	wantFiles := []map[string]any{
		{"path": "Workflow Show/extras/a.mkv", "size": float64(111)},
		{"path": "Workflow Show/extras/b c.mkv", "size": float64(222)},
	}
	for i, want := range wantFiles {
		got, _ := files[i].(map[string]any)
		if got["path"] != want["path"] || got["size"] != want["size"] {
			t.Fatalf("file %d=%v, want %v", i, got, want)
		}
	}
	// The acquisition's own display title (the fixture's "fixture") wins over
	// the managed anime title.
	if facts["event.link"] != "#/?record="+root || facts["anime.title"] != "fixture" {
		t.Fatalf("link=%v title=%v", facts["event.link"], facts["anime.title"])
	}
	if deliveries := resolveWorkflowAdmissions(t, service, "unknown_files_imported"); deliveries != 1 {
		t.Fatalf("unknown_files_imported deliveries=%d, want 1", deliveries)
	}

	// A notice payload is frozen and delivered to external adapters as it
	// stands, so it carries no absolute container path and the link cannot name
	// one of the files: it opens the acquisition's execution record.
	var link string
	if err := owner.QueryRowContext(ctx, `SELECT unknown_files_notification($1, '[{"path":"Workflow Show/extras/b c.mkv","size":1}]'::jsonb)->>'event.link'`, root).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if want := "#/?record=" + root; link != want {
		t.Fatalf("single-file link=%s, want %s", link, want)
	}
}

func TestAcquisitionWithoutUnknownFilesAdmitsNothing(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	root := seedUnknownFilesAcquisition(t, owner, "known", 0, "admitted")
	settleAcquisition(t, owner, root, "succeeded")
	if count, _, _, _ := admissionFacts(t, db, "unknown_files_imported"); count != 0 {
		t.Fatalf("an acquisition without unknown files admitted %d notices", count)
	}
}

// TestUnknownFilesNoticeDoesNotWaitForPlacementHandoff pins the ordering the
// notice relies on. Live work keeps settlement waiting only while a placement
// continuation is live; an exhausted handoff (obligation failed) lets the
// acquisition settle before any placement exists, and a manual retry places
// the files later. The notice reads the completed unknown-route file
// operations, so it lists the files at settlement and the late placement
// admits nothing more.
func TestUnknownFilesNoticeDoesNotWaitForPlacementHandoff(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	createWorkflowTarget(t, service, "unknown-late", TriggerUnknownFilesImported)
	owner := disposabledb.Owner(t)
	root := seedUnknownFilesAcquisition(t, owner, "late", 2, "failed")

	var live bool
	var placements int
	if err := owner.QueryRowContext(ctx, `SELECT acquisition_live_work($1), (SELECT count(*) FROM placements p JOIN file_operations fo ON fo.id = p.source_owner_ref WHERE fo.acquisition_id = $1)`, root).Scan(&live, &placements); err != nil {
		t.Fatal(err)
	}
	if live || placements != 0 {
		t.Fatalf("live_work=%v placements=%d, want a settleable root with no placement", live, placements)
	}
	settleAcquisition(t, owner, root, "succeeded")
	count, ownerID, _, facts := admissionFacts(t, db, "unknown_files_imported")
	if count != 1 || ownerID != root || facts["out.count"] != float64(2) {
		t.Fatalf("admissions=%d owner=%s facts=%v, want one notice listing two files", count, ownerID, facts)
	}
	files, _ := facts["out.files"].([]any)
	first, _ := files[0].(map[string]any)
	if len(files) != 2 || first["path"] != "Workflow Show/extras/a.mkv" || first["size"] != float64(111) {
		t.Fatalf("unknown files=%v", files)
	}

	// The late handoff: a manual retry places both unknown files after
	// settlement. It never admits a second notice.
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, f := range []struct{ ref, target string }{
		{"f1", "/library/Workflow Show/extras/a.mkv"},
		{"f2", "/library/Workflow Show/extras/b c.mkv"},
	} {
		fo := "fo-late-" + f.ref
		insertPlacement(t, tx, fo, f.target, "anime-late", true)
		if _, err := tx.ExecContext(ctx, `UPDATE placement_obligations SET state='admitted', reason_code=NULL, reason=NULL, settled_at=CURRENT_TIMESTAMP WHERE file_operation_id=$1`, fo); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count, _, _, _ := admissionFacts(t, db, "unknown_files_imported"); count != 1 {
		t.Fatalf("the late placement changed the notice count to %d", count)
	}
	if deliveries := resolveWorkflowAdmissions(t, service, "unknown_files_imported"); deliveries != 1 {
		t.Fatalf("unknown_files_imported deliveries=%d, want 1", deliveries)
	}
}
