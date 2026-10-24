package application

import (
	"context"
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/parser"
)

// fixedShapeParserAdapter is a non-default registered parser that always returns
// one configured typed draft. It lets the refresh path be driven with the exact
// parser outcome under test instead of relying on a regex matching a title.
type fixedShapeParserAdapter struct {
	kind  string
	draft parser.EpisodeEvidenceDraft
}

func (a *fixedShapeParserAdapter) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{
		ID:                    a.kind,
		ImplementationVersion: "shape-parser-v1",
		OutputContractVersion: "episode-evidence-v1",
		DisplayName:           "Shape parser",
		Capabilities:          []string{"release_title", "file_name"},
		ConfigVersion:         "shape-parser-config-v1",
	}
}

func (a *fixedShapeParserAdapter) ValidateConfig(json.RawMessage) error { return nil }

func (a *fixedShapeParserAdapter) ParseDraft(_ context.Context, input parser.EpisodeParseInput, _ json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	if input.Kind == parser.InputFileName {
		// File-name inputs of these feeds carry no separate evidence; the
		// release-directory contracts under test are release-title driven.
		return parser.EpisodeEvidenceDraft{Status: parser.StatusMissing, Shape: parser.ShapeUnknown}, nil
	}
	return a.draft, nil
}

func registerShapeParser(t *testing.T, kind string, draft parser.EpisodeEvidenceDraft) {
	t.Helper()
	if parser.IsRegistered(parser.ParserKind(kind)) {
		t.Fatalf("test parser kind %q is already registered", kind)
	}
	if err := parser.DefaultRegistry().Register(&fixedShapeParserAdapter{kind: kind, draft: draft}); err != nil {
		t.Fatalf("register shape parser %q: %v", kind, err)
	}
}

// sourceShapeSnapshot is the minimal RSS source snapshot the refresh evaluation
// needs. linkOffset is the RSS link's own episode_offset.
func sourceShapeSnapshot(kind string, linkOffset int) sourceSnapshot {
	parserRules := animedomain.ParserRules{}
	return sourceSnapshot{
		sourceID:       "source-shape",
		subscriptionID: "subscription-shape",
		managedAnimeID: "anime-shape",
		groupName:      "RealGroup",
		parserKind:     kind,
		parserVersion:  3,
		episodeOffset:  linkOffset,
		profileVersion: 5,
		effective: animedomain.EffectiveConfig{
			Rules: animedomain.EffectiveRules{Parser: parserRules},
		},
	}
}

// The refresh/discovery path treats the selected parser's title shape as
// evidence only, exactly like the owner writer: a range/list/season-only/
// special/fractional/missing result goes on to the batch plan and never becomes
// the first captured number.
func TestBuildFeedEvaluationsShapeMatrix(t *testing.T) {
	tests := []struct {
		name        string
		kind        string
		draft       parser.EpisodeEvidenceDraft
		wantVerdict animedomain.ReleaseVerdict
		wantReason  string
		wantEpisode *float64
	}{
		{
			name: "batch range is title evidence only",
			kind: "test-shape-range",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusUnsupported, Shape: parser.ShapeRange, RawCapture: "[Group] Show - 09~16 [1080p]",
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
		},
		{
			name: "list is title evidence only",
			kind: "test-shape-list",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusUnsupported, Shape: parser.ShapeList, RawCapture: "01,02,03",
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
		},
		{
			name: "season only is title evidence only",
			kind: "test-shape-season",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusUnsupported, Shape: parser.ShapeSeasonOnly, RawCapture: "S02",
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
		},
		{
			name: "special is title evidence only",
			kind: "test-shape-special",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusUnsupported, Shape: parser.ShapeSpecial, RawCapture: "NCOP",
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
		},
		{
			name: "missing evidence is title evidence only",
			kind: "test-shape-missing",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusMissing, Shape: parser.ShapeUnknown,
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
		},
		{
			name: "fractional episode is title evidence only",
			kind: "test-shape-decimal",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusValue, Shape: parser.ShapeSingle, RawCapture: "7.5", Episode: "7.5",
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
		},
		{
			name: "integer resolves to the scalar episode",
			kind: "test-shape-integer",
			draft: parser.EpisodeEvidenceDraft{
				Status: parser.StatusValue, Shape: parser.ShapeSingle, RawCapture: "12", Episode: "12",
			},
			wantVerdict: animedomain.ReleaseVerdict("accepted_pending_download"),
			wantReason:  "accepted",
			wantEpisode: float64Pointer(12),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registerShapeParser(t, test.kind, test.draft)
			// The title would be parsed as episode 9 by the bundled patterns;
			// the selected parser's typed answer must win instead.
			items := []acquisitiondomain.FeedItem{{GUID: "guid-" + test.kind, Title: "[Group] Show - 09~16 [1080p]"}}
			feed, err := buildFeedEvaluations(context.Background(), items, sourceShapeSnapshot(test.kind, 0))
			if err != nil {
				t.Fatalf("build feed evaluations: %v", err)
			}
			if len(feed) != 1 {
				t.Fatalf("evaluations = %d, want 1", len(feed))
			}
			result := feed[0].result
			if result.Verdict != test.wantVerdict {
				t.Fatalf("verdict = %q, want %q", result.Verdict, test.wantVerdict)
			}
			if result.ReasonCode != test.wantReason {
				t.Fatalf("reason code = %q, want %q", result.ReasonCode, test.wantReason)
			}
			episode := parsedFactsEpisode(t, feed[0].parsedFacts)
			if test.wantEpisode == nil {
				if episode != nil {
					t.Fatalf("persisted parsed episode = %v, want absent", *episode)
				}
			} else if episode == nil || *episode != *test.wantEpisode {
				t.Fatalf("persisted parsed episode = %v, want %v", episode, *test.wantEpisode)
			}
		})
	}
}

// An unregistered/offline selected parser fails the refresh evaluation closed
// with a typed reason. It must never fall back to the bundled regex parser, even
// though the title is perfectly parseable by it.
func TestBuildFeedEvaluationsOfflineSelectedParserFailsClosed(t *testing.T) {
	const kind = "test-shape-offline-parser"
	if parser.IsRegistered(parser.ParserKind(kind)) {
		t.Fatalf("test parser kind %q must stay unregistered", kind)
	}
	items := []acquisitiondomain.FeedItem{{GUID: "guid-offline", Title: "[Group] Show - 12 [1080p]"}}
	feed, err := buildFeedEvaluations(context.Background(), items, sourceShapeSnapshot(kind, 0))
	if err != nil {
		t.Fatalf("build feed evaluations: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("evaluations = %d, want 1", len(feed))
	}
	result := feed[0].result
	if result.Verdict != animedomain.ReleaseVerdict("parse_failed") {
		t.Fatalf("verdict = %q, want parse_failed (no fallback to the bundled parser)", result.Verdict)
	}
	if result.ReasonCode != "parser_selection_unavailable" {
		t.Fatalf("reason code = %q, want parser_selection_unavailable", result.ReasonCode)
	}
	if episode := parsedFactsEpisode(t, feed[0].parsedFacts); episode != nil {
		t.Fatalf("persisted parsed episode = %v, want absent for an offline parser", *episode)
	}
}

// The RSS link's episode_offset is the runtime offset authority.
func TestBuildFeedEvaluationsUsesLinkEpisodeOffset(t *testing.T) {
	const kind = "test-shape-offset-parser"
	registerShapeParser(t, kind, parser.EpisodeEvidenceDraft{
		Status: parser.StatusValue, Shape: parser.ShapeSingle, RawCapture: "7", Episode: "7",
	})
	items := []acquisitiondomain.FeedItem{{GUID: "guid-offset", Title: "[Group] Show - 07 [1080p]"}}
	feed, err := buildFeedEvaluations(context.Background(), items, sourceShapeSnapshot(kind, 3))
	if err != nil {
		t.Fatalf("build feed evaluations: %v", err)
	}
	if episode := parsedFactsEpisode(t, feed[0].parsedFacts); episode == nil || *episode != 10 {
		t.Fatalf("persisted parsed episode = %v, want 10 (link offset 3)", episode)
	}
}

func parsedFactsEpisode(t *testing.T, raw json.RawMessage) *float64 {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var facts struct {
		Episode *float64 `json:"episode"`
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("decode parsed facts %s: %v", raw, err)
	}
	return facts.Episode
}

func float64Pointer(value float64) *float64 { return &value }
