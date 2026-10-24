package mediafactsfixture

import (
	"context"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
)

// The fixture file names used by PostgreSQL tests resolve episode 01 through
// the same release evidence Freeze builds.
func TestFixtureVideoNamesResolve(t *testing.T) {
	for title, name := range map[string]string{
		"[Test Group] Fault Anime - 01": "Fault Anime - 01.mkv",
		"Vars Show - 01":                "Vars Show - 01.mkv",
	} {
		evidence, err := acquisitiontarget.ResolveParserEvidence(context.Background(), acquisitiontarget.ParserSelectionInput{
			Kind: string(parser.ParserRegex), EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns,
			MediaKind: "tv", Season: 1,
		}, title)
		if err != nil {
			t.Fatal(err)
		}
		identity, _, err := acquisitiontarget.ResolveFileEpisode(context.Background(), evidence, name)
		if err != nil || identity.State != episodeidentity.StateResolved || identity.Episode == nil || string(*identity.Episode) != "1" {
			t.Fatalf("%q under %q = %+v %v", name, title, identity, err)
		}
	}
}
