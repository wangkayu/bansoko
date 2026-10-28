package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
