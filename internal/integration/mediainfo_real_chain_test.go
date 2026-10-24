package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/filemaintenance"
	observabilityport "bansoko/internal/observability/port"
)

// realChainMediaFactsTemplate names the resolution and the video codec in the
// TV video file name, so a run shows which values the placed file's name and
// the persisted facts carry.
const realChainMediaFactsTemplate = "${anime.title} ${ep.s00e00} [${media.resolution}] [${media.video_codec}].${file.ext}"

// requireMediaInfo fails the test when the mediainfo binary is not on PATH. The
// real-chain lane declares mediainfo as a prerequisite exactly like
// qbittorrent-nox: a missing tool must fail the lane, never skip the proof.
func requireMediaInfo(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("mediainfo")
	if err != nil {
		t.Fatalf("mediainfo is required on PATH for the real-chain media facts proof: %v", err)
	}
	version, err := exec.Command(path, "--Version").CombinedOutput()
	if err != nil {
		t.Fatalf("mediainfo --Version failed: %v: %s", err, version)
	}
	t.Logf("MEDIAINFO EVIDENCE binary=%s version=%q", path, strings.Join(strings.Fields(string(version)), " "))
}

// recordingRuntimeLogger keeps every runtime event so a test can assert that a
// run produced no degraded-diagnostics.
type recordingRuntimeLogger struct {
	mu     sync.Mutex
	events []observabilityport.RuntimeEvent
}

func (l *recordingRuntimeLogger) record(e observabilityport.RuntimeEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}
func (l *recordingRuntimeLogger) Debug(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}
func (l *recordingRuntimeLogger) Info(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}
func (l *recordingRuntimeLogger) Warn(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}
func (l *recordingRuntimeLogger) Error(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}

func (l *recordingRuntimeLogger) snapshot() []observabilityport.RuntimeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]observabilityport.RuntimeEvent(nil), l.events...)
}

// assertRealChainMediaFacts requires the four execution-time facts of the
// downloaded video to be stored with the catalog vocabulary, and returns them.
// The file was read by the real mediainfo binary from the file qB downloaded.
func assertRealChainMediaFacts(t *testing.T, ctx context.Context, db *sql.DB, artifactID string) map[string]string {
	t.Helper()
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT facts FROM artifact_files WHERE artifact_id = $1 AND media_kind = 'video'`, artifactID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("artifact file facts are not a JSON object: %s: %v", raw, err)
	}
	got := map[string]string{}
	for _, key := range []string{"resolution", "video_codec", "audio_codec", "container"} {
		value, _ := stored[key].(string)
		got[key] = value
	}
	want := map[string]string{"resolution": "1080p", "video_codec": "HEVC", "audio_codec": "AAC", "container": "mkv"}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("artifact_files.facts[%q] = %q, want %q (facts: %s)", key, got[key], expected, raw)
		}
	}
	t.Logf("MEDIAINFO EVIDENCE artifact_files.facts resolution=%q video_codec=%q audio_codec=%q container=%q",
		got["resolution"], got["video_codec"], got["audio_codec"], got["container"])
	return got
}

// assertRenderedFromRealFacts renders the placed file's naming template again
// from its persisted facts, through the maintenance renderer that reads
// artifact_files.facts, and requires the resolution and video codec to appear in
// the path. It returns the rendered path.
func assertRenderedFromRealFacts(t *testing.T, ctx context.Context, db *sql.DB, rules filemaintenance.NamingRulesSource, animeID, placedPath string, sizeBytes int64) string {
	t.Helper()
	var placementID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM placements WHERE managed_anime_id = $1 AND physical_path = $2`, animeID, placedPath).Scan(&placementID); err != nil {
		t.Fatalf("placement of %q: %v", placedPath, err)
	}
	renderer := filemaintenance.NewNamingTargetRenderer(db, rules)
	rendered, err := renderer.RenderCalibratedTarget(ctx, animeID, placedPath, sizeBytes, filemaintenance.CalibratedRenderFacts{
		EpisodeDisplay: "1",
		PlacementID:    placementID,
	})
	if err != nil {
		t.Fatalf("render the placed file from its persisted facts: %v", err)
	}
	if !strings.Contains(rendered, "[1080p] [HEVC].mkv") {
		t.Fatalf("rendered path %q does not carry the real media.resolution and media.video_codec", rendered)
	}
	return rendered
}

// mediaProbeEvents counts the degraded media-probe diagnostics of one run.
func mediaProbeEvents(logger *recordingRuntimeLogger) []observabilityport.RuntimeEvent {
	var out []observabilityport.RuntimeEvent
	for _, e := range logger.snapshot() {
		if e.Operation == "media_probe" {
			out = append(out, e)
		}
	}
	return out
}
