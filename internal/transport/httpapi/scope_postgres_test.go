package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"bansoko/internal/redact"
	"bansoko/internal/transport/httpapi/generated"
)

// scopeRig is an authenticating rig (no SkipAuth) with an owner session.
func scopeRig(t *testing.T) (*testRig, map[string]string) {
	t.Helper()
	rig := newTestRig(t, "development", false)
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, body)
	}
	owner := decodeBody[generated.LoginResult](t, body)
	if owner.SessionToken == nil || *owner.SessionToken == "" {
		t.Fatal("bootstrap owner returned no session token")
	}
	return rig, map[string]string{"Authorization": "Bearer " + *owner.SessionToken}
}

// createScopedToken creates a PAT with the given scopes as the given bearer and
// returns its id and Authorization headers.
func createScopedToken(t *testing.T, rig *testRig, as map[string]string, name string, scopes ...string) (string, map[string]string) {
	t.Helper()
	input := map[string]any{"name": name}
	if scopes != nil {
		input["scopes"] = scopes
	}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", input, as)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create %s token %v: %d %s", name, scopes, recorder.Code, body)
	}
	created := decodeBody[generated.ApiTokenCreateResult](t, body)
	want := scopes
	if want == nil {
		want = []string{"read", "write"}
	}
	if got := scopeStrings(created.Scopes); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("created scopes = %v, want %v", got, want)
	}
	return created.TokenId, map[string]string{"Authorization": "Bearer " + created.Token}
}

func scopeStrings(scopes generated.ApiTokenScopes) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, string(scope))
	}
	return out
}

func assertScopeRefused(t *testing.T, surface string, recorder *httptest.ResponseRecorder, body []byte, scope string) {
	t.Helper()
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("%s: got %d %s, want 403", surface, recorder.Code, body)
	}
	failure := decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "TOKEN_SCOPE_INSUFFICIENT" || failure.RequiredScope == nil || string(*failure.RequiredScope) != scope {
		t.Fatalf("%s: unexpected refusal %+v (%s)", surface, failure, body)
	}
}

// TestApiTokenScopesAreEnforced covers the scope model end to end: a read
// token reads but cannot write (403 naming write); a read,write token cannot
// read a raw token or mint/rotate a token with secrets (no escalation); an
// invalid scope set is 422; the list shows each token's scopes; a session
// holds every scope.
func TestApiTokenScopesAreEnforced(t *testing.T) {
	rig, session := scopeRig(t)

	readID, readOnly := createScopedToken(t, rig, session, "reader", "read")
	writerID, writer := createScopedToken(t, rig, session, "writer", "read", "write")
	secretID, _ := createScopedToken(t, rig, session, "full", "read", "write", "secrets")
	_, _ = createScopedToken(t, rig, session, "defaulted")

	// Read operations, including a read-only POST preview, are open to read.
	if recorder, body := rig.do(t, http.MethodGet, "/api/v1/anime", nil, readOnly); recorder.Code != http.StatusOK {
		t.Fatalf("read token GET: %d %s", recorder.Code, body)
	}
	if recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/rss-list-view", map[string]any{}, readOnly); recorder.Code == http.StatusForbidden {
		t.Fatalf("a read-only POST preview must not need write: %s", body)
	}

	// Writes are refused for read, before the handler runs.
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", draftPayload(), readOnly)
	assertScopeRefused(t, "read token candidate save", recorder, body, "write")
	recorder, body = rig.do(t, http.MethodDelete, "/api/v1/api-tokens/"+writerID, nil, readOnly)
	assertScopeRefused(t, "read token revoke", recorder, body, "write")
	if recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", draftPayload(), writer); recorder.Code != http.StatusCreated {
		t.Fatalf("read,write token candidate save: %d %s", recorder.Code, body)
	}

	// The raw value needs secrets; a token never mints or rotates beyond itself.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+readID+"/raw", nil, writer)
	assertScopeRefused(t, "read,write raw read", recorder, body, "secrets")
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/api-tokens",
		map[string]any{"name": "escalate", "scopes": []string{"read", "write", "secrets"}}, writer)
	assertScopeRefused(t, "read,write creating a secrets token", recorder, body, "secrets")
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/api-tokens/"+secretID, nil, writer)
	assertScopeRefused(t, "read,write rotating a secrets token", recorder, body, "secrets")
	if _, child := createScopedToken(t, rig, writer, "child", "read"); child == nil {
		t.Fatal("a token can mint a narrower token")
	}

	// Invalid sets are refused as input errors.
	for _, scopes := range [][]string{{"write"}, {"read", "read"}, {"read", "admin"}, {}} {
		recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", map[string]any{"name": "bad", "scopes": scopes}, session)
		if recorder.Code != http.StatusUnprocessableEntity && recorder.Code != http.StatusBadRequest {
			t.Fatalf("scopes %v: got %d %s, want 422", scopes, recorder.Code, body)
		}
	}

	// Rotation keeps the set; the list shows it.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/api-tokens/"+readID, nil, session)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotate read token: %d %s", recorder.Code, body)
	}
	rotated := decodeBody[generated.ApiTokenCreateResult](t, body)
	if got := scopeStrings(rotated.Scopes); strings.Join(got, ",") != "read" {
		t.Fatalf("rotation changed the scope set: %v", got)
	}
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", draftPayload(),
		map[string]string{"Authorization": "Bearer " + rotated.Token})
	assertScopeRefused(t, "rotated read token write", recorder, body, "write")

	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens", nil, session)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list: %d %s", recorder.Code, body)
	}
	listed := map[string]string{}
	for _, token := range decodeBody[[]generated.ApiToken](t, body) {
		listed[token.Name] = strings.Join(scopeStrings(token.Scopes), ",")
	}
	want := map[string]string{"reader": "read", "writer": "read,write", "full": "read,write,secrets", "defaulted": "read,write", "child": "read"}
	for name, scopes := range want {
		if listed[name] != scopes {
			t.Fatalf("list scopes for %s = %q, want %q (all %v)", name, listed[name], scopes, listed)
		}
	}

	// The session holds every scope.
	if recorder, body := rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+secretID+"/raw", nil, session); recorder.Code != http.StatusOK {
		t.Fatalf("session raw read: %d %s", recorder.Code, body)
	}
}

// mcpCallToolAs is mcpCallTool with the caller's Authorization header on both
// the handshake and the call, so the replayed request runs as that bearer.
func mcpCallToolAs(t *testing.T, rig *testRig, headers map[string]string, tool string, args map[string]any) []byte {
	t.Helper()
	send := func(sessionID, method string, params map[string]any) (string, []byte) {
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		rec := httptest.NewRecorder()
		rig.handler.ServeHTTP(rec, req)
		return rec.Header().Get("Mcp-Session-Id"), rec.Body.Bytes()
	}
	sessionID, body := send("", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "scope-test", "version": "1.0.0"},
	})
	if sessionID == "" {
		t.Fatalf("initialize did not return a session: %s", body)
	}
	_, body = send(sessionID, "tools/call", map[string]any{"name": tool, "arguments": args})
	return body
}

// TestMCPToolsRunWithTheBearerScopes proves the MCP surface inherits the scope
// check: a write tool called with a read token fails with the same refusal,
// while a read tool succeeds.
func TestMCPToolsRunWithTheBearerScopes(t *testing.T) {
	rig, session := scopeRig(t)
	_, readOnly := createScopedToken(t, rig, session, "agent", "read")

	body := mcpCallToolAs(t, rig, readOnly, "create_candidate", map[string]any{"draft": draftPayload()["draft"]})
	_, isError, text := decodeMCPToolResult(t, "create_candidate", body)
	if !isError || !strings.Contains(text, "TOKEN_SCOPE_INSUFFICIENT") || !strings.Contains(text, "write") {
		t.Fatalf("a write tool with a read token must be refused with the scope code: error=%v %s", isError, text)
	}
	var count int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM anime_config_candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("the refused tool call wrote %d candidates", count)
	}

	body = mcpCallToolAs(t, rig, readOnly, "list_candidates", map[string]any{})
	if _, isError, text := decodeMCPToolResult(t, "list_candidates", body); isError {
		t.Fatalf("a read tool with a read token must succeed: %s", text)
	}
}

// TestSecretPlaceholderRoundTrip covers the x-secret contract on a candidate:
// a token without secrets reads the fixed placeholder (never the passkey); a
// PATCH that sends the placeholder back keeps the stored locator for the same
// source; a placeholder that matches no single stored source is refused; the
// session and a secrets token read the stored value.
func TestSecretPlaceholderRoundTrip(t *testing.T) {
	rig, session := scopeRig(t)
	_, writer := createScopedToken(t, rig, session, "agent", "read", "write")
	_, full := createScopedToken(t, rig, session, "full", "read", "write", "secrets")
	const (
		rawLocator = "https://pt.example.test/rss?passkey=SEC033_PASSKEY_MARKER"
		marker     = "SEC033_PASSKEY_MARKER"
	)

	payload := draftPayload()
	payload["draft"].(map[string]any)["rss_sources"].([]any)[0].(map[string]any)["locator"] = rawLocator
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", payload, session)
	if recorder.Code != http.StatusCreated || !bytes.Contains(body, []byte(marker)) {
		t.Fatalf("session save must return the stored locator: %d %s", recorder.Code, body)
	}
	candidate := decodeBody[generated.AnimeConfigCandidate](t, body)
	path := "/api/v1/anime-config-candidates/" + candidate.Id

	readAs := func(headers map[string]string) (map[string]any, []byte) {
		t.Helper()
		recorder, body := rig.do(t, http.MethodGet, path, nil, headers)
		if recorder.Code != http.StatusOK {
			t.Fatalf("candidate GET: %d %s", recorder.Code, body)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded, body
	}
	firstLocator := func(candidate map[string]any) string {
		t.Helper()
		draft := candidate["draft"].(map[string]any)
		return draft["rss_sources"].([]any)[0].(map[string]any)["locator"].(string)
	}

	masked, maskedBody := readAs(writer)
	if bytes.Contains(maskedBody, []byte(marker)) || firstLocator(masked) != redact.SecretPlaceholder {
		t.Fatalf("a token without secrets must read the placeholder: %s", maskedBody)
	}
	recorder, listBody := rig.do(t, http.MethodGet, "/api/v1/anime-config-candidates", nil, writer)
	if recorder.Code != http.StatusOK || bytes.Contains(listBody, []byte(marker)) {
		t.Fatalf("the candidate list must mask too: %d %s", recorder.Code, listBody)
	}
	if full, body := readAs(full); firstLocator(full) != rawLocator {
		t.Fatalf("a secrets token reads the stored value: %s", body)
	}

	// Send the masked draft back with a title change: the locator is kept.
	draft := masked["draft"].(map[string]any)
	draft["title"] = "API Show Renamed"
	version := strconv.FormatInt(int64(masked["version"].(float64)), 10)
	recorder, body = rig.do(t, http.MethodPatch, path, map[string]any{"draft": draft},
		map[string]string{"Authorization": writer["Authorization"], "If-Match": version})
	if recorder.Code != http.StatusOK {
		t.Fatalf("placeholder PATCH: %d %s", recorder.Code, body)
	}
	if bytes.Contains(body, []byte(marker)) {
		t.Fatalf("the PATCH response must stay masked: %s", body)
	}
	stored, storedBody := readAs(session)
	if firstLocator(stored) != rawLocator || stored["draft"].(map[string]any)["title"] != "API Show Renamed" {
		t.Fatalf("the placeholder PATCH must keep the stored locator and apply the edit: %s", storedBody)
	}

	// A placeholder with no single stored match is refused, nothing written.
	ambiguous, _ := readAs(writer)
	sources := ambiguous["draft"].(map[string]any)["rss_sources"].([]any)
	duplicate := map[string]any{}
	for key, value := range sources[0].(map[string]any) {
		duplicate[key] = value
	}
	duplicate["group_name"] = "OtherGroup"
	ambiguous["draft"].(map[string]any)["rss_sources"] = append(sources, duplicate)
	version = strconv.FormatInt(int64(ambiguous["version"].(float64)), 10)
	recorder, body = rig.do(t, http.MethodPatch, path, map[string]any{"draft": ambiguous["draft"]},
		map[string]string{"Authorization": writer["Authorization"], "If-Match": version})
	if recorder.Code != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("SECRET_PLACEHOLDER_UNRESOLVED")) {
		t.Fatalf("an unresolvable placeholder must be 422 SECRET_PLACEHOLDER_UNRESOLVED: %d %s", recorder.Code, body)
	}

	mismatched, _ := readAs(writer)
	mismatched["draft"].(map[string]any)["rss_sources"].([]any)[0].(map[string]any)["locator_display"] = "https://elsewhere.example.test"
	version = strconv.FormatInt(int64(mismatched["version"].(float64)), 10)
	recorder, body = rig.do(t, http.MethodPatch, path, map[string]any{"draft": mismatched["draft"]},
		map[string]string{"Authorization": writer["Authorization"], "If-Match": version})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a placeholder whose display names another host must be 422: %d %s", recorder.Code, body)
	}

	// A new candidate cannot carry the placeholder: there is nothing to keep.
	fresh := draftPayload()
	fresh["draft"].(map[string]any)["rss_sources"].([]any)[0].(map[string]any)["locator"] = redact.SecretPlaceholder
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", fresh, writer)
	if recorder.Code != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("SECRET_PLACEHOLDER_UNRESOLVED")) {
		t.Fatalf("a new candidate with the placeholder must be 422: %d %s", recorder.Code, body)
	}
	if final, body := readAs(session); firstLocator(final) != rawLocator {
		t.Fatalf("refused writes changed the stored locator: %s", body)
	}
}
