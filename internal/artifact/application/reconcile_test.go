package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/deployment"
	"bansoko/internal/download/domain"
)

// seedReconcileArtifactFiles inserts the primary artifact file record used by
// the recovery settlement.
func insertPrimaryArtifactFile(t *testing.T, db *sql.DB, artifactID, sourcePath string, size int64, fingerprintJSON string) {
	t.Helper()
	stmt := `INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('reconcile-primary', $1, (SELECT acquisition_id FROM artifacts WHERE id=$1), 'primary', 'show.mkv', $2, 'video', $3, $4, '{}'::jsonb, 'not_applicable')`
	if _, err := db.Exec(stmt, artifactID, sourcePath, size, fingerprintJSON); err != nil {
		t.Fatalf("insert primary artifact file: %v", err)
	}
}

func writeReconcileFile(t *testing.T, content string) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "show.mkv")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, int64(len(content))
}

func artifactFingerprintJSON(value string, size int64) string {
	return fmt.Sprintf(`{"algorithm":"metadata","value":"%s","size_bytes":%d}`, value, size)
}

func TestReconcileArtifactSettlesCommittedObservation(t *testing.T) {
	db := openArtifactTestDB(t)
	path, size := writeReconcileFile(t, "artifact-content")
	correlation, _ := json.Marshal(map[string]any{
		"artifact_path":  path,
		"files_complete": true,
		"files": []map[string]any{{
			"path": path, "name": "show.mkv", "size_bytes": size, "media_kind": "video",
		}},
	})
	seedArtifactOwner(t, db, "artifact-rec", "task-rec", "completed", "pending", string(correlation), "{}")
	insertPrimaryArtifactFile(t, db, "artifact-rec", path, size, artifactFingerprintJSON(fmt.Sprintf("meta:size=%d", size), size))

	claim, _ := claimArtifactWorkItem(t, db, "artifact-rec", []byte(`{}`))
	service, err := NewService(db, &fakeInspector{byPath: map[string]domain.ArtifactFacts{
		path: {Path: path, SizeBytes: size},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "op-1", WindowID: "win-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateCompleted || result.Effect != deployment.EffectNone || result.EvidenceRef != claim.ID {
		t.Fatalf("expected settled present, got %+v", result)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM artifacts WHERE id = 'artifact-rec'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("artifact must settle verified, got %q", state)
	}
}

func TestReconcileArtifactFingerprintChangedIsConflict(t *testing.T) {
	db := openArtifactTestDB(t)
	path, size := writeReconcileFile(t, "artifact-content")
	correlation, _ := json.Marshal(map[string]any{
		"artifact_path":  path,
		"files_complete": true,
		"files": []map[string]any{{
			"path": path, "name": "show.mkv", "size_bytes": size, "media_kind": "video",
		}},
	})
	seedArtifactOwner(t, db, "artifact-conf", "task-conf", "completed", "pending", string(correlation), "{}")
	// Frozen fingerprint names another file of the same size.
	insertPrimaryArtifactFile(t, db, "artifact-conf", path, size, artifactFingerprintJSON(fmt.Sprintf("meta:size=%d;dev=1;ino=1", size), size))

	service, err := NewService(db, &fakeInspector{byPath: map[string]domain.ArtifactFacts{
		path: {Path: path, SizeBytes: size},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "op-1", WindowID: "win-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateBlocked || result.Effect != deployment.EffectNone {
		t.Fatalf("changed fingerprint must be conflict, got %+v", result)
	}
}

func TestReconcileArtifactMissingFileIsNoEffect(t *testing.T) {
	db := openArtifactTestDB(t)
	path := filepath.Join(t.TempDir(), "show.mkv") // never created
	correlation, _ := json.Marshal(map[string]any{
		"artifact_path":  path,
		"files_complete": true,
		"files": []map[string]any{{
			"path": path, "name": "show.mkv", "size_bytes": 13, "media_kind": "video",
		}},
	})
	seedArtifactOwner(t, db, "artifact-none", "task-none", "completed", "pending", string(correlation), "{}")

	service, err := NewService(db, &fakeInspector{err: os.ErrNotExist})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "op-1", WindowID: "win-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateBlocked || result.Effect != deployment.EffectNone {
		t.Fatalf("missing file must be no-effect completed, got %+v", result)
	}
}
