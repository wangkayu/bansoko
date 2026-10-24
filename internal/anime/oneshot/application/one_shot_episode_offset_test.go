package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
)

// The request episode_offset is part of both replay identities. Omitted (0) it
// leaves the offset-free identity unchanged, so existing markers keep
// replaying; any other value is a different request.
func TestOneShotRequestHashIncludesTheEpisodeOffset(t *testing.T) {
	const key = "0123456789abcdef"
	input := AcquireSeedInput{
		CandidateID:     "candidate-1",
		Revision:        3,
		EffectiveDigest: "sha256:" + strings.Repeat("d", 64),
		Seed:            validOneShotSeed(),
	}
	legacy, err := canonicalOneShotRequestHashWithoutOffset(input, key)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := canonicalOneShotRequestHash(input, key)
	if err != nil {
		t.Fatal(err)
	}
	if zero != legacy {
		t.Fatal("an omitted episode_offset must keep the offset-free saved-revision identity")
	}
	shifted := input
	shifted.EpisodeOffset = 2
	two, err := canonicalOneShotRequestHash(shifted, key)
	if err != nil {
		t.Fatal(err)
	}
	if two == zero {
		t.Fatal("a different episode_offset must change the saved-revision identity")
	}

	draftDigest := "sha256:" + strings.Repeat("e", 64)
	currentZero, err := canonicalOneShotCurrentInputRequestHash(draftDigest, input, key)
	if err != nil {
		t.Fatal(err)
	}
	currentTwo, err := canonicalOneShotCurrentInputRequestHash(draftDigest, shifted, key)
	if err != nil {
		t.Fatal(err)
	}
	if currentZero == currentTwo {
		t.Fatal("a different episode_offset must change the current-input identity")
	}
}

// canonicalOneShotRequestHashWithoutOffset is the pre-offset identity shape.
func canonicalOneShotRequestHashWithoutOffset(input AcquireSeedInput, key string) (string, error) {
	return animeapplication.CanonicalJSONHash(struct {
		Kind            string       `json:"kind"`
		IdempotencyKey  string       `json:"idempotency_key"`
		CandidateID     string       `json:"candidate_id"`
		Revision        int64        `json:"revision"`
		EffectiveDigest string       `json:"effective_digest"`
		SelectedSeed    SelectedSeed `json:"selected_seed"`
	}{"acquire_seed_once", key, input.CandidateID, input.Revision, input.EffectiveDigest, input.Seed})
}

// Replaying a key against a frozen snapshot with a different episode_offset is
// IDEMPOTENCY_KEY_REUSED in both input variants; the same offset replays.
func TestOneShotReplayRejectsADifferentEpisodeOffset(t *testing.T) {
	const key = "0123456789abcdef"
	seed := validOneShotSeed()
	saved := OneShotInputSnapshot{
		CandidateID:     "candidate-1",
		Revision:        3,
		SelectedSeed:    seed,
		EffectiveDigest: "sha256:" + strings.Repeat("d", 64),
		EpisodeOffset:   2,
	}
	request := AcquireSeedInput{CandidateID: "candidate-1", Revision: 3, Seed: seed, EpisodeOffset: 2}
	if _, err := oneShotSavedRevisionReplayRequestHash(saved, request, key); err != nil {
		t.Fatalf("same-offset saved-revision replay: %v", err)
	}
	request.EpisodeOffset = 0
	if _, err := oneShotSavedRevisionReplayRequestHash(saved, request, key); !isIdempotencyReused(err) {
		t.Fatalf("different-offset saved-revision replay err = %v, want IDEMPOTENCY_KEY_REUSED", err)
	}

	draft := agentDraftWithLabel(t)
	current := OneShotInputSnapshot{
		SelectedSeed:  seed,
		InputSource:   AcquireSeedInputSourceCurrentInput,
		Draft:         json.RawMessage(`{}`),
		EpisodeOffset: -1,
	}
	digest, err := animeapplication.CanonicalAnimeDraftHash(draft)
	if err != nil {
		t.Fatal(err)
	}
	current.DraftDigest = digest
	currentRequest := AcquireSeedInput{InputSource: AcquireSeedInputSourceCurrentInput, Draft: &draft, Seed: seed, EpisodeOffset: -1}
	if _, err := oneShotCurrentInputReplayRequestHash(current, currentRequest, key); err != nil {
		t.Fatalf("same-offset current-input replay: %v", err)
	}
	currentRequest.EpisodeOffset = 1
	if _, err := oneShotCurrentInputReplayRequestHash(current, currentRequest, key); !isIdempotencyReused(err) {
		t.Fatalf("different-offset current-input replay err = %v, want IDEMPOTENCY_KEY_REUSED", err)
	}
}

func isIdempotencyReused(err error) bool {
	var business *apperrors.Business
	return errors.As(err, &business) && business.Status == 409 && business.Code == "IDEMPOTENCY_KEY_REUSED"
}

// The frozen snapshot carries the request offset on the wire; a legacy
// snapshot without the field decodes as 0.
func TestOneShotInputSnapshotFreezesTheEpisodeOffset(t *testing.T) {
	raw, err := json.Marshal(OneShotInputSnapshot{CandidateID: "candidate-1", Revision: 1, SelectedSeed: validOneShotSeed(), EpisodeOffset: -12})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["episode_offset"]) != "-12" {
		t.Fatalf("snapshot episode_offset = %s, want -12", wire["episode_offset"])
	}
	var decoded OneShotInputSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.EpisodeOffset != -12 {
		t.Fatalf("decoded offset = %d, %v; want -12", decoded.EpisodeOffset, err)
	}
	var legacy OneShotInputSnapshot
	if err := json.Unmarshal([]byte(`{"candidate_id":"c","revision":1,"selected_seed":{"locator":"x","provider":"mikan","provider_entry_ref":"e","provider_group_ref":"g","published_at":"2026-01-01T00:00:00Z","title":"t"}}`), &legacy); err != nil || legacy.EpisodeOffset != 0 {
		t.Fatalf("legacy snapshot offset = %d, %v; want 0", legacy.EpisodeOffset, err)
	}
}

// The range is the shared episode-offset range; outside it both one-shot entry
// points answer a typed 422 before reading or writing anything.
func TestOneShotEpisodeOffsetRange(t *testing.T) {
	for _, offset := range []int{-1000, 0, 1000} {
		if err := validateOneShotEpisodeOffset(offset); err != nil {
			t.Fatalf("offset %d rejected: %v", offset, err)
		}
	}
	for _, offset := range []int{-1001, 1001} {
		var business *apperrors.Business
		if err := validateOneShotEpisodeOffset(offset); !errors.As(err, &business) || business.Status != 422 || business.Code != "OFFSET_OUT_OF_RANGE" {
			t.Fatalf("offset %d err = %v, want 422 OFFSET_OUT_OF_RANGE", offset, err)
		}
	}
	// No preview source, marker store or database is configured: the offset
	// check must answer first.
	service := &Service{}
	var business *apperrors.Business
	if _, err := service.DownloadSnapshotSeed(context.Background(), SnapshotSeedDownloadInput{EpisodeOffset: -1001}, "0123456789abcdef"); !errors.As(err, &business) || business.Code != "OFFSET_OUT_OF_RANGE" {
		t.Fatalf("snapshot download err = %v, want OFFSET_OUT_OF_RANGE", err)
	}
}
