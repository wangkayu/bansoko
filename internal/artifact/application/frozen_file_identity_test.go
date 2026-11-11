package application

import (
	"testing"

	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
)

func TestFrozenPlannedFileIdentityIsTheFinalAdmissionDecision(t *testing.T) {
	manualNumber := episodeidentity.EpisodeNumber("7")
	manualKey := "tv:e7"
	automaticRaw := episodeidentity.EpisodeNumber("2")
	automaticNumber := episodeidentity.EpisodeNumber("12")
	automaticKey := "tv:e12"
	plan := &batchplan.Plan{Files: []batchplan.File{
		{SourcePath: "Pack/Show 01.mkv", Outcome: batchplan.OutcomePlanned, Identity: &episodeidentity.EpisodeIdentity{State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode, RawEpisode: &manualNumber, Episode: &manualNumber, CoverageKey: &manualKey}},
		{SourcePath: "Pack/Show 02.mkv", Outcome: batchplan.OutcomePlanned, Identity: &episodeidentity.EpisodeIdentity{State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode, RawEpisode: &automaticRaw, Episode: &automaticNumber, Offset: 10, CoverageKey: &automaticKey}},
	}}
	manifest := map[string]struct{}{"Pack/Show 01.mkv": {}, "Pack/Show 02.mkv": {}}

	for _, tc := range []struct {
		path    string
		episode string
		offset  int
	}{
		{path: "Pack/Show 01.mkv", episode: "7"},
		{path: "Show 02.mkv", episode: "12", offset: 10},
	} {
		identity, ok := frozenPlannedFileIdentity(plan, manifest, tc.path)
		if !ok || identity.Episode == nil || string(*identity.Episode) != tc.episode || identity.Offset != tc.offset {
			t.Fatalf("planned identity for %q = %+v, %t; want final episode %s with offset %d", tc.path, identity, ok, tc.episode, tc.offset)
		}
	}
	if _, ok := frozenPlannedFileIdentity(plan, manifest, "Pack/Missing.mkv"); ok {
		t.Fatal("a file outside the frozen manifest received an identity")
	}
}
