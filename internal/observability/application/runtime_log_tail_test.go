package application

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

func tailLine(level, message string) string {
	return fmt.Sprintf(`{"timestamp":"2026-10-28T08:00:00Z","level":%q,"message":%q}`+"\n", level, message)
}

func TestRuntimeLogTailKeepsTheNewestLinesWithinTheLineBound(t *testing.T) {
	tail := newRuntimeLogTail(3, 1<<20)
	for i := 0; i < 5; i++ {
		_, _ = tail.Write([]byte(tailLine("info", fmt.Sprintf("line-%d", i))))
	}
	entries := tail.Entries(RuntimeLogAll, 0)
	if len(entries) != 3 || entries[0].Message != "line-2" || entries[2].Message != "line-4" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestRuntimeLogTailKeepsTheByteBound(t *testing.T) {
	line := tailLine("info", strings.Repeat("x", 100))
	tail := newRuntimeLogTail(1000, 4*len(strings.TrimRight(line, "\n")))
	for i := 0; i < 20; i++ {
		_, _ = tail.Write([]byte(line))
	}
	if got := len(tail.Entries(RuntimeLogAll, 0)); got != 4 {
		t.Fatalf("lines kept = %d, want 4 within the byte bound", got)
	}
	if tail.bytes > 4*len(strings.TrimRight(line, "\n")) {
		t.Fatalf("stored bytes %d exceed the bound", tail.bytes)
	}
	// A single line larger than the whole bound is dropped, not stored.
	_, _ = tail.Write([]byte(tailLine("info", strings.Repeat("y", 1000))))
	if got := len(tail.Entries(RuntimeLogAll, 0)); got != 4 {
		t.Fatalf("oversize line changed the tail: %d", got)
	}
}

func TestRuntimeLogTailLevelFilterAndLimit(t *testing.T) {
	tail := NewRuntimeLogTail()
	for _, level := range []string{"info", "warn", "error", "info", "error"} {
		_, _ = tail.Write([]byte(tailLine(level, level)))
	}
	if got := len(tail.Entries(RuntimeLogAll, 0)); got != 5 {
		t.Fatalf("all = %d", got)
	}
	if got := len(tail.Entries(RuntimeLogWarn, 0)); got != 3 {
		t.Fatalf("warn+ = %d", got)
	}
	errs := tail.Entries(RuntimeLogError, 1)
	if len(errs) != 1 || errs[0].Level != "error" {
		t.Fatalf("error limit 1 = %+v", errs)
	}
}

// Only the allowlisted fields of a line reach a reader.
func TestRuntimeLogTailDropsFieldsOutsideTheAllowlist(t *testing.T) {
	tail := NewRuntimeLogTail()
	_, _ = tail.Write([]byte(`{"timestamp":"2026-10-28T08:00:00Z","level":"error","message":"m","component":"c","secret_note":"leak-me"}` + "\n"))
	entries := tail.Entries(RuntimeLogAll, 0)
	if len(entries) != 1 || entries[0].Component != "c" {
		t.Fatalf("entries = %+v", entries)
	}
	if fmt.Sprintf("%+v", entries[0]) != fmt.Sprintf("%+v", RuntimeLogEntry{At: entries[0].At, Level: "error", Message: "m", Component: "c"}) {
		t.Fatalf("entry carries more than the allowlist: %+v", entries[0])
	}
}

// The teed logger writes the same line to stdout and to the tail; a new logger
// (a restart) starts with an empty tail.
func TestTeedRuntimeLoggerFillsStdoutAndTail(t *testing.T) {
	var stdout bytes.Buffer
	logger := NewTeedRuntimeLogger(&stdout)
	logger.Warn(context.Background(), observabilityport.RuntimeEvent{Message: "tail-warning", Component: "test", Operation: "tail", Outcome: "failure", ReasonCode: "tail_test"})
	if !strings.Contains(stdout.String(), "tail-warning") {
		t.Fatalf("stdout lost the line: %q", stdout.String())
	}
	entries := logger.Tail().Entries(RuntimeLogWarn, 0)
	if len(entries) != 1 || entries[0].Message != "tail-warning" || entries[0].Level != "warn" {
		t.Fatalf("tail = %+v", entries)
	}
	if restarted := NewTeedRuntimeLogger(&bytes.Buffer{}); len(restarted.Tail().Entries(RuntimeLogAll, 0)) != 0 {
		t.Fatal("a new logger must start with an empty tail")
	}
	if NewJSONRuntimeLogger(&bytes.Buffer{}).Tail() != nil {
		t.Fatal("a logger built without a tail must report none")
	}
}
