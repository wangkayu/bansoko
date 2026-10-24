package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServer implements the contract surface the CLI tests drive. It
// asserts the exact request the CLI must emit (path, method, headers, body)
// so the CLI cannot drift from the OpenAPI contract silently.
type fakeServer struct {
	sessionToken string
	pat          string

	requests []string
	bodies   map[string][]byte
	headers  map[string]http.Header
	animeID  string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestKey := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		requestKey += "?" + r.URL.RawQuery
	}
	f.requests = append(f.requests, requestKey)
	if f.bodies == nil {
		f.bodies = map[string][]byte{}
	}
	if f.headers == nil {
		f.headers = map[string]http.Header{}
	}
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		f.bodies[requestKey] = append([]byte(nil), raw...)
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	f.headers[requestKey] = r.Header.Clone()
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v1/auth/login":
		// Login is anonymous; the returned session is what later calls carry.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_token": f.sessionToken,
			"expires_at":    "2026-08-08T00:00:00Z",
			"owner":         map[string]any{"id": "owner-1", "role": "owner", "username": "owner"},
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/api-tokens":
		var input struct {
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		scopes := input.Scopes
		if scopes == nil {
			scopes = []string{"read", "write"}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": f.pat, "token_id": "token-1",
			"created_at": "2026-08-07T00:00:00Z", "scopes": scopes,
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/api-tokens":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "token-1", "name": "bot", "created_at": "2026-08-07T00:00:00Z", "scopes": []string{"read", "write"}},
		})
	case r.Method == "DELETE" && r.URL.Path == "/api/v1/api-tokens/token-1":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && r.URL.Path == "/api/v1/anime":
		if r.Header.Get("Authorization") != "Bearer "+f.pat && r.Header.Get("Authorization") != "Bearer "+f.sessionToken {
			http.Error(w, `{"code":"SESSION_REQUIRED","message":"auth"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"managed_anime": map[string]any{"id": "anime-1", "title": "Test Show", "version": 1},
				"subscription":  map[string]any{"id": "sub-1", "managed_anime_id": "anime-1", "origin": "custom", "state": "active", "version": 1},
				"metadata":      map[string]any{"managed_anime_id": "anime-1", "version": 1},
				"metadata_hold": map[string]any{"held": false, "missing": []string{}},
			},
		})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/catalog/providers/anibt/search"):
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"kind": "anibt_entry", "provider": "anibt", "title": "Test Show", "provider_entry_ref": "ref-1", "bgm_id": "400602", "catalog_state": "available", "preference": "neutral", "allowed_actions": []string{"prepare_subscribe"}},
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/parse/release-title":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"parser_version": "parser-v1",
			"facts":          map[string]any{"group_name": "TestGroup", "quality": "1080p"},
			"episode":        map[string]any{"matched": true, "episode_number": 1, "confidence": "certain"},
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/anime-config-candidates":
		// candidate create
		if r.Header.Get("Authorization") != "Bearer "+f.sessionToken && r.Header.Get("Authorization") != "Bearer "+f.pat {
			http.Error(w, `{"code":"SESSION_REQUIRED","message":"auth"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":               "cand-1",
			"version":          1,
			"current_revision": 7,
			"origin":           "custom",
			"origin_entry_ref": nil,
			"title":            "Test Show",
			"title_override":   nil,
			"instance_label":   nil,
			"draft": map[string]any{
				"origin": "custom", "title": "Test Show",
				"metadata": map[string]any{}, "subgroups": map[string]any{"max_rss": 10, "entries": []any{}},
				"rules": map[string]any{"sections": map[string]any{}}, "rss_sources": []any{},
			},
			"activated_managed_anime_id": nil,
			"created_at":                 "2026-08-11T00:00:00Z",
			"updated_at":                 "2026-08-11T00:00:00Z",
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/anime-config-candidates":
		// Candidate list is a wrapper object, not a bare array.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"id": "cand-1", "version": 1, "current_revision": 7, "origin": "custom", "title": "Test Show",
				"title_override": nil, "instance_label": nil, "origin_entry_ref": nil,
				"draft": map[string]any{
					"origin": "custom", "title": "Test Show",
					"metadata": map[string]any{}, "subgroups": map[string]any{"max_rss": 10, "entries": []any{}},
					"rules": map[string]any{"sections": map[string]any{}}, "rss_sources": []any{},
				},
				"activated_managed_anime_id": nil,
				"created_at":                 "2026-08-11T00:00:00Z",
				"updated_at":                 "2026-08-11T00:00:00Z",
			}},
		})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/anime-config-candidates/"):
		// candidate get (skip the subscribe sub-path)
		if strings.HasSuffix(r.URL.Path, "/subscribe") {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cand-1", "version": 1, "origin": "custom", "title": "Test Show",
			"title_override": nil, "instance_label": nil, "origin_entry_ref": nil,
			"draft": map[string]any{
				"origin": "custom", "title": "Test Show",
				"metadata": map[string]any{}, "subgroups": map[string]any{"max_rss": 10, "entries": []any{}},
				"rules": map[string]any{"sections": map[string]any{}}, "rss_sources": []any{},
			},
			"activated_managed_anime_id": nil,
			"created_at":                 "2026-08-11T00:00:00Z",
			"updated_at":                 "2026-08-11T00:00:00Z",
		})
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/api/v1/anime-config-candidates/"):
		ifMatch := r.Header.Get("If-Match")
		if ifMatch != "1" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"VERSION_CONFLICT","message":"version mismatch","current_version":2}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cand-1", "version": 2, "origin": "custom", "title": "Test Show",
			"title_override": nil, "instance_label": nil, "origin_entry_ref": nil,
			"draft": map[string]any{
				"origin": "custom", "title": "Test Show",
				"metadata": map[string]any{}, "subgroups": map[string]any{"max_rss": 10, "entries": []any{}},
				"rules": map[string]any{"sections": map[string]any{}}, "rss_sources": []any{},
			},
			"activated_managed_anime_id": nil,
			"created_at":                 "2026-08-11T00:00:00Z",
			"updated_at":                 "2026-08-11T00:00:00Z",
		})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/subscribe"):
		// candidate activate
		if r.Header.Get("Authorization") != "Bearer "+f.sessionToken && r.Header.Get("Authorization") != "Bearer "+f.pat {
			http.Error(w, `{"code":"SESSION_REQUIRED","message":"auth"}`, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Idempotency-Key") == "" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"missing idempotency key"}`, http.StatusBadRequest)
			return
		}
		if r.Header.Get("If-Match") == "" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"missing If-Match"}`, http.StatusBadRequest)
			return
		}
		var input struct {
			Revision int64 `json:"revision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Revision != 7 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"REQUEST_INVALID","message":"revision must be 7"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"managed_anime":             map[string]any{"id": "anime-1", "title": "Test Show", "version": 1},
			"subscription":              map[string]any{"id": "sub-1", "managed_anime_id": "anime-1", "origin": "custom", "state": "active", "version": 1},
			"metadata":                  map[string]any{"managed_anime_id": "anime-1", "version": 1},
			"rss_sources":               []map[string]any{},
			"effective_config_versions": map[string]any{"subgroups": 1},
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/anime/rss-list-view":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config_identity": "test-config", "generated_at": "2026-08-11T00:00:00Z", "groups": []any{},
			"limit_per_group": 100, "metadata_hold": map[string]any{"held": false, "missing": []any{}},
			"mode": "current_input", "truncated": false, "unused_feed_identities": []any{},
			"versions": map[string]any{"parser": "parser-v1", "evaluator": "eval-v1", "renderer": "render-v1", "safety": "safe-v1", "torrent_parser": "torrent-v1"},
		})
	case r.Method == "DELETE" && r.URL.Path == "/api/v1/anime-config-candidates/cand-1":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "POST" && r.URL.Path == "/api/v1/catalog/providers/anibt/entries/entry-1/groups:discover":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{{
				"display_name": "Test Group", "feed_url": "https://example.test/rss/g1", "group_ref": "g1",
				"observed_at": "2026-08-11T00:00:00Z", "provider": "anibt",
			}},
			"observed_at": "2026-08-11T00:00:00Z", "provider": "anibt", "provider_entry_ref": "entry-1",
			"schema_version": "v1", "snapshot_version": 3, "source_version": "source-v1",
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/catalog/providers/anibt/entries/entry-1/rss":
		if r.URL.Query().Get("group_ref") != "g1" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"group_ref"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content_hash": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"freshness":    "fresh", "group_ref": "g1", "provider": "anibt", "provider_entry_ref": "entry-1",
			"seeds": []any{}, "snapshot_id": "snapshot-1", "snapshot_version": 3,
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/catalog/providers/anibt/entries/entry-1/rss":
		var input struct {
			GroupRef string `json:"group_ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.GroupRef != "g1" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"group_ref"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content_hash": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"freshness":    "fresh", "group_ref": "g1", "provider": "anibt", "provider_entry_ref": "entry-1",
			"seeds": []any{}, "snapshot_id": "snapshot-2", "snapshot_version": 4,
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/one-shot-acquisitions":
		if r.Header.Get("Idempotency-Key") == "" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"missing idempotency key"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"download_task_id": "task-1", "managed_anime_id": "anime-1", "one_shot_id": "shot-1",
			"release_id": "release-1", "rss_source_id": "rss-1", "state": "running", "subscription_id": "sub-1",
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/one-shot-acquisitions/shot-1":
		attemptID := "attempt-1"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"one_shot_id": "shot-1", "managed_anime_id": "anime-1", "subscription_id": "sub-1", "release_id": "release-1", "rss_source_id": "rss-1",
			"state": "retention_pending", "version": 3, "current_attempt_id": attemptID,
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/one-shot-acquisitions/shot-1/attempts":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id": "attempt-1", "sequence": 1, "mode": "initial", "state": "failed",
				"created_at": "2026-08-11T00:00:00Z", "input_digest": "sha256:attempt-1",
			}},
			"next_cursor": "next-cursor",
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/one-shot-acquisitions/shot-1/attempts/attempt-1":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "attempt-1", "acquisition_id": "shot-1", "sequence": 1, "mode": "initial", "state": "failed",
			"created_at": "2026-08-11T00:00:00Z", "updated_at": "2026-08-11T00:00:00Z", "input_digest": "sha256:attempt-1",
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/acquisitions/acq-1/import/retry":
		if r.Header.Get("If-Match") != "3" || r.Header.Get("Idempotency-Key") == "" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"import retry headers"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"acquisition_id": "acq-1", "attempt": 2, "retried_files": 1, "reopened_placements": 0, "version": 4})
	case r.Method == "GET" && r.URL.Path == "/api/v1/metadata/search":
		if r.URL.Query().Get("provider") != "bgm" || r.URL.Query().Get("query") != "Test Show" || r.URL.Query().Get("limit") != "5" {
			http.Error(w, `{"code":"REQUEST_INVALID","message":"metadata query"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider": "bgm", "query": "Test Show",
			"candidates": []map[string]any{{
				"provider": "bgm", "provider_work_id": "work-1", "title": "Test Show", "media_kind": "tv",
				"details_status": "available", "score": 0.99,
			}},
		})
	case r.Method == "GET" && r.URL.Path == "/api/v1/anime/anime-1/workspace":
		if r.Header.Get("Authorization") != "Bearer "+f.sessionToken && r.Header.Get("Authorization") != "Bearer "+f.pat {
			http.Error(w, `{"code":"SESSION_REQUIRED","message":"auth"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"managed_anime":       map[string]any{"id": "anime-1", "title": "Test Show", "version": 1},
			"subscription":        map[string]any{"id": "sub-1", "managed_anime_id": "anime-1", "origin": "custom", "state": "active", "version": 1},
			"metadata":            map[string]any{"managed_anime_id": "anime-1", "version": 1},
			"metadata_hold":       map[string]any{"held": false, "missing": []string{}},
			"subgroups":           map[string]any{"selected": []any{}, "available": []any{}},
			"rules":               map[string]any{"sections": map[string]any{}},
			"rss_sources":         []map[string]any{},
			"rss_source_runtimes": []map[string]any{},
			"releases":            []map[string]any{},
			"download_tasks":      []map[string]any{},
			"artifacts":           []map[string]any{},
			"file_operations":     []map[string]any{},
			"logs":                []map[string]any{},
		})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

// withCache redirects the token cache to a temp dir so tests never touch the
// real user configuration directory.
func withCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	configDirOverride = filepath.Join(dir, "config")
	t.Cleanup(func() { configDirOverride = "" })
	return dir
}

func TestLoginCachesSessionAndListUsesIt(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	if err := run([]string{"login", "--endpoint", ts.URL, "--username", "owner", "--password", "pw"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	token, err := readToken()
	if err != nil || token != "session-abc123" {
		t.Fatalf("cached token = %q, %v; want session-abc123", token, err)
	}
	if err := run([]string{"list", "--endpoint", ts.URL}); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestAPITokenCreateListRevokeRoundTrip(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123", pat: "pat-raw-64hex"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	if err := run([]string{"api-token", "create", "--endpoint", ts.URL, "--token", "session-abc123", "--name", "bot"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if body := string(server.bodies["POST /api/v1/api-tokens"]); strings.Contains(body, "scopes") {
		t.Fatalf("create without --scopes must leave the server default: %s", body)
	}
	if err := run([]string{"api-token", "list", "--endpoint", ts.URL, "--token", "session-abc123", "--json"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := run([]string{"api-token", "create", "--endpoint", ts.URL, "--token", "session-abc123", "--name", "reader", "--scopes", "read"}); err != nil {
		t.Fatalf("create --scopes: %v", err)
	}
	if body := string(server.bodies["POST /api/v1/api-tokens"]); !strings.Contains(body, `"scopes":["read"]`) {
		t.Fatalf("create --scopes read must send the set: %s", body)
	}
	if err := run([]string{"api-token", "revoke", "--endpoint", ts.URL, "--token", "session-abc123", "--id", "token-1"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// The CLI must have hit exactly create/list/revoke in order.
	want := []string{
		"POST /api/v1/api-tokens",
		"GET /api/v1/api-tokens",
		"POST /api/v1/api-tokens",
		"DELETE /api/v1/api-tokens/token-1",
	}
	if len(server.requests) < len(want) {
		t.Fatalf("requests = %v, want at least %v", server.requests, want)
	}
	for i, expected := range want {
		if server.requests[i] != expected {
			t.Fatalf("request[%d] = %q, want %q", i, server.requests[i], expected)
		}
	}
}

func TestPATAuthenticatedSearchAndList(t *testing.T) {
	withCache(t)
	server := &fakeServer{pat: "pat-raw-64hex"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	// With no cached token, --token must authenticate the search.
	if err := run([]string{"search", "--endpoint", ts.URL, "--token", "pat-raw-64hex", "--provider", "anibt", "--query", "Test"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if err := run([]string{"list", "--endpoint", ts.URL, "--token", "pat-raw-64hex"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	// The GET /anime handler only answers with the PAT, so this asserts the
	// CLI forwarded the token as a Bearer header.
}

func TestCandidateActivateEmitsIdempotencyKeyAndIfMatch(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	if err := run([]string{"candidate", "activate", "--endpoint", ts.URL, "--token", "session-abc123", "--id", "cand-1", "--revision", "7", "--version", "1", "--key", "test-key"}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	var activationInput struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(server.bodies["POST /api/v1/anime-config-candidates/cand-1/subscribe"], &activationInput); err != nil {
		t.Fatalf("decode activation body: %v", err)
	}
	if activationInput.Revision != 7 {
		t.Fatalf("activation revision = %d, want 7", activationInput.Revision)
	}
	if got := server.headers["POST /api/v1/anime-config-candidates/cand-1/subscribe"].Get("If-Match"); got != "1" {
		t.Fatalf("activation If-Match = %q, want 1", got)
	}
	// Activate must have hit the subscribe endpoint.
	found := false
	for _, req := range server.requests {
		if req == "POST /api/v1/anime-config-candidates/cand-1/subscribe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("activate never reached the server: %v", server.requests)
	}
}

func TestCandidateListAndCreate(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	draftFile := filepath.Join(t.TempDir(), "draft.json")
	draft := `{"draft":{"origin":"custom","title":"Test Show","metadata":{},"subgroups":{"max_rss":10,"entries":[]},"rules":{"sections":{}},"rss_sources":[]}}`
	if err := os.WriteFile(draftFile, []byte(draft), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"candidate", "create", "--endpoint", ts.URL, "--token", "session-abc123", "--draft-file", draftFile}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := run([]string{"candidate", "list", "--endpoint", ts.URL, "--token", "session-abc123"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	// Verify the exact request paths.
	wantCreate := "POST /api/v1/anime-config-candidates"
	wantList := "GET /api/v1/anime-config-candidates"
	foundCreate, foundList := false, false
	for _, req := range server.requests {
		if req == wantCreate {
			foundCreate = true
		}
		if req == wantList {
			foundList = true
		}
	}
	if !foundCreate {
		t.Fatalf("create never reached the server: %v", server.requests)
	}
	if !foundList {
		t.Fatalf("list never reached the server: %v", server.requests)
	}
}

func TestCandidatePatchConflictReportsCurrentVersion(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	draftFile := filepath.Join(t.TempDir(), "draft.json")
	draft := `{"draft":{"origin":"custom","title":"Test Show","metadata":{},"subgroups":{"max_rss":10,"entries":[]},"rules":{"sections":{}},"rss_sources":[]}}`
	if err := os.WriteFile(draftFile, []byte(draft), 0o600); err != nil {
		t.Fatal(err)
	}

	// version=99 does not match server's expected v1 -> 409 with current_version=2.
	err := run([]string{"candidate", "patch", "--endpoint", ts.URL, "--token", "session-abc123", "--id", "cand-1", "--draft-file", draftFile, "--version", "99"})
	if err == nil {
		t.Fatal("patch with stale version must fail")
	}
	if !strings.Contains(err.Error(), "current_version=2") {
		t.Fatalf("error must carry current_version=2, got %v", err)
	}
}

func TestStatusJSONRoundTrips(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	output := capture(t, func() {
		if err := run([]string{"status", "--endpoint", ts.URL, "--token", "session-abc123", "--anime-id", "anime-1", "--json"}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(output, `"managed_anime"`) {
		t.Fatalf("status --json did not print the raw workspace: %s", output)
	}
}

func TestMikanSearchReturnsTypedError(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	// The fake server only serves anibt search; a mikan search request hits the
	// default 404 path, which exercises the typed error decode path.
	err := run([]string{"search", "--endpoint", ts.URL, "--token", "session-abc123", "--provider", "mikan", "--query", "x"})
	if err == nil {
		t.Fatal("mikan search must fail")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error must carry the typed shape, got %v", err)
	}
}

func TestMissingCredentialErrors(t *testing.T) {
	withCache(t)
	if err := run([]string{"list", "--endpoint", "http://127.0.0.1:9999"}); err == nil {
		t.Fatal("list without a credential must fail")
	} else if !strings.Contains(err.Error(), "no cached credential") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExternalCommandsUseContractEndpoints(t *testing.T) {
	withCache(t)
	server := &fakeServer{sessionToken: "session-abc123"}
	ts := httptest.NewServer(server)
	defer ts.Close()

	runCommand := func(args ...string) error {
		args = append(args, "--endpoint", ts.URL, "--token", "session-abc123")
		return run(args)
	}

	draftFile := filepath.Join(t.TempDir(), "draft.json")
	wrappedDraft := `{"draft":` + validAnimeDraftJSON + `}`
	if err := os.WriteFile(draftFile, []byte(wrappedDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	feedJSON := `[{"provider":"anibt","provider_entry_ref":"entry-1","provider_group_ref":"g1","snapshot_id":"snapshot-1","snapshot_version":3,"content_hash":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]`

	if err := runCommand("candidate", "preview", "--id", "cand-1", "--revision", "7", "--feed-json", feedJSON, "--json"); err != nil {
		t.Fatalf("candidate preview: %v", err)
	}
	var candidatePreview struct {
		InputSource string           `json:"input_source"`
		CandidateID string           `json:"candidate_id"`
		Revision    int64            `json:"revision"`
		Feed        []map[string]any `json:"feed"`
	}
	decodeBody(t, server, "POST /api/v1/anime/rss-list-view", &candidatePreview)
	if candidatePreview.InputSource != "candidate_revision" || candidatePreview.CandidateID != "cand-1" || candidatePreview.Revision != 7 || len(candidatePreview.Feed) != 1 {
		t.Fatalf("candidate preview body = %+v", candidatePreview)
	}
	assertRequestSeen(t, server, "POST /api/v1/anime/rss-list-view")

	if err := runCommand("candidate", "delete", "--id", "cand-1"); err != nil {
		t.Fatalf("candidate delete: %v", err)
	}
	assertRequestSeen(t, server, "DELETE /api/v1/anime-config-candidates/cand-1")

	if err := runCommand("preview", "--draft-file", draftFile,
		"--feed-provider", "anibt", "--feed-entry-ref", "entry-1", "--feed-group-ref", "g1",
		"--feed-snapshot-id", "snapshot-1", "--feed-snapshot-version", "3", "--feed-content-hash",
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "--json"); err != nil {
		t.Fatalf("current input preview: %v", err)
	}
	var currentPreview struct {
		InputSource string           `json:"input_source"`
		Draft       map[string]any   `json:"draft"`
		Feed        []map[string]any `json:"feed"`
	}
	decodeBody(t, server, "POST /api/v1/anime/rss-list-view", &currentPreview)
	if currentPreview.InputSource != "current_input" {
		t.Fatalf("current input source = %q", currentPreview.InputSource)
	}
	if draft, ok := currentPreview.Draft["title"].(string); !ok || draft != "Test Show" {
		t.Fatalf("current input draft = %#v", currentPreview.Draft)
	}
	if len(currentPreview.Feed) != 1 {
		t.Fatalf("current input feed = %#v", currentPreview.Feed)
	}
	viewJSON := `{"config_identity":"cfg","generated_at":"2026-08-11T00:00:00Z","groups":[{"display_name":"Group A","feed":{"state":"ready"},"items":[{"evaluation":{"reason_code":"FILTER_MATCH","reason_detail":"matches policy","verdict":"accepted"},"files":[{"filename":"episode.mkv","included":true},{"filename":"sample.txt","included":false,"reason_code":"FILE_FILTERED"}],"seed":{"title":"Episode 1"}}],"selection":{"effective_selected":true}}]}`
	output := capture(t, func() {
		if err := printRSSListView([]byte(viewJSON)); err != nil {
			t.Errorf("print RSS list view: %v", err)
		}
	})
	for _, expected := range []string{"group Group A [selected]", "seed Episode 1: accepted", "matches policy", "file episode.mkv: included", "file sample.txt: excluded (FILE_FILTERED)"} {
		if !strings.Contains(output, expected) {
			t.Errorf("RSS list view output missing %q:\n%s", expected, output)
		}
	}

	if err := runCommand("feed", "groups", "--provider", "anibt", "--entry-ref", "entry-1"); err != nil {
		t.Fatalf("feed groups: %v", err)
	}
	assertRequestSeen(t, server, "POST /api/v1/catalog/providers/anibt/entries/entry-1/groups:discover")
	if err := runCommand("feed", "snapshot", "--provider", "anibt", "--entry-ref", "entry-1", "--group-ref", "g1"); err != nil {
		t.Fatalf("feed snapshot: %v", err)
	}
	assertRequestSeen(t, server, "GET /api/v1/catalog/providers/anibt/entries/entry-1/rss?group_ref=g1")
	if err := runCommand("feed", "refresh", "--provider", "anibt", "--entry-ref", "entry-1", "--group-ref", "g1"); err != nil {
		t.Fatalf("feed refresh: %v", err)
	}
	var feedRefresh struct {
		GroupRef string `json:"group_ref"`
	}
	decodeBody(t, server, "POST /api/v1/catalog/providers/anibt/entries/entry-1/rss", &feedRefresh)
	if feedRefresh.GroupRef != "g1" {
		t.Fatalf("feed refresh body = %+v", feedRefresh)
	}

	if err := runCommand("one-shot", "acquire", "--draft-json", validAnimeDraftJSON, "--seed-json", validSelectedSeedJSON,
		"--idempotency-key", "acquire-key"); err != nil {
		t.Fatalf("one-shot acquire: %v", err)
	}
	var acquireInput struct {
		InputSource      string         `json:"input_source"`
		Draft            map[string]any `json:"draft"`
		OverrideFilter   *bool          `json:"override_filter"`
		SelectedFileRefs []string       `json:"selected_file_refs"`
		SelectedSeed     map[string]any `json:"selected_seed"`
	}
	decodeBody(t, server, "POST /api/v1/one-shot-acquisitions", &acquireInput)
	if acquireInput.InputSource != "current_input" || acquireInput.Draft["title"] != "Test Show" || acquireInput.OverrideFilter != nil || acquireInput.SelectedFileRefs != nil || acquireInput.SelectedSeed["provider_group_ref"] != "g1" {
		t.Fatalf("one-shot acquire body = %+v", acquireInput)
	}
	if got := server.headers["POST /api/v1/one-shot-acquisitions"].Get("Idempotency-Key"); got != "acquire-key" {
		t.Fatalf("one-shot acquire Idempotency-Key = %q", got)
	}

	if err := runCommand("one-shot", "acquire", "--candidate-id", "cand-1", "--revision", "7", "--seed-json", validSelectedSeedJSON, "--idempotency-key", "saved-key"); err != nil {
		t.Fatalf("saved-revision one-shot acquire: %v", err)
	}
	var savedAcquireInput struct {
		InputSource string `json:"input_source"`
		CandidateID string `json:"candidate_id"`
		Revision    int64  `json:"revision"`
		Draft       any    `json:"draft"`
	}
	decodeBody(t, server, "POST /api/v1/one-shot-acquisitions", &savedAcquireInput)
	if savedAcquireInput.InputSource != "saved_revision" || savedAcquireInput.CandidateID != "cand-1" || savedAcquireInput.Revision != 7 || savedAcquireInput.Draft != nil {
		t.Fatalf("saved-revision one-shot acquire body = %+v", savedAcquireInput)
	}

	if err := runCommand("one-shot", "get", "--id", "shot-1"); err != nil {
		t.Fatalf("one-shot get: %v", err)
	}
	assertRequestSeen(t, server, "GET /api/v1/one-shot-acquisitions/shot-1")
	if err := runCommand("one-shot", "attempts", "--id", "shot-1", "--limit", "2", "--cursor", "next-cursor"); err != nil {
		t.Fatalf("one-shot attempts: %v", err)
	}
	assertRequestSeen(t, server, "GET /api/v1/one-shot-acquisitions/shot-1/attempts?limit=2&cursor=next-cursor")
	if err := runCommand("one-shot", "attempt", "--id", "shot-1", "--attempt-id", "attempt-1"); err != nil {
		t.Fatalf("one-shot attempt: %v", err)
	}
	assertRequestSeen(t, server, "GET /api/v1/one-shot-acquisitions/shot-1/attempts/attempt-1")

	if err := runCommand("retry-import", "--acquisition-id", "acq-1", "--version", "3"); err != nil {
		t.Fatalf("retry-import: %v", err)
	}
	assertRequestSeen(t, server, "POST /api/v1/acquisitions/acq-1/import/retry")
	if err := runCommand("metadata", "search", "--q", "Test Show", "--limit", "5"); err != nil {
		t.Fatalf("metadata search: %v", err)
	}
	assertRequestSeen(t, server, "GET /api/v1/metadata/search?provider=bgm&query=Test%20Show&limit=5")
}

func TestOneShotAcquireInputModes(t *testing.T) {
	withCache(t)
	validSeed := "--seed-json=" + validSelectedSeedJSON
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "candidate requires revision",
			args: []string{"one-shot", "acquire", "--candidate-id", "cand-1", validSeed},
			want: "--revision",
		},
		{
			name: "revision requires candidate",
			args: []string{"one-shot", "acquire", "--revision", "7", validSeed},
			want: "--candidate-id",
		},
		{
			name: "candidate and draft are exclusive",
			args: []string{"one-shot", "acquire", "--candidate-id", "cand-1", "--revision", "7", "--draft-json", validAnimeDraftJSON, validSeed},
			want: "exactly one input mode",
		},
		{
			name: "seed is required",
			args: []string{"one-shot", "acquire", "--draft-json", validAnimeDraftJSON},
			want: "--seed-json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

const validAnimeDraftJSON = `{"origin":"custom","title":"Test Show","metadata":{},"subgroups":{"max_rss":10,"entries":[]},"rules":{"sections":{}},"rss_sources":[]}`

const validSelectedSeedJSON = `{"provider":"anibt","provider_entry_ref":"entry-1","provider_group_ref":"g1","locator":"https://example.test/show.torrent","title":"Test Show [01]","published_at":"2026-08-11T00:00:00Z","info_hash":"0123456789abcdef0123456789abcdef01234567"}`

func assertRequestSeen(t *testing.T, server *fakeServer, want string) {
	t.Helper()
	for _, request := range server.requests {
		if request == want {
			return
		}
	}
	t.Fatalf("request %q not seen; got %v", want, server.requests)
}

func decodeBody(t *testing.T, server *fakeServer, key string, dst any) {
	t.Helper()
	raw, ok := server.bodies[key]
	if !ok {
		t.Fatalf("request body %q not captured", key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode %s body: %v", key, err)
	}
}

func capture(t *testing.T, fn func()) string {
	t.Helper()
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	old := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = old }()
	fn()
	_ = file.Sync()
	raw, _ := os.ReadFile(file.Name())
	return string(raw)
}
