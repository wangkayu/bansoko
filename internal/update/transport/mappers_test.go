package transport

import (
	"encoding/json"
	"testing"
	"time"

	updateapplication "bansoko/internal/update/application"
)

func TestCheckPreservesHTTPShape(t *testing.T) {
	checkedAt := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	createdAt := checkedAt.Add(-time.Hour)
	updatedAt := checkedAt.Add(-time.Minute)
	targetVersion := "0.9.0"
	targetSchema := "001"
	lastError := ""
	value := updateapplication.Check{
		ActiveOperation: &updateapplication.Status{
			OperationID:         "embedded-op-1",
			Channel:             "embedded",
			Step:                "staged",
			TargetReleaseID:     "release-1",
			TargetVersion:       &targetVersion,
			TargetSchemaVersion: &targetSchema,
			LastError:           &lastError,
			CreatedAt:           createdAt,
			UpdatedAt:           updatedAt,
		},
		Candidate: &updateapplication.Candidate{
			ReleaseID:              "release-2",
			Version:                "0.9.1",
			ReleaseSequence:        8,
			PublishedAt:            checkedAt.Add(-2 * time.Hour),
			TargetMigrationVersion: "002",
			RequiredDowntime:       true,
			BackupRequired:         true,
			RollbackClass:          "restore_required",
			ManifestDigest:         "manifest-digest",
			PayloadSize:            42,
		},
		CheckedAt: checkedAt,
		Current: updateapplication.Current{
			ReleaseID:          "release-1",
			Version:            "0.9.0",
			ImageCompatibility: "base-v1",
		},
		Compatibility: updateapplication.Compatibility{
			Kind:            updateapplication.CompatibilityKind("overlay"),
			MinImageVersion: "base-v1",
			Reason:          "The candidate can be applied as an embedded overlay.",
		},
		Status: updateapplication.CheckStatus("update_available"),
	}

	got := Check(value)
	if got.Status != "update_available" || got.Current.ReleaseId != "release-1" {
		t.Fatalf("check projection = %+v", got)
	}
	if got.ActiveOperation == nil || got.ActiveOperation.LastError == nil || *got.ActiveOperation.LastError != "" {
		t.Fatalf("active operation optional values were not preserved: %+v", got.ActiveOperation)
	}
	if got.Candidate == nil || got.Candidate.ReleaseId != "release-2" || got.Candidate.PayloadSize != 42 {
		t.Fatalf("candidate projection = %+v", got.Candidate)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"status", "current", "compatibility", "checked_at", "candidate", "active_operation"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("HTTP field %q missing from %s", key, body)
		}
	}
}
