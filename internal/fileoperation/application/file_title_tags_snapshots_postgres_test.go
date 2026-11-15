package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatecatalog/notification"
)

// TestPrepareFreezesTheDownloadFileNameParseForVideoAndSubtitle drives the
// production preparation chain (plan claim -> prepare -> typed plan commit) and
// reads the committed snapshots, not a direct UPDATE of a snapshot. Each
// operation freezes the reltitle parse of its own downloaded file name: the
// subtitle's CHT suffix makes its parse differ from the video's, so neither
// block can be the other side's. The library target names are never parsed.
func TestPrepareFreezesTheDownloadFileNameParseForVideoAndSubtitle(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "sub-1", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		p["fixture_media_values"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tv-1", "kind": "tv", "season": 1, "version": 1}, "reason_code": nil}
		p["fixture_anime_season"] = 1
		sections := p["effective_sections"].(map[string]any)
		rules := naming.DefaultNamingRules()
		rules.SubtitleSuffixTemplate = "${defaultValue('und', file.sub.lang_tag)}"
		sections[animedomain.SectionNamingRules] = rules
		sections[animedomain.SectionFileEffect] = map[string]any{"operation": "move", "collision_policy": "versioned"}
	})
	defer db.Close()

	// The download listing keeps its relative names (the subtitle binder pairs
	// on them), while the files on disk carry the names a real download would:
	// the frt parse must come from the downloaded file, not from the listing.
	// The subtitle's name still starts with the video's stem (the binder rule),
	// and its CHT suffix makes its own parse differ from the video's.
	videoDownload := filepath.Join(t.TempDir(), "[文件组] Fixture Anime - 01 [1080p].mkv")
	if err := os.Rename(fixture.sourcePath, videoDownload); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).Exec(`UPDATE artifact_files SET source_path=$2 WHERE id=$1`, fixture.fileID, videoDownload); err != nil {
		t.Fatal(err)
	}
	subtitleDownload := filepath.Join(t.TempDir(), "[文件组] Fixture Anime - 01 [1080p].CHT.ass")
	if err := os.WriteFile(subtitleDownload, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "artifact-file-sub-1", "sub-1", "source.chs.ass", subtitleDownload, "subtitle", 8)
	videoTarget := fixtureDefaultVideoTarget(t, fixture)
	if err := os.MkdirAll(filepath.Dir(videoTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoTarget, []byte("existing video"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	if len(operations) != 2 {
		t.Fatalf("expected video and subtitle operations, got %d", len(operations))
	}
	video := operationForFile(t, operations, fixture.fileID)
	subtitle := operationForFile(t, operations, "artifact-file-sub-1")
	if video.state != ownerStatePending || subtitle.state != ownerStatePending {
		t.Fatalf("operation states video=%q subtitle=%q", video.state, subtitle.state)
	}
	// The library target of the video is the rendered name, not the download
	// name; the block must still describe the download name.
	var videoSnapshot struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(video.renderSnapshot, &videoSnapshot); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(videoSnapshot.Filename, "文件组") {
		t.Fatalf("video target name %q already carries the download group; the fixture cannot tell the sources apart", videoSnapshot.Filename)
	}

	for _, side := range []struct {
		name   string
		row    operationRow
		lang   any
		source string
	}{
		{name: "video", row: video, lang: nil, source: videoDownload},
		{name: "subtitle", row: subtitle, lang: "TC", source: subtitleDownload},
	} {
		block := frozenFileTitleTags(t, side.row.renderSnapshot)
		if block.Schema != acquisitiondomain.TitleTagSchema || block.Implementation == "" {
			t.Fatalf("%s frozen block provenance = %+v", side.name, block)
		}
		if block.Diagnostic != "" {
			t.Fatalf("%s frozen block diagnostic = %q", side.name, block.Diagnostic)
		}
		// The parse is the one of the downloaded file's own name (checked
		// against the parser on that exact path), frozen into the snapshot.
		want := naming.ParseFileTitleTags(naming.FileNamingFacts{
			OriginalName: filepath.Base(side.source),
			Basename:     strings.TrimSuffix(filepath.Base(side.source), filepath.Ext(side.source)),
			Extension:    strings.TrimPrefix(strings.ToLower(filepath.Ext(side.source)), "."),
			Kind:         "video",
		})
		if want == nil {
			t.Fatalf("%s source name has no parse", side.name)
		}
		if strings.Join(block.Tags, ",") != strings.Join(want.Tags, ",") || strings.Join(block.ValueTags, ",") != strings.Join(want.ValueTags, ",") {
			t.Fatalf("%s frozen tags = %v %v, want %v %v", side.name, block.Tags, block.ValueTags, want.Tags, want.ValueTags)
		}
		facts := notification.FileReltitleFacts(side.row.renderSnapshot, "frt", notificationText)
		for key, value := range map[string]any{
			"frt.title":      "Fixture Anime",
			"frt.group":      "文件组",
			"frt.res":        "1080p",
			"frt.lang":       side.lang,
			"frt.uncensored": false,
			"frt.rev":        nil,
			"frt.source":     nil,
			"frt.vc":         nil,
			"frt.ac":         nil,
			"frt.subflag":    nil,
		} {
			if facts[key] != value {
				t.Fatalf("%s %s = %#v, want %#v (block %+v)", side.name, key, facts[key], value, block)
			}
		}
	}
}

// frozenFileTitleTags decodes the block the preparation committed inside one
// operation's frozen render snapshot.
func frozenFileTitleTags(t *testing.T, snapshot []byte) acquisitiondomain.StoredTitleTags {
	t.Helper()
	var wrapper struct {
		FileTitleTags *acquisitiondomain.StoredTitleTags `json:"file_title_tags"`
	}
	if err := json.Unmarshal(snapshot, &wrapper); err != nil {
		t.Fatalf("decode frozen render snapshot: %v (%s)", err, snapshot)
	}
	if wrapper.FileTitleTags == nil {
		t.Fatalf("frozen render snapshot has no file_title_tags: %s", snapshot)
	}
	return *wrapper.FileTitleTags
}
