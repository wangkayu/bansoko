package application

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
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
// a runtime record. Anything else the line held is dropped. The task fields are
// the lifecycle facts a bug report needs; owner_id is deliberately absent, and
// raw errors never reach this projection.
type RuntimeLogEntry struct {
	At            time.Time
	Level         string
	Message       string
	Component     string
	Operation     string
	Outcome       string
	ScriptName    string
	ExitCode      *int
	ScriptSummary string
	HTTPStatus    int
	ReasonCode    string
	RequestID     string
	CorrelationID string
	ErrorClass    string
	OwnerType     string
	Kind          string
	WorkItemID    string
	Stage         string
	StateBefore   string
	StateAfter    string
	Disposition   string
	Attempt       int
	DurationMS    int64
	Retryable     *bool
}

// RuntimeLogTail is an io.Writer that keeps the most recent complete JSON
// log lines. slog's JSON handler issues exactly one Write per record.
type RuntimeLogTail struct {
	mu       sync.Mutex
	maxLines int
	maxBytes int
	lines    []string
	// seqs are stable, monotonically increasing sequence numbers, one per
	// retained line. Cursors name a sequence and never a slice position, so a
	// trim does not silently shift what a cursor refers to. epoch identifies
	// this process's tail: a cursor from another process (or from before a
	// restart) is a gap, because sequence numbers restart there.
	seqs    []uint64
	nextSeq uint64
	epoch   string
	bytes   int
}

// NewRuntimeLogTail builds a tail with the product bounds.
func NewRuntimeLogTail() *RuntimeLogTail {
	return newRuntimeLogTail(RuntimeLogMaxLines, RuntimeLogMaxBytes)
}

func newRuntimeLogTail(maxLines, maxBytes int) *RuntimeLogTail {
	return &RuntimeLogTail{maxLines: maxLines, maxBytes: maxBytes, epoch: newRuntimeLogTailEpoch()}
}

func newRuntimeLogTailEpoch() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
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
	t.nextSeq++
	t.lines = append(t.lines, line)
	t.seqs = append(t.seqs, t.nextSeq)
	t.bytes += len(line)
	for len(t.lines) > t.maxLines || t.bytes > t.maxBytes {
		t.bytes -= len(t.lines[0])
		t.lines[0] = ""
		t.lines = t.lines[1:]
		t.seqs = t.seqs[1:]
	}
	return len(p), nil
}

// Entries returns up to limit of the most recent lines at or above level that
// match every whitespace-separated (case-insensitive) term in query, in the
// order they were written (oldest first). Both filters apply while scanning
// backwards, before the limit is taken; searching only ever sees the
// allowlisted fields. That ordering is the same one a future reader of on-disk
// log segments must keep so it can filter across volumes before paging.
func (t *RuntimeLogTail) Entries(level RuntimeLogLevel, query string, limit int) []RuntimeLogEntry {
	if limit <= 0 || limit > RuntimeLogMaxLimit {
		limit = RuntimeLogMaxLimit
	}
	terms := runtimeLogSearchTerms(query)
	t.mu.Lock()
	snapshot := append([]string(nil), t.lines...)
	t.mu.Unlock()

	out := make([]RuntimeLogEntry, 0, min(limit, len(snapshot)))
	for i := len(snapshot) - 1; i >= 0 && len(out) < limit; i-- {
		entry, ok := parseRuntimeLogLine(snapshot[i])
		if !ok || !runtimeLevelAllows(level, entry.Level) || !runtimeLogEntryMatches(entry, terms) {
			continue
		}
		out = append(out, entry)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func runtimeLogSearchTerms(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	return words
}

// runtimeLogEntryMatches requires every term to occur in the same lowercased
// projection of the allowlisted values. Fields added to RuntimeLogEntry must be
// added here too, otherwise search silently ignores them.
func runtimeLogEntryMatches(entry RuntimeLogEntry, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	text := strings.ToLower(runtimeLogSearchText(entry))
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

// runtimeLogSearchText mirrors the plain-text line the owner sees and copies,
// built only from allowlisted values. Keys are included so a term copied from a
// displayed `key=value` pair also matches.
func runtimeLogSearchText(entry RuntimeLogEntry) string {
	fields := []string{entry.At.Format(time.RFC3339Nano), entry.Level}
	if source := strings.Trim(strings.Join([]string{entry.Component, entry.Operation}, "."), "."); source != "" {
		fields = append(fields, source)
	}
	if entry.Message != "" {
		fields = append(fields, entry.Message)
	}
	for _, pair := range []struct{ key, value string }{
		{"kind", entry.Kind},
		{"work_item_id", entry.WorkItemID},
		{"stage", entry.Stage},
		{"state_before", entry.StateBefore},
		{"state_after", entry.StateAfter},
		{"disposition", entry.Disposition},
		{"owner_type", entry.OwnerType},
		{"outcome", entry.Outcome},
		{"script_name", entry.ScriptName},
		{"script_summary", entry.ScriptSummary},
		{"reason_code", entry.ReasonCode},
		{"error_class", entry.ErrorClass},
		{"request_id", entry.RequestID},
		{"correlation_id", entry.CorrelationID},
	} {
		if pair.value != "" {
			fields = append(fields, pair.key+"="+pair.value)
		}
	}
	if entry.Attempt > 0 {
		fields = append(fields, "attempt="+strconv.Itoa(entry.Attempt))
	}
	if entry.DurationMS > 0 {
		fields = append(fields, "duration_ms="+strconv.FormatInt(entry.DurationMS, 10))
	}
	if entry.Retryable != nil {
		fields = append(fields, "retryable="+strconv.FormatBool(*entry.Retryable))
	}
	if entry.ExitCode != nil {
		fields = append(fields, "exit_code="+strconv.Itoa(*entry.ExitCode))
	}
	if entry.HTTPStatus != 0 {
		fields = append(fields, "http_status="+strconv.Itoa(entry.HTTPStatus))
	}
	return strings.Join(fields, " ")
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
	var raw struct {
		Timestamp     string `json:"timestamp"`
		Level         string `json:"level"`
		Message       string `json:"message"`
		Component     string `json:"component"`
		Operation     string `json:"operation"`
		Outcome       string `json:"outcome"`
		ScriptName    string `json:"script_name"`
		ExitCode      *int   `json:"exit_code"`
		ScriptSummary string `json:"script_summary"`
		HTTPStatus    int    `json:"http_status"`
		ReasonCode    string `json:"reason_code"`
		RequestID     string `json:"request_id"`
		CorrelationID string `json:"correlation_id"`
		ErrorClass    string `json:"error_class"`
		OwnerType     string `json:"owner_type"`
		Kind          string `json:"kind"`
		WorkItemID    string `json:"work_item_id"`
		Stage         string `json:"stage"`
		StateBefore   string `json:"state_before"`
		StateAfter    string `json:"state_after"`
		Disposition   string `json:"disposition"`
		Attempt       int    `json:"attempt"`
		DurationMS    int64  `json:"duration_ms"`
		Retryable     *bool  `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return RuntimeLogEntry{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, raw.Timestamp)
	if err != nil {
		return RuntimeLogEntry{}, false
	}
	if raw.Attempt < 0 {
		raw.Attempt = 0
	}
	if raw.DurationMS < 0 {
		raw.DurationMS = 0
	}
	if raw.ExitCode != nil && *raw.ExitCode < 0 {
		raw.ExitCode = nil
	}
	summary := safeRuntimeScriptSummary(raw.ScriptSummary)
	if summary == "" || raw.HTTPStatus < 100 || raw.HTTPStatus > 599 {
		raw.HTTPStatus = 0
	}
	return RuntimeLogEntry{
		At:            at.UTC(),
		Level:         safeRuntimeField(raw.Level),
		Message:       safeRuntimeMessage(raw.Message),
		Component:     safeRuntimeField(raw.Component),
		Operation:     safeRuntimeField(raw.Operation),
		Outcome:       safeRuntimeField(raw.Outcome),
		ScriptName:    safeRuntimeScriptName(raw.ScriptName),
		ExitCode:      raw.ExitCode,
		ScriptSummary: summary,
		HTTPStatus:    raw.HTTPStatus,
		ReasonCode:    safeRuntimeField(raw.ReasonCode),
		RequestID:     safeRuntimeRequestID(raw.RequestID),
		CorrelationID: safeRuntimeField(raw.CorrelationID),
		ErrorClass:    safeRuntimeField(raw.ErrorClass),
		OwnerType:     safeRuntimeField(raw.OwnerType),
		Kind:          safeRuntimeField(raw.Kind),
		WorkItemID:    safeRuntimeField(raw.WorkItemID),
		Stage:         safeRuntimeField(raw.Stage),
		StateBefore:   safeRuntimeField(raw.StateBefore),
		StateAfter:    safeRuntimeField(raw.StateAfter),
		Disposition:   safeRuntimeField(raw.Disposition),
		Attempt:       raw.Attempt,
		DurationMS:    raw.DurationMS,
		Retryable:     raw.Retryable,
	}, true
}

// Query answers a page from the bounded in-memory tail. It is the fallback
// when the on-disk history is unavailable. Cursors minted from the tail carry a
// process epoch and a stable line sequence (plus a hash for a line cursor), so a
// trimmed tail or a different process reports a gap instead of silently
// returning the wrong lines. Like the file source, every page carries
// NextAfter; an incremental read with no matches still advances to the end of
// what it scanned.
func (t *RuntimeLogTail) Query(q RuntimeLogQuery) (RuntimeLogPage, error) {
	page := RuntimeLogPage{Source: RuntimeLogSourceMemory}
	if t == nil {
		return page, nil
	}
	if q.Before != "" && q.After != "" {
		return RuntimeLogPage{}, ErrRuntimeLogCursorInvalid
	}
	limit := normalizeRuntimeLogLimit(q.Limit)
	level := q.Level
	if level == "" {
		level = RuntimeLogAll
	}
	terms := runtimeLogSearchTerms(q.Query)
	t.mu.Lock()
	snapshot := append([]string(nil), t.lines...)
	seqs := append([]uint64(nil), t.seqs...)
	nextSeq := t.nextSeq
	epoch := t.epoch
	t.mu.Unlock()

	cursorKind := ""
	var cursor runtimeLogCursor
	useCursor := false
	if q.Before != "" || q.After != "" {
		value := q.Before
		if q.After != "" {
			cursorKind = "after"
			value = q.After
		} else {
			cursorKind = "before"
		}
		decoded, err := decodeRuntimeLogCursor(value)
		if err != nil {
			return RuntimeLogPage{}, err
		}
		if decoded.kind != "m" || decoded.epoch != epoch {
			// A file cursor, or a tail cursor from another process/restart,
			// cannot address this tail.
			page.Gap = true
		} else {
			cursor = decoded
			useCursor = true
			if len(seqs) > 0 && cursor.seq < seqs[0] {
				page.Gap = true
				if cursorKind == "before" {
					// The line this backwards page started from was trimmed.
					// Reset to the newest page instead of leaving the reader
					// with an empty window.
					useCursor = false
				}
			}
			if cursor.seq > nextSeq+1 {
				// Not a boundary this tail could have minted.
				page.Gap = true
				if cursorKind == "before" {
					useCursor = false
				}
			}
			if useCursor && cursor.hash != 0 {
				found := false
				for index, seq := range seqs {
					if seq != cursor.seq {
						continue
					}
					found = true
					if runtimeLogLineHash(snapshot[index]) != cursor.hash {
						page.Gap = true
					}
					break
				}
				if !found {
					page.Gap = true
					if cursorKind == "before" {
						useCursor = false
					}
				}
			}
		}
	}

	type item struct {
		seq   uint64
		line  string
		entry RuntimeLogEntry
	}
	items := make([]item, 0, len(snapshot))
	for index, line := range snapshot {
		seq := seqs[index]
		if useCursor {
			switch cursorKind {
			case "after":
				if seq < cursor.seq {
					continue
				}
			case "before":
				if seq >= cursor.seq {
					continue
				}
			}
		}
		entry, ok := parseRuntimeLogLine(line)
		if !ok || !runtimeLogEntryMatchesQuery(entry, level, terms, q.WorkItemID) {
			continue
		}
		items = append(items, item{seq: seq, line: line, entry: entry})
	}
	if len(items) > limit {
		if cursorKind == "before" || (cursorKind == "" && q.After == "") {
			// Backwards paging and the first page take the newest matches.
			items = items[len(items)-limit:]
		} else {
			items = items[:limit]
		}
	}
	page.Entries = make([]RuntimeLogEntry, 0, len(items))
	for _, item := range items {
		page.Entries = append(page.Entries, item.entry)
	}
	if len(items) > 0 {
		page.NextBefore = encodeRuntimeLogMemoryLineCursor(epoch, items[0].seq, items[0].line)
	}

	switch {
	case cursorKind == "before" && useCursor:
		// Older lines belong before the region this page was read from.
		page.NextAfter = encodeRuntimeLogMemoryBoundary(epoch, cursor.seq)
	case cursorKind == "after" && len(items) == limit:
		// The page was capped, so the next unread line is right after the
		// last one returned; a burst larger than the page must not be skipped.
		page.NextAfter = encodeRuntimeLogMemoryBoundary(epoch, items[len(items)-1].seq+1)
	default:
		// Initial, empty, and fully-scanned incremental pages advance to the
		// next sequence this tail will assign.
		page.NextAfter = encodeRuntimeLogMemoryBoundary(epoch, nextSeq+1)
	}
	return page, nil
}

// NewTeedRuntimeLogger is NewJSONRuntimeLogger whose records also fill a
// bounded in-memory tail, readable through Tail.
func NewTeedRuntimeLogger(w io.Writer) *JSONRuntimeLogger {
	return NewTeedRuntimeLoggerWithStore(w, nil)
}

// NewPersistentRuntimeLogger is NewTeedRuntimeLogger whose records also fill
// the on-disk history under dir. Construction never fails: an unwritable
// directory leaves the logger on its in-memory tail until the store recovers.
func NewPersistentRuntimeLogger(w io.Writer, dir string) *JSONRuntimeLogger {
	return NewTeedRuntimeLoggerWithStore(w, NewRuntimeLogStore(RuntimeLogStoreOptions{Dir: dir}))
}

// NewTeedRuntimeLoggerWithStore tees records to the given writer, the bounded
// tail, and (when non-nil) the on-disk store.
func NewTeedRuntimeLoggerWithStore(w io.Writer, store *RuntimeLogStore) *JSONRuntimeLogger {
	if w == nil {
		w = io.Discard
	}
	tail := NewRuntimeLogTail()
	writers := []io.Writer{w, tail}
	if store != nil {
		writers = append(writers, store)
	}
	logger := NewJSONRuntimeLogger(io.MultiWriter(writers...))
	logger.tail = tail
	logger.store = store
	return logger
}

// Tail returns the in-memory tail, or nil for a logger built without one.
func (l *JSONRuntimeLogger) Tail() *RuntimeLogTail {
	if l == nil {
		return nil
	}
	return l.tail
}
