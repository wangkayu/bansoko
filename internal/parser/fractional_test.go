package parser

import (
	"context"
	"testing"

	"bansoko/internal/episodeidentity"
)

// A fractional episode is captured as-is by the parser and refused once by
// the resolver: single, range and list, from a file name or a release title.
func TestFractionalEpisodeHasNoIdentity(t *testing.T) {
	cases := []struct {
		kind    InputKind
		text    string
		shape   ParseShape
		capture string
	}{
		{InputFileName, "[Group] Show - 12.5 [1080p].mkv", ShapeSingle, "12.5"},
		{InputFileName, "[Group] Show - 12-12.5 [1080p].mkv", ShapeRange, "12-12.5"},
		{InputFileName, "[Group] Show [12][12.5][1080p].mkv", ShapeList, "[12][12.5]"},
		{InputReleaseTitle, "[Group] Show - 12.5 [1080p][CHS]", ShapeSingle, "12.5"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			parsed, err := ParseEpisode(context.Background(), DefaultRegistry(), ParserSelection{ID: ParserRegex}, EpisodeParseInput{Kind: tc.kind, Text: tc.text})
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Shape != tc.shape || parsed.RawCapture != tc.capture {
				t.Fatalf("shape=%s capture=%q, want %s %q", parsed.Shape, parsed.RawCapture, tc.shape, tc.capture)
			}
			identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv", FileEvidence: Evidence(parsed, tc.kind), Offset: 1})
			if identity.State != episodeidentity.StateUnresolved || identity.FailureCode == nil || *identity.FailureCode != episodeidentity.FailEpisodeFractional {
				t.Fatalf("identity=%+v, want unresolved episode_fractional", identity)
			}
			if identity.CoverageKey != nil {
				t.Fatalf("coverage=%v, want none", *identity.CoverageKey)
			}
		})
	}
	// A decimal elsewhere in the name is not the episode's.
	parsed, err := ParseEpisode(context.Background(), DefaultRegistry(), ParserSelection{ID: ParserRegex}, EpisodeParseInput{Kind: InputFileName, Text: "[Group] Show [01-12][AAC2.0].mkv"})
	if err != nil {
		t.Fatal(err)
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv", FileEvidence: Evidence(parsed, InputFileName)})
	if identity.FailureCode == nil || *identity.FailureCode != episodeidentity.FailEpisodeShapeUnsupported {
		t.Fatalf("integer range with a decimal tag: failure=%v, want episode_shape_unsupported", identity.FailureCode)
	}
}
