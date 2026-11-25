package target

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
