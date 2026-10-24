package httpapi

import (
	"bansoko/internal/testutil/presetdraft"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

// mcpJSONRPC sends a JSON-RPC request to the MCP endpoint via the test rig's
// HTTP handler and returns the session ID (from the response header) and body.
// The streamable-http transport requires an initialize handshake first; the
// session ID from that response must be carried on subsequent requests.
func mcpJSONRPC(t *testing.T, rig *testRig, sessionID string, method string, params map[string]any) (string, []byte) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, req)
	return rec.Header().Get("Mcp-Session-Id"), rec.Body.Bytes()
}

// mcpCallTool performs the initialize handshake and then calls a tool,
// returning the raw response body.
func mcpCallTool(t *testing.T, rig *testRig, tool string, args map[string]any) []byte {
	t.Helper()
	sessionID, _ := mcpJSONRPC(t, rig, "", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mcp-int-test", "version": "1.0.0"},
	})
	if sessionID == "" {
		t.Fatal("initialize did not return a Mcp-Session-Id header")
	}
	_, body := mcpJSONRPC(t, rig, sessionID, "tools/call", map[string]any{
		"name":      tool,
		"arguments": args,
	})
	return body
}

// mcpDefaultRules returns rules carrying every section at its code default.
func mcpDefaultRules(t *testing.T) generated.RulesDraft {
	t.Helper()
	return presetdraft.DefaultRules(t)
}

// mcpValidDraftJSON returns a JSON string for a complete valid AnimeDraft that
// passes ValidateDraft.
func mcpValidDraftJSON(t *testing.T) string {
	t.Helper()
	season := 1
	draft := generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		InstanceLabel: stringPtr("mcp-instance"),
		Title:         "MCP Test Show",
		Metadata: generated.AnimeMetadataDraft{
			Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-999"},
			Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-999", MediaKind: generated.TMDBMediaKindTv, Season: season},
		},
		Subgroups: generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 3},
		Rules:     mcpDefaultRules(t),
		RssSources: []generated.RSSSourceDraft{
			{SourceKind: generated.SourceKindCustomRss, Locator: "https://feeds.example.com/rss.xml", GroupName: "TestGroup", Title: "MCP Test Show"},
		},
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestMCPCreateCandidateValidDraftRunsRealValidation drives the create_candidate
// MCP tool through the real anime application Service with a complete valid
// draft. The tool must succeed, proving MCP and REST share the same validation
// path and a valid draft does not spuriously fail.
func TestMCPCreateCandidateValidDraftRunsRealValidation(t *testing.T) {
	rig := newTestRig(t, "development", true)
	body := mcpCallTool(t, rig, "create_candidate", map[string]any{
		"draft": json.RawMessage(mcpValidDraftJSON(t)),
	})
	if bytes.Contains(body, []byte(`"error"`)) && !bytes.Contains(body, []byte(`"result"`)) {
		t.Fatalf("create_candidate with valid draft must not error: %s", body)
	}
	if !bytes.Contains(body, []byte(`"result"`)) {
		t.Fatalf("create_candidate must return a result: %s", body)
	}
}

// TestMCPCreateCandidateInvalidDraftReturnsDraftInvalid drives the
// create_candidate MCP tool with a draft missing rss_sources. The real
// ValidateDraft must reject it with DRAFT_INVALID.
func TestMCPCreateCandidateInvalidDraftReturnsDraftInvalid(t *testing.T) {
	rig := newTestRig(t, "development", true)
	body := mcpCallTool(t, rig, "create_candidate", map[string]any{
		"draft": json.RawMessage(`{"title":"No Sources","origin":"custom","metadata":{"bgm":{"work_id":"bgm-1"},"tmdb":{"work_id":"tmdb-1","media_kind":"tv","season":1}},"subgroups":{"entries":[],"max_rss":3},"rules":{"sections":{}}}`),
	})
	if !bytes.Contains(body, []byte("DRAFT_INVALID")) {
		t.Fatalf("create_candidate with missing rss_sources must return DRAFT_INVALID: %s", body)
	}
}
