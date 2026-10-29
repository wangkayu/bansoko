package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactadapter "bansoko/internal/artifact/adapter"
	"bansoko/internal/download/domain"
)

// A season box of 619 files (131 videos in nested folders, two subtitles per
// video and 226 extras) is inspected end to end without the 128-file cap and
// without reading content: every file is a sparse placeholder, the videos 64
// GiB each, so reading any of them would take far longer than the bound
// below. Every video gets its own probe; one probe timing out leaves only that
// file's media facts empty. The committed summaries carry counts and a
// digest, never the file list.
func TestInspectLargeReleaseReadsNoContentAndProbesEveryVideo(t *testing.T) {
	const (
		videos       = 131
		extras       = 226
		videoSize    = int64(1) << 36
		extraSize    = int64(1) << 30
		subtitleBody = "[Script Info]\nTitle: placeholder\n"
	)
	root := t.TempDir()
	var release []frozenReleaseFile
	add := func(rel string, size int64, kind string, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if content != "" {
			_, err = file.WriteString(content)
		} else {
			err = file.Truncate(size)
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Skipf("sparse placeholder files unsupported here: %v", err)
		}
		recorded := size
		release = append(release, frozenReleaseFile{SourcePath: rel, SizeBytes: &recorded, MediaKind: kind})
	}
	for episode := 1; episode <= videos; episode++ {
		folder := fmt.Sprintf("Show Box/Disc %02d/Season %d", (episode-1)/12+1, (episode-1)/26+1)
		name := fmt.Sprintf("Show - %03d", episode)
		add(folder+"/"+name+".mkv", videoSize, "video", "")
		add(folder+"/Subs/"+name+".chs.ass", int64(len(subtitleBody)), "subtitle", subtitleBody)
		add(folder+"/Subs/"+name+".cht.ass", int64(len(subtitleBody)), "subtitle", subtitleBody)
	}
	for extra := 1; extra <= extras; extra++ {
		add(fmt.Sprintf("Show Box/Extras/Scans/Booklet %02d/scan-%03d.png", (extra-1)/50+1, extra), extraSize, "", "")
	}
	if len(release) != 619 {
		t.Fatalf("fixture lists %d files", len(release))
	}

	input := inspectInput{LocalRoot: root}
	targets, err := resolveArtifactFilesFromFrozenInput(input, frozenDownloadInput{TorrentVersion: "v1", ReleaseFiles: release}, providerCorrelation{})
	if err != nil {
		t.Fatalf("a 619-file release must resolve: %v", err)
	}
	if len(targets) != 619 {
		t.Fatalf("targets = %d", len(targets))
	}

	started := time.Now()
	inspector := artifactadapter.OSArtifactInspector{}
	facts := make([]domain.ArtifactFacts, len(targets))
	for index, target := range targets {
		facts[index], err = inspector.Inspect(context.Background(), target.path)
		if err != nil {
			t.Fatalf("inspect %s: %v", target.path, err)
		}
	}
	if elapsed := time.Since(started); elapsed > 60*time.Second {
		t.Fatalf("inspecting 619 files (%d TiB of placeholders) took %s; inspection must not read content", int64(videos)*videoSize>>40, elapsed)
	}

	timedOut := filepath.Join(root, "Show Box/Disc 01/Season 1/Show - 005.mkv")
	resolvedTimedOut, err := filepath.EvalSymlinks(timedOut)
	if err != nil {
		t.Fatal(err)
	}
	hevc := domain.MediaFacts{Resolution: "1080p", VideoCodec: "HEVC", Container: "mkv"}
	prober := &fakeProber{byPath: map[string]domain.MediaFacts{}, errs: map[string]error{resolvedTimedOut: domain.ErrMediaProbeTimeout}}
	for _, inspected := range facts {
		if inspected.MediaKind == "video" && inspected.Path != resolvedTimedOut {
			prober.byPath[inspected.Path] = hevc
		}
	}
	service := &Service{prober: prober, logger: &recordingLogger{}}
	probeCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer cancel()
	service.probeMediaFacts(probeCtx, "artifact-box", targets, facts)
	if len(prober.calls) != videos {
		t.Fatalf("probed %d files, want every one of the %d videos", len(prober.calls), videos)
	}

	plan, err := buildInspectionPlanForFiles(artifactSnapshot{id: "artifact-box", providerCorrelation: []byte(`{"external_id":"tag:box","file_count":619}`)}, input, targets, facts, time.Now())
	if err != nil {
		t.Fatalf("a 619-file release must plan: %v", err)
	}
	if len(plan.files) != 619 {
		t.Fatalf("plan records %d files", len(plan.files))
	}
	probed, empty := 0, 0
	for _, file := range plan.files {
		var envelope map[string]any
		if err := json.Unmarshal(file.fingerprint, &envelope); err != nil {
			t.Fatal(err)
		}
		value, _ := envelope["value"].(string)
		if envelope["algorithm"] != "metadata" || !strings.HasPrefix(value, fmt.Sprintf("meta:size=%d;dev=", file.sizeBytes)) {
			t.Fatalf("%s fingerprint = %s", file.relativePath, file.fingerprint)
		}
		if file.mediaKind != "video" {
			continue
		}
		var recorded map[string]any
		if err := json.Unmarshal(file.facts, &recorded); err != nil {
			t.Fatal(err)
		}
		if _, ok := recorded["resolution"]; ok {
			probed++
		} else if strings.HasSuffix(file.relativePath, "Show - 005.mkv") {
			empty++
		} else {
			t.Fatalf("%s lost its media facts: %s", file.relativePath, file.facts)
		}
	}
	if probed != videos-1 || empty != 1 {
		t.Fatalf("probed=%d empty=%d", probed, empty)
	}

	inventory, fingerprints, err := inspectionSnapshots(plan.inventoryBase, plan.files, plan.file.fileRef)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(inventory, &summary); err != nil {
		t.Fatal(err)
	}
	total := float64(videos)*float64(videoSize) + float64(2*videos)*float64(len(subtitleBody)) + float64(extras)*float64(extraSize)
	if summary["file_count"] != float64(619) || summary["video_count"] != float64(videos) || summary["subtitle_count"] != float64(2*videos) ||
		summary["other_count"] != float64(extras) || summary["total_size_bytes"] != total {
		t.Fatalf("inventory summary = %s", inventory)
	}
	for _, key := range inventoryListKeys {
		if _, ok := summary[key]; ok {
			t.Fatalf("inventory summary carries the file list %q", key)
		}
	}
	if len(inventory) > 4096 || len(fingerprints) > 1024 {
		t.Fatalf("summaries grow with the file list: inventory %d bytes, fingerprints %d bytes", len(inventory), len(fingerprints))
	}
	if strings.Contains(string(inventory)+string(fingerprints), "Show - ") {
		t.Fatal("summaries carry file names")
	}
}
