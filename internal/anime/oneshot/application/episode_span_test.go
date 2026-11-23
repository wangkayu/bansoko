package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
)

func TestOneShotRequestHashKeepsDefaultSpanCompatible(t *testing.T) {
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
	for _, span := range []int{0, 1} {
		input.EpisodeSpan = span
		got, err := canonicalOneShotRequestHash(input, key)
		if err != nil {
			t.Fatal(err)
		}
		if got != legacy {
			t.Fatalf("span %d changed the legacy request hash", span)
		}
	}
	input.EpisodeSpan = 2
	combined, err := canonicalOneShotRequestHash(input, key)
	if err != nil {
		t.Fatal(err)
	}
	if combined == legacy {
		t.Fatal("a combined-episode span must change the request hash")
	}

	draftDigest := "sha256:" + strings.Repeat("e", 64)
	input.EpisodeSpan = 0
	currentLegacy, err := canonicalOneShotCurrentInputRequestHash(draftDigest, input, key)
	if err != nil {
		t.Fatal(err)
	}
	input.EpisodeSpan = 1
	currentDefault, err := canonicalOneShotCurrentInputRequestHash(draftDigest, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if currentDefault != currentLegacy {
		t.Fatal("explicit default span changed the current-input request hash")
	}
	input.EpisodeSpan = 2
	currentCombined, err := canonicalOneShotCurrentInputRequestHash(draftDigest, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if currentCombined == currentLegacy {
		t.Fatal("a combined-episode span must change the current-input request hash")
	}
}

func TestOneShotReplayRejectsADifferentEpisodeSpan(t *testing.T) {
	const key = "0123456789abcdef"
	seed := validOneShotSeed()
	saved := OneShotInputSnapshot{
		CandidateID:     "candidate-1",
		Revision:        3,
		SelectedSeed:    seed,
		EffectiveDigest: "sha256:" + strings.Repeat("d", 64),
		EpisodeSpan:     2,
	}
	request := AcquireSeedInput{CandidateID: "candidate-1", Revision: 3, Seed: seed, EpisodeSpan: 2}
	if _, err := oneShotSavedRevisionReplayRequestHash(saved, request, key); err != nil {
		t.Fatalf("same-span replay: %v", err)
	}
	request.EpisodeSpan = 1
	if _, err := oneShotSavedRevisionReplayRequestHash(saved, request, key); !isIdempotencyReused(err) {
		t.Fatalf("different-span replay err = %v, want IDEMPOTENCY_KEY_REUSED", err)
	}

	saved.EpisodeSpan = 0
	request.EpisodeSpan = 1
	if _, err := oneShotSavedRevisionReplayRequestHash(saved, request, key); err != nil {
		t.Fatalf("legacy and explicit default spans must replay: %v", err)
	}
}

func TestOneShotInputSnapshotFreezesEpisodeSpan(t *testing.T) {
	raw, err := json.Marshal(OneShotInputSnapshot{
		CandidateID: "candidate-1", Revision: 1, SelectedSeed: validOneShotSeed(), EpisodeSpan: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["episode_span"]) != "12" {
		t.Fatalf("snapshot episode_span = %s, want 12", wire["episode_span"])
	}
	var decoded OneShotInputSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.EpisodeSpan != 12 {
		t.Fatalf("decoded span = %d, %v; want 12", decoded.EpisodeSpan, err)
	}
	var legacy OneShotInputSnapshot
	if err := json.Unmarshal([]byte(`{"candidate_id":"c","revision":1,"selected_seed":{"locator":"x","provider":"mikan","provider_entry_ref":"e","provider_group_ref":"g","published_at":"2026-01-01T00:00:00Z","title":"t"}}`), &legacy); err != nil || legacy.EpisodeSpan != 1 {
		t.Fatalf("legacy snapshot span = %d, %v; want default 1", legacy.EpisodeSpan, err)
	}
}

func TestOneShotEpisodeSpanRange(t *testing.T) {
	for _, span := range []int{0, 1, 12} {
		if err := validateOneShotEpisodeSpan(span); err != nil {
			t.Fatalf("span %d rejected: %v", span, err)
		}
	}
	for _, span := range []int{-1, 13} {
		var business *apperrors.Business
		if err := validateOneShotEpisodeSpan(span); !errors.As(err, &business) || business.Status != 422 || business.Code != "SPAN_OUT_OF_RANGE" {
			t.Fatalf("span %d err = %v, want 422 SPAN_OUT_OF_RANGE", span, err)
		}
	}
	service := &Service{}
	var business *apperrors.Business
	if _, err := service.DownloadSnapshotSeed(context.Background(), SnapshotSeedDownloadInput{EpisodeSpan: 13}, "0123456789abcdef"); !errors.As(err, &business) || business.Code != "SPAN_OUT_OF_RANGE" {
		t.Fatalf("snapshot download err = %v, want SPAN_OUT_OF_RANGE", err)
	}
}
