package application

import (
	"testing"
)

func bindingManifestFixture(t *testing.T, files ...map[string]any) (dbPolicy func(map[string]any)) {
	return func(p map[string]any) {
		rows := []map[string]any{{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}}}
		for i, f := range files {
			f["ordinal"] = i + 1
			f["identity_evidence"] = map[string]any{}
			rows = append(rows, f)
		}
		p["fixture_manifest"] = rows
	}
}
