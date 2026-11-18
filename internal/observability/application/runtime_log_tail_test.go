package application

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	entries := tail.Entries(RuntimeLogAll, "", 0)
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
	if got := len(tail.Entries(RuntimeLogAll, "", 0)); got != 4 {
		t.Fatalf("lines kept = %d, want 4 within the byte bound", got)
	}
	if tail.bytes > 4*len(strings.TrimRight(line, "\n")) {
		t.Fatalf("stored bytes %d exceed the bound", tail.bytes)
	}
	// A single line larger than the whole bound is dropped, not stored.
	_, _ = tail.Write([]byte(tailLine("info", strings.Repeat("y", 1000))))
	if got := len(tail.Entries(RuntimeLogAll, "", 0)); got != 4 {
		t.Fatalf("oversize line changed the tail: %d", got)
	}
}

func TestRuntimeLogTailLevelFilterAndLimit(t *testing.T) {
	tail := NewRuntimeLogTail()
	for _, level := range []string{"info", "warn", "error", "info", "error"} {
		_, _ = tail.Write([]byte(tailLine(level, level)))
	}
	if got := len(tail.Entries(RuntimeLogAll, "", 0)); got != 5 {
		t.Fatalf("all = %d", got)
	}
	if got := len(tail.Entries(RuntimeLogWarn, "", 0)); got != 3 {
		t.Fatalf("warn+ = %d", got)
	}
	errs := tail.Entries(RuntimeLogError, "", 1)
	if len(errs) != 1 || errs[0].Level != "error" {
		t.Fatalf("error limit 1 = %+v", errs)
	}
}

// Only the allowlisted fields of a line reach a reader.
func TestRuntimeLogTailDropsFieldsOutsideTheAllowlist(t *testing.T) {
	tail := NewRuntimeLogTail()
	_, _ = tail.Write([]byte(`{"timestamp":"2026-10-28T08:00:00Z","level":"error","message":"m","component":"c","secret_note":"leak-me"}` + "\n"))
	entries := tail.Entries(RuntimeLogAll, "", 0)
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
	entries := logger.Tail().Entries(RuntimeLogWarn, "", 0)
	if len(entries) != 1 || entries[0].Message != "tail-warning" || entries[0].Level != "warn" {
		t.Fatalf("tail = %+v", entries)
	}
	if restarted := NewTeedRuntimeLogger(&bytes.Buffer{}); len(restarted.Tail().Entries(RuntimeLogAll, "", 0)) != 0 {
		t.Fatal("a new logger must start with an empty tail")
	}
	if NewJSONRuntimeLogger(&bytes.Buffer{}).Tail() != nil {
		t.Fatal("a logger built without a tail must report none")
	}
}

// The task fields reach a reader, including retryable=false; owner_id and any
// field outside the allowlist do not.
func TestRuntimeLogTailProjectsSafeTaskFields(t *testing.T) {
	tail := NewRuntimeLogTail()
	_, _ = tail.Write([]byte(`{"timestamp":"2026-10-28T08:00:00Z","level":"info","message":"transition","component":"worker","operation":"dispatch","outcome":"success","reason_code":"state_changed","request_id":"req-1","correlation_id":"corr-1","error_class":"none","owner_type":"subscription","owner_id":"owner-leak","kind":"work_item","work_item_id":"wi-1","stage":"download","state_before":"queued","state_after":"running","disposition":"retry","attempt":2,"duration_ms":1234,"retryable":false,"secret_note":"leak-me"}` + "\n"))
	entries := tail.Entries(RuntimeLogAll, "", 0)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	entry := entries[0]
	if entry.OwnerType != "subscription" || entry.Kind != "work_item" || entry.WorkItemID != "wi-1" ||
		entry.Stage != "download" || entry.StateBefore != "queued" || entry.StateAfter != "running" || entry.Disposition != "retry" {
		t.Fatalf("task fields dropped: %+v", entry)
	}
	if entry.Attempt != 2 || entry.DurationMS != 1234 {
		t.Fatalf("numeric task fields dropped: %+v", entry)
	}
	if entry.Retryable == nil || *entry.Retryable {
		t.Fatalf("retryable=false must be preserved, got %+v", entry.Retryable)
	}
	// Search may only read the same allowlisted projection: neither the dropped
	// owner_id nor an unknown field can be found.
	if got := tail.Entries(RuntimeLogAll, "owner-leak", 0); len(got) != 0 {
		t.Fatalf("dropped owner_id is searchable: %+v", got)
	}
	if got := tail.Entries(RuntimeLogAll, "owner_id", 0); len(got) != 0 {
		t.Fatalf("dropped owner_id key is searchable: %+v", got)
	}
	if got := tail.Entries(RuntimeLogAll, "leak-me", 0); len(got) != 0 {
		t.Fatalf("unknown field is searchable: %+v", got)
	}
	// Terms copied from the displayed line work, including keys and the
	// component.operation source.
	for _, query := range []string{"retryable", "owner_type=subscription", "worker.dispatch", "state_before", "attempt=2"} {
		if got := tail.Entries(RuntimeLogAll, query, 0); len(got) != 1 {
			t.Fatalf("query %q matched %d lines, want 1", query, len(got))
		}
	}
}

func TestRuntimeLogTailSearchMatchesEveryTermCaseInsensitively(t *testing.T) {
	tail := NewRuntimeLogTail()
	lines := []string{
		`{"timestamp":"2026-10-28T08:00:00Z","level":"info","message":"Provider search failed","kind":"metadata_match","work_item_id":"wi-42"}`,
		`{"timestamp":"2026-10-28T08:00:01Z","level":"warn","message":"Provider search retry scheduled","kind":"metadata_match","attempt":2}`,
		`{"timestamp":"2026-10-28T08:00:02Z","level":"error","message":"downloader unreachable","kind":"download","work_item_id":"wi-9"}`,
	}
	for _, line := range lines {
		_, _ = tail.Write([]byte(line + "\n"))
	}
	if got := len(tail.Entries(RuntimeLogAll, "PROVIDER SEARCH", 0)); got != 2 {
		t.Fatalf("two-term case-insensitive match = %d, want 2", got)
	}
	if got := len(tail.Entries(RuntimeLogAll, "provider metadata_match", 0)); got != 2 {
		t.Fatalf("term in a second field = %d, want 2", got)
	}
	if got := len(tail.Entries(RuntimeLogAll, "provider wi-42", 0)); got != 1 {
		t.Fatalf("terms must all match the same line, got %d", got)
	}
	if got := len(tail.Entries(RuntimeLogWarn, "provider", 0)); got != 1 {
		t.Fatalf("level and text filters must stack, got %d", got)
	}
	if got := len(tail.Entries(RuntimeLogAll, "   ", 0)); got != 3 {
		t.Fatalf("blank query must not filter, got %d", got)
	}
}

// The text and level filters run while scanning, before the limit is taken, so
// an older match is not hidden by newer non-matching lines.
func TestRuntimeLogTailFiltersBeforeTheLimit(t *testing.T) {
	tail := newRuntimeLogTail(10, 1<<20)
	_, _ = tail.Write([]byte(`{"timestamp":"2026-10-28T08:00:00Z","level":"info","message":"target transition"}` + "\n"))
	for i := 0; i < 5; i++ {
		_, _ = tail.Write([]byte(fmt.Sprintf(`{"timestamp":"2026-10-28T08:00:0%dZ","level":"info","message":"filler-%d"}`, i+1, i) + "\n"))
	}
	entries := tail.Entries(RuntimeLogAll, "target", 1)
	if len(entries) != 1 || entries[0].Message != "target transition" {
		t.Fatalf("filtered page = %+v", entries)
	}
}

func TestRuntimeLogTailCursorUsesStableSequenceAndEpoch(t *testing.T) {
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	tail := newRuntimeLogTail(3, 1<<20)
	for i := 0; i < 3; i++ {
		_, _ = tail.Write([]byte(runtimeLogTestLine(now.Add(time.Duration(i)*time.Second), "info", fmt.Sprintf("line %d", i), "")))
	}
	page, err := tail.Query(RuntimeLogQuery{Limit: 10})
	if err != nil || len(page.Entries) != 3 {
		t.Fatalf("initial page = %+v err=%v", page, err)
	}
	cursor, err := decodeRuntimeLogCursor(page.NextAfter)
	if err != nil || cursor.kind != "m" || cursor.epoch != tail.epoch || cursor.seq != 4 || cursor.hash != 0 {
		t.Fatalf("next_after = %q decoded %+v err=%v, want boundary seq 4 of this tail", page.NextAfter, cursor, err)
	}

	// A filter with no match still advances to the scanned end.
	empty, err := tail.Query(RuntimeLogQuery{WorkItemID: "missing", Limit: 1})
	if err != nil || len(empty.Entries) != 0 || empty.NextAfter == "" {
		t.Fatalf("empty task query = %+v err=%v, want a watermark", empty, err)
	}

	// The boundary cursor is exclusive: a later line gets the boundary's
	// sequence and is returned exactly once.
	_, _ = tail.Write([]byte(runtimeLogTestLine(now.Add(3*time.Second), "info", "line 3", "")))
	after, err := tail.Query(RuntimeLogQuery{After: page.NextAfter, Limit: 10})
	if err != nil || len(after.Entries) != 1 || after.Entries[0].Message != "line 3" {
		t.Fatalf("after boundary = %+v err=%v", after, err)
	}

	// Trimming cannot shift what an old line cursor meant. A backwards cursor
	// whose line was trimmed reports a gap and resets to the newest page, the
	// same policy as the file source.
	initialBefore := page.NextBefore
	for i := 4; i < 7; i++ {
		_, _ = tail.Write([]byte(runtimeLogTestLine(now.Add(time.Duration(i)*time.Second), "info", fmt.Sprintf("line %d", i), "")))
	}
	trimmed, err := tail.Query(RuntimeLogQuery{Before: initialBefore, Limit: 10})
	if err != nil || !trimmed.Gap || len(trimmed.Entries) != 3 || trimmed.Entries[0].Message != "line 4" {
		t.Fatalf("trimmed before cursor = %+v err=%v, want a gap reset to the newest page", trimmed, err)
	}

	// A tail from another process/restart has its own epoch; its cursor is a
	// gap, not a silently wrong slice.
	other := newRuntimeLogTail(3, 1<<20)
	_, _ = other.Write([]byte(runtimeLogTestLine(now, "info", "other process", "")))
	otherPage, _ := other.Query(RuntimeLogQuery{Limit: 10})
	foreign, err := tail.Query(RuntimeLogQuery{After: otherPage.NextAfter, Limit: 10})
	if err != nil || !foreign.Gap || len(foreign.Entries) != 3 {
		t.Fatalf("foreign epoch cursor = %+v err=%v, want a gap over the retained tail", foreign, err)
	}
}
