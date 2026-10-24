package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Exit codes. The table is fixed: scripts and agents branch on it, the agent
// guide documents it, and TestExitCodeTable locks it.
//
//	0 success
//	1 any other failure (including a failed pipeline or a wait timeout)
//	2 usage error (bad flag, missing argument, unknown command)
//	3 authentication or permission (no credential, 401, 403)
//	4 not found (404, 410)
//	5 conflict or precondition not met (409, 412, 422, 428)
//	6 server unreachable or server fault (connection error, 5xx)
const (
	exitOK          = 0
	exitOther       = 1
	exitUsage       = 2
	exitAuth        = 3
	exitNotFound    = 4
	exitConflict    = 5
	exitUnavailable = 6
)

// exitForStatus maps an HTTP status onto the fixed exit-code table.
func exitForStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return exitAuth
	case status == http.StatusNotFound || status == http.StatusGone:
		return exitNotFound
	case status == http.StatusConflict || status == http.StatusPreconditionFailed ||
		status == http.StatusUnprocessableEntity || status == http.StatusPreconditionRequired:
		return exitConflict
	case status >= 500:
		return exitUnavailable
	default:
		return exitOther
	}
}

// cliError is every failure bansoko reports. It carries the exit code and,
// for API failures, the HTTP status and server error code so `--json` can
// print a machine-readable error document.
type cliError struct {
	exit           int
	code           string
	status         int
	message        string
	apiMessage     string
	currentVersion *int64
	cause          error
}

func (e *cliError) Error() string { return e.message }
func (e *cliError) Unwrap() error { return e.cause }

// errHelp marks a requested -h/--help; the flag set already printed usage.
var errHelp = flag.ErrHelp

func newUsageError(message string) error {
	return &cliError{exit: exitUsage, code: "USAGE", message: message}
}

func usageErrorf(format string, args ...any) error {
	wrapped := fmt.Errorf(format, args...)
	return &cliError{exit: exitUsage, code: "USAGE", message: wrapped.Error(), cause: errors.Unwrap(wrapped)}
}

// parseError classifies a flag-parsing failure.
func parseError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return errHelp
	}
	return &cliError{exit: exitUsage, code: "USAGE", message: err.Error(), cause: err}
}

func unreachableError(err error) error {
	return &cliError{exit: exitUnavailable, code: "UNREACHABLE", message: "server unreachable: " + err.Error(), cause: err}
}

// exitCodeOf returns the exit code for an error; anything untyped is 1.
func exitCodeOf(err error) int {
	var typed *cliError
	if errors.As(err, &typed) {
		return typed.exit
	}
	return exitOther
}

// errorDocument is the single JSON document printed on stdout in --json mode
// when a command fails.
func errorDocument(err error) map[string]any {
	detail := map[string]any{"message": err.Error(), "exit_code": exitCodeOf(err), "code": "ERROR"}
	var typed *cliError
	if errors.As(err, &typed) {
		detail["code"] = typed.code
		if typed.apiMessage != "" {
			detail["message"] = typed.apiMessage
		}
		if typed.status != 0 {
			detail["http_status"] = typed.status
		}
		if typed.currentVersion != nil {
			detail["current_version"] = *typed.currentVersion
		}
	}
	return map[string]any{"error": detail}
}

// extractGlobalJSON removes every --json / -json / --json=<bool> argument so
// the flag works before or after the command name, and reports whether JSON
// mode was requested.
func extractGlobalJSON(args []string) ([]string, bool) {
	kept := make([]string, 0, len(args))
	wantJSON := false
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "--json", "-json", "--json=true", "-json=true":
			wantJSON = true
		case "--json=false", "-json=false":
		default:
			kept = append(kept, arg)
		}
	}
	return kept, wantJSON
}

// jsonMode is set once per invocation from the global --json flag.
var (
	jsonMode    bool
	jsonEmitted bool
)

// human is where human-readable text goes: stdout normally, stderr in --json
// mode so stdout stays a single JSON document.
func human() *os.File {
	if jsonMode {
		return os.Stderr
	}
	return os.Stdout
}

// emitJSON writes the one JSON document of a --json invocation.
func emitJSON(value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(`{"error":{"code":"ERROR","message":"result could not be encoded","exit_code":1}}`)
	}
	fmt.Fprintln(os.Stdout, string(raw))
	jsonEmitted = true
}

// printRaw prints a server response body as the JSON document. An empty body
// (204) becomes {"ok":true}.
func printRaw(raw []byte) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		emitJSON(map[string]any{"ok": true})
		return
	}
	fmt.Fprintln(os.Stdout, trimmed)
	jsonEmitted = true
}
