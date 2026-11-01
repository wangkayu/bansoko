package rtruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeVersion = "9.9.9"
	fakeLine    = "9.9"
	fakeDigest  = "lexicon:sha256:deadbeef"

	// operationTimeout keeps a wedged client from hanging the whole package;
	// every request the tests expect to succeed answers in milliseconds.
	operationTimeout = 30 * time.Second
)

// binariesDir holds the binaries the tests run. They are built once per test
// process: the cost belongs to a fresh checkout, not to every test case.
var binariesDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rtruntime-binaries-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create binaries dir: %v\n", err)
		os.Exit(1)
	}
	binariesDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// fakeBinary builds testdata/fakert once and returns its path.
func fakeBinary(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		fakePath = filepath.Join(binariesDir, "reltitle-fake")
		fakeErr = buildGoBinary(fakePath, "./testdata/fakert")
	})
	if fakeErr != nil {
		t.Fatalf("build testdata/fakert: %v", fakeErr)
	}
	return fakePath
}

// buildGoBinary compiles one package through the go command, the same way a
// person installs the parser: the tests must run a real file, not a library
// call standing in for one. GOPROXY is pinned off so a missing module fails
// the build instead of silently reaching the network.
func buildGoBinary(out, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Env = buildEnv("GOPROXY=off", "GOFLAGS=-mod=readonly")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// buildEnv replaces the named variables instead of appending duplicates: the
// first entry wins for a child on the platforms this runs on, so appending
// would leave a pre-set GOFLAGS in force.
func buildEnv(overrides ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		replaced := false
		for _, override := range overrides {
			if strings.HasPrefix(override, name+"=") {
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, entry)
		}
	}
	return append(env, overrides...)
}

// installFake places a private copy of the fake at the one path the probe
// reads, so a case can change its mode, size or identity without disturbing
// the shared build.
func installFake(t *testing.T) (configRoot, path string) {
	t.Helper()
	source := fakeBinary(t)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read the fake binary: %v", err)
	}
	configRoot = t.TempDir()
	path = BinaryPath(configRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the rt directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("install the fake binary: %v", err)
	}
	// WriteFile's mode passes through the umask, so state the bits the probe
	// actually checks.
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("make the fake binary executable: %v", err)
	}
	return configRoot, path
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	t.Cleanup(cancel)
	return ctx
}

// startFake runs the fake directly, without the probe: the client tests are
// about the protocol, not about adoption.
func startFake(t *testing.T) *Client {
	t.Helper()
	client, err := Start(testContext(t), fakeBinary(t))
	if err != nil {
		t.Fatalf("start the fake binary: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestProbeAdoptsAWorkingBinary(t *testing.T) {
	configRoot, path := installFake(t)
	client, info := Probe(testContext(t), configRoot, fakeVersion)
	if client == nil {
		t.Fatalf("the probe refused a working binary: %s", info.Note)
	}
	t.Cleanup(func() { _ = client.Close() })

	if info.Source != SourceManual {
		t.Fatalf("source = %q, want %q", info.Source, SourceManual)
	}
	if info.Path != path {
		t.Fatalf("path = %q, want %q", info.Path, path)
	}
	if info.Version != fakeVersion {
		t.Fatalf("version = %q, want the hello version %q", info.Version, fakeVersion)
	}
	if info.Line != fakeLine {
		t.Fatalf("line = %q, want the hello line %q", info.Line, fakeLine)
	}
	if info.ConfigDigest != fakeDigest {
		t.Fatalf("digest = %q, want the hello digest %q", info.ConfigDigest, fakeDigest)
	}
	if info.Note != "" {
		t.Fatalf("an equal-version build deserves no note, got %q", info.Note)
	}
	if reported := client.Info(); reported != info {
		t.Fatalf("the adopted client reports %+v, but the probe reported %+v", reported, info)
	}
}

func TestProbeRefusesAnUnusableBinary(t *testing.T) {
	cases := []struct {
		name string
		// prepare returns the configuration root and the path the probe will
		// read; it sets whatever knobs the child must inherit.
		prepare  func(t *testing.T) (string, string)
		wantNote string
	}{
		{
			name: "absent",
			prepare: func(t *testing.T) (string, string) {
				root := t.TempDir()
				return root, BinaryPath(root)
			},
			// An absent file is the ordinary first start, not a complaint.
			wantNote: "",
		},
		{
			name: "symbolic link",
			prepare: func(t *testing.T) (string, string) {
				root, path := installFake(t)
				target := filepath.Join(t.TempDir(), "reltitle-target")
				if err := os.Rename(path, target); err != nil {
					t.Fatalf("move the fake binary: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("link the fake binary: %v", err)
				}
				return root, path
			},
			wantNote: "symbolic link",
		},
		{
			name: "not executable",
			prepare: func(t *testing.T) (string, string) {
				root, path := installFake(t)
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("remove the executable bit: %v", err)
				}
				return root, path
			},
			wantNote: "not executable",
		},
		{
			name: "larger than the limit",
			prepare: func(t *testing.T) (string, string) {
				root := t.TempDir()
				path := BinaryPath(root)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("create the rt directory: %v", err)
				}
				file, err := os.Create(path)
				if err != nil {
					t.Fatalf("create the oversized file: %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("close the oversized file: %v", err)
				}
				// Truncate rather than write: the size is what the probe
				// reads, and the file must stay cheap to create.
				if err := os.Truncate(path, MaxBinaryBytes+1); err != nil {
					t.Fatalf("grow the oversized file: %v", err)
				}
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatalf("make the oversized file executable: %v", err)
				}
				return root, path
			},
			wantNote: "larger than",
		},
		{
			name: "wrong protocol",
			prepare: func(t *testing.T) (string, string) {
				root, path := installFake(t)
				t.Setenv("FAKERT_PROTOCOL", "rt-serve-2")
				return root, path
			},
			wantNote: "rt-serve-2",
		},
		{
			name: "exits on start",
			prepare: func(t *testing.T) (string, string) {
				root, path := installFake(t)
				t.Setenv("FAKERT_EXIT_ON_START", "1")
				return root, path
			},
			wantNote: "transport failed",
		},
		{
			name: "crashes during the parse self-check",
			prepare: func(t *testing.T) (string, string) {
				root, path := installFake(t)
				t.Setenv("FAKERT_CRASH_ON_PARSE", "1")
				return root, path
			},
			wantNote: "parse self-check",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			configRoot, path := testCase.prepare(t)
			client, info := Probe(testContext(t), configRoot, fakeVersion)
			if client != nil {
				_ = client.Close()
				t.Fatalf("the probe adopted a binary it must refuse")
			}
			if info.Source != SourceBuiltin {
				t.Fatalf("source = %q, want the builtin fallback %q", info.Source, SourceBuiltin)
			}
			if info.Path != path {
				t.Fatalf("path = %q, want %q", info.Path, path)
			}
			if testCase.wantNote == "" {
				if info.Note != "" {
					t.Fatalf("an absent binary is not a note, got %q", info.Note)
				}
				return
			}
			if !strings.Contains(info.Note, testCase.wantNote) {
				t.Fatalf("note %q does not explain the refusal (want %q)", info.Note, testCase.wantNote)
			}
		})
	}
}

func TestClientParsesATitle(t *testing.T) {
	client := startFake(t)
	const title = "[Test] Smoke Title - 01 [1080p]"
	result, err := client.Parse(testContext(t), title)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.RawTitle != title {
		t.Fatalf("raw_title = %q, want %q", result.RawTitle, title)
	}
	found := false
	for _, attribute := range result.Attributes {
		if attribute.Type == "resolution" && attribute.Normalized == "1080p" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the parsed model lost the resolution attribute: %+v", result.Attributes)
	}
}

func TestClientReadsTheVocabulary(t *testing.T) {
	client := startFake(t)
	dimensions, err := client.Vocabulary(testContext(t))
	if err != nil {
		t.Fatalf("vocabulary: %v", err)
	}
	if len(dimensions) != 2 {
		t.Fatalf("dimensions = %+v, want the two the fake reports", dimensions)
	}
	if dimensions[0].Dimension != "resolution" || len(dimensions[0].Values) != 2 {
		t.Fatalf("first dimension = %+v, want resolution with two values", dimensions[0])
	}
}

func TestClientParseRestartsOnceAfterTheProcessDies(t *testing.T) {
	client := startFake(t)
	ctx := testContext(t)
	if _, err := client.Parse(ctx, "[Test] Before the crash - 01"); err != nil {
		t.Fatalf("parse before the crash: %v", err)
	}

	// Kill from outside the protocol: the client only learns about it from
	// the closed pipe, which is what an out-of-memory or SIGKILL looks like.
	client.mu.Lock()
	process := client.cmd.Process
	client.mu.Unlock()
	if process == nil {
		t.Fatal("the client lost its process before the test could kill it")
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("kill the parser process: %v", err)
	}

	const title = "[Test] After the crash - 02 [720p]"
	result, err := client.Parse(ctx, title)
	if err != nil {
		t.Fatalf("parse after the process died: %v", err)
	}
	if result.RawTitle != title {
		t.Fatalf("raw_title = %q, want the restarted process's answer %q", result.RawTitle, title)
	}
}

func TestClientParseHonoursTheDeadline(t *testing.T) {
	// The fake answers this much later than the deadline below. Start gets a
	// generous context so only the parse under test can time out.
	t.Setenv("FAKERT_SLEEP_MS", "800")
	client, err := Start(testContext(t), fakeBinary(t))
	if err != nil {
		t.Fatalf("start the fake binary: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.Parse(ctx, "[Test] Too slow - 01"); err == nil {
		t.Fatal("a process that outlived the deadline was answered")
	}
	// The client restarts once after the kill; that retry must fail on the
	// expired context rather than wait for the fake again.
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the deadline did not bound the wait: took %s", elapsed)
	}
}

func TestVersionOfReportsTheBinaryVersion(t *testing.T) {
	path := fakeBinary(t)
	t.Setenv("FAKERT_VERSION", "1.2.3")
	version, err := VersionOf(testContext(t), path)
	if err != nil {
		t.Fatalf("version check: %v", err)
	}
	if version != "1.2.3" {
		t.Fatalf("version = %q, want %q", version, "1.2.3")
	}
}

func TestVersionOfRejectsAFileThatCannotRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "reltitle")
	if _, err := VersionOf(testContext(t), missing); err == nil {
		t.Fatal("a missing binary reported a version")
	}
}

func TestCompareVersionsOrdersReleaseVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.4", -1},
		{"1.2.3", "1.2.3", 0},
		{"1.2.10", "1.2.9", 1},
		{"v1.2.3", "1.2.3", 0},
		// A version that does not parse is unknown, and an unknown build
		// never claims to be newer than a real one.
		{"1.2", "1.2.3", -1},
		{"1.2.3", "1.2", 1},
		{"0.0.0-dev", "0.0.0", -1},
		{"garbage", "1.0.0", -1},
		{"1.0.0", "garbage", 1},
		{"b", "a", 1},
	}
	for _, testCase := range cases {
		if got := compareVersions(testCase.a, testCase.b); got != testCase.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", testCase.a, testCase.b, got, testCase.want)
		}
	}
}

func TestVersionNoteFlagsOnlyAnOlderBuild(t *testing.T) {
	cases := []struct {
		name               string
		installed, builtin string
		wantNote           bool
	}{
		{"older", "1.2.2", "1.2.3", true},
		{"equal", "1.2.3", "1.2.3", false},
		{"newer", "1.3.0", "1.2.3", false},
		{"unparseable installed version", "0.0.0-dev", "1.0.0", true},
		{"unparseable builtin version", "1.0.0", "garbage", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			note := versionNote(testCase.installed, testCase.builtin)
			if testCase.wantNote && note == "" {
				t.Fatalf("versionNote(%q, %q) said nothing; an older build must be flagged", testCase.installed, testCase.builtin)
			}
			if !testCase.wantNote && note != "" {
				t.Fatalf("versionNote(%q, %q) = %q, want no note", testCase.installed, testCase.builtin, note)
			}
			if !testCase.wantNote {
				return
			}
			if !strings.Contains(note, testCase.installed) || !strings.Contains(note, testCase.builtin) {
				t.Fatalf("note %q must name both versions", note)
			}
		})
	}
}
