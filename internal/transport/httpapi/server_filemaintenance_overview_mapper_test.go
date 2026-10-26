package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	reconciliationapplication "bansoko/internal/reconciliation/application"
)

func TestMapFileMaintenanceOverviewViewMapsNestedValuesAndCopiesMaps(t *testing.T) {
	now := time.Unix(123, 0).UTC()
	reason := "stale"
	policyVersion := 7
	progress := map[string]any{"done": 2, "nested": map[string]any{"remaining": 1}}
	capacity := map[string]any{"free": int64(42)}
	consumers := []filemaintenance.ReconciliationConsumerStateView{{
		Consumer:       "planner",
		Generation:     4,
		ManagedAnimeID: "anime-1",
		Reason:         &reason,
		ReasonCode:     &reason,
		State:          "ready",
		UpdatedAt:      now,
		Version:        3,
	}}
	placements := []filemaintenance.PlacementView{{ID: "placement-1", ManagedAnimeID: "anime-1", PhysicalPath: "/library/a.mkv"}}
	decisions := []filemaintenance.ReplacementDecisionView{{ID: "decision-1", ManagedAnimeID: "anime-1", State: "admitted", Pools: &[]filemaintenance.ReplacementPoolEvidence{}}}
	cleanups := []filemaintenance.CleanupOperationView{{
		CreatedAt:      now,
		FailureCode:    &reason,
		ID:             "cleanup-1",
		ManagedAnimeID: "anime-1",
		PlacementID:    "placement-1",
		Reason:         &reason,
		State:          "admitted",
		UpdatedAt:      &now,
	}}
	alignments := []filemaintenance.AlignmentOperationView{{
		CompletedAt:    &now,
		CreatedAt:      now,
		FailureCode:    &reason,
		ID:             "alignment-1",
		ManagedAnimeID: "anime-1",
		ObservationRef: "observation-1",
		PolicyVersion:  &policyVersion,
		Reason:         &reason,
		ScanGeneration: 4,
		State:          "aligned",
		UpdatedAt:      &now,
	}}
	value := filemaintenance.OverviewView{
		ManagedAnimeID: "anime-1",
		Scan: &reconciliationapplication.ScanView{
			CapacityStats:    capacity,
			CompletedAt:      &now,
			FailureCode:      &reason,
			FailureReason:    &reason,
			Generation:       4,
			InputFingerprint: "scan-fp",
			ManagedAnimeID:   "anime-1",
			Progress:         progress,
			StartedAt:        &now,
			State:            "completed",
			Version:          5,
		},
		Consumers:  &consumers,
		Placements: &placements,
		Decisions:  &decisions,
		Cleanups:   &cleanups,
		Alignments: &alignments,
	}

	mapped, err := mapFileMaintenanceOverviewView(value)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.ManagedAnimeId != "anime-1" || mapped.Scan == nil || mapped.Scan.Generation != 4 || mapped.Scan.State != "completed" {
		t.Fatalf("mapped overview identity/scan = %+v", mapped)
	}
	if mapped.Consumers == nil || len(*mapped.Consumers) != 1 || (*mapped.Consumers)[0].Reason == nil || *(*mapped.Consumers)[0].Reason != reason {
		t.Fatalf("mapped consumers = %+v", mapped.Consumers)
	}
	if mapped.Placements == nil || len(*mapped.Placements) != 1 || mapped.Cleanups == nil || mapped.Alignments == nil || mapped.Decisions == nil {
		t.Fatalf("mapped collections = %+v", mapped)
	}
	if mapped.Scan.Progress["nested"].(map[string]interface{})["remaining"] != 1 {
		t.Fatalf("nested progress = %#v", mapped.Scan.Progress)
	}
	mapped.Scan.Progress["nested"].(map[string]interface{})["remaining"] = 9
	if progress["nested"].(map[string]any)["remaining"] != 1 {
		t.Fatal("scan progress aliases application map")
	}
	if *(*mapped.Cleanups)[0].Reason == "" || (*mapped.Cleanups)[0].UpdatedAt == &now || (*mapped.Alignments)[0].PolicyVersion == &policyVersion {
		t.Fatal("nested pointer values were not copied")
	}
}

func TestMapFileMaintenanceOverviewViewMapsRetentionAndPreservesShapes(t *testing.T) {
	now := time.Unix(456, 0).UTC()
	global := filemaintenance.RetentionPolicyDefaultView{
		Policy: filemaintenance.RetentionPolicy{
			AdmissionMode:  filemaintenance.ModeAll,
			QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"anibt.format","ranks":[["MKV"]]}]}`),
		},
		UpdatedAt: &now,
		Version:   3,
	}
	instance := filemaintenance.InstanceRetentionPolicyView{
		ManagedAnimeID:       "anime-1",
		Policy:               global.Policy,
		SourceDefaultVersion: 2,
		UpdatedAt:            &now,
		Version:              4,
	}
	mapped, err := mapFileMaintenanceOverviewView(filemaintenance.OverviewView{
		ManagedAnimeID: "anime-1",
		GlobalDefault:  &global,
		InstancePolicy: &instance,
		Consumers:      &[]filemaintenance.ReconciliationConsumerStateView{},
		Placements:     &[]filemaintenance.PlacementView{},
		Decisions:      &[]filemaintenance.ReplacementDecisionView{},
		Cleanups:       &[]filemaintenance.CleanupOperationView{},
		Alignments:     &[]filemaintenance.AlignmentOperationView{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mapped.GlobalDefault == nil || mapped.InstancePolicy == nil || mapped.InstancePolicy.SourceDefaultVersion != 2 || mapped.InstancePolicy.Version != 4 {
		t.Fatalf("mapped retention = global=%+v instance=%+v", mapped.GlobalDefault, mapped.InstancePolicy)
	}
	if mapped.Consumers == nil || mapped.Placements == nil || mapped.Decisions == nil || mapped.Cleanups == nil || mapped.Alignments == nil {
		t.Fatal("empty collection pointers were lost")
	}
	mapped, err = mapFileMaintenanceOverviewView(filemaintenance.OverviewView{})
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Consumers != nil || mapped.Placements != nil || mapped.Decisions != nil || mapped.Cleanups != nil || mapped.Alignments != nil || mapped.Scan != nil {
		t.Fatalf("nil collection shape changed = %+v", mapped)
	}
}
