package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
)

// fixtureSubtitle is one subtitle of the shared one-video fixture.
type fixtureSubtitle struct{ relativePath, ref string }

// subtitleTargetsFixture prepares the shared one-video fixture with the given
// subtitles (in manifest order) under the shipped default naming rules and
// returns the planned operations.
func subtitleTargetsFixture(t *testing.T, subtitles ...fixtureSubtitle) (fileFixture, []operationRow, func(string, string, string)) {
	t.Helper()
	return subtitleTargetsFixtureWith(t, nil, subtitles...)
}

// subtitleTargetsFixtureWith is subtitleTargetsFixture with a further policy
// edit applied after the defaults.
func subtitleTargetsFixtureWith(t *testing.T, edit func(map[string]any), subtitles ...fixtureSubtitle) (fileFixture, []operationRow, func(string, string, string)) {
	t.Helper()
	manifest := make([]map[string]any, 0, len(subtitles))
	for _, subtitle := range subtitles {
		manifest = append(manifest, map[string]any{"file_ref": subtitle.ref, "relative_path": subtitle.relativePath, "exact_size": 8, "media_kind": "subtitle"})
	}
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, manifest...)(p)
		p["fixture_media_values"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tv-1", "kind": "tv", "season": 1, "version": 1}, "reason_code": nil}
		p["fixture_anime_season"] = 1
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = naming.DefaultNamingRules()
		if edit != nil {
			edit(p)
		}
	})
	t.Cleanup(func() { db.Close() })
	dir := t.TempDir()
	for _, subtitle := range subtitles {
		source := filepath.Join(dir, subtitle.relativePath)
		if err := os.WriteFile(source, []byte("subtitle"), 0o600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, fixture, "artifact-file-"+subtitle.ref, subtitle.ref, subtitle.relativePath, source, "subtitle", 8)
	}
	service := newFixtureServiceWithPorts(t, db, fixture, &testExecutor{}, freshTargetObserver{})
	prepareFixture(t, db, service, fixture.artifactID)
	requirement := func(ref, want, reason string) {
		assertRequirement(t, db, fixture.artifactID, ref, want, reason)
	}
	if failed := failedDeliveryResults(t, db, fixture.artifactID); failed != 0 {
		t.Fatalf("no file of this release may fail: %d failed results", failed)
	}
	return fixture, loadOperations(t, db, fixture.artifactID), requirement
}

// The owner's FLsnow case at placement: one video and two subtitles whose own
// tags differ only in the Chinese script (`.chs&jpn.ass` / `.cht&jpn.ass`).
// The shipped suffix is sub.lang_tag, read from each subtitle file first,
// so the two frozen targets are distinct and both subtitles place beside the
// video. The release-level rt.sub_lang default rendered one target for both
// and failed them.
func TestSubtitlesOfOneVideoFreezeTheirOwnLanguageTargets(t *testing.T) {
	fixture, operations, requirement := subtitleTargetsFixture(t,
		fixtureSubtitle{"source.chs&jpn.ass", "sub-chs"},
		fixtureSubtitle{"source.cht&jpn.ass", "sub-cht"})
	if len(operations) != 3 {
		t.Fatalf("the video and both subtitles must plan, got %+v", operations)
	}
	video := strings.TrimSuffix(fixtureDefaultVideoTarget(t, fixture), ".mkv")
	for ref, suffix := range map[string]string{"sub-chs": "zh-Hans", "sub-cht": "zh-Hant"} {
		operation := operationForFile(t, operations, "artifact-file-"+ref)
		if want := video + "." + suffix + ".ass"; operation.targetPath != want || operation.failureCode != "" || !operation.boundVideoOperationID.Valid {
			t.Fatalf("%s froze %+v, want %q beside the video", ref, operation, want)
		}
		requirement(ref, "required", "file_rules_selected")
	}
}

// Two subtitles of one video that render one target are settled by order:
// the first in manifest path order keeps the target and places, the other is
// excluded as target_duplicate (not failed). The video is unaffected. Here
// `.chs.ass` and `.sc.ass` both read SC, whose tag is zh-Hans.
func TestSubtitlesOfOneVideoOnOneTargetKeepTheFirstByPath(t *testing.T) {
	// The manifest lists the later path first: path order decides, never
	// manifest ordinal or arrival.
	fixture, operations, requirement := subtitleTargetsFixture(t,
		fixtureSubtitle{"source.sc.ass", "sub-sc"},
		fixtureSubtitle{"source.chs.ass", "sub-chs"})
	if len(operations) != 2 {
		t.Fatalf("the video and the first subtitle must plan, got %+v", operations)
	}
	kept := operationForFile(t, operations, "artifact-file-sub-chs")
	if want := strings.TrimSuffix(fixtureDefaultVideoTarget(t, fixture), ".mkv") + ".zh-Hans.ass"; kept.targetPath != want || kept.failureCode != "" {
		t.Fatalf("the first subtitle by path froze %+v, want %q", kept, want)
	}
	operationForFile(t, operations, fixture.fileID)
	requirement("sub-chs", "required", "file_rules_selected")
	requirement("sub-sc", "excluded", "target_duplicate")
}

// Bound subtitles follow their video, not the file rules: a not_in "jpn" rule keeps the video `source.mkv` and never
// drops `source.chs&jpn.ass`, which places beside it.
func TestBoundSubtitleIsNotSubjectToTheFileRules(t *testing.T) {
	fixture, operations, requirement := subtitleTargetsFixtureWith(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileRules] = map[string]any{"must_contain": "", "not_in": "jpn"}
	}, fixtureSubtitle{"source.chs&jpn.ass", "sub-chs"})
	if len(operations) != 2 {
		t.Fatalf("the video and its subtitle must plan, got %+v", operations)
	}
	subtitle := operationForFile(t, operations, "artifact-file-sub-chs")
	if want := strings.TrimSuffix(fixtureDefaultVideoTarget(t, fixture), ".mkv") + ".zh-Hans.ass"; subtitle.targetPath != want || subtitle.failureCode != "" || !subtitle.boundVideoOperationID.Valid {
		t.Fatalf("the bound subtitle froze %+v, want %q beside the video", subtitle, want)
	}
	requirement("sub-chs", "required", "file_rules_selected")
}
