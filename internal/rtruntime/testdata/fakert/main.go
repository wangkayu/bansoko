// Command fakert stands in for the reltitle binary in the rtruntime tests. It
// speaks just enough of rt-serve-1 for the adoption chain and the client, and
// carries the failure knobs the refusal tests pull, so no test needs the real
// parser to prove a broken installation is refused.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultVersion  = "9.9.9"
	defaultProtocol = "rt-serve-1"
	// fakeDigest is deliberately not a real digest: a test that sees this
	// value knows the answer came from the fake and not from a dictionary.
	fakeDigest = "lexicon:sha256:deadbeef"
)

func main() {
	version := valueOr("FAKERT_VERSION", defaultVersion)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version":
			fmt.Println(version)
			return
		case "serve":
			serve(version)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "usage: fakert -version | fakert serve")
	os.Exit(2)
}

func valueOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type serveRequest struct {
	ID    int64  `json:"id"`
	Op    string `json:"op"`
	Title string `json:"title"`
}

type serveError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type serveResponse struct {
	ID     int64       `json:"id"`
	OK     bool        `json:"ok"`
	Result any         `json:"result,omitempty"`
	Error  *serveError `json:"error,omitempty"`
}

// serve answers one line per request, like the real server, and exits on
// stdin close. The knobs are checked here rather than in the protocol handler
// so the process can fail exactly the way a broken installation does.
func serve(version string) {
	if os.Getenv("FAKERT_EXIT_ON_START") == "1" {
		fmt.Fprintln(os.Stderr, "fakert: refusing to start")
		os.Exit(1)
	}
	protocol := valueOr("FAKERT_PROTOCOL", defaultProtocol)
	if path := os.Getenv("FAKERT_HELLO_COUNTER_FILE"); path != "" && bumpInstanceCount(path) > 1 {
		// A later process of the same binary speaks another protocol, the way
		// a replaced file does after a client restart.
		protocol = valueOr("FAKERT_LATER_PROTOCOL", "rt-serve-0")
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4096), 8<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if delay := sleepBeforeAnswer(); delay > 0 {
			time.Sleep(delay)
		}
		var request serveRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(serveResponse{Error: &serveError{Code: "bad_request", Message: "the request is not a JSON object"}})
			continue
		}
		if request.Op == "parse" && os.Getenv("FAKERT_OVERSIZED_ON_PARSE") == "1" {
			// An answer with no newline and no end: the read must be cut by
			// the client's cap, not by a deadline.
			_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 4<<20))
			select {}
		}
		_ = encoder.Encode(answer(request, version, protocol))
	}
}

// bumpInstanceCount counts the processes this binary has started, so a test
// can make the second process behave differently from the first.
func bumpInstanceCount(path string) int {
	count := 1
	if data, err := os.ReadFile(path); err == nil {
		if value, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			count = value + 1
		}
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(count)), 0o644)
	return count
}

func sleepBeforeAnswer() time.Duration {
	millis, err := strconv.Atoi(strings.TrimSpace(os.Getenv("FAKERT_SLEEP_MS")))
	if err != nil || millis <= 0 {
		return 0
	}
	return time.Duration(millis) * time.Millisecond
}

// answer keeps the parse result fixed instead of deriving it from the title:
// the tests assert exact values, and a canned answer cannot drift with the
// title they happen to send.
func answer(request serveRequest, version, protocol string) serveResponse {
	switch request.Op {
	case "hello":
		return serveResponse{ID: request.ID, OK: true, Result: map[string]string{
			"protocol":      protocol,
			"version":       version,
			"line":          "9.9",
			"config_digest": fakeDigest,
		}}
	case "vocabulary":
		return serveResponse{ID: request.ID, OK: true, Result: map[string]any{
			"dimensions": []map[string]any{
				{"dimension": "resolution", "values": []string{"720p", "1080p"}},
				{"dimension": "source_type", "values": []string{"WEB-DL", "BluRay"}},
			},
		}}
	case "parse":
		if os.Getenv("FAKERT_CRASH_ON_PARSE") == "1" {
			fmt.Fprintln(os.Stderr, "fakert: crashing mid-request")
			os.Exit(1)
		}
		return serveResponse{ID: request.ID, OK: true, Result: map[string]any{
			"raw_title":    request.Title,
			"content_kind": "TV_EP",
			"attributes": []map[string]any{
				{"type": "resolution", "normalized": "1080p"},
				{"type": "source_type", "normalized": "WEB-DL"},
			},
		}}
	default:
		return serveResponse{ID: request.ID, Error: &serveError{Code: "unknown_op", Message: "unknown operation"}}
	}
}
