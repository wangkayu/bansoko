package migrations

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// These tests prove the acquisition root columns on the execution graph: every
// linked row is pinned to the same root as its parent, a file operation names
// exactly one required requirement file, and the work item owner matrix
// carries the acquisition shapes while every other shape asserts both root
// columns stay consistent.

// seedAcquiredChain seeds a download-branch acquisition root named "acq-"+tag
// through the real queued accept (release chain, root, input, task and first
// work items), marks its task completed and, when withArtifact, adds a verified
// artifact with one file plus the root's requirement set: file-a required and
// file-b excluded. It returns the root id. Download tasks can only belong to a
// download-branch root, so the accept path is the one consistent way to build
// the graph.
func seedAcquiredChain(t *testing.T, db *sql.DB, tag string, withArtifact bool) string {
	t.Helper()
	ctx := context.Background()
	seedAcceptableRelease(t, db, tag)
	params := validQueued(t, "acq-"+tag, "key-"+tag, tag)
	if _, err := callQueuedAccept(t, db, params); err != nil {
		t.Fatalf("accept the download root: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, params.TaskID); err != nil {
		t.Fatalf("complete the task: %v", err)
	}
	if !withArtifact {
		return params.AcquisitionID
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, $3, '{}', '{}', 'verified', CURRENT_TIMESTAMP)`, []any{"art-" + tag, params.TaskID, params.AcquisitionID}},
		{`INSERT INTO artifact_files
(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ($1, $2, $3, 'ep.mkv', 'ep.mkv', $4, 'video', 0, '{"sha256":"fp"}'::jsonb, 'not_applicable')`,
			[]any{"af-" + tag, "art-" + tag, params.AcquisitionID, "/downloads/" + tag + "/ep.mkv"}},
		// A file operation belongs to the root's postprocessing plan.
		{`INSERT INTO postprocessing_plans(id, artifact_id, acquisition_id, artifact_version, state, input_digest)
VALUES ($1, $2, $3, 1, 'queued', $4)`, []any{"plan-" + tag, "art-" + tag, params.AcquisitionID, acqDigest("6")}},
		// The requirement header is the root's own columns (PRUNE-046 S4); its
		// files reference each other through deferred keys, so they commit
		// together with the manifest files the accept wrote.
		{`UPDATE acquisitions SET requirements_manifest_digest=$2, requirements_selection_digest=$3, requirements_evidence_digest=$3,
 requirements_digest=$3, requirements_item_count=2, requirements_required_count=1, requirements_sealed_at=CURRENT_TIMESTAMP
WHERE id=$1`, []any{params.AcquisitionID, params.ManifestDigest, acqDigest("5")}},
		{`UPDATE acquisition_files SET requirement='required', requirement_reason_code='selected', selection_evidence='{}'
WHERE acquisition_id=$1 AND file_ref='file-a'`, []any{params.AcquisitionID}},
		{`UPDATE acquisition_files SET requirement='excluded', requirement_reason_code='not_selected', selection_evidence='{}'
WHERE acquisition_id=$1 AND file_ref='file-b'`, []any{params.AcquisitionID}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed artifact graph: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}
	return params.AcquisitionID
}

// seedCompletedFileOperation seeds an adopted-root chain with an artifact and
// one completed file operation on the root's required file, and returns the
// operation id's root. The Release and DownloadTask owners are mutually
// deferred foreign keys, which seedAcquiredChain commits together.
func seedCompletedFileOperation(t *testing.T, db *sql.DB, tag, operationID string) {
	t.Helper()
	root := seedAcquiredChain(t, db, tag, true)
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO file_operations
(postprocessing_plan_id, file_ready_at, file_wall_until, file_max_attempts, file_attempt_timeout_seconds, id, artifact_id, artifact_version, artifact_file_id, acquisition_id, file_ref, operation,
 source_path, target_path, source_fingerprint, target_fingerprint, collision_policy,
 frozen_render_snapshot, frozen_config_snapshot, expected_size_bytes, observed_target_size_bytes,
 state, observed_at, idempotency_key)
VALUES ('plan-`+tag+`', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60, $1, 'art-`+tag+`', 1, 'af-`+tag+`', '`+root+`', 'file-a', 'copy',
 '/downloads/`+tag+`/ep.mkv', '/library/ep.mkv', '{"sha256":"fp"}'::jsonb, '{"sha256":"fp"}'::jsonb, 'version',
 '{}', '{}', 100, 100, 'completed', CURRENT_TIMESTAMP, $1)`, operationID); err != nil {
		t.Fatalf("seed completed file operation: %v", err)
	}
}

func TestAcquisitionRootColumns(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	for table, columns := range map[string][]string{
		"download_tasks":  {"acquisition_id"},
		"artifacts":       {"acquisition_id"},
		"artifact_files":  {"acquisition_id"},
		"file_operations": {"acquisition_id", "file_ref", "required_tag"},
		"work_items":      {"acquisition_id"},
	} {
		actual, err := tableColumns(ctx, db, table)
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		for _, column := range columns {
			if _, ok := actual[column]; !ok {
				t.Fatalf("required column %s.%s is missing", table, column)
			}
		}
	}

	seedAcquiredChain(t, db, "base", true)

	// The same-root chain links task, artifact, file and operation to one root;
	// its requirement set marks file-a required and file-b excluded.
	seedAcquiredChain(t, db, "rooted", true)
	// A second root owns a bare task, so cross-root artifact links face a task
	// that has no artifact yet (download_task_id is UNIQUE on artifacts).
	seedAcquiredChain(t, db, "bare", false)
	if _, err := db.ExecContext(ctx, `
INSERT INTO file_operations
(postprocessing_plan_id, file_ready_at, file_wall_until, file_max_attempts, file_attempt_timeout_seconds, id, artifact_id, artifact_version, artifact_file_id, acquisition_id, file_ref, operation,
 source_path, target_path, source_fingerprint, target_fingerprint, collision_policy,
 frozen_render_snapshot, frozen_config_snapshot, state, observed_at, idempotency_key)
VALUES ('plan-rooted', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60, 'op-rooted', 'art-rooted', 1, 'af-rooted', 'acq-rooted', 'file-a', 'copy',
 '/downloads/rooted/ep.mkv', '/library/ep.mkv', '{"sha256":"fp"}'::jsonb, '{"sha256":"fp"}'::jsonb,
 'version', '{}', '{}', 'completed', CURRENT_TIMESTAMP, 'key-op-rooted')`); err != nil {
		t.Fatalf("a same-root operation on a required file must insert: %v", err)
	}

	// One root has exactly one download task. The insert runs inside a
	// transaction with its release so the expected failure rolls both back.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-dup', 'src-rooted', 'key-dup', '{}', '{}', 'discovered', 'new')`); err != nil {
		t.Fatalf("seed the duplicate's release: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO download_tasks(id, release_id, acquisition_id, frozen_input, frozen_policy, state, idempotency_key)
VALUES ('dt-dup', 'rel-dup', 'acq-rooted', '{}', '{}', 'queued', 'dt-dup')`); err == nil {
		t.Error("a second download task for the same root must be rejected")
	} else if code := sqlState(err); code != "23505" {
		t.Errorf("duplicate root task: SQLSTATE %q, want 23505 (%v)", code, err)
	}
	_ = tx.Rollback()

	// A child linked to a different root than its parent is rejected, and an
	// operation cannot name an excluded requirement file.
	for _, tc := range []struct {
		name, statement string
	}{
		{"artifact under another root", `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state)
VALUES ('art-cross', 'dt-acq-bare', 'acq-rooted', '{}', '{}', 'pending')`},
		{"artifact file under another root", `
INSERT INTO artifact_files
(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ('af-cross', 'art-rooted', 'acq-bare', 'x.mkv', 'x.mkv', '/downloads/rooted/x.mkv', 'video', 0, '{"sha256":"fp"}'::jsonb, 'not_applicable')`},
		{"operation under another root", `
INSERT INTO file_operations
(postprocessing_plan_id, file_ready_at, file_wall_until, file_max_attempts, file_attempt_timeout_seconds, id, artifact_id, artifact_version, artifact_file_id, acquisition_id, file_ref, operation,
 source_path, target_path, source_fingerprint, collision_policy, frozen_render_snapshot, frozen_config_snapshot,
 state, idempotency_key)
VALUES ('plan-rooted', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60, 'op-cross', 'art-rooted', 1, 'af-rooted', 'acq-bare', 'file-a', 'copy',
 '/downloads/rooted/ep.mkv', '/library/x.mkv', '{"sha256":"fp"}'::jsonb,
 'version', '{}', '{}', 'pending', 'key-op-cross')`},
		{"operation on an excluded file", `
INSERT INTO file_operations
(postprocessing_plan_id, file_ready_at, file_wall_until, file_max_attempts, file_attempt_timeout_seconds, id, artifact_id, artifact_version, artifact_file_id, acquisition_id, file_ref, operation,
 source_path, target_path, source_fingerprint, collision_policy, frozen_render_snapshot, frozen_config_snapshot,
 state, idempotency_key)
VALUES ('plan-rooted', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60, 'op-excluded', 'art-rooted', 1, 'af-rooted', 'acq-rooted', 'file-b', 'copy',
 '/downloads/rooted/ep.mkv', '/library/y.mkv', '{"sha256":"fp"}'::jsonb,
 'version', '{}', '{}', 'pending', 'key-op-excluded')`},
	} {
		if _, err := db.ExecContext(ctx, tc.statement); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		} else if code := sqlState(err); code != "23503" {
			t.Errorf("%s: SQLSTATE %q, want 23503 (%v)", tc.name, code, err)
		}
	}

	// The root and the requirement file reference are both mandatory.
	for _, tc := range []struct {
		name, statement string
	}{
		{"operation without a file_ref", `
INSERT INTO file_operations
(postprocessing_plan_id, file_ready_at, file_wall_until, file_max_attempts, file_attempt_timeout_seconds, id, artifact_id, artifact_version, artifact_file_id, acquisition_id, operation,
 source_path, target_path, source_fingerprint, collision_policy, frozen_render_snapshot, frozen_config_snapshot,
 state, idempotency_key)
VALUES ('plan-rooted', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60, 'op-noref', 'art-rooted', 1, 'af-rooted', 'acq-rooted', 'copy',
 '/downloads/rooted/ep.mkv', '/library/z.mkv', '{"sha256":"fp"}'::jsonb,
 'version', '{}', '{}', 'pending', 'key-op-noref')`},
		{"operation without a root", `
INSERT INTO file_operations
(postprocessing_plan_id, file_ready_at, file_wall_until, file_max_attempts, file_attempt_timeout_seconds, id, artifact_id, artifact_version, artifact_file_id, file_ref, operation,
 source_path, target_path, source_fingerprint, collision_policy, frozen_render_snapshot, frozen_config_snapshot,
 state, idempotency_key)
VALUES ('plan-base', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60, 'op-noroot', 'art-base', 1, 'af-base', 'file-a', 'copy',
 '/downloads/base/ep.mkv', '/library/w.mkv', '{"sha256":"fp"}'::jsonb,
 'version', '{}', '{}', 'pending', 'key-op-noroot')`},
	} {
		if _, err := db.ExecContext(ctx, tc.statement); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		} else if code := sqlState(err); code != "23502" {
			t.Errorf("%s: SQLSTATE %q, want 23502 (%v)", tc.name, code, err)
		}
	}
}

func TestAcquisitionWorkItemOwnerShapes(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	// The accept guard is gone with PRUNE-046 S4; the work item owner shapes
	// below need bare roots, not an accepted graph.
	now := time.Now().UTC()
	for _, id := range []string{"acq-settle", "acq-other"} {
		if err := insertAcq(db, validAcq(id, "key-"+id, now)); err != nil {
			t.Fatalf("seed root %s: %v", id, err)
		}
	}
	// Seeds the rss source runtime owner used by the root-carrying refresh case.
	_ = seedAcquiredChain(t, db, "owned", true)

	// The settle item is owned by the acquisition root itself.
	if _, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-settle', 'acquisition', 'acq-settle', 'acq-settle', 'acquisition_settle', 'wi-settle', 3, '{}', 1)`); err != nil {
		t.Fatalf("the acquisition_settle shape must insert: %v", err)
	}

	// The root is derived from the owner: an omitted acquisition_id is filled in.
	var derived sql.NullString
	if err := db.QueryRowContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-derive', 'acquisition', 'acq-other', 'acquisition_settle', 'wi-derive', 3, '{}', 1) RETURNING acquisition_id`).Scan(&derived); err != nil {
		t.Fatalf("an acquisition_settle item without a caller root must insert: %v", err)
	}
	if derived.String != "acq-other" {
		t.Fatalf("derived root = %q, want acq-other", derived.String)
	}

	for _, tc := range []struct {
		name, statement, wantState string
	}{
		{"settle item whose root is not its owner", `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-bad-2', 'acquisition', 'acq-settle', 'acq-other', 'acquisition_settle', 'wi-bad-2', 3, '{}', 1)`, "23514"},
		{"a refresh item carrying a root", `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-bad-4', 'rss_source_runtime', 'src-owned', 'acq-settle', 'rss_refresh', 'wi-bad-4', 3, '{}', 1)`, "23514"},
	} {
		if _, err := db.ExecContext(ctx, tc.statement); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		} else if code := sqlState(err); code != tc.wantState {
			t.Errorf("%s: SQLSTATE %q, want %s (%v)", tc.name, code, tc.wantState, err)
		}
	}
}
