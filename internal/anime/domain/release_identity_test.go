package domain

import (
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/episodeidentity"
)

// The release title identity is evidence only: no title shape fails the
// release, because acceptance belongs to the batch plan over the files' own
// identities. A range/list/season-only/special/invalid/missing identity must
// still never leak the first captured number into parsed_facts.episode.
func TestEvaluateFeedItemWithIdentityShapeMatrix(t *testing.T) {
	rangeTitle := "[Group] Show - 09~16 [1080p]"
	tests := []struct {
		name         string
		title        string
		evidence     episodeidentity.EpisodeEvidence
		wantVerdict  ReleaseVerdict
		wantReason   string
		wantEpisode  *float32
		wantState    string
		wantShape    string
		wantResolved bool
	}{
		{
			name:  "integer single resolves to a scalar episode",
			title: "[Group] Show - 12 [1080p]",
			evidence: episodeidentity.NewValueEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", "12"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: float32Pointer(12), wantState: episodeidentity.StateResolved,
			wantShape: episodeidentity.ShapeSingle, wantResolved: true,
		},
		{
			name:  "fractional single has no release identity",
			title: "[Group] Show - 07.5 [1080p]",
			evidence: episodeidentity.NewValueEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", "07.5"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnresolved,
			wantShape: episodeidentity.ShapeSingle,
		},
		{
			name:  "batch range is never the first captured number",
			title: rangeTitle,
			evidence: episodeidentity.NewUnsupportedEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", episodeidentity.ShapeRange, rangeTitle, "range"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnsupported, wantShape: episodeidentity.ShapeRange,
		},
		{
			name:  "list shape is typed unsupported",
			title: "[Group] Show - 01,02,03 [1080p]",
			evidence: episodeidentity.NewUnsupportedEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", episodeidentity.ShapeList, "01,02,03", "list"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnsupported, wantShape: episodeidentity.ShapeList,
		},
		{
			name:  "season-only shape is typed unsupported",
			title: "[Group] Show S02 [1080p]",
			evidence: episodeidentity.NewUnsupportedEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", episodeidentity.ShapeSeasonOnly, "S02", "season only"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnsupported, wantShape: episodeidentity.ShapeSeasonOnly,
		},
		{
			name:  "special shape has its own typed failure",
			title: "[Group] Show NCOP [1080p]",
			evidence: episodeidentity.NewUnsupportedEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", episodeidentity.ShapeSpecial, "NCOP", "special"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnsupported, wantShape: episodeidentity.ShapeSpecial,
		},
		{
			name:  "unknown shape is typed unsupported",
			title: "[Group] Show [1080p]",
			evidence: episodeidentity.NewUnsupportedEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title", episodeidentity.ShapeUnknown, "", "unknown"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnsupported, wantShape: episodeidentity.ShapeUnknown,
		},
		{
			name:  "invalid evidence is typed invalid",
			title: "[Group] Show - 999999 [1080p]",
			evidence: episodeidentity.EpisodeEvidence{
				Source: episodeidentity.EpisodeSourceReleaseParser, InputKind: "release_title",
				Status: episodeidentity.StatusInvalid, Shape: episodeidentity.ShapeSingle,
				RawCapture: "999999", Reason: "out of bounds",
			},
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateInvalid, wantShape: episodeidentity.ShapeSingle,
		},
		{
			name:  "missing evidence is typed unresolved",
			title: "[Group] Show [1080p]",
			evidence: episodeidentity.NewMissingEvidence(
				episodeidentity.EpisodeSourceReleaseParser, "release_title"),
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateUnresolved, wantShape: episodeidentity.ShapeUnknown,
		},
		{
			name:  "conflicting evidence is typed ambiguous",
			title: "[Group] Show - 12 [1080p]",
			evidence: episodeidentity.EpisodeEvidence{
				Source: episodeidentity.EpisodeSourceReleaseParser, InputKind: "release_title",
				Status: episodeidentity.StatusConflict, Shape: episodeidentity.ShapeSingle,
				RawCapture: "12",
			},
			wantVerdict: ReleaseVerdict("accepted_pending_download"), wantReason: "accepted",
			wantEpisode: nil, wantState: episodeidentity.StateConflict, wantShape: episodeidentity.ShapeSingle,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
				MediaKind:    "tv",
				FileEvidence: test.evidence,
			})
			if identity.State != test.wantState {
				t.Fatalf("identity state = %q, want %q", identity.State, test.wantState)
			}
			if identity.Shape != test.wantShape {
				t.Fatalf("identity shape = %q, want %q", identity.Shape, test.wantShape)
			}

			result := EvaluateFeedItemWithIdentity(
				acquisitiondomain.FeedItem{Title: test.title},
				SourceEvaluationInput{},
				EffectiveConfig{},
				MetadataHold{},
				testRules(),
				identity,
			)
			if result.Verdict != test.wantVerdict {
				t.Fatalf("verdict = %q, want %q", result.Verdict, test.wantVerdict)
			}
			if result.ReasonCode != test.wantReason {
				t.Fatalf("reason code = %q, want %q", result.ReasonCode, test.wantReason)
			}
			if result.ParsedFacts == nil {
				t.Fatal("parsed facts must always be present")
			}
			assertEpisodeScalar(t, result.ParsedFacts.Episode, test.wantEpisode)
			if result.ParsedFacts.Special == nil {
				t.Fatal("the release-facts special flag must always be persisted")
			}
		})
	}
}

// A range title is accepted for planning but never becomes a single episode:
// neither the display episode nor the release-level naming episode is filled.
func TestEvaluateFeedItemWithIdentityRangeTitleIsEvidenceOnly(t *testing.T) {
	rangeTitle := "[Group] Show - 09~16 [1080p]"
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind: "tv",
		FileEvidence: episodeidentity.NewUnsupportedEvidence(
			episodeidentity.EpisodeSourceReleaseParser, "release_title", episodeidentity.ShapeRange, rangeTitle, "range"),
	})
	result := EvaluateFeedItemWithIdentity(
		acquisitiondomain.FeedItem{Title: rangeTitle},
		SourceEvaluationInput{}, EffectiveConfig{}, MetadataHold{}, testRules(), identity,
	)
	if result.Verdict != ReleaseVerdict("accepted_pending_download") {
		t.Fatalf("verdict = %q, a batch title goes on to the batch plan", result.Verdict)
	}
	if result.ParsedFacts.Episode != nil || result.FrozenEpisode != nil {
		t.Fatalf("range release episode = %v / %+v, want none", result.ParsedFacts.Episode, result.FrozenEpisode)
	}
}

func assertEpisodeScalar(t *testing.T, got, want *float32) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("parsed episode = %v, want absent", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("parsed episode is absent, want %v", *want)
	}
	if *got != *want {
		t.Fatalf("parsed episode = %v, want %v", *got, *want)
	}
}

func float32Pointer(value float32) *float32 { return &value }
