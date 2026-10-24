package migrations

import (
	"encoding/json"
	"testing"
)

func inspectionSnapshotFixture(t *testing.T, files []map[string]any) (string, string) {
	t.Helper()
	observed := make([]map[string]any, 0, len(files))
	indexed := map[string]any{}
	fingerprints := make([]map[string]any, 0, len(files))
	for _, file := range files {
		entry := map[string]any{}
		for key, value := range file {
			if key != "id" {
				entry[key] = value
			}
		}
		observed = append(observed, entry)
		ref := file["file_ref"].(string)
		indexed[ref] = file["fingerprint"]
		fingerprints = append(fingerprints, map[string]any{"file_ref": ref, "relative_path": file["relative_path"], "source_path": file["source_path"], "fingerprint": file["fingerprint"]})
	}
	inventory := map[string]any{"schema_version": "artifact-inspect/v1", "primary_file_ref": files[0]["file_ref"], "inspected_primary_file": observed[0], "files": observed, "inspected_files": observed}
	fp := map[string]any{"schema_version": "artifact-fingerprint/v1", "primary_file_ref": files[0]["file_ref"], "files": indexed, "inspected_files": fingerprints}
	a, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(fp)
	if err != nil {
		t.Fatal(err)
	}
	return string(a), string(b)
}
