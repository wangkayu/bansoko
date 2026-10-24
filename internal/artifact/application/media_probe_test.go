package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/worker"
)

// fakeProber answers per path and records every call.
type fakeProber struct {
	byPath map[string]domain.MediaFacts
	errs   map[string]error
	calls  []string
	// onCall runs before the answer, for tests that change the context.
	onCall func()
}

func (f *fakeProber) Probe(_ context.Context, path string) (domain.MediaFacts, error) {
	f.calls = append(f.calls, path)
	if f.onCall != nil {
		f.onCall()
	}
	if err := f.errs[path]; err != nil {
		return domain.MediaFacts{}, err
	}
	return f.byPath[path], nil
}

// recordingLogger keeps every runtime event with its level.
type recordingLogger struct {
	events []recordedEvent
}

type recordedEvent struct {
	level string
	event observabilityport.RuntimeEvent
}

func (l *recordingLogger) Debug(_ context.Context, e observabilityport.RuntimeEvent) {
	l.events = append(l.events, recordedEvent{"debug", e})
}
func (l *recordingLogger) Info(_ context.Context, e observabilityport.RuntimeEvent) {
	l.events = append(l.events, recordedEvent{"info", e})
}
func (l *recordingLogger) Warn(_ context.Context, e observabilityport.RuntimeEvent) {
	l.events = append(l.events, recordedEvent{"warn", e})
}
func (l *recordingLogger) Error(_ context.Context, e observabilityport.RuntimeEvent) {
	l.events = append(l.events, recordedEvent{"error", e})
}

var hevcFacts = domain.MediaFacts{Resolution: "1080p", VideoCodec: "HEVC", AudioCodec: "AAC", Container: "mkv"}

func probeFixture() ([]artifactInspectionTarget, []domain.ArtifactFacts) {
	paths := []string{"/downloads/show/e01.mkv", "/downloads/show/e01.zh.ass", "/downloads/show/e02.mkv"}
	kinds := []string{"video", "subtitle", "video"}
	targets := make([]artifactInspectionTarget, len(paths))
	inspected := make([]domain.ArtifactFacts, len(paths))
	for i := range paths {
		targets[i] = artifactInspectionTarget{path: paths[i], mediaKind: kinds[i]}
		inspected[i] = domain.ArtifactFacts{Path: paths[i], MediaKind: kinds[i]}
	}
	return targets, inspected
}

func TestProbeMediaFactsFillsVideoFilesOnly(t *testing.T) {
	targets, inspected := probeFixture()
	prober := &fakeProber{byPath: map[string]domain.MediaFacts{
		"/downloads/show/e01.mkv": hevcFacts,
		"/downloads/show/e02.mkv": {Resolution: "720p", VideoCodec: "AVC", Container: "mp4"},
	}}
	logger := &recordingLogger{}
	service := &Service{prober: prober, logger: logger}

	service.probeMediaFacts(context.Background(), "artifact-1", targets, inspected)

	if len(prober.calls) != 2 || prober.calls[0] != "/downloads/show/e01.mkv" || prober.calls[1] != "/downloads/show/e02.mkv" {
		t.Fatalf("probed %v, want the two videos only", prober.calls)
	}
	if targets[0].media != hevcFacts {
		t.Fatalf("first video facts = %+v", targets[0].media)
	}
	if !targets[1].media.IsZero() {
		t.Fatalf("a subtitle carries media facts: %+v", targets[1].media)
	}
	if want := (domain.MediaFacts{Resolution: "720p", VideoCodec: "AVC", Container: "mp4"}); targets[2].media != want {
		t.Fatalf("second video facts = %+v, want %+v (an absent audio stream stays empty)", targets[2].media, want)
	}
	if len(logger.events) != 0 {
		t.Fatalf("successful probes logged %+v", logger.events)
	}
}

func TestProbeMediaFactsFailureLeavesFactsEmptyAndLogsABoundedDiagnostic(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantClass string
	}{
		{"tool exit", fmt.Errorf("%w: tool exited with an error", domain.ErrMediaProbeFailed), "failed"},
		{"timeout", domain.ErrMediaProbeTimeout, "timeout"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"oversized or malformed output", fmt.Errorf("%w: malformed JSON", domain.ErrMediaProbeOutputInvalid), "output_invalid"},
		{"unclassified error", errors.New("open /private/library/Secret Show/e01.mkv: permission denied"), "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets, inspected := probeFixture()
			prober := &fakeProber{
				errs:   map[string]error{"/downloads/show/e01.mkv": tc.err},
				byPath: map[string]domain.MediaFacts{"/downloads/show/e02.mkv": hevcFacts},
			}
			logger := &recordingLogger{}
			service := &Service{prober: prober, logger: logger}

			service.probeMediaFacts(context.Background(), "artifact-9", targets, inspected)

			if !targets[0].media.IsZero() {
				t.Fatalf("failed probe left facts %+v", targets[0].media)
			}
			if targets[2].media != hevcFacts {
				t.Fatalf("one failing file stopped the next one: %+v", targets[2].media)
			}
			if len(logger.events) != 1 {
				t.Fatalf("logged %d events, want exactly one: %+v", len(logger.events), logger.events)
			}
			got := logger.events[0]
			if got.level != "warn" || got.event.ReasonCode != "media_probe_"+tc.wantClass || got.event.ErrorClass != tc.wantClass ||
				got.event.OwnerType != "artifact" || got.event.OwnerID != "artifact-9" || got.event.Outcome != "degraded" {
				t.Fatalf("diagnostic = %+v", got)
			}
			serialized := fmt.Sprintf("%+v", got.event)
			for _, leak := range []string{"/downloads", "/private", "Secret Show", "permission denied"} {
				if strings.Contains(serialized, leak) {
					t.Fatalf("diagnostic leaked %q: %s", leak, serialized)
				}
			}
		})
	}
}

func TestProbeMediaFactsMissingToolStopsAfterTheFirstFile(t *testing.T) {
	targets, inspected := probeFixture()
	prober := &fakeProber{errs: map[string]error{
		"/downloads/show/e01.mkv": fmt.Errorf("%w: not found", domain.ErrMediaProbeUnavailable),
		"/downloads/show/e02.mkv": fmt.Errorf("%w: not found", domain.ErrMediaProbeUnavailable),
	}}
	logger := &recordingLogger{}
	service := &Service{prober: prober, logger: logger}

	service.probeMediaFacts(context.Background(), "artifact-1", targets, inspected)

	if len(prober.calls) != 1 {
		t.Fatalf("probed %v, want one call: the tool is missing for every file", prober.calls)
	}
	if len(logger.events) != 1 || logger.events[0].event.ErrorClass != "unavailable" {
		t.Fatalf("diagnostics = %+v, want one unavailable", logger.events)
	}
	for i, target := range targets {
		if !target.media.IsZero() {
			t.Fatalf("target %d carries facts %+v", i, target.media)
		}
	}
}

func TestProbeMediaFactsEmptyAnswerIsALoggedNoValue(t *testing.T) {
	targets, inspected := probeFixture()
	prober := &fakeProber{} // every video answers with no value
	logger := &recordingLogger{}
	service := &Service{prober: prober, logger: logger}

	service.probeMediaFacts(context.Background(), "artifact-1", targets, inspected)

	if len(prober.calls) != 2 {
		t.Fatalf("probed %v", prober.calls)
	}
	if len(logger.events) != 2 {
		t.Fatalf("logged %d events, want one per video with no value: %+v", len(logger.events), logger.events)
	}
	for _, recorded := range logger.events {
		if recorded.level != "warn" || recorded.event.ReasonCode != "media_probe_no_value" {
			t.Fatalf("diagnostic = %+v", recorded)
		}
	}
	for i, target := range targets {
		if !target.media.IsZero() {
			t.Fatalf("target %d carries facts %+v", i, target.media)
		}
	}
}

func TestProbeMediaFactsWithoutAProberDoesNothing(t *testing.T) {
	targets, inspected := probeFixture()
	logger := &recordingLogger{}
	(&Service{logger: logger}).probeMediaFacts(context.Background(), "artifact-1", targets, inspected)
	var missing *Service
	missing.probeMediaFacts(context.Background(), "artifact-1", targets, inspected)
	if len(logger.events) != 0 {
		t.Fatalf("an unwired prober logged %+v", logger.events)
	}
	for i, target := range targets {
		if !target.media.IsZero() {
			t.Fatalf("target %d carries facts %+v", i, target.media)
		}
	}
}

func TestProbeMediaFactsFailureWithoutALoggerDoesNotPanic(t *testing.T) {
	targets, inspected := probeFixture()
	prober := &fakeProber{errs: map[string]error{"/downloads/show/e01.mkv": domain.ErrMediaProbeFailed}}
	(&Service{prober: prober}).probeMediaFacts(context.Background(), "artifact-1", targets, inspected)
	if !targets[0].media.IsZero() {
		t.Fatalf("facts = %+v", targets[0].media)
	}
}

func TestProbeMediaFactsStopsWhenTheInspectionContextEnds(t *testing.T) {
	targets, inspected := probeFixture()
	ctx, cancel := context.WithCancel(context.Background())
	prober := &fakeProber{
		byPath: map[string]domain.MediaFacts{"/downloads/show/e01.mkv": hevcFacts, "/downloads/show/e02.mkv": hevcFacts},
		onCall: cancel, // the attempt window closes while the first file is read
	}
	logger := &recordingLogger{}
	service := &Service{prober: prober, logger: logger}

	service.probeMediaFacts(ctx, "artifact-1", targets, inspected)

	if len(prober.calls) != 1 {
		t.Fatalf("probed %v, want the loop to stop once the context ended", prober.calls)
	}
	if targets[0].media != hevcFacts || !targets[2].media.IsZero() {
		t.Fatalf("facts: first=%+v second=%+v", targets[0].media, targets[2].media)
	}
	if len(logger.events) != 1 || logger.events[0].event.ErrorClass != "canceled" {
		t.Fatalf("diagnostics = %+v, want one canceled", logger.events)
	}
}

func TestProbeMediaFactsRespectsAnExpiredDeadline(t *testing.T) {
	targets, inspected := probeFixture()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	prober := &fakeProber{byPath: map[string]domain.MediaFacts{"/downloads/show/e01.mkv": hevcFacts}}
	logger := &recordingLogger{}
	service := &Service{prober: prober, logger: logger}

	service.probeMediaFacts(ctx, "artifact-1", targets, inspected)

	if len(prober.calls) != 0 {
		t.Fatalf("probed %v after the deadline", prober.calls)
	}
	if len(logger.events) != 1 || logger.events[0].event.ErrorClass != "timeout" {
		t.Fatalf("diagnostics = %+v, want one timeout", logger.events)
	}
}

func TestBuildInspectionPlanStoresMediaFactsUnderTheNamingKeys(t *testing.T) {
	at := time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)
	root := hermeticDownloadsRoot(t)
	videoPath := filepath.Join(root, "show", "show.mkv")
	subtitlePath := filepath.Join(root, "show", "show.zh.ass")
	plan, err := buildInspectionPlanForFiles(
		artifactSnapshot{id: "artifact-media"},
		inspectInput{LocalRoot: root},
		[]artifactInspectionTarget{
			{path: videoPath, mediaKind: "video", primary: true, media: hevcFacts},
			{path: subtitlePath, mediaKind: "subtitle", language: "zh"},
		},
		[]domain.ArtifactFacts{
			{Path: videoPath, SizeBytes: 100, SHA256: strings.Repeat("a", 64), MediaKind: "video", ObservedAt: at},
			{Path: subtitlePath, SizeBytes: 20, SHA256: strings.Repeat("b", 64), MediaKind: "subtitle", ObservedAt: at},
		},
		at,
	)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]map[string]any{}
	for _, file := range plan.files {
		var facts map[string]any
		if err := json.Unmarshal(file.facts, &facts); err != nil {
			t.Fatal(err)
		}
		byKind[file.mediaKind] = facts
	}
	for key, want := range map[string]string{"resolution": "1080p", "video_codec": "HEVC", "audio_codec": "AAC", "container": "mkv"} {
		if byKind["video"][key] != want {
			t.Fatalf("video facts[%q] = %v, want %q (%v)", key, byKind["video"][key], want, byKind["video"])
		}
	}
	for _, key := range []string{"resolution", "video_codec", "audio_codec", "container"} {
		if _, present := byKind["subtitle"][key]; present {
			t.Fatalf("a subtitle carries %q: %v", key, byKind["subtitle"])
		}
	}
	// The pre-existing facts are untouched by the media keys.
	if byKind["video"]["source"] != "artifact_inspect" || byKind["video"]["media_kind"] != "video" {
		t.Fatalf("base facts changed: %v", byKind["video"])
	}
}

func TestBuildInspectionPlanOmitsEmptyMediaFactsAndBoundsValues(t *testing.T) {
	at := time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)
	root := hermeticDownloadsRoot(t)
	videoPath := filepath.Join(root, "show", "show.mkv")
	plan, err := buildInspectionPlanForFiles(
		artifactSnapshot{id: "artifact-partial"},
		inspectInput{LocalRoot: root},
		[]artifactInspectionTarget{{path: videoPath, mediaKind: "video", primary: true, media: domain.MediaFacts{VideoCodec: strings.Repeat("X", 100), Container: "mkv"}}},
		[]domain.ArtifactFacts{{Path: videoPath, SizeBytes: 100, SHA256: strings.Repeat("a", 64), MediaKind: "video", ObservedAt: at}},
		at,
	)
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(plan.file.facts, &facts); err != nil {
		t.Fatal(err)
	}
	if _, present := facts["resolution"]; present {
		t.Fatalf("an empty fact was stored: %v", facts)
	}
	if _, present := facts["audio_codec"]; present {
		t.Fatalf("an empty fact was stored: %v", facts)
	}
	if codec, _ := facts["video_codec"].(string); len(codec) != maxMediaFactLen {
		t.Fatalf("video_codec length = %d, want it bounded to %d", len(codec), maxMediaFactLen)
	}
	if facts["container"] != "mkv" {
		t.Fatalf("container = %v", facts["container"])
	}
}

// artifactMediaFacts reads the persisted facts object of the single video file.
func artifactMediaFacts(t *testing.T, service *Service, artifactID string) map[string]any {
	t.Helper()
	var raw []byte
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT facts FROM artifact_files WHERE artifact_id = $1 AND media_kind = 'video'`, artifactID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("artifact file facts are not a JSON object: %s (%v)", raw, err)
	}
	return facts
}

func TestHandlePersistsProbedMediaFactsOnTheArtifactFile(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:media-task","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-media-ok", "task-media-ok", "completed", "pending", correlation, `{"files":[{"name":"Show - 01.mkv"}]}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-media-ok", []byte(`{"download_task_id":"task-media-ok","provider_task_id":"tag:media-task","local_root":"/stale/configured-root"}`))
	at := time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 1024, SHA256: strings.Repeat("b", 64), ObservedAt: at, MediaKind: "video"}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	prober := &fakeProber{byPath: map[string]domain.MediaFacts{canonicalPath: hevcFacts}}
	logger := &recordingLogger{}
	service.SetMediaProber(prober, logger)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("outcome = %+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	if len(prober.calls) != 1 || prober.calls[0] != canonicalPath {
		t.Fatalf("probed %v, want the inspected canonical path", prober.calls)
	}
	facts := artifactMediaFacts(t, service, "artifact-media-ok")
	for key, want := range map[string]string{"resolution": "1080p", "video_codec": "HEVC", "audio_codec": "AAC", "container": "mkv"} {
		if facts[key] != want {
			t.Fatalf("artifact_files.facts[%q] = %v, want %q (%v)", key, facts[key], want, facts)
		}
	}
	if len(logger.events) != 0 {
		t.Fatalf("a successful probe logged %+v", logger.events)
	}
}

func TestHandleProbeFailureStillVerifiesWithEmptyMediaFacts(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:media-fail","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-media-fail", "task-media-fail", "completed", "pending", correlation, `{"files":[{"name":"Show - 01.mkv"}]}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-media-fail", []byte(`{"download_task_id":"task-media-fail","provider_task_id":"tag:media-fail","local_root":"/stale/configured-root"}`))
	at := time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 1024, SHA256: strings.Repeat("b", 64), ObservedAt: at, MediaKind: "video"}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	prober := &fakeProber{errs: map[string]error{canonicalPath: fmt.Errorf("%w: not found", domain.ErrMediaProbeUnavailable)}}
	logger := &recordingLogger{}
	service.SetMediaProber(prober, logger)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("a failed probe must not fail the artifact: outcome = %+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	facts := artifactMediaFacts(t, service, "artifact-media-fail")
	for _, key := range []string{"resolution", "video_codec", "audio_codec", "container"} {
		if value, present := facts[key]; present {
			t.Fatalf("artifact_files.facts[%q] = %v after a failed probe, want it absent (%v)", key, value, facts)
		}
	}
	if facts["source"] != "artifact_inspect" || facts["media_kind"] != "video" {
		t.Fatalf("base facts missing after a failed probe: %v", facts)
	}
	if len(logger.events) != 1 || logger.events[0].level != "warn" || logger.events[0].event.ReasonCode != "media_probe_unavailable" ||
		logger.events[0].event.OwnerID != "artifact-media-fail" {
		t.Fatalf("diagnostics = %+v, want one bounded unavailable warning", logger.events)
	}
	if strings.Contains(fmt.Sprintf("%+v", logger.events[0].event), root) {
		t.Fatalf("the diagnostic leaked the private path: %+v", logger.events[0])
	}
}
