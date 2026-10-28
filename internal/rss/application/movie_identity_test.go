package application

import (
	"context"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
)

// A movie metadata binding must resolve the canonical movie:0 identity
// regardless of any digits or years in the title: the typed parse stays
// evidence only and can never misjudge the release as a TV episode.
func TestResolveReleaseParserEvidenceMovieBindingIsCanonicalMovieIdentity(t *testing.T) {
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	snapshot.mediaKind = "movie"
	snapshot.mediaSeason = 0

	for _, title := range []string{
		"[RealGroup] Some Movie [1080p]",
		"[RealGroup] Some Movie 2024 [1080p]",
		"[RealGroup] Some Movie - 07 [1080p]",
	} {
		evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, title)
		if err != nil {
			t.Fatalf("resolve %q: %v", title, err)
		}
		identity := evidence.Identity
		if identity.State != episodeidentity.StateResolved || identity.Kind != episodeidentity.KindMovie {
			t.Fatalf("%q: state=%s kind=%s, want resolved/movie", title, identity.State, identity.Kind)
		}
		if identity.Episode == nil || string(*identity.Episode) != "0" || identity.Offset != 0 {
			t.Fatalf("%q: movie identity must be 0/0/0, got episode=%v offset=%d", title, identity.Episode, identity.Offset)
		}
		if identity.CoverageKey == nil || *identity.CoverageKey != "movie:0" {
			t.Fatalf("%q: coverage=%v, want movie:0", title, identity.CoverageKey)
		}
		if identity.SelectedSource == nil || *identity.SelectedSource != episodeidentity.EpisodeSourceMovie {
			t.Fatalf("%q: selected source=%v, want movie", title, identity.SelectedSource)
		}
		provenance := evidence.Provenance()
		if provenance.MediaKind != "movie" {
			t.Fatalf("%q: provenance media_kind=%q, want movie", title, provenance.MediaKind)
		}
	}
}

// An absent metadata binding keeps the historical TV behavior: the identity
// resolves from the typed parse with the TV coverage key, so existing TV
// instances are untouched by the movie binding contract.
func TestResolveReleaseParserEvidenceDefaultBindingStaysTV(t *testing.T) {
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	if snapshot.mediaKind != "" && snapshot.mediaKind != "tv" {
		t.Fatalf("fixture media kind = %q, want unset/tv default", snapshot.mediaKind)
	}

	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	identity := evidence.Identity
	if identity.State != episodeidentity.StateResolved || identity.Kind != episodeidentity.KindTVEpisode {
		t.Fatalf("state=%s kind=%s, want resolved/tv_episode", identity.State, identity.Kind)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:e7" {
		t.Fatalf("coverage=%v, want tv:e7", identity.CoverageKey)
	}
	if provenance := evidence.Provenance(); provenance.MediaKind != "tv" {
		t.Fatalf("provenance media_kind=%q, want the tv default", provenance.MediaKind)
	}
}

// A bound TV season is not part of the release coverage key (an identity is
// the instance plus the episode number); it travels into the frozen provenance
// evidence for naming only.
func TestResolveReleaseParserEvidenceBoundSeasonIsNamingOnly(t *testing.T) {
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	snapshot.mediaKind = "tv"
	snapshot.mediaSeason = 2

	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Identity.CoverageKey == nil || *evidence.Identity.CoverageKey != "tv:e7" {
		t.Fatalf("coverage=%v, want tv:e7 whatever the bound season", evidence.Identity.CoverageKey)
	}
	if provenance := evidence.Provenance(); provenance.Season != 2 {
		t.Fatalf("provenance season=%d, want 2", provenance.Season)
	}
}

// The owner media-kind pin selects the same canonical movie:0 identity a movie
// binding does, even when the TMDB tuple is a TV binding: the release identity
// is frozen through FrozenMediaBinding, so the override — not the tuple — picks
// the branch. This is the release-side half of the three-state matrix; the
// domain test pins the projection itself.
func TestResolveReleaseParserEvidenceMediaKindOverridePinsMovieIdentity(t *testing.T) {
	override := "movie"
	// A TV tuple with a bound season: with no override this resolves a TV
	// episode, so the movie:0 result below can only come from the pin.
	tvBinding := &animedomain.TMDBBinding{WorkId: "260972", MediaKind: animedomain.TMDBMediaKindTv, Season: 2}
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	snapshot.mediaKind, snapshot.mediaSeason, snapshot.mediaSeasonBound = animedomain.FrozenMediaBinding(tvBinding, &override)

	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Some Show - 07 [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Identity.Kind != episodeidentity.KindMovie || evidence.Identity.CoverageKey == nil || *evidence.Identity.CoverageKey != "movie:0" {
		t.Fatalf("kind=%s coverage=%v, want the canonical movie:0 identity", evidence.Identity.Kind, evidence.Identity.CoverageKey)
	}
	if provenance := evidence.Provenance(); provenance.MediaKind != "movie" || provenance.Season != 2 {
		t.Fatalf("provenance media_kind=%q season=%d, want movie/2 (the bound season stays as evidence)", provenance.MediaKind, provenance.Season)
	}
}

// Clearing the pin (or never setting one) falls back to the bound TMDB kind,
// so a movie override that is removed returns to the ordinary TV path without
// touching the TMDB tuple.
func TestResolveReleaseParserEvidenceClearedMediaKindOverrideFallsBackToBinding(t *testing.T) {
	tvBinding := &animedomain.TMDBBinding{WorkId: "260972", MediaKind: animedomain.TMDBMediaKindTv, Season: 2}
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	snapshot.mediaKind, snapshot.mediaSeason, snapshot.mediaSeasonBound = animedomain.FrozenMediaBinding(tvBinding, nil)

	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Identity.Kind != episodeidentity.KindTVEpisode || evidence.Identity.CoverageKey == nil || *evidence.Identity.CoverageKey != "tv:e7" {
		t.Fatalf("kind=%s coverage=%v, want tv:e7", evidence.Identity.Kind, evidence.Identity.CoverageKey)
	}
	if provenance := evidence.Provenance(); provenance.MediaKind != "tv" {
		t.Fatalf("provenance media_kind=%q, want the bound tv kind", provenance.MediaKind)
	}
}

// The pre-download target render must select the movie naming branch from the
// frozen movie identity, and the frozen target config snapshot must carry the
// media kind evidence — digits in the filename must not re-open TV parsing.
func TestTargetPlanningMovieBindingUsesMovieNamingBranch(t *testing.T) {
	initTestNamingVars(t)
	service := &Service{
		targetAcquisitionConfigured: true,
		targetProbe:                 missingTargetProbe{},
	}
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	snapshot.mediaKind = "movie"
	service.db = openRSSDatabase(t)
	seedRSSOwner(t, service.db, false, true)
	snapshot.managedAnimeID = "anime-rss-test"
	snapshot.effective.Rules.Naming = movieBranchNamingRules(t)
	item := acquisitiondomain.FeedItem{
		GUID:  "guid-movie-target",
		Title: "[RealGroup] Some Movie 2024 [1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{
				SourcePath: "Some Movie 2024 [1080p].mkv",
				Filename:   "Some Movie 2024 [1080p].mkv",
				SizeBytes:  sizePointer(2048),
			},
		},
	}
	// Mirror the production evaluation: the verdict path derives the preview
	// result from the frozen identity, and that result carries the movie
	// naming-branch selector into target planning.
	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, item.Title)
	if err != nil {
		t.Fatal(err)
	}
	rules, _, err := attrrules.Current()
	if err != nil {
		t.Fatal(err)
	}
	release := animedomain.EvaluateFeedItemWithIdentity(item, animedomain.SourceEvaluationInput{
		Index: 0, GroupName: snapshot.groupName,
	}, snapshot.effective, animedomain.MetadataHold{}, rules, evidence.Identity)
	if release.Verdict != animedomain.ReleaseVerdict("accepted_pending_download") {
		t.Fatalf("verdict = %q, want accepted_pending_download", release.Verdict)
	}
	if release.FrozenMediaKind != "movie" {
		t.Fatalf("frozen media kind = %q, want movie", release.FrozenMediaKind)
	}

	acquisition := service.preflightTargetAcquisition(context.Background(), snapshot, item, release, &evidence)
	if acquisition == nil {
		t.Fatal("preflight returned no acquisition")
	}
	if acquisition.admission.Admission != acquisitiontarget.AdmissionDownload {
		t.Fatalf("admission = %+v, want download-required", acquisition.admission)
	}
	if len(acquisition.set.Targets) != 1 {
		t.Fatalf("target set = %+v, want exactly one target", acquisition.set)
	}
	rendered := acquisition.set.Targets[0].TargetPath
	if !strings.Contains(rendered, "/movie") {
		t.Fatalf("target %q did not use the movie naming branch", rendered)
	}
	if strings.Contains(rendered, "/tv") {
		t.Fatalf("target %q used the TV naming branch for a movie binding", rendered)
	}

	provenance := decodeTargetParserProvenance(t, acquisition.set.Targets[0].ConfigSnapshot)
	if provenance.MediaKind != "movie" {
		t.Fatalf("frozen target parser media_kind=%q, want movie", provenance.MediaKind)
	}
}

// movieBranchNamingRules builds naming rules whose TV and movie branches are
// distinguishable, so a test can prove which branch the renderer selected.
func movieBranchNamingRules(t *testing.T) animedomain.NamingRules {
	rules := defaultNamingRules(t)
	rules.Tv.DirectoryTemplate = "/tv"
	rules.Tv.VideoFilenameTemplate = "tv.mkv"
	rules.Movie.DirectoryTemplate = "/movie"
	rules.Movie.VideoFilenameTemplate = "movie.mkv"
	return rules
}

// The media fact snapshot input digest must move when the frozen metadata
// binding changes, so the next evaluation freezes a fresh snapshot instead of
// silently keeping the old identity inputs.
func TestMediaFactsInputDigestMovesWithMediaBinding(t *testing.T) {
	snapshot := targetPlanningSnapshot(t, string(parser.ParserRegex), `\[E(?P<episode>\d{1,4})\]`, 0)
	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	const title = "[RealGroup] Preview Show [E07] [1080p]"
	digestOf := func() string {
		t.Helper()
		resolved, err := resolveReleaseParserEvidence(context.Background(), snapshot, title)
		if err != nil {
			t.Fatal(err)
		}
		return acquisitiontarget.ReleaseMediaInputDigest(releaseMediaSnapshotInput(snapshot, title, resolved, nil))
	}
	baseDigest := acquisitiontarget.ReleaseMediaInputDigest(releaseMediaSnapshotInput(snapshot, title, evidence, nil))

	snapshot.mediaSeason = 3
	if digestOf() == baseDigest {
		t.Fatal("input digest did not change with the bound season edit")
	}
	snapshot.mediaSeason = 0
	snapshot.mediaKind = "movie"
	if digestOf() == baseDigest {
		t.Fatal("input digest did not change with the media kind edit")
	}
}
