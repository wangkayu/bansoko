package parser

import (
	"context"
	"strings"
	"testing"

	"bansoko/internal/episodeidentity"
)

// A double-episode file name parses to a range or list the resolver turns
// into consecutive coverage keys; the same range in a release title
// stays unsupported.
func TestMultiEpisodeFileNameResolves(t *testing.T) {
	for text, want := range map[string]string{
		"Repl Double Show - 03-04.mkv":     "tv:e3,tv:e4",
		"[Group] Show - 01-02 [1080p].mkv": "tv:e1,tv:e2",
		"[Group] Show [01][02][1080p].mkv": "tv:e1,tv:e2",
		"[Group] Show - 01-05 [1080p].mkv": "",
		"[Group] Show [01][03][1080p].mkv": "",
	} {
		parsed, err := ParseEpisode(context.Background(), DefaultRegistry(), ParserSelection{ID: ParserRegex}, EpisodeParseInput{Kind: InputFileName, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv", FileEvidence: Evidence(parsed, InputFileName)})
		got := ""
		if identity.State == episodeidentity.StateResolved {
			got = strings.Join(identity.Keys(), ",")
		}
		if got != want {
			t.Fatalf("%s: keys %q (state %s, shape %s), want %q", text, got, identity.State, parsed.Shape, want)
		}
	}
	parsed, err := ParseEpisode(context.Background(), DefaultRegistry(), ParserSelection{ID: ParserRegex}, EpisodeParseInput{Kind: InputReleaseTitle, Text: "[RealGroup] Repl Double Show - 03-04 [1080p]"})
	if err != nil {
		t.Fatal(err)
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv", FileEvidence: Evidence(parsed, InputReleaseTitle)})
	if identity.State == episodeidentity.StateResolved {
		t.Fatalf("release title range resolved: %v", identity.Keys())
	}
}
