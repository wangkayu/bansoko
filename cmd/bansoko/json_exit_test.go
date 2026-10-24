package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bansoko/internal/agentguide"
)

// routeFake answers every request from a method+path table, so a test can
// drive any subcommand and choose the exact status the CLI must handle.
type routeFake struct {
	routes map[string]route
	seen   []string
}

type route struct {
	status int
	body   string
}

func (f *routeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/v1")
	f.seen = append(f.seen, key)
	entry, ok := f.routes[key]
	if !ok {
		entry = route{status: http.StatusNotFound, body: `{"code":"NOT_FOUND","message":"no such route ` + key + `"}`}
	}
	if entry.body != "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(entry.status)
	_, _ = w.Write([]byte(entry.body))
}

// runCaptured runs one invocation through execute and returns its exit code
// with stdout and stderr captured separately.
func runCaptured(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	outFile, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	code := execute(args)
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outFile.Close()
	_ = errFile.Close()
	stdout, _ := os.ReadFile(outFile.Name())
	stderr, _ := os.ReadFile(errFile.Name())
	return code, string(stdout), string(stderr)
}

// singleJSONDocument fails unless s is exactly one JSON value.
func singleJSONDocument(t *testing.T, label, s string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(s))
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("%s: stdout is not a JSON document: %v\n%q", label, err, s)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("%s: stdout holds more than one JSON document:\n%q", label, s)
	}
	return value
}

const stubWorkspaceDone = `{"managed_anime":{"id":"anime-1"},"download_tasks":[{"id":"d1","state":"completed"}],"artifacts":[{"id":"a1","state":"verified"}],"file_operations":[{"id":"f1","operation":"hardlink","state":"completed"}]}`

func jsonModeRoutes() map[string]route {
	return map[string]route{
		"POST /auth/login":                                         {200, `{"session_token":"tok","expires_at":"2030-01-01T00:00:00Z","owner":{"id":"o1","role":"owner","username":"owner"}}`},
		"GET /anime":                                               {200, `[]`},
		"GET /catalog/providers/anibt/search":                      {200, `[]`},
		"POST /parse/release-title":                                {200, `{"parser_version":"p1","facts":{}}`},
		"POST /parse/torrent-files":                                {200, `{"name":"n","info_hash":"h","files":[],"total_size":1,"parser_version":"p1"}`},
		"POST /anime/rss-list-view":                                {200, `{"groups":[]}`},
		"GET /anime-config-candidates":                             {200, `[]`},
		"GET /anime-config-candidates/cand-1":                      {200, `{"id":"cand-1","version":1}`},
		"POST /anime-config-candidates":                            {201, `{"id":"cand-1","version":1,"title":"t"}`},
		"PATCH /anime-config-candidates/cand-1":                    {200, `{"id":"cand-1","version":2,"title":"t"}`},
		"POST /anime-config-candidates/cand-1/subscribe":           {201, `{"managed_anime":{"id":"anime-1"},"subscription":{"id":"s1"},"rss_sources":[]}`},
		"DELETE /anime-config-candidates/cand-1":                   {204, ``},
		"POST /catalog/providers/anibt/entries/e1/groups:discover": {200, `{"provider":"anibt","provider_entry_ref":"e1","snapshot_version":1,"groups":[]}`},
		"GET /catalog/providers/anibt/entries/e1/rss":              {200, `{"provider":"anibt"}`},
		"POST /catalog/providers/anibt/entries/e1/rss":             {200, `{"provider":"anibt"}`},
		"POST /one-shot-acquisitions":                              {201, `{"one_shot_id":"shot-1"}`},
		"GET /one-shot-acquisitions/shot-1":                        {200, `{"one_shot_id":"shot-1","state":"running","version":1}`},
		"GET /one-shot-acquisitions/shot-1/attempts":               {200, `{"items":[]}`},
		"GET /one-shot-acquisitions/shot-1/attempts/att-1":         {200, `{"id":"att-1"}`},
		"GET /metadata/search":                                     {200, `{"provider":"bgm","candidates":[]}`},
		"POST /rss-sources/src-1/refresh":                          {202, `{"accepted":true,"work_item_id":"w1"}`},
		"POST /releases/rel-1/retry":                               {200, `{"release_id":"rel-1","outcome":"accepted"}`},
		"POST /acquisitions/acq-1/import/retry":                    {202, `{"acquisition_id":"acq-1","attempt":2,"retried_files":1,"reopened_placements":0,"version":4}`},
		"GET /anime/anime-1/workspace":                             {200, stubWorkspaceDone},
		"POST /artifacts/art-1/retry":                              {202, `{"owner_type":"artifact","owner_id":"art-1"}`},
		"POST /api-tokens":                                         {201, `{"token_id":"t1","token":"raw","name":"n"}`},
		"GET /api-tokens":                                          {200, `[]`},
		"DELETE /api-tokens/t1":                                    {204, ``},
	}
}

// Every subcommand with the global --json prints exactly one JSON document on
// stdout (before or after the command name) and keeps human text off stdout.
func TestJSONModePrintsExactlyOneDocumentForEverySubcommand(t *testing.T) {
	withCache(t)
	fake := &routeFake{routes: jsonModeRoutes()}
	ts := httptest.NewServer(fake)
	defer ts.Close()

	dir := t.TempDir()
	draftFile := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(draftFile, []byte(`{"draft":`+validAnimeDraftJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bareDraftFile := filepath.Join(dir, "bare-draft.json")
	if err := os.WriteFile(bareDraftFile, []byte(validAnimeDraftJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := [][]string{
		{"login", "--username", "owner", "--password", "pw"},
		{"list"},
		{"search", "--query", "frieren"},
		{"parse-title", "--title", "[G] Show - 01"},
		{"parse-torrent", "--url", "https://example.test/a.torrent"},
		{"preview", "--draft-file", draftFile},
		{"candidate", "list"},
		{"candidate", "get", "--id", "cand-1"},
		{"candidate", "create", "--draft-file", draftFile},
		{"candidate", "patch", "--id", "cand-1", "--draft-file", draftFile, "--version", "1"},
		{"candidate", "activate", "--id", "cand-1", "--revision", "1", "--version", "1"},
		{"candidate", "preview", "--id", "cand-1", "--revision", "1"},
		{"candidate", "delete", "--id", "cand-1"},
		{"feed", "groups", "--provider", "anibt", "--entry-ref", "e1"},
		{"feed", "snapshot", "--provider", "anibt", "--entry-ref", "e1", "--group-ref", "g1"},
		{"feed", "refresh", "--provider", "anibt", "--entry-ref", "e1", "--group-ref", "g1"},
		{"one-shot", "acquire", "--draft-json", validAnimeDraftJSON, "--seed-json", validSelectedSeedJSON},
		{"one-shot", "get", "--id", "shot-1"},
		{"one-shot", "attempts", "--id", "shot-1"},
		{"one-shot", "attempt", "--id", "shot-1", "--attempt-id", "att-1"},
		{"metadata", "search", "--q", "Show"},
		{"refresh", "--source-id", "src-1"},
		{"retry-release", "--release-id", "rel-1"},
		{"retry-import", "--acquisition-id", "acq-1", "--version", "3"},
		{"status", "--anime-id", "anime-1"},
		{"wait", "--anime-id", "anime-1", "--interval", "1ms", "--timeout", "5s"},
		{"retry", "--owner", "artifact", "--owner-id", "art-1", "--work-item-id", "w1"},
		{"api-token", "create", "--name", "n"},
		{"api-token", "list"},
		{"api-token", "revoke", "--id", "t1"},
		{"guide"},
		{"guide", "--skill"},
	}
	_ = bareDraftFile
	for _, base := range cases {
		label := strings.Join(base, " ")
		t.Run(label, func(t *testing.T) {
			connection := []string{"--endpoint", ts.URL, "--token", "tok"}
			switch base[0] {
			case "login":
				connection = []string{"--endpoint", ts.URL}
			case "guide":
				connection = nil
			}
			// --json after the command ...
			after := append(append(append([]string{}, base...), connection...), "--json")
			code, stdout, stderr := runCaptured(t, after...)
			if code != exitOK {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			singleJSONDocument(t, label, stdout)
			// ... and before it.
			before := append(append([]string{"--json"}, base...), connection...)
			code, stdout, stderr = runCaptured(t, before...)
			if code != exitOK {
				t.Fatalf("--json first: exit %d, stderr %q", code, stderr)
			}
			singleJSONDocument(t, label+" (--json first)", stdout)
		})
	}
}

func TestJSONModeFailureIsOneErrorDocumentWithTheExitCode(t *testing.T) {
	withCache(t)
	fake := &routeFake{routes: map[string]route{
		"GET /anime":                       {404, `{"code":"ANIME_NOT_FOUND","message":"missing"}`},
		"PATCH /anime-config-candidates/c": {409, `{"code":"VERSION_CONFLICT","message":"stale","current_version":9}`},
	}}
	ts := httptest.NewServer(fake)
	defer ts.Close()

	code, stdout, stderr := runCaptured(t, "--json", "list", "--endpoint", ts.URL, "--token", "tok")
	if code != exitNotFound {
		t.Fatalf("exit = %d, want %d", code, exitNotFound)
	}
	doc, _ := singleJSONDocument(t, "404", stdout).(map[string]any)
	detail, _ := doc["error"].(map[string]any)
	if detail["code"] != "ANIME_NOT_FOUND" || detail["http_status"] != float64(404) || detail["exit_code"] != float64(exitNotFound) || detail["message"] != "missing" {
		t.Fatalf("error document = %v", doc)
	}
	if !strings.Contains(stderr, "ANIME_NOT_FOUND") {
		t.Fatalf("stderr should carry the human message, got %q", stderr)
	}

	file := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(file, []byte(`{"draft":`+validAnimeDraftJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runCaptured(t, "candidate", "patch", "--id", "c", "--draft-file", file, "--version", "1", "--json", "--endpoint", ts.URL, "--token", "tok")
	doc, _ = singleJSONDocument(t, "409", stdout).(map[string]any)
	detail, _ = doc["error"].(map[string]any)
	if code != exitConflict || detail["current_version"] != float64(9) {
		t.Fatalf("conflict exit %d, document %v", code, doc)
	}

	// Usage errors are documents too.
	code, stdout, _ = runCaptured(t, "--json", "candidate", "get")
	doc, _ = singleJSONDocument(t, "usage", stdout).(map[string]any)
	detail, _ = doc["error"].(map[string]any)
	if code != exitUsage || detail["code"] != "USAGE" {
		t.Fatalf("usage exit %d, document %v", code, doc)
	}
}

// The exit-code table is part of the agent contract (see the agent guide).
func TestExitCodeTable(t *testing.T) {
	withCache(t)
	statusCases := []struct {
		status int
		want   int
	}{
		{400, exitOther}, {401, exitAuth}, {403, exitAuth}, {404, exitNotFound}, {410, exitNotFound},
		{409, exitConflict}, {412, exitConflict}, {422, exitConflict}, {428, exitConflict},
		{429, exitOther}, {500, exitUnavailable}, {502, exitUnavailable}, {503, exitUnavailable},
	}
	for _, tc := range statusCases {
		if got := exitForStatus(tc.status); got != tc.want {
			t.Errorf("HTTP %d -> exit %d, want %d", tc.status, got, tc.want)
		}
	}
	// The numeric values themselves are pinned.
	if exitOK != 0 || exitOther != 1 || exitUsage != 2 || exitAuth != 3 || exitNotFound != 4 || exitConflict != 5 || exitUnavailable != 6 {
		t.Fatal("exit code values changed; update the table, the guide and this test together")
	}

	// End to end through execute: a server answer picks the code.
	for _, tc := range statusCases {
		fake := &routeFake{routes: map[string]route{"GET /anime": {tc.status, `{"code":"X","message":"m"}`}}}
		ts := httptest.NewServer(fake)
		code, _, _ := runCaptured(t, "list", "--endpoint", ts.URL, "--token", "tok")
		ts.Close()
		if code != tc.want {
			t.Errorf("list with HTTP %d exited %d, want %d", tc.status, code, tc.want)
		}
	}

	usageCases := [][]string{
		{},
		{"no-such-command"},
		{"candidate"},
		{"candidate", "no-such"},
		{"candidate", "get"},
		{"list", "--no-such-flag"},
		{"guide", "extra"},
		{"guide", "--name", "x"},
		{"api-token", "revoke"},
	}
	for _, args := range usageCases {
		if code, _, _ := runCaptured(t, append(args, "--endpoint", "http://127.0.0.1:1", "--token", "tok")...); code != exitUsage {
			t.Errorf("%v exited %d, want %d", args, code, exitUsage)
		}
	}

	// No credential: authentication class, before any request.
	if code, _, _ := runCaptured(t, "list", "--endpoint", "http://127.0.0.1:1"); code != exitAuth {
		t.Errorf("missing credential exited %d, want %d", code, exitAuth)
	}
	// Nothing listens there: unreachable.
	if code, _, _ := runCaptured(t, "list", "--endpoint", "http://127.0.0.1:1", "--token", "tok"); code != exitUnavailable {
		t.Errorf("unreachable server exited %d, want %d", code, exitUnavailable)
	}
	// A failed pipeline is the generic failure.
	failing := `{"download_tasks":[{"id":"d1","state":"failed","failure_reason":"x"}],"artifacts":[],"file_operations":[]}`
	fake := &routeFake{routes: map[string]route{"GET /anime/a/workspace": {200, failing}}}
	ts := httptest.NewServer(fake)
	defer ts.Close()
	if code, _, _ := runCaptured(t, "wait", "--anime-id", "a", "--interval", "1ms", "--timeout", "5s", "--endpoint", ts.URL, "--token", "tok"); code != exitOther {
		t.Errorf("failed pipeline exited %d, want %d", code, exitOther)
	}
	// -h is not an error.
	if code, _, _ := runCaptured(t, "list", "-h"); code != exitOK {
		t.Errorf("list -h exited %d, want 0", code)
	}
}

func TestGuideCommand(t *testing.T) {
	code, stdout, _ := runCaptured(t, "guide")
	if code != exitOK || stdout != agentguide.Guide() {
		t.Fatalf("guide exit %d, %d bytes, want the embedded guide", code, len(stdout))
	}
	code, stdout, _ = runCaptured(t, "guide", "--skill")
	if code != exitOK || stdout != agentguide.Skill(agentguide.SkillName) {
		t.Fatalf("guide --skill exit %d", code)
	}
	code, stdout, _ = runCaptured(t, "guide", "--skill", "--name", "bansoko-cli")
	if code != exitOK || !strings.HasPrefix(stdout, "---\nname: bansoko-cli\ndescription: ") {
		t.Fatalf("guide --skill --name exit %d, start %q", code, stdout[:min(60, len(stdout))])
	}
	code, stdout, _ = runCaptured(t, "guide", "--json")
	doc, _ := singleJSONDocument(t, "guide", stdout).(map[string]any)
	if code != exitOK || doc["format"] != "guide" || doc["content"] != agentguide.Guide() {
		t.Fatalf("guide --json = exit %d", code)
	}
}

// Every `bansoko ...` command the agent guide names must exist: the command
// path must be accepted by the dispatcher (its -h answers with flag help
// instead of an "unsupported command" usage error).
func TestGuideNamesOnlyExistingCommands(t *testing.T) {
	span := regexp.MustCompile("`(bansoko [^`]+)`")
	word := regexp.MustCompile(`^[a-z][a-z-]*$`)
	seen := map[string]bool{}
	for _, match := range span.FindAllStringSubmatch(agentguide.Guide(), -1) {
		tokens := strings.Fields(match[1])[1:]
		var path []string
		for _, token := range tokens {
			if !word.MatchString(token) {
				break
			}
			path = append(path, token)
		}
		if len(path) == 0 {
			t.Errorf("guide command %q names no subcommand", match[1])
			continue
		}
		key := strings.Join(path, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		oldErr := os.Stderr
		devNull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		os.Stderr = devNull
		err := run(append(append([]string{}, path...), "-h"))
		os.Stderr = oldErr
		_ = devNull.Close()
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("guide names `bansoko %s`, which the CLI does not accept (%v)", key, err)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the guide names no bansoko command")
	}
}

// The usage text lists every command the dispatcher accepts, so the help an
// agent reads cannot drift from the code.
func TestUsageListsEveryCommand(t *testing.T) {
	text := usage()
	for _, command := range []string{
		"login", "list", "search", "parse-title", "parse-torrent", "preview", "candidate", "feed",
		"one-shot", "metadata", "refresh", "retry-release", "retry-import", "status", "wait",
		"retry", "api-token", "guide",
	} {
		if !strings.Contains(text, "bansoko "+command) {
			t.Errorf("usage does not list %q", command)
		}
	}
}
