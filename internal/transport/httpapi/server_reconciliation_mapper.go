package httpapi

import (
	"fmt"
	"strconv"
	"time"

	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapReconciliationScanSubmitInput(input generated.ReconciliationScanSubmitInput) reconciliationapplication.ScanSubmitInput {
	mapped := reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: string(input.ManagedAnimeId),
		TreeTop:        cloneString(input.TreeTop),
		RequestID:      cloneString(input.RequestId),
	}
	if input.Trigger != nil {
		trigger := reconciliationapplication.ScanSubmitTrigger(*input.Trigger)
		mapped.Trigger = &trigger
	}
	return mapped
}

func mapReconciliationScanView(view reconciliationapplication.ScanView) (generated.ReconciliationScanView, error) {
	if view.Generation < 0 {
		return generated.ReconciliationScanView{}, fmt.Errorf("reconciliation generation %d is below the OpenAPI minimum", view.Generation)
	}
	if view.Version < 1 {
		return generated.ReconciliationScanView{}, fmt.Errorf("reconciliation version %d is below the OpenAPI minimum", view.Version)
	}
	generation, err := int64ToHTTPInt(view.Generation, "generation")
	if err != nil {
		return generated.ReconciliationScanView{}, err
	}
	version, err := int64ToHTTPInt(view.Version, "version")
	if err != nil {
		return generated.ReconciliationScanView{}, err
	}
	return generated.ReconciliationScanView{
		ManagedAnimeId:   generated.ID(view.ManagedAnimeID),
		State:            generated.ReconciliationScanViewState(view.State),
		Generation:       generation,
		InputFingerprint: view.InputFingerprint,
		Progress:         cloneJSONMap(view.Progress),
		CapacityStats:    cloneJSONMap(view.CapacityStats),
		FailureCode:      cloneString(view.FailureCode),
		FailureReason:    cloneString(view.FailureReason),
		Version:          version,
		StartedAt:        cloneTime(view.StartedAt),
		CompletedAt:      cloneTime(view.CompletedAt),
	}, nil
}

func mapReconciliationObservationPage(page reconciliationapplication.ObservationPage) (generated.ReconciliationObservationPage, error) {
	items := make([]generated.ReconciliationObservation, len(page.Items))
	for index, observation := range page.Items {
		if observation.SizeBytes < 0 {
			return generated.ReconciliationObservationPage{}, fmt.Errorf("reconciliation observation %d has negative size_bytes", index)
		}
		size, err := int64ToHTTPInt(observation.SizeBytes, "size_bytes")
		if err != nil {
			return generated.ReconciliationObservationPage{}, err
		}
		items[index] = generated.ReconciliationObservation{
			Facts:       cloneJSONMapPtr(observation.Facts),
			FilePath:    observation.FilePath,
			Fingerprint: observation.Fingerprint,
			ObservedAt:  observation.ObservedAt,
			SizeBytes:   size,
		}
	}
	return generated.ReconciliationObservationPage{Items: items}, nil
}

func int64ToHTTPInt(value int64, field string) (int, error) {
	if strconv.IntSize == 32 && (value < -1<<31 || value > 1<<31-1) {
		return 0, fmt.Errorf("reconciliation %s %d exceeds HTTP integer range", field, value)
	}
	return int(value), nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneJSONMap(value map[string]any) map[string]interface{} {
	if value == nil {
		return nil
	}
	copy := make(map[string]interface{}, len(value))
	for key, item := range value {
		copy[key] = cloneJSONValue(item)
	}
	return copy
}

func cloneJSONMapPtr(value map[string]any) *map[string]interface{} {
	copy := cloneJSONMap(value)
	if copy == nil {
		return nil
	}
	return &copy
}

func cloneJSONValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneJSONMap(item)
	case []any:
		copy := make([]any, len(item))
		for index, child := range item {
			copy[index] = cloneJSONValue(child)
		}
		return copy
	default:
		return value
	}
}
