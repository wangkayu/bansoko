package application

import "time"

// ScanSubmitTrigger identifies why a scan was admitted.
type ScanSubmitTrigger string

const (
	ScanSubmitTriggerManual   ScanSubmitTrigger = "manual"
	ScanSubmitTriggerPeriodic ScanSubmitTrigger = "periodic"
)

type ScanSubmitInput struct {
	// TreeTop is the scope of the scan: the directory the work's own files
	// live in. Callers that do not know it (the UI never does) omit it and the
	// last scan scope's tree top is reused; the fingerprint covers the
	// effective tree top.
	TreeTop        *string            `json:"tree_top,omitempty"`
	ManagedAnimeID string             `json:"managed_anime_id"`
	RequestID      *string            `json:"request_id,omitempty"`
	Trigger        *ScanSubmitTrigger `json:"trigger,omitempty"`
}

type ScanView struct {
	CapacityStats    map[string]any
	CompletedAt      *time.Time
	FailureCode      *string
	FailureReason    *string
	Generation       int64
	InputFingerprint string
	ManagedAnimeID   string
	Progress         map[string]any
	StartedAt        *time.Time
	State            ScanState
	Version          int64
}

type Observation struct {
	Facts       map[string]any
	FilePath    string
	Fingerprint string
	ObservedAt  time.Time
	SizeBytes   int64
}

type ObservationPage struct {
	Items []Observation
}

func cloneScanSubmitInput(input ScanSubmitInput) ScanSubmitInput {
	input.TreeTop = cloneString(input.TreeTop)
	input.RequestID = cloneString(input.RequestID)
	if input.Trigger != nil {
		trigger := *input.Trigger
		input.Trigger = &trigger
	}
	return input
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

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	copy := make(map[string]any, len(value))
	for key, item := range value {
		copy[key] = cloneJSONValue(item)
	}
	return copy
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
