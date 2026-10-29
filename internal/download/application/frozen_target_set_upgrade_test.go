package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// A download task admitted before migration 101 embedded every target spec and
// observation in frozen_input.target_facts. The migration rewrites it to the
// reference form (admission, set digest, target count) and the queued task is
// then submitted from the rewritten row exactly as before: the submit freezes
// its path snapshot against the stored input and reaches downloading.
func TestMigration101RewrittenTaskStillSubmits(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-m101", "queued", "null")
	owner := ownerTestDB(t)
	const preUpgrade = `{
  "admission": "download_required",
  "reason_code": null,
  "reason": null,
  "target_set_digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
  "targets": [
    {"id": "target-a", "target_ref": "a", "source_path": "file-a.mkv", "render_snapshot": {"vars": {"input": {"files": [{"name": "file-a.mkv"}]}}}},
    {"id": "target-b", "target_ref": "b", "source_path": "file-b.mkv", "render_snapshot": {"vars": {"input": {"files": [{"name": "file-a.mkv"}]}}}}
  ],
  "observations": [
    {"target_id": "target-a", "state": "missing"},
    {"target_id": "target-b", "state": "missing"}
  ]
}`
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET frozen_input = jsonb_set(frozen_input, '{target_facts}', $2::jsonb) WHERE id = $1`, "task-qb-m101", preUpgrade); err != nil {
		t.Fatalf("write the pre-upgrade target facts: %v", err)
	}
	// The pre-upgrade selected_release also carried a copy of the release's
	// internal facts, its file list included.
	var selectedBefore string
	if err := owner.QueryRowContext(ctx, `SELECT COALESCE(frozen_input->'selected_release', '{}'::jsonb)::text FROM download_tasks WHERE id = $1`, "task-qb-m101").Scan(&selectedBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
UPDATE download_tasks
SET frozen_input = jsonb_set(frozen_input, '{selected_release}', $2::jsonb || jsonb_build_object('internal_facts', jsonb_build_object('release_files', jsonb_build_array(jsonb_build_object('source_path', 'file-a.mkv')))))
WHERE id = $1`, "task-qb-m101", selectedBefore); err != nil {
		t.Fatalf("write the pre-upgrade selected release: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "migrations", "101_frozen_target_set_reference.sql"))
	if err != nil {
		t.Fatalf("read migration 101: %v", err)
	}
	if _, err := owner.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("apply migration 101: %v", err)
	}
	const reference = `{
  "admission": "download_required",
  "reason_code": null,
  "reason": null,
  "target_set_digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
  "target_count": 2
}`
	var rewritten bool
	if err := owner.QueryRowContext(ctx, `SELECT frozen_input->'target_facts' = $2::jsonb FROM download_tasks WHERE id = $1`, "task-qb-m101", reference).Scan(&rewritten); err != nil {
		t.Fatal(err)
	}
	if !rewritten {
		t.Fatal("migration 101 did not rewrite the task's target facts to the reference form")
	}
	var selectedKept bool
	if err := owner.QueryRowContext(ctx, `SELECT frozen_input->'selected_release' = $2::jsonb FROM download_tasks WHERE id = $1`, "task-qb-m101", selectedBefore).Scan(&selectedKept); err != nil {
		t.Fatal(err)
	}
	if !selectedKept {
		t.Fatal("migration 101 did not drop the selected release's copy of the internal facts")
	}

	provider := &fakeProvider{
		lookupFound: true,
		lookupTask:  domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", State: domain.TaskDownloading, Progress: 0.4},
	}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-m101", workitems.KindQBSubmit)
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted {
		t.Fatalf("submit outcome after migration 101 = %+v", outcome)
	}
	var state string
	var frozen []byte
	if err := db.QueryRowContext(ctx, `SELECT state, frozen_input FROM download_tasks WHERE id = $1`, "task-qb-m101").Scan(&state, &frozen); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		TargetFacts  json.RawMessage `json:"target_facts"`
		PathSnapshot json.RawMessage `json:"path_snapshot"`
	}
	if err := json.Unmarshal(frozen, &stored); err != nil {
		t.Fatal(err)
	}
	if state != "downloading" || len(stored.PathSnapshot) == 0 {
		t.Fatalf("task after submit: state=%q path snapshot frozen=%t", state, len(stored.PathSnapshot) > 0)
	}
	var kept bool
	if err := db.QueryRowContext(ctx, `SELECT $1::jsonb = $2::jsonb`, string(stored.TargetFacts), reference).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Fatal("the submit changed the rewritten target facts")
	}
}
