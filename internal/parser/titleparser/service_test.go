package titleparser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/rtruntime"
)

// fakeParser is the process-wide reltitle adapter with a settable build, so a
// test can observe the version change an adoption causes without running a
// real binary.
type fakeParser struct {
	version  string
	digest   string
	failure  string
	adoptErr error
	// adoptedVersion replaces version when ProbeAndAdopt succeeds.
	adoptedVersion string
}

func (f *fakeParser) ActiveVersion() string   { return f.version }
func (f *fakeParser) ActiveDigest() string    { return f.digest }
func (f *fakeParser) ExternalFailure() string { return f.failure }

func (f *fakeParser) ProbeAndAdopt(ctx context.Context, configRoot, builtinVersion string) error {
	if f.adoptErr != nil {
		return f.adoptErr
	}
	f.version = "reltitle@" + f.adoptedVersion
	f.digest = "lexicon:sha256:adopted"
	return nil
}

func newFakeParser() *fakeParser {
	return &fakeParser{version: reltitletags.ImplementationVersion, digest: "lexicon:sha256:builtin"}
}

// channelVersionAbove raises the bundled reltitle's patch number by one, so
// fixtures that need "a build newer than the built-in parser" keep that meaning
// when the dependency moves.
func channelVersionAbove(t *testing.T) string {
	t.Helper()
	version := strings.TrimPrefix(strings.TrimPrefix(reltitletags.ImplementationVersion, "reltitle@"), "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		t.Fatalf("%q is not X.Y.Z", version)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("patch number of %q: %v", version, err)
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1)
}

// channelFixture serves one manifest and its artifact, and returns the injected
// fetch/update functions that read them. It mirrors rtruntime's own
// channelServer without touching the production channel root.
func channelFixture(t *testing.T, version string, artifact []byte) (*httptest.Server, *rtruntime.Channel, FetchFunc, UpdateFunc) {
	t.Helper()
	digest := sha256.Sum256(artifact)
	document := map[string]any{
		"schema_version": rtruntime.ChannelSchema,
		"line":           rtruntime.LineOf(version),
		"protocol":       rtruntime.Protocol,
		"version":        version,
		"sequence":       9,
		"published_at":   time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"notes":          map[string]any{"zh-CN": "- 新版本", "en": "- new build"},
		"artifacts": map[string]any{
			"linux/amd64": map[string]any{
				"path":   version + "/reltitle",
				"sha256": hex.EncodeToString(digest[:]),
				"size":   len(artifact),
			},
		},
		"release_url": "https://example.test/reltitle/" + version,
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
	channel, err := rtruntime.DecodeChannel(body, rtruntime.LineOf(version))
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(ctx context.Context, client *http.Client, lane, line string) (*rtruntime.Channel, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/channel.json", nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, errors.New("channel answered " + response.Status)
		}
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		return rtruntime.DecodeChannel(payload, line)
	}
	update := func(ctx context.Context, options rtruntime.UpdateOptions) (rtruntime.UpdateResult, error) {
		result := rtruntime.UpdateResult{Channel: *channel, Previous: options.CurrentVersion, Version: options.CurrentVersion}
		if !rtruntime.Newer(channel.Version, options.CurrentVersion) {
			result.Reason = "the channel offers " + channel.Version + ", which is not newer than " + options.CurrentVersion
			return result, nil
		}
		response, err := options.Client.Get(server.URL + "/" + version + "/reltitle")
		if err != nil {
			return result, err
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			return result, err
		}
		target := rtruntime.BinaryPath(options.ConfigRoot)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return result, err
		}
		if err := os.WriteFile(target, payload, 0o755); err != nil {
			return result, err
		}
		result.Installed = true
		result.Version = channel.Version
		result.Path = target
		return result, nil
	}
	return server, channel, fetch, update
}

func newService(t *testing.T, parser Parser, fetch FetchFunc, update UpdateFunc, source string) *Service {
	t.Helper()
	t.Setenv(EnvLane, "")
	return New(Options{
		ConfigRoot: t.TempDir(),
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		Note:       "startup note",
		Source:     source,
		Parser:     parser,
		Fetch:      fetch,
		Update:     update,
		Now:        func() time.Time { return time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC) },
	})
}

func TestStatusOfTheBuiltinParser(t *testing.T) {
	parser := newFakeParser()
	service := newService(t, parser, nil, nil, SourceBuiltin)

	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Source != SourceBuiltin || status.Version != reltitletags.ImplementationVersion {
		t.Fatalf("identity = %+v", status)
	}
	if status.Line != "0.1" || status.ConfigDigest != "lexicon:sha256:builtin" {
		t.Fatalf("line/digest = %+v", status)
	}
	if status.BinaryPath != filepath.Join(service.configRoot, "rt", "reltitle") {
		t.Fatalf("binary path = %q", status.BinaryPath)
	}
	if status.Note != "startup note" || status.Lane != LaneStable {
		t.Fatalf("note/lane = %+v", status)
	}
	if status.Channel != nil {
		t.Fatalf("no check has run, channel = %+v", status.Channel)
	}
}

func TestCheckReportsTheChannelAndNewer(t *testing.T) {
	parser := newFakeParser()
	server, channel, fetch, update := channelFixture(t, channelVersionAbove(t), []byte("#!/bin/sh\necho reltitle\n"))
	_ = server
	service := newService(t, parser, fetch, update, SourceBuiltin)

	status, err := service.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if status.Channel == nil {
		t.Fatal("check did not report a channel")
	}
	if status.Channel.Version != channel.Version || status.Channel.ReleaseURL != channel.ReleaseURL {
		t.Fatalf("channel = %+v", status.Channel)
	}
	if status.Channel.Notes.ZhCN != "- 新版本" || status.Channel.Notes.EN != "- new build" {
		t.Fatalf("notes = %+v", status.Channel.Notes)
	}
	if !status.Channel.Newer {
		t.Fatal("a newer channel must be reported as newer")
	}
	if status.Channel.Error != "" {
		t.Fatalf("a successful check carries an error: %q", status.Channel.Error)
	}
}

// A binary installed from a retired line still reads the line compiled into
// this build, so the channel can advance it across the line move instead of
// asking for a manifest that no longer exists.
func TestChannelLineFollowsTheBuiltinParser(t *testing.T) {
	parser := newFakeParser()
	parser.version = "reltitle@0.0.4-test.10"
	above := channelVersionAbove(t)
	_, channel, fetch, update := channelFixture(t, above, []byte("#!/bin/sh\necho reltitle\n"))
	service := newService(t, parser, fetch, update, SourceChannel)

	status, err := service.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if status.Channel == nil || status.Channel.Version != channel.Version || !status.Channel.Newer {
		t.Fatalf("channel = %+v, want the builtin line's newer build", status.Channel)
	}

	var readLine string
	baseUpdate := service.update
	service.update = func(ctx context.Context, options rtruntime.UpdateOptions) (rtruntime.UpdateResult, error) {
		readLine = options.ChannelLine
		return baseUpdate(ctx, options)
	}
	if _, err := service.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if readLine != "0.1" {
		t.Fatalf("update read line %q, want the builtin line 0.1", readLine)
	}
}

func TestCheckFailureKeepsTheLastSuccessfulAnswer(t *testing.T) {
	parser := newFakeParser()
	_, _, fetch, update := channelFixture(t, "0.1.0", []byte("artifact"))
	service := newService(t, parser, fetch, update, SourceBuiltin)
	if _, err := service.Check(context.Background()); err != nil {
		t.Fatalf("first check: %v", err)
	}

	failing := func(ctx context.Context, client *http.Client, lane, line string) (*rtruntime.Channel, error) {
		return nil, errors.New("channel is down")
	}
	service.fetch = failing
	status, err := service.Check(context.Background())
	if !errors.Is(err, ErrChannel) {
		t.Fatalf("check error = %v, want ErrChannel", err)
	}
	if status.Channel == nil || status.Channel.Version != "0.1.0" || status.Channel.ReleaseURL == "" {
		t.Fatalf("the last successful answer was discarded: %+v", status.Channel)
	}
	if status.Channel.Error != "channel is down" {
		t.Fatalf("error = %q", status.Channel.Error)
	}

	// A plain read keeps the same state without another check.
	read, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if read.Channel == nil || read.Channel.Version != "0.1.0" || read.Channel.Error != "channel is down" {
		t.Fatalf("status = %+v", read.Channel)
	}
}

func TestFirstCheckFailureStillReportsAChannel(t *testing.T) {
	parser := newFakeParser()
	failing := func(ctx context.Context, client *http.Client, lane, line string) (*rtruntime.Channel, error) {
		return nil, errors.New("no manifest")
	}
	service := newService(t, parser, failing, nil, SourceBuiltin)

	status, err := service.Check(context.Background())
	if !errors.Is(err, ErrChannel) {
		t.Fatalf("check error = %v, want ErrChannel", err)
	}
	if status.Channel == nil {
		t.Fatal("the first failure must still report a channel object")
	}
	if status.Channel.Version != "" || status.Channel.Error != "no manifest" {
		t.Fatalf("channel = %+v", status.Channel)
	}
	if status.Channel.Newer {
		t.Fatal("an empty channel version cannot be newer")
	}
	if status.Channel.CheckedAt.IsZero() {
		t.Fatal("the failed check must be stamped")
	}
}

func TestInstallAdoptsANewerBuild(t *testing.T) {
	above := channelVersionAbove(t)
	parser := newFakeParser()
	parser.adoptedVersion = above
	_, _, fetch, update := channelFixture(t, above, []byte("#!/bin/sh\necho reltitle\n"))
	service := newService(t, parser, fetch, update, SourceBuiltin)

	status, err := service.Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if status.Source != SourceChannel {
		t.Fatalf("source = %q, want channel", status.Source)
	}
	if status.Version != "reltitle@"+above || status.Line != "0.1" {
		t.Fatalf("version/line = %+v", status)
	}
	if status.Note != "" {
		t.Fatalf("a successful adoption kept the startup note: %q", status.Note)
	}
	if !strings.Contains(status.Message, "installed reltitle "+above) {
		t.Fatalf("message = %q", status.Message)
	}
	if status.Channel == nil || status.Channel.Version != above || status.Channel.Newer {
		t.Fatalf("channel after install = %+v", status.Channel)
	}
	if _, err := os.Stat(rtruntime.BinaryPath(service.configRoot)); err != nil {
		t.Fatalf("the installed binary is missing: %v", err)
	}
}

func TestInstallWithoutANewerBuildStatesTheReason(t *testing.T) {
	parser := newFakeParser()
	_, _, fetch, update := channelFixture(t, "0.1.0", []byte("artifact"))
	service := newService(t, parser, fetch, update, SourceBuiltin)

	status, err := service.Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if status.Source != SourceBuiltin {
		t.Fatalf("source changed without an install: %q", status.Source)
	}
	if status.Message == "" || !strings.Contains(status.Message, "not newer") {
		t.Fatalf("message = %q", status.Message)
	}
	if status.Channel == nil || status.Channel.Version != "0.1.0" || status.Channel.Newer {
		t.Fatalf("channel = %+v", status.Channel)
	}
	if _, err := os.Stat(rtruntime.BinaryPath(service.configRoot)); !os.IsNotExist(err) {
		t.Fatalf("the binary path was written without a newer build: %v", err)
	}
}

func TestInstallKeepsTheOldBackendWhenAdoptionFails(t *testing.T) {
	parser := newFakeParser()
	parser.adoptErr = errors.New("the binary failed its self-check")
	_, _, fetch, update := channelFixture(t, channelVersionAbove(t), []byte("artifact"))
	service := newService(t, parser, fetch, update, SourceBuiltin)

	status, err := service.Install(context.Background())
	if !errors.Is(err, ErrUpdate) {
		t.Fatalf("install error = %v, want ErrUpdate", err)
	}
	if status.Source != SourceBuiltin || status.Version != reltitletags.ImplementationVersion {
		t.Fatalf("the old backend was not kept: %+v", status)
	}
	if !strings.Contains(status.Note, "not switched") {
		t.Fatalf("note = %q", status.Note)
	}
	if status.Channel == nil || !strings.Contains(status.Channel.Error, "self-check") {
		t.Fatalf("channel = %+v", status.Channel)
	}
}

func TestInstallClassifiesARefusedDownloadAsAnUpdateFailure(t *testing.T) {
	parser := newFakeParser()
	update := func(context.Context, rtruntime.UpdateOptions) (rtruntime.UpdateResult, error) {
		result := rtruntime.UpdateResult{Channel: rtruntime.Channel{Version: "0.0.4"}, Version: "0.0.3"}
		return result, errors.Join(rtruntime.ErrAdoption, errors.New("the temporary binary failed its self-check"))
	}
	service := newService(t, parser, nil, update, SourceBuiltin)

	status, err := service.Install(context.Background())
	if !errors.Is(err, ErrUpdate) || errors.Is(err, ErrChannel) {
		t.Fatalf("install error = %v, want ErrUpdate without ErrChannel", err)
	}
	if status.Source != SourceBuiltin || status.Version != reltitletags.ImplementationVersion {
		t.Fatalf("the old backend was not kept: %+v", status)
	}
	if status.Channel == nil || !strings.Contains(status.Channel.Error, "temporary binary") {
		t.Fatalf("channel = %+v", status.Channel)
	}
}

// TestNormalizeLaneReportsUnknownValues pins the composition contract: every
// value maps onto a lane the read model can report, and the boolean says
// whether the configured value was one of the two lanes at all — the
// composition site turns a false into a startup warning instead of letting the
// stable fallback pass silently.
func TestNormalizeLaneReportsUnknownValues(t *testing.T) {
	for _, entry := range []struct {
		raw   string
		want  string
		known bool
	}{
		{"", LaneStable, true},
		{"stable", LaneStable, true},
		{" Test ", LaneTest, true},
		{"tests", LaneStable, false},
		{"garbage", LaneStable, false},
	} {
		got, known := NormalizeLane(entry.raw)
		if got != entry.want || known != entry.known {
			t.Fatalf("NormalizeLane(%q) = (%q, %v), want (%q, %v)", entry.raw, got, known, entry.want, entry.known)
		}
	}
}
