package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const testConfigDir = "../../configs"

func runCLI(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func decodeRawTitle(t *testing.T, stdout string) string {
	t.Helper()
	var res struct {
		RawTitle    string `json:"raw_title"`
		ContentKind string `json:"content_kind"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if res.ContentKind == "" {
		t.Fatalf("result has no content_kind:\n%s", stdout)
	}
	return res.RawTitle
}

func TestRunTitleSources(t *testing.T) {
	const title = "[Group] Some Show - 03 [1080p]"
	tests := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"-title flag", "", []string{"-config", testConfigDir, "-title", title}},
		{"positional argument", "", []string{"-config", testConfigDir, title}},
		{"standard input", title + "\nignored second line\n", []string{"-config", testConfigDir}},
		{"leading -- is skipped", "", []string{"--", "-config", testConfigDir, "-title", title}},
		{"flag wins over argument and stdin", "[Other] Title - 01", []string{"-config", testConfigDir, "-title", title, "[Another] Title - 02"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.stdin, tc.args...)
			if code != 0 {
				t.Fatalf("exit code %d, stderr: %s", code, stderr)
			}
			if got := decodeRawTitle(t, stdout); got != title {
				t.Fatalf("raw_title=%q, want %q", got, title)
			}
		})
	}
}

func TestRunDefaultEmbeddedConfig(t *testing.T) {
	// Without -config the CLI must use the configs embedded in the binary.
	const title = "[Group] Some Show - 03 [1080p]"
	code, stdout, stderr := runCLI(t, "", "-title", title)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	if got := decodeRawTitle(t, stdout); got != title {
		t.Fatalf("raw_title=%q, want %q", got, title)
	}
}

func TestRunNoTitle(t *testing.T) {
	for name, stdin := range map[string]string{"empty stdin": "", "blank line": "   \n"} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, stdin, "-config", testConfigDir)
			if code != 2 {
				t.Fatalf("exit code %d, want 2", code)
			}
			if stdout != "" {
				t.Fatalf("unexpected stdout: %q", stdout)
			}
			if !strings.Contains(stderr, "no title provided") {
				t.Fatalf("stderr %q does not explain the problem", stderr)
			}
		})
	}
}

func TestRunBadConfigDir(t *testing.T) {
	// The config is loaded before the title is read, so a bad config wins over a missing title.
	code, stdout, stderr := runCLI(t, "", "-config", t.TempDir())
	if code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "load config:") || !strings.Contains(stderr, "lexicon.yaml") {
		t.Fatalf("stderr does not name the failing file: %q", stderr)
	}
}

func TestRunUnknownFlag(t *testing.T) {
	code, _, stderr := runCLI(t, "", "-nope")
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(stderr, "-nope") {
		t.Fatalf("stderr does not mention the flag: %q", stderr)
	}
}
