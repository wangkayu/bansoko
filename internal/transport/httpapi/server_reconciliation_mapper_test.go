package httpapi

import (
	"testing"
	"time"

	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestReconciliationMappers(t *testing.T) {
	trigger := generated.ReconciliationScanSubmitInputTriggerPeriodic
	input := mapReconciliationScanSubmitInput(generated.ReconciliationScanSubmitInput{
		ManagedAnimeId: "anime-1",
		RequestId:      reconciliationStringPtr("request-1"),
		Trigger:        &trigger,
	})
	if input.ManagedAnimeID != "anime-1" || input.RequestID == nil || *input.RequestID != "request-1" || input.Trigger == nil || *input.Trigger != reconciliationapplication.ScanSubmitTriggerPeriodic {
		t.Fatalf("unexpected mapped input: %+v", input)
	}

	now := time.Now().UTC()
	view, err := mapReconciliationScanView(reconciliationapplication.ScanView{
		ManagedAnimeID:   "anime-1",
		State:            reconciliationapplication.ScanCompleted,
		Generation:       3,
		InputFingerprint: "fingerprint",
		Version:          4,
		StartedAt:        &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.ManagedAnimeId != "anime-1" || view.Generation != 3 || view.Version != 4 || view.State != generated.ReconciliationScanViewStateCompleted {
		t.Fatalf("unexpected mapped view: %+v", view)
	}
}

func reconciliationStringPtr(value string) *string { return &value }

func TestReconciliationMappersCloneNestedValues(t *testing.T) {
	progress := map[string]any{"nested": map[string]any{"items": []any{"before"}}}
	failure := "failed"
	page, err := mapReconciliationScanView(reconciliationapplication.ScanView{
		ManagedAnimeID: "anime-1",
		State:          reconciliationapplication.ScanFailed,
		Generation:     1,
		Version:        1,
		Progress:       progress,
		FailureCode:    &failure,
	})
	if err != nil {
		t.Fatal(err)
	}
	progress["nested"].(map[string]any)["items"].([]any)[0] = "changed"
	if page.Progress["nested"].(map[string]interface{})["items"].([]any)[0] != "before" {
		t.Fatal("mapped progress aliases application data")
	}
	*page.FailureCode = "changed"
	if failure != "failed" {
		t.Fatal("mapped failure code aliases application data")
	}

	facts := map[string]any{"parser": map[string]any{"episode": float64(2)}}
	mappedPage, err := mapReconciliationObservationPage(reconciliationapplication.ObservationPage{
		Items: []reconciliationapplication.Observation{{Facts: facts, SizeBytes: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	facts["parser"].(map[string]any)["episode"] = float64(3)
	if got := (*mappedPage.Items[0].Facts)["parser"].(map[string]interface{})["episode"]; got != float64(2) {
		t.Fatalf("mapped facts alias application data: %v", got)
	}
}

func TestReconciliationMappersRejectInvalidHTTPProjection(t *testing.T) {
	if _, err := mapReconciliationScanView(reconciliationapplication.ScanView{Generation: -1, Version: 1}); err == nil {
		t.Fatal("negative generation was accepted")
	}
	if _, err := mapReconciliationScanView(reconciliationapplication.ScanView{Generation: 1, Version: 0}); err == nil {
		t.Fatal("zero version was accepted")
	}
	if _, err := mapReconciliationObservationPage(reconciliationapplication.ObservationPage{
		Items: []reconciliationapplication.Observation{{SizeBytes: -1}},
	}); err == nil {
		t.Fatal("negative observation size was accepted")
	}
}
