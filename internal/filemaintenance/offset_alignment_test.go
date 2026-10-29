package filemaintenance

import (
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/episodeidentity"
	facts "bansoko/internal/release/facts"
)

// frozenIdentityFacts is the effective_facts document of a file whose frozen
// identity is the single raw episode raw (kind "" = frozen before the kind).
func frozenIdentityFacts(t *testing.T, kind, raw string, keys ...string) []byte {
	t.Helper()
	episode := episodeidentity.EpisodeNumber(raw)
	source := episodeidentity.EpisodeSourceFileParser
	coverage := "tv:e" + raw
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: kind,
		RawEpisode: &episode, Episode: &episode, CoverageKey: &coverage, SelectedSource: &source,
		CoverageKeys: keys,
	}
	document, err := json.Marshal(facts.EffectiveFileFacts{Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestOffsetRekeyedKeyMovesOnlySingleEpisodeFiles(t *testing.T) {
	unresolved, _ := json.Marshal(facts.EffectiveFileFacts{Identity: episodeidentity.EpisodeIdentity{State: episodeidentity.StateUnresolved}})
	cases := []struct {
		name      string
		candidate offsetRekeyCandidate
		offset    int
		want      string
		ok        bool
	}{
		{"single episode", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", effectiveFacts: frozenIdentityFacts(t, episodeidentity.KindTVEpisode, "1")}, 1, "tv:e2", true},
		{"offset back to the raw episode", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", effectiveFacts: frozenIdentityFacts(t, episodeidentity.KindTVEpisode, "3")}, 0, "tv:e3", true},
		{"kind frozen before kinds reads the owner kind", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", effectiveFacts: frozenIdentityFacts(t, "", "1")}, 2, "tv:e3", true},
		{"several coverage keys", offsetRekeyCandidate{keyCount: 2, fileRef: "ep.mkv", effectiveFacts: frozenIdentityFacts(t, episodeidentity.KindTVEpisode, "1")}, 1, "", false},
		{"multi-episode identity", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", effectiveFacts: frozenIdentityFacts(t, episodeidentity.KindTVEpisode, "1", "tv:e1", "tv:e2")}, 1, "", false},
		{"frozen movie", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", effectiveFacts: frozenIdentityFacts(t, episodeidentity.KindMovie, "1")}, 1, "", false},
		{"owner movie for a kindless identity", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", ownerMediaKind: "movie", effectiveFacts: frozenIdentityFacts(t, "", "1")}, 1, "", false},
		{"unresolved identity", offsetRekeyCandidate{keyCount: 1, fileRef: "ep.mkv", effectiveFacts: unresolved}, 1, "", false},
		{"no media facts", offsetRekeyCandidate{keyCount: 1, effectiveFacts: []byte(`{}`)}, 1, "", false},
	}
	for _, tc := range cases {
		got, ok := offsetRekeyedKey(tc.candidate, tc.offset)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s: key=%q ok=%v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestOffsetAlignmentChangeIDIsOnePerLinkVersion(t *testing.T) {
	first := offsetAlignmentChangeID("src-1", 3)
	if !strings.HasPrefix(first, "offset:") || first != offsetAlignmentChangeID("src-1", 3) {
		t.Fatalf("change id %q must be stable with the offset: prefix", first)
	}
	if first == offsetAlignmentChangeID("src-1", 4) || first == offsetAlignmentChangeID("src-2", 3) {
		t.Fatal("another link version must get another change id")
	}
}

func TestRouteOutcomeRecordsWhatTheRoutingDid(t *testing.T) {
	cases := []struct {
		route alignmentRoute
		want  string
	}{
		{alignmentRoute{Action: routeProceed}, alignmentOutcomeMoved},
		{alignmentRoute{Action: routeWait, Reason: reasonTargetOccupantMoving}, alignmentOutcomeMoved},
		{alignmentRoute{Action: routeHold, Reason: ReasonReplacementPending}, alignmentOutcomeMoved},
		{alignmentRoute{Action: routeHold, Reason: ReasonReplacementPending, Settle: true}, alignmentOutcomeUnchanged},
		{alignmentRoute{Action: routeDrop, Reason: "placement_not_active"}, alignmentOutcomeDeleted},
		{alignmentRoute{Action: routeVersion}, alignmentOutcomeVersioned},
		{alignmentRoute{Action: routeOverwrite}, alignmentOutcomeOverwritten},
		{alignmentRoute{Action: routeSkip, Reason: "target_occupied"}, alignmentOutcomeSkipped},
	}
	for _, tc := range cases {
		if got := routeOutcome(tc.route); got != tc.want {
			t.Fatalf("route %+v outcome=%s, want %s", tc.route, got, tc.want)
		}
	}
}
