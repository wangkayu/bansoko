package facts

import (
	"encoding/json"
	"testing"

	"bansoko/internal/filekinds"
	persistence "bansoko/internal/release/facts/adapter/persistence/media"
)

// The file kind list version is frozen with the release facts and survives the
// adapter mapping; dropping it would let a later product list change the
// classification of an already evaluated release.
func TestReleaseFactsFreezeTheFileKindsVersion(t *testing.T) {
	raw, err := json.Marshal(ReleaseFacts{Title: "Show", FileKindsVersion: filekinds.Version})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		FileKindsVersion string `json:"file_kinds_version"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.FileKindsVersion != filekinds.Version {
		t.Fatalf("frozen release facts = %s (%v)", raw, err)
	}
	adapter, err := json.Marshal(persistence.ReleaseFacts{Title: "Show", FileKindsVersion: filekinds.Version})
	if err != nil {
		t.Fatal(err)
	}
	if string(adapter) != string(raw) {
		t.Fatalf("adapter shape drifted: %s vs %s", adapter, raw)
	}
}
