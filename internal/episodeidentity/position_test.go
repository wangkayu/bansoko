package episodeidentity

import "testing"

func TestPositionProjectsSingleSpanAndManualRanges(t *testing.T) {
	cases := []struct {
		name        string
		identity    EpisodeIdentity
		wantDisplay string
		wantSource  EpisodeEvidenceSource
	}{
		{
			name: "single",
			identity: NewResolver().Resolve(EpisodeIdentityInput{MediaKind: "tv", Offset: 2,
				FileEvidence: NewValueEvidence(EpisodeSourceFileParser, "file_name", "3")}),
			wantDisplay: "5", wantSource: EpisodeSourceFileParser,
		},
		{
			name: "combined span",
			identity: NewResolver().Resolve(EpisodeIdentityInput{MediaKind: "tv", Span: 2,
				FileEvidence: NewValueEvidence(EpisodeSourceFileParser, "file_name", "2")}),
			wantDisplay: "3-4", wantSource: EpisodeSourceFileParser,
		},
		{
			name: "manual range", identity: NewManualRangeIdentity("manual.mkv", 5, 16),
			wantDisplay: "5-16", wantSource: EpisodeSourceManual,
		},
	}
	for _, tc := range cases {
		position, ok := tc.identity.Position()
		if !ok || position.Display() != tc.wantDisplay || position.Source != string(tc.wantSource) {
			t.Fatalf("%s: position = %+v, ok = %v; want %s from %s", tc.name, position, ok, tc.wantDisplay, tc.wantSource)
		}
	}
	if _, ok := (EpisodeIdentity{State: StateUnresolved}).Position(); ok {
		t.Fatal("an unresolved identity must not project an episode position")
	}
}
