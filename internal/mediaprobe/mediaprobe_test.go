package mediaprobe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/mediaprobe/mediaprobetest"
	"time"
)

type mediaReport struct {
	Media struct {
		Track []struct {
			Type          string      `json:"@type"`
			Format        string      `json:"Format"`
			Width         json.Number `json:"Width"`
			Height        json.Number `json:"Height"`
			Duration      json.Number `json:"Duration"`
			Channels      json.Number `json:"Channels"`
			SamplingRate  json.Number `json:"SamplingRate"`
			StreamCount   json.Number `json:"StreamCount"`
			FormatVersion string      `json:"Format_Version"`
		} `json:"track"`
	} `json:"media"`
}

func decodeReport(t *testing.T, raw []byte) mediaReport {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var report mediaReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("report is not MediaInfo JSON: %v\n%s", err, raw)
	}
	return report
}

func requireTrack(t *testing.T, report mediaReport, kind string) int {
	t.Helper()
	for i, track := range report.Media.Track {
		if track.Type == kind {
			return i
		}
	}
	t.Fatalf("no %s track in report: %+v", kind, report.Media.Track)
	return -1
}

func requireNumber(t *testing.T, value json.Number, want int64, field string) {
	t.Helper()
	got, err := value.Int64()
	if err != nil {
		t.Fatalf("%s is not an integer: %v", field, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", field, got, want)
	}
}

func requireSeconds(t *testing.T, value json.Number, want float64, field string) {
	t.Helper()
	got, err := value.Float64()
	if err != nil {
		t.Fatalf("%s is not a float: %v", field, err)
	}
	if got != want {
		t.Fatalf("%s = %v s, want %v s", field, got, want)
	}
}

func requireFacts(t *testing.T, raw []byte, container, videoCodec, audioCodec string) {
	t.Helper()
	report := decodeReport(t, raw)

	general := report.Media.Track[requireTrack(t, report, "General")]
	if general.Format != container {
		t.Fatalf("container = %q, want %q", general.Format, container)
	}
	requireSeconds(t, general.Duration, 120, "duration")

	video := report.Media.Track[requireTrack(t, report, "Video")]
	if video.Format != videoCodec {
		t.Fatalf("video codec = %q, want %q", video.Format, videoCodec)
	}
	requireNumber(t, video.Width, 1920, "width")
	requireNumber(t, video.Height, 1080, "height")

	audio := report.Media.Track[requireTrack(t, report, "Audio")]
	if audio.Format != audioCodec {
		t.Fatalf("audio codec = %q, want %q", audio.Format, audioCodec)
	}
	requireNumber(t, audio.Channels, 2, "channels")
	requireNumber(t, audio.SamplingRate, 48000, "sampling rate")
}

func TestSelfCheck(t *testing.T) {
	// The probe must work with no external mediainfo on PATH at all.
	t.Setenv("PATH", "")
	version, err := SelfCheck(context.Background())
	if err != nil {
		t.Fatalf("SelfCheck: %v", err)
	}
	if !strings.Contains(version, "MediaInfoLib") {
		t.Fatalf("unexpected version string: %q", version)
	}
	t.Logf("embedded engine: %s", version)
}

func TestProbeJSONMatroska(t *testing.T) {
	t.Setenv("PATH", "")
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "fixture.mkv", mediaprobetest.Options{})
	raw, err := ProbeJSON(context.Background(), path, DefaultMaxOutputBytes)
	if err != nil {
		t.Fatalf("ProbeJSON: %v", err)
	}
	requireFacts(t, raw, "Matroska", "HEVC", "AAC")
}

func TestProbeJSONWebM(t *testing.T) {
	t.Setenv("PATH", "")
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "fixture.webm", mediaprobetest.Options{DocType: "webm", VideoCodec: "V_VP9", AudioCodec: "A_OPUS"})
	raw, err := ProbeJSON(context.Background(), path, DefaultMaxOutputBytes)
	if err != nil {
		t.Fatalf("ProbeJSON: %v", err)
	}
	requireFacts(t, raw, "WebM", "VP9", "Opus")
}

func TestProbeJSONStreamsAcrossChunks(t *testing.T) {
	t.Setenv("PATH", "")
	// The void tail pushes the segment well past streamChunkSize so the probe
	// performs multiple buffer iterations instead of a single read.
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "large.mkv", mediaprobetest.Options{TailPadding: 3 * streamChunkSize})
	raw, err := ProbeJSON(context.Background(), path, DefaultMaxOutputBytes)
	if err != nil {
		t.Fatalf("ProbeJSON: %v", err)
	}
	requireFacts(t, raw, "Matroska", "HEVC", "AAC")
}

func TestProbeJSONCorruptData(t *testing.T) {
	t.Setenv("PATH", "")
	path := filepath.Join(t.TempDir(), "garbage.mkv")
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := ProbeJSON(context.Background(), path, DefaultMaxOutputBytes)
	if err != nil {
		t.Fatalf("corrupt data must not crash the probe: %v", err)
	}
	// MediaInfo still answers with a JSON document; it simply has no
	// recognized tracks beyond a generic one at most.
	decodeReport(t, raw)
}

func TestProbeJSONRejectsNonRegularFile(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := ProbeJSON(context.Background(), t.TempDir(), DefaultMaxOutputBytes); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("directory probe = %v, want ErrNotRegularFile", err)
	}
}

func TestProbeJSONOutputLimit(t *testing.T) {
	t.Setenv("PATH", "")
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "fixture.mkv", mediaprobetest.Options{})
	_, err := ProbeJSON(context.Background(), path, 64)
	var limitErr *OutputLimitError
	if !errors.As(err, &limitErr) || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("probe = %v, want OutputLimitError", err)
	}
}

func TestProbeJSONCancelledContext(t *testing.T) {
	t.Setenv("PATH", "")
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "fixture.mkv", mediaprobetest.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeJSON(ctx, path, DefaultMaxOutputBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe = %v, want context.Canceled", err)
	}
}

func TestProbeJSONDeadline(t *testing.T) {
	t.Setenv("PATH", "")
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "fixture.mkv", mediaprobetest.Options{TailPadding: 4 * streamChunkSize})
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // make the deadline fire deterministically
	if _, err := ProbeJSON(ctx, path, DefaultMaxOutputBytes); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline probe = %v, want context.DeadlineExceeded", err)
	}
}

func TestProbeJSONConcurrent(t *testing.T) {
	t.Setenv("PATH", "")
	path := mediaprobetest.WriteMatroska(t, t.TempDir(), "fixture.mkv", mediaprobetest.Options{})
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := ProbeJSON(context.Background(), path, DefaultMaxOutputBytes)
			if err != nil {
				errs <- err
				return
			}
			report := decodeReport(t, raw)
			video := report.Media.Track[requireTrack(t, report, "Video")]
			if video.Format != "HEVC" {
				errs <- errors.New("unexpected video codec " + video.Format)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent probe: %v", err)
	}
}

func TestProbeJSONMissingFile(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := ProbeJSON(context.Background(), filepath.Join(t.TempDir(), "absent.mkv"), DefaultMaxOutputBytes); !errors.Is(err, ErrProbeFailed) {
		t.Fatalf("missing file probe = %v, want ErrProbeFailed", err)
	}
}
