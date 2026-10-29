// Command bansoko is the thin CLI companion to the REST contract and
// the embedded MCP server. It reuses the generated OpenAPI types for request
// bodies and JSON output, so there is no second contract to keep in sync. A
// session token (login) or a long-lived API token (api-token create) is cached
// under ~/.config/bansoko with 0600 permissions.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const apiVersionPrefix = "/api/v1"

func main() {
	os.Exit(execute(os.Args[1:]))
}

// execute runs one invocation and returns the process exit code (see
// exitcode.go for the fixed table). With the global --json flag stdout carries
// exactly one JSON document: the command result, or on failure an error
// document; all human-readable text goes to stderr.
func execute(args []string) int {
	args, wantJSON := extractGlobalJSON(args)
	jsonMode, jsonEmitted = wantJSON, false
	defer func() { jsonMode, jsonEmitted = false, false }()
	err := run(args)
	if err == nil {
		if jsonMode && !jsonEmitted {
			emitJSON(map[string]any{"ok": true})
		}
		return exitOK
	}
	if errors.Is(err, errHelp) {
		return exitOK
	}
	fmt.Fprintln(os.Stderr, "bansoko:", err)
	if jsonMode && !jsonEmitted {
		emitJSON(errorDocument(err))
	}
	return exitCodeOf(err)
}

func run(args []string) error {
	if len(args) == 0 {
		return newUsageError(usage())
	}
	command := args[0]
	switch command {
	case "login":
		return cmdLogin(args[1:])
	case "list":
		return cmdList(args[1:])
	case "search":
		return cmdSearch(args[1:])
	case "parse-title":
		return cmdParseTitle(args[1:])
	case "parse-torrent":
		return cmdParseTorrent(args[1:])
	case "preview":
		return cmdPreview(args[1:])
	case "refresh":
		return cmdRefresh(args[1:])
	case "retry-release":
		return cmdRetryRelease(args[1:])
	case "retry-import":
		return cmdRetryImport(args[1:])
	case "cancel-download":
		return cmdCancelDownload(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "retry":
		return cmdRetry(args[1:])
	case "candidate":
		return cmdCandidate(args[1:])
	case "feed":
		return cmdFeed(args[1:])
	case "one-shot":
		return cmdOneShot(args[1:])
	case "metadata":
		return cmdMetadata(args[1:])
	case "api-token":
		return cmdAPIToken(args[1:])
	case "guide":
		return cmdGuide(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage())
		return nil
	default:
		return usageErrorf("unsupported bansoko command %q\n\n%s", command, usage())
	}
}

func usage() string {
	return `bansoko drives the API over REST. MCP-first agents should use the
embedded MCP server at /api/v1/mcp; bansoko is the scriptable fallback.

Usage:
  bansoko login --username USER --password PASS      cache a session token
  bansoko list [--json]                              list managed anime
  bansoko search [--provider anibt|mikan] --query Q  provider keyword search
  bansoko parse-title --title TITLE                  parse a release title
  bansoko parse-torrent --url URL                    fetch+parse a torrent
  bansoko preview --draft-file FILE [feed flags]     current_input RSS list view
  bansoko candidate list [--origin O] [--ref R]      list config candidates
  bansoko candidate get --id ID                      show a candidate
  bansoko candidate create --draft-file FILE         save a new candidate
  bansoko candidate patch --id ID --draft-file FILE --version V  CAS update
  bansoko candidate activate --id ID --revision R --version V [--key K]
  bansoko candidate preview --id ID --revision R [feed flags]  candidate_revision RSS list view
  bansoko candidate delete --id ID                   delete a candidate
  bansoko feed groups --provider P --entry-ref R     discover provider groups
  bansoko feed snapshot --provider P --entry-ref R --group-ref G
  bansoko feed refresh --provider P --entry-ref R --group-ref G
  bansoko one-shot acquire --seed-json JSON [input flags]
  bansoko one-shot get --id ID                       show one-shot state
  bansoko one-shot attempts --id ID                  list execution attempts
  bansoko one-shot attempt --id ID --attempt-id ID   show one attempt
  bansoko metadata search --q QUERY [--provider bgm|tmdb]
  bansoko refresh --source-id ID                     manual RSS refresh
  bansoko retry-release --release-id ID              accept a failed release again
  bansoko retry-import --acquisition-id ID --version V [--key K]  retry a failed import stage
  bansoko cancel-download --acquisition-id ID --version V [--key K]  cancel a download before import
  bansoko status --anime-id ID [--json]              owner-chain status
  bansoko wait --anime-id ID [--interval S] [--timeout S]
  bansoko retry --owner rss|download|artifact --owner-id ID [--reason R]
  bansoko api-token create --name N [--scopes read,write] | list | revoke --id ID
  bansoko guide [--skill] [--name N]                 print the agent guide (SKILL.md with --skill)

Global:
  --json       stdout carries exactly one JSON document (the result, or an error
               document on failure); human text goes to stderr
  --endpoint   API origin (default http://127.0.0.1:8080, or $BANSOKO_ENDPOINT)
  --token      explicit bearer token overriding the cached credential

Exit codes: 0 ok, 1 other failure, 2 usage error, 3 authentication or permission,
4 not found, 5 conflict or precondition (409, 412, 422, 428), 6 server
unreachable or 5xx.

Token cache: ~/.config/bansoko/ (token, config), 0600.
`
}

// --- flag helpers ---

// commonFlags carries the flags shared by every authenticated command.
type commonFlags struct {
	json     bool
	endpoint string
	token    string
}

func newCommonFlags(fs *flag.FlagSet) *commonFlags {
	cf := &commonFlags{json: jsonMode}
	fs.BoolVar(&cf.json, "json", jsonMode, "print the raw JSON response (also accepted before the command)")
	fs.StringVar(&cf.endpoint, "endpoint", "", "API origin (default $BANSOKO_ENDPOINT or http://127.0.0.1:8080)")
	fs.StringVar(&cf.token, "token", "", "explicit bearer token (default: cached credential)")
	return cf
}

func (cf *commonFlags) resolve() (*client, error) {
	endpoint := cf.endpoint
	if endpoint == "" {
		endpoint = os.Getenv("BANSOKO_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080"
	}
	endpoint = strings.TrimRight(endpoint, "/")
	token := cf.token
	if token == "" {
		cached, err := readToken()
		if err != nil {
			return nil, err
		}
		token = cached
	}
	return &client{endpoint: endpoint, token: token}, nil
}

// --- config / token cache ---

// configDirOverride lets tests redirect the token cache away from the real
// user configuration directory.
var configDirOverride string

func configDir() (string, error) {
	if configDirOverride != "" {
		if err := os.MkdirAll(configDirOverride, 0o700); err != nil {
			return "", err
		}
		return configDirOverride, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "bansoko")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func writeToken(token string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "token"), []byte(strings.TrimSpace(token)+"\n"), 0o600)
}

func readToken() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return "", &cliError{exit: exitAuth, code: "NO_CREDENTIAL", message: "no cached credential; run 'bansoko login' or 'bansoko api-token create', or pass --token"}
	}
	return strings.TrimSpace(string(raw)), nil
}

// --- HTTP client ---

type client struct {
	endpoint string
	token    string
}

func (c *client) do(ctx context.Context, method, path string, body any, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+apiVersionPrefix+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, unreachableError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, unreachableError(err)
	}
	return resp.StatusCode, raw, nil
}

// errorShape is the typed error envelope every non-2xx response uses.
type errorShape struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	CurrentVersion *int64 `json:"current_version,omitempty"`
}

// fail decodes a non-2xx response into a typed CLI error carrying the HTTP
// status, the server error code and the exit code derived from the status.
func fail(code int, raw []byte) error {
	var shape errorShape
	if err := json.Unmarshal(raw, &shape); err == nil && shape.Code != "" {
		message := fmt.Sprintf("%s (HTTP %d): %s", shape.Code, code, shape.Message)
		if code == 409 && shape.CurrentVersion != nil {
			message = fmt.Sprintf("%s (HTTP 409): %s (current_version=%d)", shape.Code, shape.Message, *shape.CurrentVersion)
		}
		return &cliError{exit: exitForStatus(code), status: code, code: shape.Code, apiMessage: shape.Message, currentVersion: shape.CurrentVersion, message: message}
	}
	return &cliError{exit: exitForStatus(code), status: code, code: "HTTP_" + strconv.Itoa(code), message: fmt.Sprintf("HTTP %d: %s", code, strings.TrimSpace(string(raw)))}
}

// newID generates a fresh idempotency key for side-effecting calls.
func newID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("bansoko-%d", time.Now().UnixNano())
	}
	return "bansoko-" + hex.EncodeToString(raw)
}
