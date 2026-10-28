package application

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/redact"
)

// JSONRuntimeLogger adapts RuntimeLogger to newline-delimited standard-library
// slog JSON records. It owns no durable observability state.
type JSONRuntimeLogger struct {
	mu     sync.Mutex
	logger *slog.Logger
	tail   *RuntimeLogTail
}

const maxRuntimeMessageBytes = 1024

// NewJSONRuntimeLogger creates a structured runtime logger writing to w. A nil
// writer is treated as a safe discard sink so optional integrations cannot panic.
func NewJSONRuntimeLogger(w io.Writer) *JSONRuntimeLogger {
	if w == nil {
		w = io.Discard
	}
	return &JSONRuntimeLogger{
		logger: slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level:       slog.LevelDebug,
			ReplaceAttr: replaceRuntimeAttr,
		})),
	}
}

var _ observabilityport.RuntimeLogger = (*JSONRuntimeLogger)(nil)

func (l *JSONRuntimeLogger) Debug(ctx context.Context, event observabilityport.RuntimeEvent) {
	l.log(ctx, slog.LevelDebug, event)
}

func (l *JSONRuntimeLogger) Info(ctx context.Context, event observabilityport.RuntimeEvent) {
	l.log(ctx, slog.LevelInfo, event)
}

func (l *JSONRuntimeLogger) Warn(ctx context.Context, event observabilityport.RuntimeEvent) {
	l.log(ctx, slog.LevelWarn, event)
}

func (l *JSONRuntimeLogger) Error(ctx context.Context, event observabilityport.RuntimeEvent) {
	l.log(ctx, slog.LevelError, event)
}

func (l *JSONRuntimeLogger) log(ctx context.Context, level slog.Level, event observabilityport.RuntimeEvent) {
	if l == nil || l.logger == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	attrs := make([]slog.Attr, 0, 13)
	if value := safeRuntimeField(event.Component); value != "" {
		attrs = append(attrs, slog.String("component", value))
	}
	if value := safeRuntimeField(event.Operation); value != "" {
		attrs = append(attrs, slog.String("operation", value))
	}
	if value := safeRuntimeField(event.Outcome); value != "" {
		attrs = append(attrs, slog.String("outcome", value))
	}
	if value := safeRuntimeField(event.ReasonCode); value != "" {
		attrs = append(attrs, slog.String("reason_code", value))
	}
	if value := safeRuntimeRequestID(event.RequestID); value != "" {
		attrs = append(attrs, slog.String("request_id", value))
	}
	if value := safeRuntimeField(event.CorrelationID); value != "" {
		attrs = append(attrs, slog.String("correlation_id", value))
	}
	if value := safeRuntimeField(event.OwnerType); value != "" {
		attrs = append(attrs, slog.String("owner_type", value))
	}
	if value := safeRuntimeField(event.OwnerID); value != "" {
		attrs = append(attrs, slog.String("owner_id", value))
	}
	if value := safeRuntimeField(event.WorkItemID); value != "" {
		attrs = append(attrs, slog.String("work_item_id", value))
	}
	if event.Attempt > 0 {
		attrs = append(attrs, slog.Int("attempt", event.Attempt))
	}
	if event.Retryable != nil {
		attrs = append(attrs, slog.Bool("retryable", *event.Retryable))
	}
	if event.DurationMS > 0 {
		attrs = append(attrs, slog.Int64("duration_ms", event.DurationMS))
	}
	if value := safeRuntimeField(event.ErrorClass); value != "" {
		attrs = append(attrs, slog.String("error_class", value))
	}

	if event.Generation > 0 {
		attrs = append(attrs, slog.Int64("generation", event.Generation))
	}
	if event.CandidateCount != nil {
		attrs = append(attrs, slog.Int("candidate_count", *event.CandidateCount))
	}
	if event.ExecutableCount != nil {
		attrs = append(attrs, slog.Int("executable_count", *event.ExecutableCount))
	}
	if event.ExcludeReasonCounts != nil {
		reasonCounts := safeRuntimeReasonCounts(event.ExcludeReasonCounts)
		if len(reasonCounts) == 0 {
			attrs = append(attrs, slog.Any("exclude_reason_counts", map[string]int{}))
		} else {
			attrs = append(attrs, slog.Attr{Key: "exclude_reason_counts", Value: slog.GroupValue(reasonCounts...)})
		}
	}

	// JSONHandler emits one complete record per Handle call. The mutex also
	// protects simple writers such as bytes.Buffer used by focused tests.
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.LogAttrs(ctx, level, safeRuntimeMessage(event.Message), attrs...)
}

func replaceRuntimeAttr(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) != 0 {
		return attr
	}
	switch attr.Key {
	case slog.TimeKey:
		attr.Key = "timestamp"
	case slog.LevelKey:
		attr.Key = "level"
		attr.Value = slog.StringValue(strings.ToLower(attr.Value.String()))
	case slog.MessageKey:
		attr.Key = "message"
	}
	return attr
}

func safeRuntimeMessage(message string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, message)
	message = strings.TrimSpace(message)
	safe, matched := redact.Apply(message, maxRuntimeMessageBytes, redact.Mask, "<redacted runtime message>")
	if matched {
		return safe
	}
	if len(safe) > maxRuntimeMessageBytes {
		safe = safe[:maxRuntimeMessageBytes]
		for len(safe) > 0 && !utf8.ValidString(safe) {
			safe = safe[:len(safe)-1]
		}
	}
	return safe
}

func safeRuntimeRequestID(value string) string {
	if observabilityport.IsSafeRequestID(value) {
		return value
	}
	return ""
}

func safeRuntimeReasonCounts(counts map[string]int) []slog.Attr {
	normalized := make(map[string]int, len(counts))
	for key, count := range counts {
		normalized[normalizeRuntimeExcludeReasonCode(key)] += count
	}
	keys := make([]string, 0, len(normalized))
	for key := range normalized {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attrs := make([]slog.Attr, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, slog.Int(key, normalized[key]))
	}
	return attrs
}

func normalizeRuntimeExcludeReasonCode(value string) string {
	switch value {
	case "coverage_unresolved", "target_render_failed", "unknown", "target_missing", "already_aligned", "target_not_absolute":
		return value
	default:
		return "unknown"
	}
}

func safeRuntimeField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return ""
	}
	if len(value) > 512 {
		return "<redacted runtime field>"
	}
	safe, _ := redact.Apply(value, 512, redact.Mask, "<redacted runtime field>")
	return safe
}
