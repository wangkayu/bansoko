package application

import (
	"testing"

	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/testutil/mediafactsfixture"
)

func episodeIdentity(episode string) episodeidentity.EpisodeIdentity {
	evidence := episodeidentity.NewMissingEvidence(episodeidentity.EpisodeSourceFileParser, "file_name")
	if episode != "" {
		evidence = episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episode)
	}
	return episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv", FileEvidence: evidence})
}

// The plan is keyed by manifest path; the local relative path may omit the
// torrent folder. A video whose row disagrees with its plan entry, or that
// the plan does not know, is drift.
func TestPlanDriftComparesRowsWithThePlan(t *testing.T) {
	plan := mediafactsfixture.Plan(t, batchplan.RouteSkip,
		mediafactsfixture.PlanVideo{Path: "Show/Show - 01.mkv", Identity: episodeIdentity("1")},
		mediafactsfixture.PlanVideo{Path: "Show/Show - 02.mkv", Identity: episodeIdentity("2")},
		mediafactsfixture.PlanVideo{Path: "Show/Show SP.mkv", Identity: episodeIdentity("")},
	)
	selection := newFileSelection(manifestInput("Show/Show - 01.mkv", "Show/Show - 02.mkv", "Show/Show SP.mkv", "Show/Show - 03.mkv"), filekinds.Version, nil)
	videos := []artifactFileContext{
		{fileRef: "a", relativePath: "Show - 01.mkv"},
		{fileRef: "b", relativePath: "Show/Show - 02.mkv"},
		{fileRef: "c", relativePath: "Show SP.mkv"},
		{fileRef: "d", relativePath: "Show - 03.mkv"},
	}
	coverage := map[string]string{"a": "tv:e1", "b": "tv:e5", "d": "tv:e3"}
	drift := planDrift(&plan, selection, videos, coverage, false)
	for ref, want := range map[string]bool{"a": false, "b": true, "c": false, "d": true} {
		if drift[ref] != want {
			t.Errorf("drift[%s] = %v, want %v", ref, drift[ref], want)
		}
	}
	if movie := planDrift(&plan, selection, videos, coverage, true); len(movie) != 0 {
		t.Fatalf("a movie is not checked per file: %v", movie)
	}
	if none := planDrift(nil, selection, videos, coverage, false); len(none) != 0 {
		t.Fatalf("no plan is reported as missing facts, not drift: %v", none)
	}
}

// A multi-episode file's row matches its plan entry only with every key.
func TestPlanDriftComparesMultiEpisodeKeySets(t *testing.T) {
	double := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv",
		FileEvidence: episodeidentity.NewUnsupportedEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episodeidentity.ShapeRange, "01-02", "")})
	plan := mediafactsfixture.Plan(t, batchplan.RouteSkip, mediafactsfixture.PlanVideo{Path: "Show - 01-02.mkv", Identity: double})
	selection := newFileSelection(manifestInput("Show - 01-02.mkv"), filekinds.Version, nil)
	videos := []artifactFileContext{{fileRef: "a", relativePath: "Show - 01-02.mkv"}}
	if drift := planDrift(&plan, selection, videos, map[string]string{"a": "tv:e1,tv:e2"}, false); drift["a"] {
		t.Fatal("matching key set is not drift")
	}
	if drift := planDrift(&plan, selection, videos, map[string]string{"a": "tv:e1"}, false); !drift["a"] {
		t.Fatal("a row with only the first key is drift")
	}
}

// Only covered and target-conflict outcomes exclude a video; a drifted video
// is never excluded (it fails visibly).
func TestPlanExclusionReadsThePlanOutcome(t *testing.T) {
	plan := mediafactsfixture.Plan(t, batchplan.RouteSkip,
		mediafactsfixture.PlanVideo{Path: "Show - 01.mkv", Identity: episodeIdentity("1")},
		mediafactsfixture.PlanVideo{Path: "Show - 02.mkv", Identity: episodeIdentity("2")},
		mediafactsfixture.PlanVideo{Path: "Show - 03.mkv", Identity: episodeIdentity("3")},
	)
	plan.ApplyDedup(map[string]batchplan.Verdict{"tv:e1": batchplan.VerdictExisting, "tv:e2": batchplan.VerdictInFlight})
	selection := newFileSelection(nil, filekinds.Version, nil)
	for path, want := range map[string]string{
		"Show - 01.mkv": batchplan.ReasonCoveredExisting,
		"Show - 02.mkv": batchplan.ReasonCoveredInFlight,
		"Show - 03.mkv": "",
	} {
		if got := planExclusion(&plan, selection, artifactFileContext{fileRef: path, relativePath: path}, nil); got != want {
			t.Errorf("%s excluded as %q, want %q", path, got, want)
		}
	}
	if got := planExclusion(&plan, selection, artifactFileContext{fileRef: "x", relativePath: "Show - 01.mkv"}, map[string]bool{"x": true}); got != "" {
		t.Fatalf("a drifted video was excluded as %q", got)
	}
}
