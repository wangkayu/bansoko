package target

import (
	"context"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/episodeidentity"
)

func releaseEvidenceFor(t *testing.T, title string) ParserEvidence {
	t.Helper()
	evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Season: 1, SeasonBound: true,
	}, title)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

// A file's episode comes from its own filename. The release title's episode
// never fills a file whose name carries none, and never overrides one.
func TestResolveFileEpisodeNeverBorrowsTheReleaseTitle(t *testing.T) {
	evidence := releaseEvidenceFor(t, "[Grp] Show - 03 [1080p]")
	if evidence.Identity.State != episodeidentity.StateResolved {
		t.Fatalf("the release itself must resolve: %+v", evidence.Identity)
	}
	identity, facts, err := ResolveFileEpisode(context.Background(), evidence, "special.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if facts != nil || identity.State == episodeidentity.StateResolved || identity.CoverageKey != nil {
		t.Fatalf("a filename without an episode must stay unknown, got %+v / %+v", identity, facts)
	}
	_, facts, err = ResolveFileEpisode(context.Background(), evidence, "[Grp] Show - 05 [1080p].mkv")
	if err != nil || facts == nil || facts.Display != "5" {
		t.Fatalf("the filename episode must win over the title: %+v %v", facts, err)
	}
}

// The season is never parsed from a filename: coverage uses the bound season.
func TestResolveFileEpisodeUsesTheBoundSeason(t *testing.T) {
	evidence := releaseEvidenceFor(t, "[Grp] Show - 03 [1080p]")
	identity, _, err := ResolveFileEpisode(context.Background(), evidence, "Show S02E05.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:s1:e5" {
		t.Fatalf("coverage = %v, want the bound season tv:s1:e5", identity.CoverageKey)
	}
}

// A filename of 2..4 consecutive episodes is one multi-episode file: its
// naming facts carry the first and the last episode and it occupies every
// member's coverage key. A wider range is an unknown file.
func TestResolveFileEpisodeMultiEpisodeFilename(t *testing.T) {
	evidence := releaseEvidenceFor(t, "[Grp] Show - 03 [1080p]")
	for _, name := range []string{"Show S01E01E02.mkv", "Show [01][02].mkv", "Show - 01-02 [1080p].mkv"} {
		identity, facts, err := ResolveFileEpisode(context.Background(), evidence, name)
		if err != nil || facts == nil || facts.Number != "1" || facts.NumberEnd != "2" {
			t.Fatalf("%s: facts=%+v err=%v, want episodes 1-2", name, facts, err)
		}
		if keys := identity.Keys(); len(keys) != 2 || keys[0] != "tv:s1:e1" || keys[1] != "tv:s1:e2" {
			t.Fatalf("%s: keys=%v, want tv:s1:e1,tv:s1:e2", name, keys)
		}
	}
	for _, name := range []string{"Show 第01-12话.mkv", "Show [01][03].mkv"} {
		identity, facts, err := ResolveFileEpisode(context.Background(), evidence, name)
		if err != nil || facts != nil || len(identity.Keys()) != 0 {
			t.Fatalf("%s: facts=%+v keys=%v err=%v, want no episode", name, facts, identity.Keys(), err)
		}
	}
}

// The pre-download plan plans first-level videos with an episode in their own
// filename only. A video without one is an unknown file with no target (not
// rendered with the title's [E07]), and a nested file, another kind and a
// subtitle get none either.
func TestPreflightPlansOnlyFirstLevelVideosWithTheirOwnEpisode(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	size := int64(1024)
	file := func(path, kind string) acquisitiondomain.ReleaseFile {
		return acquisitiondomain.ReleaseFile{SourcePath: path, Filename: path[strings.LastIndex(path, "/")+1:], SizeBytes: &size, MediaKind: kind}
	}
	scenario.item.ReleaseFiles = []acquisitiondomain.ReleaseFile{
		file("Show/Preview Show [E07].mkv", "video"),
		file("Show/special.mkv", "video"),
		file("Show/Preview Show [E07].chs.ass", "subtitle"),
		file("Show/cover.jpg", "other"),
		file("Show/NCOP/ncop [E01].mkv", "video"),
	}
	result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if len(result.Set.Targets) != 1 {
		t.Fatalf("planned %d targets, want exactly the one episode video: %+v", len(result.Set.Targets), result.Set.Targets)
	}
	for _, planned := range result.ReleaseFiles {
		hasTarget := planned.TargetPath != nil
		if want := planned.SourcePath == "Show/Preview Show [E07].mkv"; hasTarget != want {
			t.Errorf("%s has target = %v, want %v", planned.SourcePath, hasTarget, want)
		}
	}
	if !strings.Contains(result.Set.Targets[0].TargetPath, "E07") {
		t.Fatalf("target = %q", result.Set.Targets[0].TargetPath)
	}
}
