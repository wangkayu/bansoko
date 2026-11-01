package rtruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// publishedStable is the v0.0.3 manifest as reltitle published it: bullet-list
// notes under the underscore spelling.
const publishedStable = `{
  "schema_version": "rt-channel-1",
  "line": "0.0",
  "protocol": "rt-serve-1",
  "version": "0.0.3",
  "sequence": 1,
  "published_at": "2026-11-01T08:59:41Z",
  "notes": {
    "zh_cn": ["第一条", "第二条"],
    "en": ["first", "second"]
  },
  "artifacts": {
    "linux/amd64": {"path": "0.0.3/reltitle-linux-amd64", "sha256": "3fdba23fd7b29bbcf3963ffbc4645b0863d7f68b278e022967dc939587706081", "size": 3862680},
    "linux/arm64": {"path": "0.0.3/reltitle-linux-arm64", "sha256": "caf7f6fa3a2eab50504b8b374300f432db6b63f4bff205c9eeae5f47a3888e00", "size": 3735704}
  },
  "release_url": "https://github.com/wangkayu/reltitle/releases/tag/v0.0.3"
}`

// alignedNotes is the shape reltitle publishes from v0.0.4 on.
const alignedNotes = `{
  "schema_version": "rt-channel-1",
  "line": "0.0",
  "protocol": "rt-serve-1",
  "version": "0.0.4",
  "sequence": 2,
  "published_at": "2026-11-02T00:00:00Z",
  "notes": {"zh-CN": "- 只有一条", "en": "- just one"},
  "artifacts": {
    "linux/amd64": {"path": "0.0.4/reltitle-linux-amd64", "sha256": "3fdba23fd7b29bbcf3963ffbc4645b0863d7f68b278e022967dc939587706081", "size": 3862680}
  }
}`

// TestDecodeChannelReadsAPreContractReleaseAsNoNotes proves a release published
// before the aligned notes shape stays installable: its identity is read and its
// notes are simply absent.
func TestDecodeChannelReadsAPreContractReleaseAsNoNotes(t *testing.T) {
	channel, err := DecodeChannel([]byte(publishedStable), "0.0")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if channel.Version != "0.0.3" || channel.Sequence != 1 {
		t.Fatalf("identity = %q seq %d", channel.Version, channel.Sequence)
	}
	if channel.Notes != (ChannelNotes{}) {
		t.Fatalf("a pre-contract release has no readable notes, got %+v", channel.Notes)
	}
	if _, err := channel.ArtifactFor("linux/amd64"); err != nil {
		t.Fatalf("amd64 artifact: %v", err)
	}
}

func TestDecodeChannelKeepsAlignedNotesText(t *testing.T) {
	channel, err := DecodeChannel([]byte(alignedNotes), "0.0")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if channel.Notes.ZhCN != "- 只有一条" || channel.Notes.EN != "- just one" {
		t.Fatalf("notes = %+v", channel.Notes)
	}
}

func TestDecodeChannelAcceptsATestBuildWithoutNotes(t *testing.T) {
	body := `{
      "schema_version": "rt-channel-1", "line": "0.0", "protocol": "rt-serve-1",
      "version": "0.0.4-test.9", "sequence": 7, "published_at": "2026-11-01T09:53:03Z",
      "artifacts": {"linux/amd64": {"path": "0.0.4-test.9/reltitle-linux-amd64",
        "sha256": "2994e6425e120e33f1a7ce7e2eda21e91a17d49be4784342ef2b2e279341318a", "size": 3862680}}
    }`
	channel, err := DecodeChannel([]byte(body), "0.0")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if channel.Notes != (ChannelNotes{}) {
		t.Fatalf("a test build carries no notes, got %+v", channel.Notes)
	}
}

func TestDecodeChannelRefuses(t *testing.T) {
	base := func() map[string]any {
		var document map[string]any
		if err := json.Unmarshal([]byte(publishedStable), &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"wrong schema", func(d map[string]any) { d["schema_version"] = "rt-channel-2" }, "schema"},
		{"wrong protocol", func(d map[string]any) { d["protocol"] = "rt-serve-2" }, "speaks"},
		{"wrong line", func(d map[string]any) { d["line"] = "0.1" }, "line"},
		{"unparseable version", func(d map[string]any) { d["version"] = "latest" }, "release version"},
		{"zero sequence", func(d map[string]any) { d["sequence"] = 0 }, "sequence"},
		{"no artifacts", func(d map[string]any) { d["artifacts"] = map[string]any{} }, "no artifacts"},
		{"short digest", func(d map[string]any) {
			artifacts := d["artifacts"].(map[string]any)
			artifacts["linux/amd64"].(map[string]any)["sha256"] = "deadbeef"
		}, "digest"},
		{"oversized artifact", func(d map[string]any) {
			artifacts := d["artifacts"].(map[string]any)
			artifacts["linux/amd64"].(map[string]any)["size"] = MaxBinaryBytes + 1
		}, "outside"},
		{"absolute path", func(d map[string]any) {
			artifacts := d["artifacts"].(map[string]any)
			artifacts["linux/amd64"].(map[string]any)["path"] = "/etc/passwd"
		}, "relative artifact path"},
		{"escaping path", func(d map[string]any) {
			artifacts := d["artifacts"].(map[string]any)
			artifacts["linux/amd64"].(map[string]any)["path"] = "0.0.3/../../x"
		}, "relative artifact path"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := base()
			testCase.mutate(document)
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeChannel(body, "0.0")
			if err == nil {
				t.Fatal("the channel document was accepted")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not explain %q", err, testCase.want)
			}
		})
	}
}

func TestChannelURLFollowsTheLane(t *testing.T) {
	cases := []struct {
		lane, line string
		want       string
		wantErr    bool
	}{
		{"", "0.0", "https://raw.githubusercontent.com/wangkayu/reltitle/update-channel/lines/0.0/channel.json", false},
		{"stable", "0.0", "https://raw.githubusercontent.com/wangkayu/reltitle/update-channel/lines/0.0/channel.json", false},
		{"test", "0.1", "https://raw.githubusercontent.com/wangkayu/reltitle/update-channel-test/lines/0.1/channel.json", false},
		{"beta", "0.0", "", true},
		{"test", "", "", true},
		{"test", "0", "", true},
		{"test", "0.x", "", true},
	}
	for _, testCase := range cases {
		got, err := ChannelURL(testCase.lane, testCase.line)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("ChannelURL(%q, %q) = %q, want an error", testCase.lane, testCase.line, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ChannelURL(%q, %q): %v", testCase.lane, testCase.line, err)
		}
		if got != testCase.want {
			t.Fatalf("ChannelURL(%q, %q) = %q, want %q", testCase.lane, testCase.line, got, testCase.want)
		}
	}
}

func TestLineOfKeepsTheMajorMinor(t *testing.T) {
	cases := map[string]string{
		"0.0.3":        "0.0",
		"v0.0.3":       "0.0",
		"0.0.4-test.9": "0.0",
		"1.2.3":        "1.2",
		"0.1.0-dev-x":  "0.1",
		"0.0":          "0.0",
		"garbage":      "",
	}
	for input, want := range cases {
		if got := LineOf(input); got != want {
			t.Errorf("LineOf(%q) = %q, want %q", input, got, want)
		}
	}
}

// channelServer publishes one manifest and its artifact from a test server, so
// the update path runs end to end without touching the network.
func channelServer(t *testing.T, version string, artifact []byte, mutate func(document map[string]any)) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(artifact)
	document := map[string]any{
		"schema_version": ChannelSchema,
		"line":           "0.0",
		"protocol":       Protocol,
		"version":        version,
		"sequence":       9,
		"published_at":   time.Now().UTC().Format(time.RFC3339),
		"notes":          map[string]any{"zh-CN": "- 新版本", "en": "- new build"},
		"artifacts": map[string]any{
			ArchSuffix(): map[string]any{
				"path":   version + "/reltitle",
				"sha256": hex.EncodeToString(digest[:]),
				"size":   len(artifact),
			},
		},
	}
	if mutate != nil {
		mutate(document)
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/channel.json"):
			w.Write(body)
		case strings.Contains(r.URL.Path, "/"+version+"/reltitle"):
			w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// withChannelRoot points the package's channel root at the test server for one
// test. The root is a constant in production so a deployment cannot be pointed
// at someone else's repository.
func withChannelRoot(t *testing.T, server *httptest.Server) {
	t.Helper()
	original := channelRootOverride
	channelRootOverride = server.URL + "/"
	t.Cleanup(func() { channelRootOverride = original })
}

func TestUpdateInstallsANewerBuild(t *testing.T) {
	artifact := []byte("#!/bin/sh\necho reltitle\n")
	server := channelServer(t, "0.0.4-test.9", artifact, nil)
	withChannelRoot(t, server)

	root := t.TempDir()
	result, err := Update(context.Background(), UpdateOptions{
		ConfigRoot:     root,
		Lane:           "test",
		Client:         server.Client(),
		CurrentVersion: "0.0.3",
		Now:            func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !result.Installed || result.Version != "0.0.4-test.9" {
		t.Fatalf("result = %+v", result)
	}
	installed, err := os.ReadFile(BinaryPath(root))
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(installed) != string(artifact) {
		t.Fatalf("installed bytes differ from the artifact")
	}
	info, err := os.Stat(BinaryPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed file is not executable: %v", info.Mode())
	}
	metadata, err := os.ReadFile(MetadataPath(root))
	if err != nil {
		t.Fatalf("read installed.json: %v", err)
	}
	var record Installed
	if err := json.Unmarshal(metadata, &record); err != nil {
		t.Fatal(err)
	}
	if record.Version != "0.0.4-test.9" || record.Source != SourceChannel {
		t.Fatalf("installed.json = %+v", record)
	}
	if leftover, _ := filepath.Glob(filepath.Join(filepath.Dir(BinaryPath(root)), ".reltitle-download-*")); len(leftover) != 0 {
		t.Fatalf("temporary downloads left behind: %v", leftover)
	}
}

func TestUpdateSkipsABuildThatIsNotNewer(t *testing.T) {
	for _, version := range []string{"0.0.3", "0.0.2"} {
		t.Run(version, func(t *testing.T) {
			server := channelServer(t, version, []byte("new"), nil)
			withChannelRoot(t, server)
			root := t.TempDir()
			result, err := Update(context.Background(), UpdateOptions{
				ConfigRoot: root, Lane: "stable", Client: server.Client(), CurrentVersion: "0.0.3",
			})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if result.Installed {
				t.Fatalf("installed %s over 0.0.3", version)
			}
			if result.Reason == "" {
				t.Fatal("a skipped update must say why")
			}
			if _, err := os.Stat(BinaryPath(root)); !os.IsNotExist(err) {
				t.Fatalf("the binary path was written anyway: %v", err)
			}
		})
	}
}

func TestUpdateKeepsTheLiveBinaryWhenTheDigestFails(t *testing.T) {
	server := channelServer(t, "0.0.4", []byte("the real bytes"), func(document map[string]any) {
		artifacts := document["artifacts"].(map[string]any)
		artifacts[ArchSuffix()].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	})
	withChannelRoot(t, server)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(BinaryPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BinaryPath(root), []byte("previous build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(context.Background(), UpdateOptions{
		ConfigRoot: root, Lane: "stable", Client: server.Client(), CurrentVersion: "0.0.3",
	}); err == nil {
		t.Fatal("a digest mismatch was accepted")
	}
	current, err := os.ReadFile(BinaryPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "previous build" {
		t.Fatalf("the live binary changed: %q", current)
	}
	if leftover, _ := filepath.Glob(filepath.Join(filepath.Dir(BinaryPath(root)), ".reltitle-download-*")); len(leftover) != 0 {
		t.Fatalf("temporary downloads left behind: %v", leftover)
	}
}

func TestUpdateRefusesAPlatformWithoutAnArtifact(t *testing.T) {
	server := channelServer(t, "0.0.4", []byte("x"), func(document map[string]any) {
		document["artifacts"] = map[string]any{
			"plan9/mips": map[string]any{
				"path":   "0.0.4/reltitle",
				"sha256": fmt.Sprintf("%064d", 0),
				"size":   1,
			},
		}
	})
	withChannelRoot(t, server)
	_, err := Update(context.Background(), UpdateOptions{
		ConfigRoot: t.TempDir(), Lane: "stable", Client: server.Client(), CurrentVersion: "0.0.3",
	})
	if err == nil || !strings.Contains(err.Error(), ArchSuffix()) {
		t.Fatalf("error = %v, want a note about the missing platform", err)
	}
}

// TestProbeLabelsACorroboratedChannelInstall proves a file the updater wrote is
// reported as a channel install after a restart, but only while the record and
// the file agree.
func TestProbeLabelsACorroboratedChannelInstall(t *testing.T) {
	root, path := installFake(t)
	digest, err := fileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	writeRecord := func(record Installed) {
		t.Helper()
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(MetadataPath(root), payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	probe := func() Info {
		t.Helper()
		client, info := Probe(testContext(t), root, fakeVersion)
		if client == nil {
			t.Fatalf("the probe refused the fake: %s", info.Note)
		}
		_ = client.Close()
		return info
	}

	writeRecord(Installed{Version: fakeVersion, SHA256: digest, Source: SourceChannel})
	if info := probe(); info.Source != SourceChannel {
		t.Fatalf("source = %q, want %q for a corroborated channel install", info.Source, SourceChannel)
	}

	writeRecord(Installed{Version: fakeVersion, SHA256: strings.Repeat("0", 64), Source: SourceChannel})
	if info := probe(); info.Source != SourceManual {
		t.Fatalf("source = %q, want %q when the recorded digest differs", info.Source, SourceManual)
	}

	writeRecord(Installed{Version: fakeVersion, SHA256: digest, Source: SourceManual})
	if info := probe(); info.Source != SourceManual {
		t.Fatalf("source = %q, want %q for a manual record", info.Source, SourceManual)
	}
}
