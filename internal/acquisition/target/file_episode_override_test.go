package target

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOneShotFileOverridesAreFinalAndFreezeSeason(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E02].mkv", "Preview Show [E01].zh.ass")
	scenario.item.Title = "[RealGroup] Preview Show [E01] [1080p]"
	request := scenario.oneShot()
	request.Offset = 10
	request.Evidence = nil
	zero := 0
	request.Overrides = []FileEpisodeOverride{{Path: "Pack/Preview Show [E01].mkv", Season: &zero, Episode: 7}}
	result := PreflightOneShot(context.Background(), request, FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload {
		t.Fatalf("admission = %+v", result.Admission)
	}
	overridden, _ := result.BatchPlan.File("Pack/Preview Show [E01].mkv")
	automatic, _ := result.BatchPlan.File("Pack/Preview Show [E02].mkv")
	if got := string(*overridden.Identity.Episode); got != "7" || overridden.Identity.Offset != 0 {
		t.Fatalf("manual identity = %+v, want final episode 7 without offset", overridden.Identity)
	}
	if got := string(*automatic.Identity.Episode); got != "12" || automatic.Identity.Offset != 10 {
		t.Fatalf("automatic identity episode = %s, offset = %d, want parser episode plus offset", got, automatic.Identity.Offset)
	}
	if overridden.Season == nil || *overridden.Season != 0 || automatic.Season != nil {
		t.Fatalf("seasons = manual %v, automatic %v; explicit zero must survive", overridden.Season, automatic.Season)
	}
	subtitle, _ := result.BatchPlan.File("Pack/Preview Show [E01].zh.ass")
	if subtitle.BoundVideo != overridden.SourcePath {
		t.Fatalf("subtitle binding = %+v", subtitle)
	}
}

func TestMovieFileOverrideUsesFinalIdentityAndSeason(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv")
	request := scenario.oneShot()
	request.MediaKind = "movie"
	request.Preview.FrozenMediaKind = "movie"
	request.Effective.Rules.Naming.Movie.DirectoryTemplate += "/${anime.season}"
	request.Effective.Rules.Naming.Movie.VideoFilenameTemplate = "${n} E${ep.number}.${file.ext}"
	request.Evidence = nil
	zero := 0
	request.Overrides = []FileEpisodeOverride{{Path: "Pack/Preview Show [E01].mkv", Season: &zero, Episode: 7}}
	result := PreflightOneShot(context.Background(), request, FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload || len(result.Set.Targets) != 1 {
		t.Fatalf("movie override admission = %+v, targets = %+v", result.Admission, result.Set.Targets)
	}
	target := result.Set.Targets[0]
	if !strings.Contains(target.TargetPath, "/0/") || !strings.Contains(target.TargetPath, "Preview Show E7.mkv") {
		t.Fatalf("movie override target = %q, want explicit season 0 and final episode 7", target.TargetPath)
	}
}

func TestFileOverrideCollapsesMultiEpisodeIdentity(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01-E03].mkv")
	request := scenario.oneShot()
	request.Evidence = nil
	request.Overrides = []FileEpisodeOverride{{Path: "Pack/Preview Show [E01-E03].mkv", Episode: 8}}
	result := PreflightOneShot(context.Background(), request, FilesystemProbe{})
	entry, _ := result.BatchPlan.File("Pack/Preview Show [E01-E03].mkv")
	if entry.Identity == nil || entry.Identity.EpisodeEnd != nil || len(entry.Identity.CoverageKeys) != 0 || len(entry.CoverageKeys) != 0 {
		t.Fatalf("manual identity retained multi-episode state: %+v", entry)
	}
	if got := entry.Keys(); len(got) != 1 || got[0] != "tv:e8" {
		t.Fatalf("coverage = %v, want only tv:e8", got)
	}
}

func TestFileOverrideCanonicalizationAndScope(t *testing.T) {
	zero := 0
	overrides, err := CanonicalFileEpisodeOverrides([]FileEpisodeOverride{
		{Path: "Pack/b.mkv", Episode: 2},
		{Path: `Pack\a.mkv`, Season: &zero, Episode: 1},
	})
	if err != nil || len(overrides) != 2 || overrides[0].Path != "Pack/a.mkv" || overrides[0].Season == nil || *overrides[0].Season != 0 {
		t.Fatalf("canonical overrides = %+v, %v", overrides, err)
	}
	for _, input := range [][]FileEpisodeOverride{
		{{Path: "Pack/a.mkv", Episode: 0}},
		{{Path: "Pack/a.mkv", Episode: 1}, {Path: `Pack\a.mkv`, Episode: 2}},
		{{Path: "Pack/a.mkv", Season: &zero, Episode: 1}, {Path: "Pack/b.mkv", Season: nil, Episode: -1}},
	} {
		if _, err := CanonicalFileEpisodeOverrides(input); !errors.Is(err, ErrFileEpisodeOverrideInvalid) {
			t.Fatalf("invalid overrides %+v returned %v", input, err)
		}
	}

	scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E01].zh.ass")
	request := scenario.oneShot()
	request.Evidence = nil
	request.Overrides = []FileEpisodeOverride{{Path: "Pack/Preview Show [E01].zh.ass", Episode: 3}}
	result := PreflightOneShot(context.Background(), request, FilesystemProbe{})
	if result.Admission.Admission != AdmissionFailed || result.BatchPlan.Version != "" {
		t.Fatalf("subtitle override was not refused before planning: %+v", result)
	}
}

func TestFileOverrideEpisodeRangeCoversConsecutiveEpisodes(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01-E02].mkv", "Preview Show [E01-E02].zh.ass")
	request := scenario.oneShot()
	request.Evidence = nil
	request.Effective.Rules.Naming.Tv.VideoFilenameTemplate = "${ep.s00e00}.${file.ext}"
	request.Overrides = []FileEpisodeOverride{{Path: "Pack/Preview Show [E01-E02].mkv", Episode: 1, EpisodeEnd: 2}}
	result := PreflightOneShot(context.Background(), request, FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload {
		t.Fatalf("range override admission = %+v", result.Admission)
	}
	entry, _ := result.BatchPlan.File("Pack/Preview Show [E01-E02].mkv")
	if entry.Identity == nil || entry.Identity.EpisodeEnd == nil || string(*entry.Identity.EpisodeEnd) != "2" {
		t.Fatalf("range identity = %+v, want episode end 2", entry.Identity)
	}
	if got := entry.Keys(); len(got) != 2 || got[0] != "tv:e1" || got[1] != "tv:e2" {
		t.Fatalf("range coverage = %v, want tv:e1 and tv:e2", got)
	}
	if len(result.Set.Targets) == 0 || !strings.Contains(result.Set.Targets[0].TargetPath, "S01E01-E02.mkv") {
		targets := make([]string, 0, len(result.Set.Targets))
		for _, target := range result.Set.Targets {
			targets = append(targets, target.TargetPath)
		}
		t.Fatalf("range targets = %v, want S01E01-E02", targets)
	}
	subtitle, _ := result.BatchPlan.File("Pack/Preview Show [E01-E02].zh.ass")
	if subtitle.BoundVideo != entry.SourcePath {
		t.Fatalf("subtitle binding = %+v, want it to follow the ranged video", subtitle)
	}
}

func TestFileOverrideEpisodeRangeCanonicalization(t *testing.T) {
	// An equal start and end is the single-episode form, with identical
	// canonical JSON and digest as omitting the end.
	ranged, err := CanonicalFileEpisodeOverrides([]FileEpisodeOverride{{Path: "Pack/a.mkv", Episode: 3, EpisodeEnd: 3}})
	if err != nil || len(ranged) != 1 || ranged[0].EpisodeEnd != 0 {
		t.Fatalf("equal-end range canonicalized to %+v, %v", ranged, err)
	}
	twelve, err := CanonicalFileEpisodeOverrides([]FileEpisodeOverride{{Path: "Pack/a.mkv", Episode: 1, EpisodeEnd: 12}})
	if err != nil || len(twelve) != 1 || twelve[0].EpisodeEnd != 12 {
		t.Fatalf("12-episode range refused: %+v, %v", twelve, err)
	}
	for _, input := range [][]FileEpisodeOverride{
		{{Path: "Pack/a.mkv", Episode: 2, EpisodeEnd: 1}},
		{{Path: "Pack/a.mkv", Episode: 1, EpisodeEnd: 13}},
		{{Path: "Pack/a.mkv", Episode: 0, EpisodeEnd: 2}},
	} {
		if _, err := CanonicalFileEpisodeOverrides(input); !errors.Is(err, ErrFileEpisodeOverrideInvalid) {
			t.Fatalf("invalid range %+v returned %v", input, err)
		}
	}
}

func TestReleaseMediaDigestIncludesCanonicalOverridesOnlyWhenPresent(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv")
	planned := PreflightOneShot(context.Background(), scenario.oneShot(), FilesystemProbe{})
	base := ReleaseMediaSnapshotInput{ReleaseID: "r", Title: scenario.item.Title, Evidence: planned.Evidence}
	without := ReleaseMediaInputDigest(base)
	empty := base
	empty.Overrides = []FileEpisodeOverride{}
	if got := ReleaseMediaInputDigest(empty); got != without {
		t.Fatalf("empty overrides changed digest: %s != %s", got, without)
	}
	one := base
	one.Overrides = []FileEpisodeOverride{{Path: "Pack/b.mkv", Episode: 2}, {Path: "Pack/a.mkv", Episode: 1}}
	two := base
	two.Overrides = []FileEpisodeOverride{{Path: "Pack/a.mkv", Episode: 1}, {Path: "Pack/b.mkv", Episode: 2}}
	if ReleaseMediaInputDigest(one) == without || ReleaseMediaInputDigest(one) != ReleaseMediaInputDigest(two) {
		t.Fatal("override digest is not canonical and input-sensitive")
	}
}
