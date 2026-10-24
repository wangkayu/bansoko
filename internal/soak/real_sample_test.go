//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"bansoko/internal/testutil/acquisitionfixture"
)

// realSample accepts RealSample downloads through the owner-held accept entry
// (accept_acquisition_queued_with_vars via the shared acquisition fixture, the
// same path the application tests use), then adds the artifact, files, plan
// commit and completed file operations on top. Ruling 2: the shape of these
// rows is the reference the directly written graph is compared with.
func realSample(t *testing.T, owner *sql.DB, p Profile) {
	t.Helper()
	ctx := context.Background()
	repeat64 := strings.Repeat("a", 64)
	for i := 1; i <= p.RealSample; i++ {
		n := 1 + (i*7)%p.Owners
		sourceID := fmt.Sprintf("r%05d-0", n)
		releaseID := fmt.Sprintf("real-rl-%03d", i)
		taskID := fmt.Sprintf("real-dt-%03d", i)
		artifactID := fmt.Sprintf("real-af-%03d", i)
		rootID := fmt.Sprintf("real-acq-%03d", i)
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		title := fmt.Sprintf("[Real] Soak Anime %d - %02d [WebRip 1080p]", n, i)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ($1, $2, $3, jsonb_build_object('title', $4::text), '{}', jsonb_build_object('episode', $5::int), 'accepted_pending_download', 'accepted')`,
			releaseID, sourceID, "guid:"+releaseID, title, i); err != nil {
			t.Fatalf("real sample release: %v", err)
		}
		files := []map[string]any{}
		for f := 0; f < 2; f++ {
			files = append(files, map[string]any{"file_ref": fmt.Sprintf("file-%d", f), "ordinal": f, "relative_path": fmt.Sprintf("real/%d-%d.mkv", i, f),
				"exact_size": 1, "media_kind": "video", "identity_evidence": map[string]any{}})
		}
		manifest, err := json.Marshal(files)
		if err != nil {
			t.Fatal(err)
		}
		acquisitionfixture.SeedObservedDownloadManifest(t, tx, rootID, taskID, releaseID, "real-key-"+releaseID, `{}`, `{}`, "completed", `null`, string(manifest))
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id=$1, verdict='submitted' WHERE id=$2`, taskID, releaseID); err != nil {
			t.Fatalf("real sample link: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
SELECT $1, id, acquisition_id, '{}', '{"fingerprints":[]}', 'verified', CURRENT_TIMESTAMP FROM download_tasks WHERE id=$2`, artifactID, taskID); err != nil {
			t.Fatalf("real sample artifact: %v", err)
		}
		fingerprint := `{"algorithm":"sha256","value":"` + repeat64 + `","size_bytes":1}`
		var ops []acquisitionfixture.PlanOperation
		for f := 0; f < 2; f++ {
			fileID := fmt.Sprintf("real-ff-%03d-%d", i, f)
			rel := fmt.Sprintf("real/%d-%d.mkv", i, f)
			if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
SELECT $1, $2, acquisition_id, $3, $4, $5, 'video', 1, $6::jsonb, '{}', 'not_applicable' FROM artifacts WHERE id=$2`,
				fileID, artifactID, fmt.Sprintf("file-%d", f), rel, "/downloads/"+rel, fingerprint); err != nil {
				t.Fatalf("real sample file: %v", err)
			}
			ops = append(ops, acquisitionfixture.PlanOperation{
				ID: fmt.Sprintf("real-fo-%03d-%d", i, f), FileID: fileID, FileRef: fmt.Sprintf("file-%d", f), Source: "/downloads/" + rel,
				Target: fmt.Sprintf("%s/Real/%d-%d.mkv", libraryRootPath(), i, f), Operation: "copy", Key: fmt.Sprintf("real-op-key:%s:%d", releaseID, f),
				Size: 1, Fingerprint: json.RawMessage(fingerprint), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
			})
		}
		acquisitionfixture.CommitOperations(t, tx, artifactID, ops)
		for _, op := range ops {
			if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET state='completed', target_fingerprint=source_fingerprint, observed_at=CURRENT_TIMESTAMP WHERE id=$1`, op.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE acquisition_id=$1`, rootID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("real sample commit: %v", err)
		}
	}
	t.Logf("real accept path: %d acquisitions accepted through accept_acquisition_queued_with_vars", p.RealSample)
}
