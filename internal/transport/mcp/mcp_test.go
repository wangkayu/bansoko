package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"gopkg.in/yaml.v3"

	"bansoko/api"
	"bansoko/internal/agentguide"
	"bansoko/internal/transport/httpapi/generated"
)

// recorded is one in-process API request a tool produced.
type recorded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// fakeAPI stands in for the full API handler chain. Tests set respond to shape
// the answer; every request is recorded.
type fakeAPI struct {
	mu      sync.Mutex
	calls   []recorded
	respond func(r *http.Request) (int, string)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(raw)})
	f.mu.Unlock()
	status, body := http.StatusOK, `{"ok":true}`
	if f.respond != nil {
		status, body = f.respond(r)
	}
	if body != "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *fakeAPI) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no API request was made")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func initializeRequest() mcp.InitializeRequest {
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "bansoko-test", Version: "1.0.0"}
	return request
}

// newTestClient wires an in-process MCP client to a server replaying tool
// calls against the fake API.
func newTestClient(t *testing.T, api http.Handler) *client.Client {
	t.Helper()
	srv, err := New(api)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	cli, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("new in-process client: %v", err)
	}
	t.Cleanup(func() { cli.Close() })
	if err := cli.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}
	if _, err := cli.Initialize(context.Background(), initializeRequest()); err != nil {
		t.Fatalf("initialize client: %v", err)
	}
	return cli
}

func callTool(t *testing.T, cli *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := cli.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// pinnedTools is the public tool surface. A client can rely on exactly these
// names; adding, renaming or dropping one is a contract change that must be
// made in api/openapi.yaml (x-mcp) and in this list together.
var pinnedTools = []string{
	"get_subscriptions", "search_anime", "parse_title", "parse_torrent", "rss_list_view",
	"discover_feed_groups", "get_feed_snapshot", "refresh_feed_snapshot", "get_business_preset", "load_business_preset",
	"list_candidates", "get_candidate", "create_candidate", "patch_candidate", "activate_candidate",
	"refresh_metadata", "refresh_rss", "retry_release", "retry_import", "get_workspace", "poll_workspace",
	"acquire_seed_once", "get_one_shot_acquisition", "list_one_shot_attempts",
	"expand_seed", "download_snapshot_seed", "search_metadata",
	"reevaluate_releases", "match_metadata", "discover_subgroups", "patch_subscription", "set_new_releases_only", "automation_run",
	"patch_anime_metadata", "patch_anime_rules", "delete_candidate", "delete_subscription", "duplicate_subscription",
	"list_execution_records", "get_execution_record", "list_execution_record_files",
	"list_tmdb_seasons", "list_tmdb_season_episodes", "preview_file_match", "match_file", "release_file_entry",
	"list_placement_issues", "redownload_placement", "release_placement",
	"list_anime_files", "align_anime_files", "recover_anime_file",
	"list_feed_sites",
}

func TestToolListPinsTheAllowlist(t *testing.T) {
	cli := newTestClient(t, &fakeAPI{})
	result, err := cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var got []string
	for _, tool := range result.Tools {
		got = append(got, tool.Name)
	}
	want := append([]string(nil), pinnedTools...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v\nwant    %v", got, want)
	}
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(catalog.ToolNames(), ",") != strings.Join(want, ",") {
		t.Fatalf("catalog tools = %v, want %v", catalog.ToolNames(), want)
	}
}

func TestCatalogIsConsistentWithOpenAPI(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]int{}
	for _, op := range catalog.Operations {
		if op.Description == "" || op.ID == "" || !knownGroups[op.Group] {
			t.Errorf("%s: incomplete catalog entry %+v", op.Tool, op)
		}
		groups[op.Group]++
		if op.Method == "GET" && op.Write {
			t.Errorf("%s: GET tool marked write", op.Tool)
		}
		if op.Destructive && !op.Write {
			t.Errorf("%s: destructive tool must be a write tool", op.Tool)
		}
	}
	for _, comp := range catalog.Composites {
		groups[comp.Group]++
		if _, ok := compositeBuilders[comp.Tool]; !ok {
			t.Errorf("composite %s has no implementation", comp.Tool)
		}
		for _, id := range comp.Operations {
			if catalog.Operation(id) == nil {
				t.Errorf("composite %s composes %s, which is not an allowlisted operation", comp.Tool, id)
			}
		}
	}
	for name := range compositeBuilders {
		found := false
		for _, comp := range catalog.Composites {
			found = found || comp.Tool == name
		}
		if !found {
			t.Errorf("implemented composite %s is missing from x-mcp-composites", name)
		}
	}
	for group := range knownGroups {
		if groups[group] == 0 {
			t.Errorf("group %s has no tools", group)
		}
	}
}

func TestInputSchemasAreWellFormed(t *testing.T) {
	cli := newTestClient(t, &fakeAPI{})
	result, err := cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ref := regexp.MustCompile(`"\$ref":"#/\$defs/([A-Za-z0-9_]+)"`)
	for _, tool := range result.Tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		var decoded struct {
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if decoded.Description == "" {
			t.Errorf("%s: empty description", tool.Name)
		}
		if decoded.InputSchema["type"] != "object" || decoded.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: input schema must be a closed object: %v", tool.Name, decoded.InputSchema["type"])
		}
		schema, _ := json.Marshal(decoded.InputSchema)
		if len(schema) > 16*1024 {
			t.Errorf("%s: input schema is %d bytes; keep tool schemas under 16 KiB", tool.Name, len(schema))
		}
		defs, _ := decoded.InputSchema["$defs"].(map[string]any)
		for _, match := range ref.FindAllStringSubmatch(string(schema), -1) {
			if _, ok := defs[match[1]]; !ok {
				t.Errorf("%s: $ref to undefined $defs/%s", tool.Name, match[1])
			}
		}
		for _, name := range asStringList(toAnyList(decoded.InputSchema["required"])) {
			props, _ := decoded.InputSchema["properties"].(map[string]any)
			if _, ok := props[name]; !ok {
				t.Errorf("%s: required %q is not a property", tool.Name, name)
			}
		}
	}
}

func toAnyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func TestInputSchemasAreDeterministic(t *testing.T) {
	first, err := ParseCatalog(api.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := ParseCatalog(api.OpenAPI)
		if err != nil {
			t.Fatal(err)
		}
		for j, op := range first.Operations {
			if string(op.InputSchema()) != string(again.Operations[j].InputSchema()) {
				t.Fatalf("%s: input schema differs between parses", op.Tool)
			}
		}
	}
}

func TestToolAnnotationsAreCorrect(t *testing.T) {
	cli := newTestClient(t, &fakeAPI{})
	result, err := cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{
		"get_subscriptions": true, "search_anime": true, "search_metadata": true, "parse_title": true, "parse_torrent": true,
		"rss_list_view": true, "get_workspace": true, "poll_workspace": true, "list_candidates": true,
		"get_candidate": true, "get_one_shot_acquisition": true, "list_one_shot_attempts": true,
		"list_execution_records": true, "get_execution_record": true, "list_execution_record_files": true,
		"list_tmdb_seasons": true, "list_tmdb_season_episodes": true, "preview_file_match": true,
		"get_feed_snapshot":     true,
		"get_business_preset":   true,
		"load_business_preset":  true,
		"list_placement_issues": true,
		"list_anime_files":      true,
		"list_feed_sites":       true,
	}
	destructive := map[string]bool{"delete_candidate": true, "delete_subscription": true}
	// create and patch candidate are not idempotent: each call inserts a row or
	// advances the version.
	nonIdempotent := map[string]bool{"create_candidate": true, "patch_candidate": true}
	for _, tool := range result.Tools {
		a := tool.Annotations
		if a.ReadOnlyHint == nil || *a.ReadOnlyHint != readOnly[tool.Name] {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, a.ReadOnlyHint, readOnly[tool.Name])
		}
		if a.DestructiveHint == nil || *a.DestructiveHint != destructive[tool.Name] {
			t.Errorf("%s: destructiveHint = %v, want %v", tool.Name, a.DestructiveHint, destructive[tool.Name])
		}
		if a.IdempotentHint == nil || *a.IdempotentHint == nonIdempotent[tool.Name] {
			t.Errorf("%s: idempotentHint = %v, want %v", tool.Name, a.IdempotentHint, !nonIdempotent[tool.Name])
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: openWorldHint must be false", tool.Name)
		}
	}
}

func TestPathAndQueryArgumentsReachTheAPI(t *testing.T) {
	api := &fakeAPI{respond: func(*http.Request) (int, string) { return 200, `[]` }}
	cli := newTestClient(t, api)

	callTool(t, cli, "get_candidate", map[string]any{"candidate_id": "cand 1/x"})
	if got := api.last(t); got.Method != "GET" || got.Path != "/api/v1/anime-config-candidates/cand 1/x" {
		t.Fatalf("get_candidate sent %s %s", got.Method, got.Path)
	}

	callTool(t, cli, "list_candidates", map[string]any{"origin": "anibt", "origin_entry_ref": "42"})
	got := api.last(t)
	if got.Path != "/api/v1/anime-config-candidates" || got.Query != "origin=anibt&origin_entry_ref=42" {
		t.Fatalf("list_candidates sent %s ?%s", got.Path, got.Query)
	}

	// search_anime keeps its historical `query` argument for the `q` parameter.
	callTool(t, cli, "search_anime", map[string]any{"provider": "anibt", "query": "frieren"})
	got = api.last(t)
	if got.Path != "/api/v1/catalog/providers/anibt/search" || got.Query != "q=frieren" {
		t.Fatalf("search_anime sent %s ?%s", got.Path, got.Query)
	}

	callTool(t, cli, "list_tmdb_season_episodes", map[string]any{"managed_anime_id": "a1", "season": 2})
	if got := api.last(t); got.Path != "/api/v1/anime/a1/tmdb/seasons/2/episodes" {
		t.Fatalf("season argument sent %s", got.Path)
	}

	callTool(t, cli, "list_execution_records", map[string]any{"from": "2026-01-01T00:00:00Z", "limit": 10, "state": "failed"})
	if got := api.last(t); !strings.Contains(got.Query, "from=2026-01-01T00%3A00%3A00Z") || !strings.Contains(got.Query, "limit=10") || !strings.Contains(got.Query, "state=failed") {
		t.Fatalf("list_execution_records query = %q", got.Query)
	}
}

func TestBodyAndVersionHeadersAreMapped(t *testing.T) {
	api := &fakeAPI{respond: func(*http.Request) (int, string) { return 200, `{"id":"c1","version":4}` }}
	cli := newTestClient(t, api)

	draft := map[string]any{"origin": "custom", "title": "Show"}
	callTool(t, cli, "patch_candidate", map[string]any{"candidate_id": "c1", "expected_version": 3, "draft": draft})
	got := api.last(t)
	if got.Method != "PATCH" || got.Header.Get("If-Match") != "3" || got.Header.Get("Idempotency-Key") != "" {
		t.Fatalf("patch_candidate request = %+v", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got.Body), &body); err != nil || body["draft"] == nil || len(body) != 1 {
		t.Fatalf("patch_candidate body = %q (%v)", got.Body, err)
	}

	callTool(t, cli, "create_candidate", map[string]any{"draft": draft})
	if got := api.last(t); got.Method != "POST" || got.Path != "/api/v1/anime-config-candidates" || got.Header.Get("If-Match") != "" {
		t.Fatalf("create_candidate request = %+v", got)
	}

	callTool(t, cli, "patch_subscription", map[string]any{"managed_anime_id": "a1", "expected_version": 7, "state": "stopped"})
	got = api.last(t)
	var patch map[string]any
	_ = json.Unmarshal([]byte(got.Body), &patch)
	if got.Path != "/api/v1/anime/a1/subscription" || patch["state"] != "stopped" || patch["expected_version"] != float64(7) || got.Header.Get("If-Match") != "" {
		t.Fatalf("patch_subscription request = %+v", got)
	}

	// A DELETE with a CAS body.
	api.respond = func(*http.Request) (int, string) { return 204, "" }
	result := callTool(t, cli, "delete_subscription", map[string]any{"managed_anime_id": "a1", "expected_version": 2})
	got = api.last(t)
	if got.Method != "DELETE" || got.Path != "/api/v1/anime/a1/subscription" || !strings.Contains(got.Body, `"expected_version":2`) {
		t.Fatalf("delete_subscription request = %+v", got)
	}
	confirmation, _ := result.StructuredContent.(map[string]any)
	if result.IsError || confirmation["deleted"] != true || confirmation["managed_anime_id"] != "a1" {
		t.Fatalf("delete_subscription result = %+v", result)
	}
	result = callTool(t, cli, "delete_candidate", map[string]any{"candidate_id": "c1"})
	confirmation, _ = result.StructuredContent.(map[string]any)
	if confirmation["deleted"] != true || confirmation["candidate_id"] != "c1" {
		t.Fatalf("delete_candidate result = %+v", result)
	}
}

func TestRSSListViewFlattensTheBody(t *testing.T) {
	api := &fakeAPI{respond: func(*http.Request) (int, string) { return 200, `{"mode":"candidate_revision","groups":[]}` }}
	cli := newTestClient(t, api)
	result := callTool(t, cli, "rss_list_view", map[string]any{"input_source": "candidate_revision", "candidate_id": "c1", "revision": 2})
	got := api.last(t)
	if got.Path != "/api/v1/anime/rss-list-view" || got.Method != "POST" {
		t.Fatalf("rss_list_view request = %+v", got)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(got.Body), &body)
	if body["input_source"] != "candidate_revision" || body["candidate_id"] != "c1" || body["revision"] != float64(2) {
		t.Fatalf("rss_list_view body = %q", got.Body)
	}
	structured, _ := result.StructuredContent.(map[string]any)
	if structured["mode"] != "candidate_revision" {
		t.Fatalf("structured result = %+v", result.StructuredContent)
	}

	// owner_draft is UI-only: refused before any API request.
	before := api.count()
	result = callTool(t, cli, "rss_list_view", map[string]any{"input_source": "owner_draft", "managed_anime_id": "a1"})
	if !result.IsError || api.count() != before || !strings.Contains(textOf(result), "owner_draft") {
		t.Fatalf("owner_draft must be refused locally: %+v", result)
	}
}

func TestIdempotencyKeys(t *testing.T) {
	api := &fakeAPI{respond: func(*http.Request) (int, string) { return 202, `{"accepted":true}` }}
	cli := newTestClient(t, api)

	callTool(t, cli, "refresh_rss", map[string]any{"rss_source_id": "s1"})
	first := api.last(t).Header.Get("Idempotency-Key")
	callTool(t, cli, "refresh_rss", map[string]any{"rss_source_id": "s1"})
	second := api.last(t).Header.Get("Idempotency-Key")
	pattern := regexp.MustCompile(`^mcp-[0-9a-f]{32}$`)
	if !pattern.MatchString(first) || !pattern.MatchString(second) || first == second {
		t.Fatalf("auto keys = %q, %q; want unique mcp-<32 hex>", first, second)
	}

	callTool(t, cli, "retry_release", map[string]any{"release_id": "r1", "idempotency_key": "caller-key-0001"})
	if got := api.last(t).Header.Get("Idempotency-Key"); got != "caller-key-0001" {
		t.Fatalf("caller key = %q", got)
	}

	// activate_candidate keeps its caller-supplied, required key.
	before := api.count()
	result := callTool(t, cli, "activate_candidate", map[string]any{"candidate_id": "c1", "revision": 1, "expected_version": 1})
	if !result.IsError || api.count() != before || !strings.Contains(textOf(result), "idempotency_key") {
		t.Fatalf("activate_candidate without a key must be refused locally: %+v", result)
	}
	callTool(t, cli, "activate_candidate", map[string]any{"candidate_id": "c1", "revision": 1, "expected_version": 5, "idempotency_key": "activate-0001", "initial_state": "stopped"})
	got := api.last(t)
	if got.Path != "/api/v1/anime-config-candidates/c1/subscribe" || got.Header.Get("If-Match") != "5" || got.Header.Get("Idempotency-Key") != "activate-0001" {
		t.Fatalf("activate_candidate request = %+v", got)
	}
	if !strings.Contains(got.Body, `"revision":1`) || !strings.Contains(got.Body, `"initial_state":"stopped"`) {
		t.Fatalf("activate_candidate body = %q", got.Body)
	}

	// duplicate_subscription also requires the caller's key.
	before = api.count()
	result = callTool(t, cli, "duplicate_subscription", map[string]any{"managed_anime_id": "a1", "instance_label": "copy"})
	if !result.IsError || api.count() != before || !strings.Contains(textOf(result), "idempotency_key") {
		t.Fatalf("duplicate_subscription without a key must be refused locally: %+v", result)
	}
	callTool(t, cli, "duplicate_subscription", map[string]any{"managed_anime_id": "a1", "instance_label": "copy", "idempotency_key": "duplicate-0001"})
	got = api.last(t)
	if got.Method != http.MethodPost || got.Path != "/api/v1/anime/a1/duplicate" || got.Header.Get("Idempotency-Key") != "duplicate-0001" || !strings.Contains(got.Body, `"instance_label":"copy"`) {
		t.Fatalf("duplicate_subscription request = %+v", got)
	}
}

func TestAPIErrorsSurfaceAsTypedToolErrors(t *testing.T) {
	api := &fakeAPI{respond: func(*http.Request) (int, string) {
		return 409, `{"code":"VERSION_CONFLICT","message":"stale","current_version":9,"request_id":"r-1"}`
	}}
	cli := newTestClient(t, api)
	result := callTool(t, cli, "patch_subscription", map[string]any{"managed_anime_id": "a1", "expected_version": 1, "state": "active"})
	if !result.IsError {
		t.Fatal("a 409 must be a tool error")
	}
	failure, _ := result.StructuredContent.(map[string]any)
	if failure["status"] != float64(409) || failure["code"] != "VERSION_CONFLICT" || failure["current_version"] != float64(9) {
		t.Fatalf("structured error = %+v", result.StructuredContent)
	}
	if !strings.Contains(textOf(result), `"code":"VERSION_CONFLICT"`) {
		t.Fatalf("text error = %q", textOf(result))
	}

	// A write rejected by the maintenance gate is the same typed error.
	api.respond = func(*http.Request) (int, string) {
		return 503, `{"code":"WRITES_REJECTED","message":"The instance is in maintenance; product writes are paused."}`
	}
	result = callTool(t, cli, "refresh_rss", map[string]any{"rss_source_id": "s1"})
	if !result.IsError || !strings.Contains(textOf(result), "WRITES_REJECTED") {
		t.Fatalf("maintenance rejection = %+v", result)
	}
}

func TestArgumentValidationHappensBeforeTheAPI(t *testing.T) {
	api := &fakeAPI{}
	cli := newTestClient(t, api)
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"unknown argument", "get_candidate", map[string]any{"candidate_id": "c1", "extra": 1}, "unknown argument"},
		{"missing path argument", "get_candidate", map[string]any{}, "candidate_id"},
		{"missing version", "patch_subscription", map[string]any{"managed_anime_id": "a1", "state": "active"}, "expected_version"},
		{"missing body field", "parse_title", map[string]any{}, "title"},
		{"non scalar path argument", "get_candidate", map[string]any{"candidate_id": map[string]any{"a": 1}}, "candidate_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := callTool(t, cli, tc.tool, tc.args)
			if !result.IsError || !strings.Contains(textOf(result), tc.want) {
				t.Fatalf("result = %+v, want an error mentioning %q", result, tc.want)
			}
		})
	}
	if api.count() != 0 {
		t.Fatalf("invalid calls reached the API %d times", api.count())
	}
}

func TestPollWorkspaceWaitsForAStableChain(t *testing.T) {
	previous := pollInterval
	pollInterval = 5 * time.Millisecond
	t.Cleanup(func() { pollInterval = previous })

	var reads int
	api := &fakeAPI{respond: func(*http.Request) (int, string) {
		reads++
		if reads < 3 {
			return 200, `{"download_tasks":[{"state":"downloading"}],"artifacts":[],"file_operations":[]}`
		}
		return 200, `{"download_tasks":[{"state":"completed"}],"artifacts":[{"state":"verified"}],"file_operations":[{"state":"completed"}]}`
	}}
	cli := newTestClient(t, api)
	result := callTool(t, cli, "poll_workspace", map[string]any{"managed_anime_id": "a1", "timeout_seconds": 5})
	structured, _ := result.StructuredContent.(map[string]any)
	if result.IsError || structured["stable"] != true || reads != 3 {
		t.Fatalf("poll result = %+v after %d reads", result, reads)
	}
	if got := api.last(t); got.Method != "GET" || got.Path != "/api/v1/anime/a1/workspace" {
		t.Fatalf("poll request = %+v", got)
	}
}

func TestPollWorkspaceTimesOutAndPropagatesErrors(t *testing.T) {
	previous := pollInterval
	pollInterval = 5 * time.Millisecond
	t.Cleanup(func() { pollInterval = previous })

	api := &fakeAPI{respond: func(*http.Request) (int, string) {
		return 200, `{"download_tasks":[{"state":"downloading"}],"artifacts":[],"file_operations":[]}`
	}}
	cli := newTestClient(t, api)
	result := callTool(t, cli, "poll_workspace", map[string]any{"managed_anime_id": "a1", "timeout_seconds": 1})
	structured, _ := result.StructuredContent.(map[string]any)
	if result.IsError || structured["stable"] != false || structured["timed_out"] != true {
		t.Fatalf("poll timeout result = %+v", result)
	}

	api.respond = func(*http.Request) (int, string) { return 404, `{"code":"ANIME_NOT_FOUND","message":"missing"}` }
	result = callTool(t, cli, "poll_workspace", map[string]any{"managed_anime_id": "missing"})
	if !result.IsError || !strings.Contains(textOf(result), "ANIME_NOT_FOUND") {
		t.Fatalf("poll error result = %+v", result)
	}
}

func TestGuideIsServedAsInstructionsResourceAndPrompt(t *testing.T) {
	cli := newTestClient(t, &fakeAPI{})
	ctx := context.Background()

	resources, err := cli.ListResources(ctx, mcp.ListResourcesRequest{})
	if err != nil || len(resources.Resources) != 1 || resources.Resources[0].URI != agentguide.ResourceURI {
		t.Fatalf("resources = %+v, err %v", resources, err)
	}
	read, err := cli.ReadResource(ctx, mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: agentguide.ResourceURI}})
	if err != nil || len(read.Contents) != 1 {
		t.Fatalf("read resource: %v %+v", err, read)
	}
	if text, ok := read.Contents[0].(mcp.TextResourceContents); !ok || text.Text != agentguide.Guide() || text.MIMEType != "text/markdown" {
		t.Fatalf("resource content = %+v", read.Contents[0])
	}

	prompts, err := cli.ListPrompts(ctx, mcp.ListPromptsRequest{})
	if err != nil || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != agentguide.PromptName {
		t.Fatalf("prompts = %+v, err %v", prompts, err)
	}
	prompt, err := cli.GetPrompt(ctx, mcp.GetPromptRequest{Params: mcp.GetPromptParams{Name: agentguide.PromptName}})
	if err != nil || len(prompt.Messages) != 1 {
		t.Fatalf("get prompt: %v %+v", err, prompt)
	}
	if text, ok := prompt.Messages[0].Content.(mcp.TextContent); !ok || text.Text != agentguide.Guide() {
		t.Fatalf("prompt content = %+v", prompt.Messages[0].Content)
	}

	initResult, err := cli.Initialize(ctx, initializeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if initResult.Instructions != agentguide.Instructions() || !strings.Contains(initResult.Instructions, agentguide.ResourceURI) {
		t.Fatalf("instructions = %q", initResult.Instructions)
	}
	if len(initResult.Instructions) > 2000 {
		t.Fatalf("instructions are %d bytes; they must stay a short summary", len(initResult.Instructions))
	}
}

func TestAuthorizationIsForwardedFromTheMCPCaller(t *testing.T) {
	api := &fakeAPI{respond: func(*http.Request) (int, string) { return 200, `[]` }}
	httpServer := httptest.NewServer(Handler(api))
	t.Cleanup(httpServer.Close)

	cli, err := client.NewStreamableHttpClient(httpServer.URL+"/api/v1/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer pat-123"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Initialize(ctx, initializeRequest()); err != nil {
		t.Fatal(err)
	}
	callTool(t, cli, "get_subscriptions", nil)
	got := api.last(t)
	if got.Header.Get("Authorization") != "Bearer pat-123" || got.Path != "/api/v1/anime" {
		t.Fatalf("forwarded request = %+v", got)
	}
}

func TestWorkspaceStableDetectsInFlightOwners(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"empty":                {`{}`, true},
		"download in flight":   {`{"download_tasks":[{"state":"downloading"}]}`, false},
		"download completed":   {`{"download_tasks":[{"state":"completed"}]}`, true},
		"download failed":      {`{"download_tasks":[{"state":"failed"}]}`, true},
		"waiting retry":        {`{"download_tasks":[{"state":"waiting_retry"}]}`, false},
		"artifact pending":     {`{"artifacts":[{"state":"pending"}]}`, false},
		"artifact verified":    {`{"artifacts":[{"state":"verified"}]}`, true},
		"file operation open":  {`{"file_operations":[{"state":"running"}]}`, false},
		"file operation done":  {`{"file_operations":[{"state":"completed"}]}`, true},
		"file operation fails": {`{"file_operations":[{"state":"failed"}]}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var workspace generated.AnimeWorkspace
			if err := json.Unmarshal([]byte(tc.body), &workspace); err != nil {
				t.Fatal(err)
			}
			if got := workspaceStable(workspace); got != tc.want {
				t.Fatalf("stable = %v, want %v", got, tc.want)
			}
		})
	}
}

// Tool output is the REST response minus the properties marked x-mcp-omit, so
// what a tool can reveal is what the response schemas declare without those.
// No schema reachable from an allowlisted operation may declare a feed,
// download, torrent or magnet URL field unless the property is withheld.
// `locator` is the one opaque URL field still returned: the RSS source locator a
// caller typed into a candidate draft, echoed back as entered (SEC-033). Any
// other schema declaring it needs a conscious decision.
func TestAllowlistedResponsesDeclareNoDownloadURLFields(t *testing.T) {
	forbidden := map[string]bool{
		"download_url": true, "feed_url": true, "torrent_url": true,
		"passkey": true, "magnet": true, "magnet_uri": true,
	}
	echoedLocatorSchemas := map[string]bool{"RSSSourceDraft": true}
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	doc := mustParseSpec(t)
	for _, op := range catalog.Operations {
		node := findOperation(doc, op.ID)
		responses, _ := node["responses"].(map[string]any)
		for status, response := range responses {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			for name, owners := range reachableProperties(doc, response, true) {
				if forbidden[name] {
					t.Errorf("%s (%s): response %s can carry the field %q", op.Tool, op.ID, status, name)
				}
				if name == "locator" {
					for owner := range owners {
						if !echoedLocatorSchemas[owner] {
							t.Errorf("%s (%s): response %s declares locator in the unreviewed schema %s", op.Tool, op.ID, status, owner)
						}
					}
				}
			}
		}
	}
}

// The feed snapshot tools and the one-shot readback do reach URL fields in the
// HTTP contract; the guard above holds only because those properties are
// withheld. Pin that, so the guard is not vacuous for them.
func TestWithheldURLFieldsAreReachableOnlyThroughOmission(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	doc := mustParseSpec(t)
	want := map[string][]string{
		"discoverProviderEntryGroups": {"feed_url"},
		"readProviderEntryRSS":        {"download_url", "expected_payload_digest"},
		"previewProviderEntryRSS":     {"download_url", "expected_payload_digest"},
		"getOneShotAcquisition":       {"locator"},
	}
	// The seed view keeps exactly what an agent needs to choose and reference a
	// seed: its key and facts plus the snapshot identity.
	seedView := []string{"stable_seed_key", "title", "published_at", "size_bytes", "info_hash", "snapshot_id", "snapshot_version", "content_hash", "freshness", "group_ref"}
	keptFields := map[string][]string{
		"discoverProviderEntryGroups": {"group_ref", "display_name", "snapshot_version", "provider_entry_ref"},
		"readProviderEntryRSS":        seedView,
		"previewProviderEntryRSS":     seedView,
		"getOneShotAcquisition":       {"stable_seed_key", "snapshot_content_hash", "title"},
	}
	for id, fields := range want {
		op := catalog.Operation(id)
		if op == nil {
			t.Fatalf("%s is not allowlisted", id)
		}
		if !op.Withholds() {
			t.Errorf("%s (%s) must withhold fields from its tool result", op.Tool, id)
		}
		responses, _ := findOperation(doc, id)["responses"].(map[string]any)
		raw, redacted := map[string]bool{}, map[string]bool{}
		for status, response := range responses {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			for name := range reachableProperties(doc, response, false) {
				raw[name] = true
			}
			for name := range reachableProperties(doc, response, true) {
				redacted[name] = true
			}
		}
		for _, field := range fields {
			if !raw[field] {
				t.Errorf("%s: the HTTP response no longer declares %q; update this test", id, field)
			}
			if redacted[field] && field != "locator" {
				t.Errorf("%s: %q is still reachable in the tool result", id, field)
			}
		}
		for _, kept := range keptFields[id] {
			if !redacted[kept] {
				t.Errorf("%s: the tool result lost %q", id, kept)
			}
		}
	}
}

// x-mcp-omit is a property-level marker with the literal value true.
func TestOmitMarkersArePropertyLevel(t *testing.T) {
	doc := mustParseSpec(t)
	count := 0
	var walk func(node any, underProperties bool, path string)
	walk = func(node any, underProperties bool, path string) {
		switch typed := node.(type) {
		case map[string]any:
			for key, value := range typed {
				if key == omitExtension {
					count++
					if value != true {
						t.Errorf("%s: x-mcp-omit must be true, got %v", path, value)
					}
					if !underProperties {
						t.Errorf("%s: x-mcp-omit must sit on a schema property", path)
					}
					continue
				}
				if key == "properties" {
					if props, ok := value.(map[string]any); ok {
						for name, prop := range props {
							walk(prop, true, path+"/properties/"+name)
						}
					}
					continue
				}
				walk(value, false, path+"/"+key)
			}
		case []any:
			for _, value := range typed {
				walk(value, false, path)
			}
		}
	}
	walk(doc, false, "")
	if count == 0 {
		t.Fatal("no x-mcp-omit marker found; the URL guard relies on them")
	}
}

func mustParseSpec(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(api.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func findOperation(doc map[string]any, id string) map[string]any {
	paths, _ := doc["paths"].(map[string]any)
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, node := range methods {
			if op, ok := node.(map[string]any); ok && op["operationId"] == id {
				return op
			}
		}
	}
	return nil
}

// reachableProperties maps every property name declared anywhere below a
// schema, response or reference to the named schemas declaring it, following
// $ref once per target.
func reachableProperties(doc map[string]any, root any, honorOmit bool) map[string]map[string]bool {
	found := map[string]map[string]bool{}
	seen := map[string]bool{}
	var walk func(node any, owner string)
	walk = func(node any, owner string) {
		switch typed := node.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok {
				if seen[ref] {
					return
				}
				seen[ref] = true
				parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
				var target any = doc
				for _, part := range parts {
					target, _ = target.(map[string]any)[part]
				}
				walk(target, parts[len(parts)-1])
				return
			}
			for key, value := range typed {
				if key != "properties" {
					walk(value, owner)
					continue
				}
				props, _ := value.(map[string]any)
				for name, prop := range props {
					if honorOmit && isOmitted(prop) {
						continue
					}
					if found[name] == nil {
						found[name] = map[string]bool{}
					}
					found[name][owner] = true
					walk(prop, owner)
				}
			}
		case []any:
			for _, value := range typed {
				walk(value, owner)
			}
		}
	}
	walk(root, "")
	return found
}

// The feed snapshot tools return the HTTP body minus the withheld properties:
// seeds keep their key, facts and the snapshot identity, never the download
// URL. The one-shot readback drops the frozen seed locator the same way.
func TestToolResultsWithholdOmittedProperties(t *testing.T) {
	const secret = "https://tracker.example.test/download/1?passkey=SECRETKEY"
	snapshot := `{"provider":"mikan","provider_entry_ref":"3001","group_ref":"678","snapshot_id":"snap-1",
		"snapshot_version":4,"freshness":"fresh","content_hash":"sha256:` + strings.Repeat("c", 64) + `",
		"seeds":[{"stable_seed_key":"seed-1","title":"[G] Show - 01","published_at":"2026-10-01T00:00:00Z",
		"download_url":"` + secret + `","size_bytes":10,"info_hash":"` + strings.Repeat("a", 40) + `",
		"expected_payload_digest":"sha256:` + strings.Repeat("d", 64) + `"}]}`
	groups := `{"provider":"mikan","provider_entry_ref":"3001","observed_at":"2026-10-01T00:00:00Z","source_version":"v1",
		"schema_version":"s1","snapshot_version":2,"groups":[{"provider":"mikan","group_ref":"678","display_name":"G",
		"feed_url":"` + secret + `","observed_at":"2026-10-01T00:00:00Z"}]}`
	acquisition := `{"one_shot_id":"os-1","state":"running","managed_anime_id":"a","subscription_id":"s","rss_source_id":"r",
		"release_id":"rel","version":1,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z",
		"input_snapshot":{"selected_seed":{"provider":"mikan","provider_entry_ref":"3001","provider_group_ref":"678",
		"stable_seed_key":"seed-1","locator":"` + secret + `","title":"[G] Show - 01","published_at":"2026-10-01T00:00:00Z"},
		"effective_sections":{"rss":{"locator":"` + secret + `","value":"keep-me","nested":[{"guid":"` + secret + `"}]}},
		"section_versions":{},"section_schemas":{},"effective_digest":"sha256:x","config_identity":{}},
		"terminal_reason":"fetch of ` + secret + ` failed",
		"download_task":{"id":"dt-1","release_id":"rel","state":"submitted","version":1,
		"frozen_input":{"release_stable_item_key":"` + secret + `","source_locator_display":"tracker.example.test",
		"selected_release":{"release_id":"rel","rss_source_id":"r","stable_item_key":"` + secret + `","release_version":1,
		"subscription_id":"s","managed_anime_id":"a"}}}}`
	api := &fakeAPI{respond: func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/groups:discover"):
			return http.StatusOK, groups
		case strings.HasSuffix(r.URL.Path, "/rss"):
			return http.StatusOK, snapshot
		case strings.HasPrefix(r.URL.Path, "/api/v1/one-shot-acquisitions/"):
			return http.StatusOK, acquisition
		}
		return http.StatusNotFound, `{"code":"NOT_FOUND","message":"no"}`
	}}
	cli := newTestClient(t, api)
	calls := []struct {
		tool string
		args map[string]any
		keep []string
	}{
		{"discover_feed_groups", map[string]any{"provider": "mikan", "provider_entry_ref": "3001"}, []string{`"group_ref":"678"`, `"display_name":"G"`, `"snapshot_version":2`}},
		{"get_feed_snapshot", map[string]any{"provider": "mikan", "provider_entry_ref": "3001", "group_ref": "678"}, []string{`"stable_seed_key":"seed-1"`, `"snapshot_id":"snap-1"`, `"snapshot_version":4`, `"info_hash"`, `"size_bytes":10`, `"published_at"`}},
		{"refresh_feed_snapshot", map[string]any{"provider": "mikan", "provider_entry_ref": "3001", "group_ref": "678"}, []string{`"stable_seed_key":"seed-1"`, `"content_hash"`}},
		{"get_one_shot_acquisition", map[string]any{"one_shot_id": "os-1"}, []string{`"stable_seed_key":"seed-1"`, `"provider_group_ref":"678"`,
			`"value":"keep-me"`, `"release_version":1`, `"terminal_reason":"fetch of ` + withheldPlaceholder + ` failed"`}},
	}
	for _, call := range calls {
		result := callTool(t, cli, call.tool, call.args)
		if result.IsError {
			t.Fatalf("%s: unexpected error %s", call.tool, textOf(result))
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{"SECRETKEY", "tracker.example.test", `locator_display"`, "download_url", "feed_url", "expected_payload_digest", `"locator"`, "stable_item_key", `"guid"`} {
			if strings.Contains(string(encoded), leaked) {
				t.Errorf("%s: tool result carries %s: %s", call.tool, leaked, encoded)
			}
		}
		for _, kept := range call.keep {
			if !strings.Contains(string(encoded), kept) {
				t.Errorf("%s: tool result lost %s: %s", call.tool, kept, encoded)
			}
		}
	}
	if got := api.last(t); got.Method != http.MethodGet || !strings.HasPrefix(got.Path, "/api/v1/one-shot-acquisitions/") {
		t.Fatalf("last request = %s %s", got.Method, got.Path)
	}
}

// A body the redactor cannot decode is refused instead of passed through.
func TestUndecodableBodyOfAWithholdingToolIsRefused(t *testing.T) {
	api := &fakeAPI{respond: func(r *http.Request) (int, string) { return http.StatusOK, "download_url=https://x/?passkey=1" }}
	cli := newTestClient(t, api)
	result := callTool(t, cli, "get_feed_snapshot", map[string]any{"provider": "mikan", "provider_entry_ref": "3001", "group_ref": "678"})
	if !result.IsError || strings.Contains(textOf(result), "passkey") {
		t.Fatalf("result = %+v", result)
	}
}

// Every x-mcp-omit property name is also withheld inside free-form subtrees,
// so a value that escapes its schema (a free-form object, an undeclared key)
// cannot carry a withheld field under the same name.
func TestWithheldNamesCoverEveryOmittedProperty(t *testing.T) {
	doc := mustParseSpec(t)
	names := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if props, ok := typed["properties"].(map[string]any); ok {
				for name, property := range props {
					if isOmitted(property) {
						names[name] = true
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(doc)
	if len(names) == 0 {
		t.Fatal("no x-mcp-omit property found")
	}
	for name := range names {
		if !withheldNames[name] {
			t.Errorf("x-mcp-omit property %q is missing from withheldNames", name)
		}
	}
	for _, name := range []string{"stable_item_key", "release_stable_item_key", "newest_stable_item_key", "identity_value", "cursor", "download_url", "feed_url", "locator"} {
		if !names[name] {
			t.Errorf("%s is no longer marked x-mcp-omit in the OpenAPI file", name)
		}
	}
}

// x-mcp-keep-url exempts a property from the value backstop. The exemptions
// are pinned, and since cover_url and poster_url became same-origin proxy paths
// (no host, no query) there are none: an image field that regains a third-party
// URL would be withheld rather than exempted.
func TestKeepURLExemptionsArePinned(t *testing.T) {
	doc := mustParseSpec(t)
	got := map[string]bool{}
	schemas := asMap(asMap(doc["components"])["schemas"])
	for name, schema := range schemas {
		for property, node := range asMap(asMap(schema)["properties"]) {
			value, present := asMap(node)[keepURLExtension]
			if !present {
				continue
			}
			if value != true {
				t.Errorf("%s.%s: x-mcp-keep-url must be true", name, property)
			}
			if isOmitted(node) {
				t.Errorf("%s.%s: a property cannot be both omitted and kept", name, property)
			}
			got[name+"."+property] = true
		}
	}
	want := []string{}
	if len(got) != len(want) {
		t.Errorf("x-mcp-keep-url properties = %v, want %v", got, want)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("%s must carry x-mcp-keep-url", name)
		}
	}
}

func TestValueBackstopWithholdsCredentialURLs(t *testing.T) {
	cases := map[string]string{
		"https://tracker.example.test/dl/1.torrent?passkey=abc":        withheldPlaceholder,
		"http://user:pw@feeds.example.test/rss":                        withheldPlaceholder,
		"HTTPS://Tracker.Example.Test/x?":                              withheldPlaceholder,
		"see https://t.example.test/a?k=1 and https://ok.example.test": "see " + withheldPlaceholder + " and https://ok.example.test",
		"https://image.tmdb.org/t/p/w342/poster.jpg":                   "https://image.tmdb.org/t/p/w342/poster.jpg",
		"/api/v1/images/poster/123":                                    "/api/v1/images/poster/123",
		"/api/v1/images/cover/abc":                                     "/api/v1/images/cover/abc",
		"[Group] Show - 01 [1080p]":                                    "[Group] Show - 01 [1080p]",
		"magnet:?xt=urn:btih:abc":                                      "magnet:?xt=urn:btih:abc",
		// Path-embedded keys (UNIT3D download and feed paths); synthetic keys.
		"https://t.example.test/torrents/download/7.0123456789abcdef0123456789abcdef": withheldPlaceholder,
		"failed https://t.example.test/rss/1.0123456789abcdef0123456789abcdef now":    "failed " + withheldPlaceholder + " now",
		"https://nyaa.si/download/1234567.torrent":                                    "https://nyaa.si/download/1234567.torrent",
	}
	for input, want := range cases {
		if got := withholdURLs(input); got != want {
			t.Errorf("withholdURLs(%q) = %q, want %q", input, got, want)
		}
	}
}

// The backstop applies to every tool, including ones whose schema reaches no
// x-mcp-omit property, to error results, and to undeclared keys; an exempted
// property keeps its URL.
func TestEveryToolResultPassesTheBackstop(t *testing.T) {
	const secret = "https://tracker.example.test/rss?token=SECRETKEY"
	api := &fakeAPI{respond: func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search"):
			return http.StatusOK, `[{"provider":"mikan","provider_entry_ref":"1","title":"Show","cover_url":"/api/v1/images/cover/` + strings.Repeat("ab", 32) + `","poster_url":"https://mikanani.example/images/1.jpg?width=400","undeclared_note":"` + secret + `","link":"` + secret + `"}]`
		case r.URL.Path == "/api/v1/anime":
			return http.StatusOK, `[{"id":"a","title":"from ` + secret + `"}]`
		}
		return http.StatusUnprocessableEntity, `{"code":"FEED_FAILED","message":"fetch ` + secret + ` failed","details":{"locator":"` + secret + `"}}`
	}}
	cli := newTestClient(t, api)
	search := callTool(t, cli, "search_anime", map[string]any{"provider": "mikan", "query": "show"})
	encoded, _ := json.Marshal(search)
	if search.IsError || !strings.Contains(string(encoded), "/api/v1/images/cover/"+strings.Repeat("ab", 32)) {
		t.Fatalf("search_anime must keep the same-origin cover_url path: %s", encoded)
	}
	if strings.Contains(string(encoded), "images/1.jpg?width=400") {
		t.Fatalf("search_anime must withhold a third-party image URL with a query: %s", encoded)
	}
	for _, result := range []*mcp.CallToolResult{search, callTool(t, cli, "get_subscriptions", map[string]any{}), callTool(t, cli, "get_candidate", map[string]any{"candidate_id": "c-1"})} {
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "SECRETKEY") || strings.Contains(string(encoded), `"link"`) {
			t.Errorf("tool result carries a credential URL: %s", encoded)
		}
	}
}
