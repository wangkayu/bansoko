package domain

import (
	"encoding/json"
	"fmt"

	"bansoko/internal/batchplan"
)

// SectionDownloadCompletion is the single Global setting deciding what
// happens to the qBittorrent task after an acquisition succeeded. It is edited only in the settings page: there is no per-anime override
// and no draft field. It stays outside the six frozen effective sections, so
// candidate revisions, business snapshots and acquisition inputs keep their
// exact shapes. Acceptance resolves it once and freezes the value in the
// release's batch plan (batchplan.Plan.DownloadCompletion and
// DownloadCompletionSeedLimits); a later edit never changes an acquisition that
// was already accepted.
const SectionDownloadCompletion = "download_completion"

// DownloadCompletionSchemaVersion pins the stored download_completion shape.
// v2 adds the optional seed_limits of the seed action.
const DownloadCompletionSchemaVersion = "download-completion-v2"

// DownloadCompletionAction is what to do with the qBittorrent task once the
// acquisition succeeded and all its file operations are terminal.
type DownloadCompletionAction string

const (
	// DownloadCompletionSeed leaves the task running (the current behaviour).
	DownloadCompletionSeed DownloadCompletionAction = "seed"
	// DownloadCompletionPause pauses (stops) the task, keeping task and files.
	DownloadCompletionPause DownloadCompletionAction = "pause"
	// DownloadCompletionRemove removes the task from qBittorrent, keeping files.
	DownloadCompletionRemove DownloadCompletionAction = "remove"
	// DownloadCompletionRemoveWithData removes the task and asks qBittorrent
	// to delete the task's own files.
	DownloadCompletionRemoveWithData DownloadCompletionAction = "remove_with_data"
)

// DownloadCompletionActions lists the closed value set in display order.
var DownloadCompletionActions = []DownloadCompletionAction{
	DownloadCompletionSeed, DownloadCompletionPause, DownloadCompletionRemove, DownloadCompletionRemoveWithData,
}

// Valid reports whether the action is one of the four closed values.
func (a DownloadCompletionAction) Valid() bool {
	for _, candidate := range DownloadCompletionActions {
		if a == candidate {
			return true
		}
	}
	return false
}

// DownloadCompletionSeedLimits are the optional share limits of the seed
// action, written to qBittorrent at completion.
type DownloadCompletionSeedLimits = batchplan.SeedLimits

// DownloadCompletionRules is the complete download_completion section value.
// SeedLimits is allowed only with the seed action; absent means seed without
// a limit.
type DownloadCompletionRules struct {
	Action     DownloadCompletionAction      `json:"action"`
	SeedLimits *DownloadCompletionSeedLimits `json:"seed_limits,omitempty"`
}

// Equal compares two values by content (SeedLimits is a pointer).
func (r DownloadCompletionRules) Equal(other DownloadCompletionRules) bool {
	return r.Action == other.Action && r.SeedLimits.Equal(other.SeedLimits)
}

// DefaultDownloadCompletionRules is the code default: keep seeding.
func DefaultDownloadCompletionRules() DownloadCompletionRules {
	return DownloadCompletionRules{Action: DownloadCompletionSeed}
}

// ValidateDownloadCompletionJSON enforces the typed shape of a stored or
// submitted download_completion value.
func ValidateDownloadCompletionJSON(raw json.RawMessage) error {
	_, err := decodeDownloadCompletionRules(raw)
	return err
}

func decodeDownloadCompletionRules(raw json.RawMessage) (DownloadCompletionRules, error) {
	var value DownloadCompletionRules
	if err := decodeStrictJSON(raw, &value); err != nil {
		return DownloadCompletionRules{}, fmt.Errorf("download_completion value must be typed download completion rules")
	}
	if !value.Action.Valid() {
		return DownloadCompletionRules{}, fmt.Errorf("download_completion.action must be seed, pause, remove, or remove_with_data")
	}
	if value.SeedLimits != nil {
		if value.Action != DownloadCompletionSeed {
			return DownloadCompletionRules{}, fmt.Errorf("download_completion.seed_limits is allowed only with action seed")
		}
		if err := value.SeedLimits.Validate(); err != nil {
			return DownloadCompletionRules{}, err
		}
	}
	return value, nil
}

// ResolvedDownloadCompletion is the live effective value with its source
// version (the Global row version, or 1 for the code default).
type ResolvedDownloadCompletion struct {
	Rules   DownloadCompletionRules
	Version int64
}

// ResolveDownloadCompletion resolves the Global setting: the saved Global row,
// otherwise the code default. There is no per-anime override.
func ResolveDownloadCompletion(globals map[string]GlobalSection) (ResolvedDownloadCompletion, error) {
	raw, version, err := globalSectionValue(SectionDownloadCompletion, globals)
	if err != nil {
		return ResolvedDownloadCompletion{}, err
	}
	rules, err := decodeDownloadCompletionRules(raw)
	if err != nil {
		return ResolvedDownloadCompletion{}, fmt.Errorf("resolved download_completion is not a valid typed value: %w", err)
	}
	return ResolvedDownloadCompletion{Rules: rules, Version: version}, nil
}

// DefaultResolvedDownloadCompletion is the resolution with no saved Global
// row.
func DefaultResolvedDownloadCompletion() ResolvedDownloadCompletion {
	resolved, err := ResolveDownloadCompletion(nil)
	if err != nil {
		panic(err)
	}
	return resolved
}

// GlobalSectionOrder is every section the generic Global settings API lists:
// the rules-owner sections followed by the Global-only download_completion.
var GlobalSectionOrder = append(append([]string(nil), RulesOwnerSectionOrder...), SectionDownloadCompletion)
