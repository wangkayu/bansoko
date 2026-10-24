package application

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

func TestJSONRuntimeLoggerMapsStableFieldsAndKeepsFalse(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	retryable := false
	logger.Error(context.Background(), observabilityport.RuntimeEvent{
		Message:       "request failed",
		Component:     "httpapi",
		Operation:     "request",
		Outcome:       "failure",
		ReasonCode:    "unknown_error",
		RequestID:     "req-1",
		CorrelationID: "corr-1",
		OwnerType:     "subscription",
		OwnerID:       "owner-1",
		WorkItemID:    "work-1",
		Attempt:       2,
		Retryable:     &retryable,
		DurationMS:    17,
		ErrorClass:    "unknown_application_error",
	})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v; output=%q", err, output.String())
	}
	for _, key := range []string{"timestamp", "level", "message", "component", "operation", "outcome", "reason_code", "request_id", "correlation_id", "owner_type", "owner_id", "work_item_id", "attempt", "retryable", "duration_ms", "error_class"} {
		if _, ok := record[key]; !ok {
			t.Fatalf("runtime JSON missing %q: %v", key, record)
		}
	}
	if got, want := record["level"], "error"; got != want {
		t.Fatalf("level = %v, want %v", got, want)
	}
	if got, want := record["message"], "request failed"; got != want {
		t.Fatalf("message = %v, want %v", got, want)
	}
	if got, ok := record["retryable"].(bool); !ok || got {
		t.Fatalf("retryable = %v, want false", record["retryable"])
	}
	if got, want := record["attempt"], float64(2); got != want {
		t.Fatalf("attempt = %v, want %v", got, want)
	}
	if got, want := record["duration_ms"], float64(17); got != want {
		t.Fatalf("duration_ms = %v, want %v", got, want)
	}
}

func TestJSONRuntimeLoggerOmitsAbsentOptionalFields(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Info(context.Background(), observabilityport.RuntimeEvent{Message: "started"})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	for _, key := range []string{"component", "operation", "outcome", "reason_code", "request_id", "correlation_id", "owner_type", "owner_id", "work_item_id", "generation", "attempt", "retryable", "duration_ms", "error_class", "exclude_reason_counts"} {
		if _, ok := record[key]; ok {
			t.Fatalf("runtime JSON unexpectedly included absent %q: %v", key, record)
		}
	}
}

func TestJSONRuntimeLoggerWritesGeneration(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Info(context.Background(), observabilityport.RuntimeEvent{Message: "generation complete", Generation: 7})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	if got, want := record["generation"], float64(7); got != want {
		t.Fatalf("generation = %v, want %v", got, want)
	}
}

func TestJSONRuntimeLoggerWritesEmptyReasonMap(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Info(context.Background(), observabilityport.RuntimeEvent{
		Message:             "alignment candidate summary",
		ExcludeReasonCounts: map[string]int{},
	})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	reasons, ok := record["exclude_reason_counts"].(map[string]any)
	if !ok {
		t.Fatalf("exclude_reason_counts = %T %v, want JSON object", record["exclude_reason_counts"], record["exclude_reason_counts"])
	}
	if len(reasons) != 0 {
		t.Fatalf("exclude_reason_counts = %v, want empty object", reasons)
	}
}

func TestJSONRuntimeLoggerWritesConsecutiveJSONLines(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Debug(context.Background(), observabilityport.RuntimeEvent{Message: "debug event"})
	logger.Warn(context.Background(), observabilityport.RuntimeEvent{Message: "warn event"})

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSON lines = %d, want 2; output=%q", len(lines), output.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not JSON: %v; line=%q", i, err, line)
		}
	}
	if !strings.Contains(lines[0], `"level":"debug"`) {
		t.Fatalf("debug level missing: %q", lines[0])
	}
	if !strings.Contains(lines[1], `"level":"warn"`) {
		t.Fatalf("warn level missing: %q", lines[1])
	}
}

func TestJSONRuntimeLoggerBoundsMessageAndRedactsUnsafeFields(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Warn(context.Background(), observabilityport.RuntimeEvent{
		Message:       strings.Repeat("x", maxRuntimeMessageBytes+100),
		RequestID:     "req-1",
		CorrelationID: "https://user:secret@example.test/correlation",
		OwnerID:       "owner\nwith-control",
	})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v; output=%q", err, output.String())
	}
	message, ok := record["message"].(string)
	if !ok || len(message) != maxRuntimeMessageBytes {
		t.Fatalf("message length = %d, want %d", len(message), maxRuntimeMessageBytes)
	}
	if got := record["request_id"]; got != "req-1" {
		t.Fatalf("request_id = %v, want exact safe ID", got)
	}
	if got, ok := record["correlation_id"]; !ok || got != "<redacted runtime field>" {
		t.Fatalf("correlation_id = %v, want redacted marker", got)
	}
	if _, ok := record["owner_id"]; ok {
		t.Fatal("control-character field must be omitted")
	}
}

func TestJSONRuntimeLoggerNormalizesMessageControlCharacters(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Warn(context.Background(), observabilityport.RuntimeEvent{Message: "runtime\nmessage\t\x00"})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	if got, want := record["message"], "runtime message"; got != want {
		t.Fatalf("message = %v, want %q", got, want)
	}
}
func TestJSONRuntimeLoggerDoesNotExposeJSONDecodeError(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Error(context.Background(), observabilityport.RuntimeEvent{Message: "json: cannot unmarshal secret token"})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	if got := record["message"]; got != "<redacted runtime message>" {
		t.Fatalf("message = %v, want redacted marker", got)
	}
	if strings.Contains(output.String(), "cannot unmarshal") || strings.Contains(output.String(), "secret token") {
		t.Fatalf("runtime JSON leaked decode error: %q", output.String())
	}
}

func TestJSONRuntimeLoggerWritesCandidateSummaryFields(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	candidateCount, executableCount := 3, 1
	logger.Info(context.Background(), observabilityport.RuntimeEvent{
		Message:         "alignment candidate summary",
		CandidateCount:  &candidateCount,
		ExecutableCount: &executableCount,
		ExcludeReasonCounts: map[string]int{
			"coverage_unresolved":   1,
			"target_render_failed":  1,
			"target_missing":        1,
			"already_aligned":       1,
			"target_not_absolute":   1,
			"unknown":               1,
			"https://unsafe":        9,
			"/var/file":             8,
			"permission denied: /x": 7,
			"password=secret-token": 6,
		},
	})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	if got, want := record["candidate_count"], float64(3); got != want {
		t.Fatalf("candidate_count = %v, want %v", got, want)
	}
	if got, want := record["executable_count"], float64(1); got != want {
		t.Fatalf("executable_count = %v, want %v", got, want)
	}
	reasons, ok := record["exclude_reason_counts"].(map[string]any)
	if !ok {
		t.Fatalf("exclude_reason_counts = %T %v, want JSON object", record["exclude_reason_counts"], record["exclude_reason_counts"])
	}
	for key, want := range map[string]float64{
		"coverage_unresolved":  1,
		"target_render_failed": 1,
		"target_missing":       1,
		"already_aligned":      1,
		"target_not_absolute":  1,
		"unknown":              1 + 9 + 8 + 7 + 6,
	} {
		if got := reasons[key]; got != want {
			t.Fatalf("exclude_reason_counts[%q] = %v, want %v", key, got, want)
		}
	}
	for _, unsafe := range []string{"https://unsafe", "/var/file", "permission denied: /x", "password=secret-token"} {
		if _, ok := reasons[unsafe]; ok {
			t.Fatalf("unsafe reason key %q must be omitted", unsafe)
		}
		if strings.Contains(output.String(), unsafe) {
			t.Fatalf("runtime JSON leaked unsafe reason key %q: %q", unsafe, output.String())
		}
	}
}

func TestJSONRuntimeLoggerRedactsUnsafeMessage(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONRuntimeLogger(&output)
	logger.Error(context.Background(), observabilityport.RuntimeEvent{
		Message: "request failed: postgres://user:password@example.test/db",
	})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	if got := record["message"]; got != "<redacted runtime message>" {
		t.Fatalf("message = %v, want redacted marker", got)
	}
	if strings.Contains(output.String(), "postgres://") || strings.Contains(output.String(), "password") {
		t.Fatalf("runtime JSON leaked unsafe message: %q", output.String())
	}
}
