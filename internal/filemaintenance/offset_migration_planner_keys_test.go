package filemaintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/release/facts"
)

// The offset planner re-identifies a frozen identity with the current
// resolver: the new key is the seasonless tv:e<N> even when the frozen
// identity is a v1 tv:s<S>:e<N>, and no metadata season is read.
func TestPlanOffsetMigrationCandidateKeysTheNewIdentityWithoutSeason(t *testing.T) {
	frozen := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    "tv",
		FileRef:      "primary",
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "3"),
	})
	legacyKey := "tv:s2:e3"
	frozen.CoverageKey = &legacyKey
	frozen.ResolverVersion = episodeidentity.LegacyResolverVersion
	effective, err := json.Marshal(facts.EffectiveFileFacts{Identity: frozen})
	if err != nil {
		t.Fatal(err)
	}
	candidate := offsetMigrationCandidate{
		placementID:       "plc-keys",
		physicalPath:      "/library/Show/Season 02/Show - S02E03.mkv",
		targetFingerprint: `{"algorithm":"sha256","value":"` + strings.Repeat("ab", 32) + `"}`,
		snapshotID:        "snapshot-keys",
		fileRef:           "primary",
		effectiveFacts:    effective,
	}
	generation := OffsetMigrationGeneration{ID: "gen-keys", ManagedAnimeID: "anime-keys", NewOffset: -1}

	// No RSS source provenance: the plan stops after re-identification, before
	// any render, which is all this test reads.
	operation, err := (&OffsetMigrationService{}).planOffsetMigrationCandidate(context.Background(), generation, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if operation.reasonCode != "source_provenance_unavailable" {
		t.Fatalf("reason=%q, want the plan to stop at source provenance", operation.reasonCode)
	}
	if operation.oldCoverageKey == nil || *operation.oldCoverageKey != legacyKey {
		t.Fatalf("old coverage=%v, want the frozen %s", operation.oldCoverageKey, legacyKey)
	}
	if operation.newCoverageKey == nil || *operation.newCoverageKey != "tv:e2" {
		t.Fatalf("new coverage=%v, want tv:e2", operation.newCoverageKey)
	}
	var identity episodeidentity.EpisodeIdentity
	if err := json.Unmarshal(operation.newIdentity, &identity); err != nil {
		t.Fatal(err)
	}
	if identity.ResolverVersion != episodeidentity.ResolverVersion {
		t.Fatalf("new identity resolver=%q, want %q", identity.ResolverVersion, episodeidentity.ResolverVersion)
	}
}

// Stored coverage keys are read in both shapes until the stored rows are
// rewritten: tv:e<N> and the v1 tv:s<S>:e<N> give the same episode.
func TestCoverageKeyReadersAcceptBothShapes(t *testing.T) {
	for _, coverage := range [][]string{{"tv:e5"}, {"tv:s1:e5"}, {"tv:s0:e5"}} {
		if got := episodeFromCoverage(coverage); got != "5" {
			t.Fatalf("episodeFromCoverage(%v)=%q, want 5", coverage, got)
		}
		kind, episodes := coverageEpisodes(coverage)
		if kind != "tv" || len(episodes) != 1 || episodes[0] != "5" {
			t.Fatalf("coverageEpisodes(%v)=%s/%v, want tv/[5]", coverage, kind, episodes)
		}
	}
	kind, episodes := coverageEpisodes([]string{"tv:e1", "tv:s1:e2"})
	if kind != "tv" || len(episodes) != 2 || episodes[0] != "1" || episodes[1] != "2" {
		t.Fatalf("mixed coverage=%s/%v, want tv/[1 2]", kind, episodes)
	}
	if kind, episodes := coverageEpisodes([]string{"movie:0"}); kind != "movie" || len(episodes) != 0 {
		t.Fatalf("movie coverage=%s/%v, want movie/[]", kind, episodes)
	}
}
