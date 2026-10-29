package target

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
)

// FrozenFacts is what a frozen carrier (the one-shot input snapshot, the
// download task's frozen input) keeps of the target set it admitted. The set
// itself is persisted once, one acquisition_targets row per target holding its
// render/config snapshot and its lock observation; the carrier binds it by
// the set digest and the target count only. No carrier grows with the number
// of targets, and no carrier holds a second copy a reader could diverge on.
type FrozenFacts struct {
	Admission string `json:"admission"`
	// ReasonCode and Reason are the planner's typed admission reason and its
	// plan detail. The subscription carrier has always written the typed
	// reason under reason; the one-shot carrier writes both.
	ReasonCode      string `json:"reason_code,omitempty"`
	Reason          string `json:"reason,omitempty"`
	TargetSetDigest string `json:"target_set_digest,omitempty"`
	TargetCount     int    `json:"target_count"`
}

// UnplannedFrozenFacts is the carrier value of an admission that planned no
// target set at all (no root configured for pre-download targets).
func UnplannedFrozenFacts() FrozenFacts {
	return FrozenFacts{Admission: string(AdmissionDownload)}
}

// VarsDigest is the reference a planned target keeps to the release-level
// naming vars evaluation it rendered with. The evaluation, input included, is
// frozen once on the carrier's frozen policy; the per-target render snapshot
// names it by this digest instead of repeating it, so a target does not grow
// with the torrent's file inventory. The digest is sha256 over the canonical
// JSON of the evaluation (the bytes anime_config_candidate_canonical_json
// produces), so it is recomputable from the stored policy in SQL. An absent
// evaluation has no digest.
func VarsDigest(evaluation *namingvars.FrozenEvaluation) (string, error) {
	if evaluation == nil {
		return "", nil
	}
	raw, err := json.Marshal(evaluation)
	if err != nil {
		return "", fmt.Errorf("encode frozen vars evaluation: %w", err)
	}
	canonical, err := animedomain.CanonicalJSON(raw)
	if err != nil {
		return "", fmt.Errorf("canonicalize frozen vars evaluation: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
