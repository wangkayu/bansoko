package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accessdomain "bansoko/internal/accesscontrol/domain"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

func runtimeLogAs(t *testing.T, account accessdomain.Account) *httptest.ResponseRecorder {
	t.Helper()
	logger := observabilityapplication.NewTeedRuntimeLogger(io.Discard)
	logger.Warn(context.Background(), observabilityport.RuntimeEvent{Message: "provider search failed", Component: "metadata_matcher", Operation: "tmdb_search", Outcome: "failure", ReasonCode: "tmdb_search_failed"})
	server := &Server{deps: Dependencies{RuntimeLogger: logger}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/observability/runtime-log", nil)
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response := httptest.NewRecorder()
	server.GetRuntimeLog(response, request, generated.GetRuntimeLogParams{})
	return response
}

func TestRuntimeLogIsOnlyForTheWebSession(t *testing.T) {
	token := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...)}
	response := runtimeLogAs(t, token)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "WEB_SESSION_REQUIRED") {
		t.Fatalf("token read = %d %s, want 403 WEB_SESSION_REQUIRED", response.Code, response.Body.String())
	}

	token.WebSession = true
	response = runtimeLogAs(t, token)
	if response.Code != http.StatusOK {
		t.Fatalf("web session read = %d %s", response.Code, response.Body.String())
	}
	var page generated.RuntimeLogPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Lines) != 1 {
		t.Fatalf("page = %+v err=%v", page, err)
	}
}

func TestRuntimeLogMapsSafeTaskFieldsAndAppliesTheTextFilter(t *testing.T) {
	logger := observabilityapplication.NewTeedRuntimeLogger(io.Discard)
	retryable := false
	logger.Warn(context.Background(), observabilityport.RuntimeEvent{
		Message: "dispatch retry", Component: "worker", Operation: "dispatch",
		OwnerType: "subscription", Kind: "work_item", WorkItemID: "wi-1",
		Stage: "download", StateBefore: "queued", StateAfter: "running",
		Disposition: "retry", Attempt: 2, DurationMS: 1234, Retryable: &retryable,
	})
	logger.Info(context.Background(), observabilityport.RuntimeEvent{Message: "unrelated line", Component: "worker"})

	account := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...), WebSession: true}
	server := &Server{deps: Dependencies{RuntimeLogger: logger}}
	query := "DISPATCH retry"
	request := httptest.NewRequest(http.MethodGet, "/api/v1/observability/runtime-log?q=dispatch", nil)
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response := httptest.NewRecorder()
	server.GetRuntimeLog(response, request, generated.GetRuntimeLogParams{Q: &query})
	if response.Code != http.StatusOK {
		t.Fatalf("read = %d %s", response.Code, response.Body.String())
	}
	var page generated.RuntimeLogPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Lines) != 1 {
		t.Fatalf("filtered lines = %+v", page.Lines)
	}
	line := page.Lines[0]
	if line.OwnerType == nil || *line.OwnerType != "subscription" || line.Kind == nil || *line.Kind != "work_item" ||
		line.WorkItemId == nil || *line.WorkItemId != "wi-1" || line.Stage == nil || *line.Stage != "download" ||
		line.StateBefore == nil || *line.StateBefore != "queued" || line.StateAfter == nil || *line.StateAfter != "running" ||
		line.Disposition == nil || *line.Disposition != "retry" {
		t.Fatalf("task fields not mapped: %+v", line)
	}
	if line.Attempt == nil || *line.Attempt != 2 || line.DurationMs == nil || *line.DurationMs != 1234 {
		t.Fatalf("numeric fields not mapped: %+v", line)
	}
	if line.Retryable == nil || *line.Retryable {
		t.Fatalf("retryable=false not mapped: %+v", line.Retryable)
	}
}

func persistentRuntimeLogServer(t *testing.T) (*Server, *observabilityapplication.JSONRuntimeLogger) {
	t.Helper()
	logger := observabilityapplication.NewPersistentRuntimeLogger(io.Discard, filepath.Join(t.TempDir(), observabilityapplication.RuntimeLogDirName))
	t.Cleanup(func() { _ = logger.Close() })
	server := &Server{deps: Dependencies{RuntimeLogger: logger}}
	return server, logger
}

func readRuntimeLogResponse(t *testing.T, server *Server, params generated.GetRuntimeLogParams) generated.RuntimeLogPage {
	t.Helper()
	account := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...), WebSession: true}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/observability/runtime-log", nil)
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response := httptest.NewRecorder()
	server.GetRuntimeLog(response, request, params)
	if response.Code != http.StatusOK {
		t.Fatalf("read = %d %s", response.Code, response.Body.String())
	}
	var page generated.RuntimeLogPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return page
}

func TestRuntimeLogScriptNameAndExitCode(t *testing.T) {
	server, logger := persistentRuntimeLogServer(t)
	ctx := context.Background()
	zero, nonzero, noExit := 0, 7, -1
	for _, event := range []observabilityport.RuntimeEvent{
		{Outcome: "completed", ScriptName: "refresh-library.sh", ExitCode: &zero},
		{Outcome: "failed", ScriptName: "refresh-library.sh", ExitCode: &nonzero},
		{Outcome: "missing", ScriptName: "refresh-library.sh"},
		{Outcome: "failed", ScriptName: "/private/scripts/example.sh", ExitCode: &noExit},
	} {
		event.Message, event.Component, event.Operation = "event script finished", "event_actions", "execute"
		logger.Info(ctx, event)
	}
	page := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{})
	if len(page.Lines) != 4 {
		t.Fatalf("lines: %+v", page.Lines)
	}
	for i, want := range []*int{&zero, &nonzero, nil, nil} {
		line := page.Lines[i]
		if (want == nil && line.ExitCode != nil) || (want != nil && (line.ExitCode == nil || *line.ExitCode != *want)) {
			t.Fatalf("row %d: wrong exit code: %+v", i, line)
		}
		if i < 3 && (line.ScriptName == nil || *line.ScriptName != "refresh-library.sh") || i == 3 && line.ScriptName != nil {
			t.Fatalf("row %d: wrong script name: %+v", i, line)
		}
	}
	query := "script_name=refresh-library.sh exit_code=0"
	matched := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{Q: &query})
	if len(matched.Lines) != 1 || matched.Lines[0].Outcome == nil || *matched.Lines[0].Outcome != "completed" {
		t.Fatalf("search result: %+v", matched.Lines)
	}
}

func TestRuntimeLogExactWorkItemAndCursorContract(t *testing.T) {
	server, logger := persistentRuntimeLogServer(t)
	ctx := context.Background()
	logger.Info(ctx, observabilityport.RuntimeEvent{Message: "first task", Component: "worker", WorkItemID: "wi-1", Kind: "work_item"})
	logger.Info(ctx, observabilityport.RuntimeEvent{Message: "second task", Component: "worker", WorkItemID: "wi-2", Kind: "work_item"})

	workItem := "wi-2"
	page := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{WorkItemId: &workItem})
	if page.Source != generated.RuntimeLogPageSourceFile {
		t.Fatalf("source = %q, want file", page.Source)
	}
	if len(page.Lines) != 1 || page.Lines[0].WorkItemId == nil || *page.Lines[0].WorkItemId != "wi-2" {
		t.Fatalf("exact work item page = %+v", page.Lines)
	}
	if page.NextAfter == nil || page.NextBefore == nil {
		t.Fatalf("cursors missing: %+v", page)
	}

	watermark := page.NextAfter
	page = readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{After: page.NextAfter})
	if len(page.Lines) != 0 || page.NextAfter == nil || *page.NextAfter != *watermark {
		t.Fatalf("after watermark = %+v, want an empty poll with the same watermark", page)
	}
	// Even an empty poll is pollable: its watermark is still present and can be
	// used again without falling back to an uncursored full read.
	again := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{After: page.NextAfter})
	if len(again.Lines) != 0 || again.NextAfter == nil {
		t.Fatalf("repeated empty poll = %+v", again)
	}

	limit := 1
	all := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{Limit: &limit})
	if len(all.Lines) != 1 || all.Lines[0].Message != "second task" {
		t.Fatalf("default page = %+v", all.Lines)
	}
	older := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{Before: all.NextBefore})
	if len(older.Lines) != 1 || older.Lines[0].Message != "first task" {
		t.Fatalf("before page = %+v", older.Lines)
	}
}

// TestRuntimeLogSwitchesBackToFileAfterRepair drives the HTTP handler against
// real files: a memory cursor obtained while the directory is unusable must be
// answered from the disk after repair, with a gap, instead of staying on memory.
func TestRuntimeLogSwitchesBackToFileAfterRepair(t *testing.T) {
	blocking := filepath.Join(t.TempDir(), observabilityapplication.RuntimeLogDirName)
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	logger := observabilityapplication.NewPersistentRuntimeLogger(io.Discard, blocking)
	t.Cleanup(func() { _ = logger.Close() })
	server := &Server{deps: Dependencies{RuntimeLogger: logger}}
	ctx := context.Background()

	logger.Info(ctx, observabilityport.RuntimeEvent{Message: "memory only", Component: "worker"})
	first := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{})
	if first.Source != generated.RuntimeLogPageSourceMemory || first.NextAfter == nil {
		t.Fatalf("blocked read = %+v, want a memory page with a watermark", first)
	}

	if err := os.Remove(blocking); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	logger.Info(ctx, observabilityport.RuntimeEvent{Message: "back online", Component: "worker"})
	after := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{After: first.NextAfter})
	if after.Source != generated.RuntimeLogPageSourceFile {
		t.Fatalf("recovered poll source = %q, want file", after.Source)
	}
	if after.Gap == nil || !*after.Gap {
		t.Fatalf("recovered poll gap = %v, want true", after.Gap)
	}
	if len(after.Lines) == 0 || after.Lines[len(after.Lines)-1].Message != "back online" {
		t.Fatalf("recovered poll lines = %+v, want the file line", after.Lines)
	}
}

// TestRuntimeLogEmptyFilterKeepsAPollableWatermarkAcrossABurst uses the real
// handler and real volumes: a filter with no matches must return a watermark,
// and a burst larger than the page limit must be read exactly once across
// successive after polls.
func TestRuntimeLogEmptyFilterKeepsAPollableWatermarkAcrossABurst(t *testing.T) {
	server, logger := persistentRuntimeLogServer(t)
	ctx := context.Background()
	logger.Info(ctx, observabilityport.RuntimeEvent{Message: "other work", Component: "worker", WorkItemID: "other"})

	workItem := "burst"
	one := 1
	initial := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{WorkItemId: &workItem, Limit: &one})
	if len(initial.Lines) != 0 || initial.NextAfter == nil {
		t.Fatalf("empty filter page = %+v, want no lines and a watermark", initial)
	}

	for index := 0; index < 5; index++ {
		logger.Info(ctx, observabilityport.RuntimeEvent{
			Message: fmt.Sprintf("burst %d", index), Component: "worker", WorkItemID: workItem,
		})
	}
	cursor := *initial.NextAfter
	var got []string
	limit := 2
	for poll := 0; poll < 10; poll++ {
		page := readRuntimeLogResponse(t, server, generated.GetRuntimeLogParams{
			WorkItemId: &workItem, Limit: &limit, After: &cursor,
		})
		for _, line := range page.Lines {
			got = append(got, line.Message)
		}
		if page.NextAfter == nil {
			t.Fatalf("poll %d returned no watermark: %+v", poll, page)
		}
		if *page.NextAfter == cursor {
			if len(page.Lines) > 0 {
				t.Fatalf("poll %d repeated the cursor with a full page", poll)
			}
			break
		}
		cursor = *page.NextAfter
	}
	want := []string{"burst 0", "burst 1", "burst 2", "burst 3", "burst 4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("burst pages = %v, want %v", got, want)
	}
}

func TestRuntimeLogRejectsConflictingAndInvalidCursors(t *testing.T) {
	server, _ := persistentRuntimeLogServer(t)
	before, after := "before", "after"
	account := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...), WebSession: true}
	for _, params := range []generated.GetRuntimeLogParams{
		{Before: &before, After: &after},
		{After: &before},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/observability/runtime-log", nil)
		request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
		response := httptest.NewRecorder()
		server.GetRuntimeLog(response, request, params)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("params %+v = %d %s, want 422", params, response.Code, response.Body.String())
		}
		var body generated.ErrorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		wantCode := "RUNTIME_LOG_CURSOR_INVALID"
		if params.Before != nil && params.After != nil {
			wantCode = "RUNTIME_LOG_CURSOR_CONFLICT"
		}
		if body.Code != wantCode {
			t.Fatalf("error code = %q, want %q", body.Code, wantCode)
		}
	}
}
