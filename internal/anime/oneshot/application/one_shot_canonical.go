package application

import (
	"encoding/json"
	"fmt"
	"strings"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
)

// oneShotCanonicalRequest is the deterministic replay identity: the HTTP
// idempotency key plus the exact saved candidate revision and selected seed.
type oneShotCanonicalRequest struct {
	Kind            string       `json:"kind"`
	IdempotencyKey  string       `json:"idempotency_key"`
	CandidateID     string       `json:"candidate_id"`
	Revision        int64        `json:"revision"`
	EffectiveDigest string       `json:"effective_digest"`
	SelectedSeed    SelectedSeed `json:"selected_seed"`
	// EpisodeOffset is omitted at 0 so an offset-free request keeps its
	// replay identity; any other value is a different request.
	EpisodeOffset int `json:"episode_offset,omitempty"`
	// FileSelection is omitted without a manual selection so a default
	// request keeps its replay identity.
	FileSelection []string                                `json:"file_selection,omitempty"`
	Overrides     []acquisitiontarget.FileEpisodeOverride `json:"overrides,omitempty"`
}

func canonicalOneShotRequestHash(input AcquireSeedInput, key string) (string, error) {
	overrides, err := acquisitiontarget.CanonicalFileEpisodeOverrides(input.Overrides)
	if err != nil {
		return "", err
	}
	input.Overrides = overrides
	return animeapplication.CanonicalJSONHash(oneShotCanonicalRequest{
		Kind:            "acquire_seed_once",
		IdempotencyKey:  key,
		CandidateID:     input.CandidateID,
		Revision:        input.Revision,
		EffectiveDigest: input.EffectiveDigest,
		SelectedSeed:    input.Seed,
		EpisodeOffset:   input.EpisodeOffset,
		FileSelection:   input.FileSelection,
		Overrides:       input.Overrides,
	})
}

// oneShotCurrentInputCanonicalRequest is the replay identity of a captured
// unsaved-draft admission. It is deliberately request-derived only: the
// idempotency key, the canonical draft digest and the selected seed. No
// resolved value (globals, effective digest) participates, so a replay is
// always decided against the originally captured input, never re-resolved
// against changed configuration.
type oneShotCurrentInputCanonicalRequest struct {
	Kind           string                                  `json:"kind"`
	IdempotencyKey string                                  `json:"idempotency_key"`
	DraftDigest    string                                  `json:"draft_digest"`
	SelectedSeed   SelectedSeed                            `json:"selected_seed"`
	EpisodeOffset  int                                     `json:"episode_offset,omitempty"`
	FileSelection  []string                                `json:"file_selection,omitempty"`
	Overrides      []acquisitiontarget.FileEpisodeOverride `json:"overrides,omitempty"`
}

func canonicalOneShotCurrentInputRequestHash(draftDigest string, input AcquireSeedInput, key string) (string, error) {
	overrides, err := acquisitiontarget.CanonicalFileEpisodeOverrides(input.Overrides)
	if err != nil {
		return "", err
	}
	input.Overrides = overrides
	return animeapplication.CanonicalJSONHash(oneShotCurrentInputCanonicalRequest{
		Kind:           "acquire_seed_once_current_input",
		IdempotencyKey: key,
		DraftDigest:    draftDigest,
		SelectedSeed:   input.Seed,
		EpisodeOffset:  input.EpisodeOffset,
		FileSelection:  input.FileSelection,
		Overrides:      input.Overrides,
	})
}

func oneShotReplayRequestHash(record animeport.OneShotMarkerRecord, input AcquireSeedInput, key string) (string, error) {
	snapshot := decodeOneShotInputSnapshot(record.InputSnapshot)
	if snapshot == nil {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no valid frozen input snapshot."}
	}
	if snapshot.InputSource == AcquireSeedInputSourceCurrentInput {
		return oneShotCurrentInputReplayRequestHash(*snapshot, input, key)
	}
	return oneShotSavedRevisionReplayRequestHash(*snapshot, input, key)
}

// oneShotCurrentInputReplayRequestHash recomputes a captured-input replay
// identity: the request's draft must hash to the stored draft digest and the
// selected seed must match; the stored digest then fixes the request hash.
func oneShotCurrentInputReplayRequestHash(snapshot OneShotInputSnapshot, input AcquireSeedInput, key string) (string, error) {
	if strings.TrimSpace(snapshot.DraftDigest) == "" || len(snapshot.Draft) == 0 {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no captured draft."}
	}
	if !input.isCurrentInput() || input.Draft == nil {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a captured current_input draft."}
	}
	requestDigest, err := animeapplication.CanonicalAnimeDraftHash(*input.Draft)
	if err != nil {
		return "", fmt.Errorf("hash replayed one-shot draft: %w", err)
	}
	if requestDigest != snapshot.DraftDigest {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different captured draft."}
	}
	storedSeedHash, err := animeapplication.CanonicalJSONHash(snapshot.SelectedSeed)
	if err != nil {
		return "", fmt.Errorf("hash stored one-shot selected seed: %w", err)
	}
	requestSeedHash, err := animeapplication.CanonicalJSONHash(input.Seed)
	if err != nil {
		return "", fmt.Errorf("hash replayed one-shot selected seed: %w", err)
	}
	if storedSeedHash != requestSeedHash {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different selected seed."}
	}
	if snapshot.EpisodeOffset != input.EpisodeOffset {
		return "", oneShotOffsetReusedError()
	}
	return canonicalOneShotCurrentInputRequestHash(snapshot.DraftDigest, input, key)
}

// oneShotSavedRevisionReplayRequestHash is the unchanged legacy replay
// identity: the canonical saved candidate revision and selected seed.
func oneShotSavedRevisionReplayRequestHash(snapshot OneShotInputSnapshot, input AcquireSeedInput, key string) (string, error) {
	if strings.TrimSpace(snapshot.CandidateID) == "" || snapshot.Revision < 1 || strings.TrimSpace(snapshot.EffectiveDigest) == "" {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no valid frozen input snapshot."}
	}
	if snapshot.CandidateID != input.CandidateID || snapshot.Revision != input.Revision {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different saved candidate revision."}
	}
	storedSeedHash, err := animeapplication.CanonicalJSONHash(snapshot.SelectedSeed)
	if err != nil {
		return "", fmt.Errorf("hash stored one-shot selected seed: %w", err)
	}
	requestSeedHash, err := animeapplication.CanonicalJSONHash(input.Seed)
	if err != nil {
		return "", fmt.Errorf("hash replayed one-shot selected seed: %w", err)
	}
	if storedSeedHash != requestSeedHash {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different selected seed."}
	}
	if snapshot.EpisodeOffset != input.EpisodeOffset {
		return "", oneShotOffsetReusedError()
	}
	if input.EffectiveDigest != "" && input.EffectiveDigest != snapshot.EffectiveDigest {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different effective configuration."}
	}
	input.EffectiveDigest = snapshot.EffectiveDigest
	return canonicalOneShotRequestHash(input, key)
}

// oneShotReplayDecision decides marker-owned replay against the locked marker
// row: the same canonical request returns the original handle; a different
// body or selected seed under the same key is a typed conflict. Snapshot
// integrity is checked against the locked row's anchors.
func oneShotReplayDecision(record animeport.OneShotMarkerRecord, requestHash string) (OneShotAdmissionResult, error) {
	if record.RequestHash != requestHash {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different one-shot request."}
	}
	var result OneShotAdmissionResult
	if err := json.Unmarshal(record.ResultSnapshot, &result); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("decode one-shot replay snapshot: %w", err)
	}
	if result.OneShotID != record.ID ||
		result.ManagedAnimeID != record.ManagedAnimeID ||
		result.SubscriptionID != record.SubscriptionID ||
		result.RSSSourceID != record.RSSSourceID ||
		result.ReleaseID != record.ReleaseID ||
		!optionalStringPointerEqual(result.DownloadTaskID, record.DownloadTaskID) {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record is inconsistent with its anchors."}
	}
	return result, nil
}

// validateOneShotEpisodeOffset applies the shared episode-offset range to a
// one-shot request: out of range is a typed 422 before any read or write.
func validateOneShotEpisodeOffset(offset int) error {
	if animedomain.EpisodeOffsetInRange(offset) {
		return nil
	}
	return &apperrors.Business{Status: 422, Code: "OFFSET_OUT_OF_RANGE", Message: animedomain.EpisodeOffsetRangeMessage() + "."}
}

func oneShotOffsetReusedError() error {
	return &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different episode_offset."}
}

func optionalStringPointerEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// oneShotInstanceLabel derives the display-only instance label required by the
// origin=oneshot mount constraint. Agent drafts carry their own label; human
// provider drafts derive one from the draft title. The label is never an
// execution key.
func oneShotInstanceLabel(draft animedomain.AnimeDraft) string {
	if draft.InstanceLabel != nil {
		if trimmed := strings.TrimSpace(*draft.InstanceLabel); trimmed != "" {
			return truncateRunes(trimmed, 256)
		}
	}
	return truncateRunes("one-shot "+strings.TrimSpace(draft.Title), 256)
}

func truncateRunes(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > max {
		runes = runes[:max]
	}
	if len(runes) == 0 {
		return "one-shot acquisition"
	}
	return string(runes)
}
