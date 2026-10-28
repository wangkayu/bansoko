package application

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// The runtime log tail is the bounded in-memory copy of what the runtime
// logger wrote to stdout, shown under the diagnostics tab so an owner can
// attach it to a bug report. It is memory only: never written to a file or a
// table, and empty again after a restart. Both bounds always hold; the older
// line is dropped first.
const (
	// RuntimeLogMaxLines is the line bound of the tail.
	RuntimeLogMaxLines = 500
	// RuntimeLogMaxBytes is the byte bound of the tail (stored line bytes).
	RuntimeLogMaxBytes = 256 << 10
	// RuntimeLogMaxLimit is the largest page a read may ask for.
	RuntimeLogMaxLimit = RuntimeLogMaxLines
)

// RuntimeLogLevel filters a tail read: all lines, warnings and errors, or
// errors only.
type RuntimeLogLevel string

const (
	RuntimeLogAll   RuntimeLogLevel = "all"
	RuntimeLogWarn  RuntimeLogLevel = "warn"
	RuntimeLogError RuntimeLogLevel = "error"
)

// RuntimeLogEntry is one tail line, projected onto the allowlisted fields of
// a runtime record. Anything else the line held is dropped.
type RuntimeLogEntry struct {
	At            time.Time
	Level         string
	Message       string
	Component     string
	Operation     string
	Outcome       string
	ReasonCode    string
	RequestID     string
	CorrelationID string
	ErrorClass    string
}

// RuntimeLogTail is an io.Writer that keeps the most recent complete JSON
// log lines. slog's JSON handler issues exactly one Write per record.
type RuntimeLogTail struct {
	mu       sync.Mutex
	maxLines int
	maxBytes int
	lines    []string
	bytes    int
}

// NewRuntimeLogTail builds a tail with the product bounds.
func NewRuntimeLogTail() *RuntimeLogTail {
	return newRuntimeLogTail(RuntimeLogMaxLines, RuntimeLogMaxBytes)
}

func newRuntimeLogTail(maxLines, maxBytes int) *RuntimeLogTail {
	return &RuntimeLogTail{maxLines: maxLines, maxBytes: maxBytes}
}

// Write stores one log line. It never fails, so a logging tee cannot break the
// stdout writer next to it.
func (t *RuntimeLogTail) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\r\n")
	if line == "" || len(line) > t.maxBytes {
		return len(p), nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	t.bytes += len(line)
	for len(t.lines) > t.maxLines || t.bytes > t.maxBytes {
		t.bytes -= len(t.lines[0])
		t.lines[0] = ""
		t.lines = t.lines[1:]
	}
	return len(p), nil
}

// Entries returns up to limit of the most recent lines at or above level, in
// the order they were written (oldest first).
func (t *RuntimeLogTail) Entries(level RuntimeLogLevel, limit int) []RuntimeLogEntry {
	if limit <= 0 || limit > RuntimeLogMaxLimit {
		limit = RuntimeLogMaxLimit
	}
	t.mu.Lock()
	snapshot := append([]string(nil), t.lines...)
	t.mu.Unlock()

	out := make([]RuntimeLogEntry, 0, min(limit, len(snapshot)))
	for i := len(snapshot) - 1; i >= 0 && len(out) < limit; i-- {
		entry, ok := parseRuntimeLogLine(snapshot[i])
		if !ok || !runtimeLevelAllows(level, entry.Level) {
			continue
		}
		out = append(out, entry)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func runtimeLevelAllows(filter RuntimeLogLevel, level string) bool {
	switch filter {
	case RuntimeLogError:
		return level == "error"
	case RuntimeLogWarn:
		return level == "warn" || level == "error"
	}
	return true
}

func parseRuntimeLogLine(line string) (RuntimeLogEntry, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return RuntimeLogEntry{}, false
	}
	text := func(key string) string {
		value, _ := raw[key].(string)
		return value
	}
	entry := RuntimeLogEntry{
		Level: text("level"), Message: text("message"), Component: text("component"), Operation: text("operation"),
		Outcome: text("outcome"), ReasonCode: text("reason_code"), RequestID: text("request_id"),
		CorrelationID: text("correlation_id"), ErrorClass: text("error_class"),
	}
	at, err := time.Parse(time.RFC3339Nano, text("timestamp"))
	if err != nil {
		return RuntimeLogEntry{}, false
	}
	entry.At = at.UTC()
	return entry, true
}

// NewTeedRuntimeLogger is NewJSONRuntimeLogger whose records also fill a
// bounded in-memory tail, readable through Tail.
func NewTeedRuntimeLogger(w io.Writer) *JSONRuntimeLogger {
	if w == nil {
		w = io.Discard
	}
	tail := NewRuntimeLogTail()
	logger := NewJSONRuntimeLogger(io.MultiWriter(w, tail))
	logger.tail = tail
	return logger
}

// Tail returns the in-memory tail, or nil for a logger built without one.
func (l *JSONRuntimeLogger) Tail() *RuntimeLogTail {
	if l == nil {
		return nil
	}
	return l.tail
}
