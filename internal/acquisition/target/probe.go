package target

import (
	"errors"
	"os"
	"time"

	"bansoko/internal/library/domain"
)

type ObservationState string

const (
	ObservationMissing           ObservationState = "missing"
	ObservationPresentMatched    ObservationState = "present_matched"
	ObservationPresentMismatched ObservationState = "present_mismatched"
	ObservationUnreadable        ObservationState = "unreadable"
)

type Observation struct {
	TargetID          string
	State             ObservationState
	ObservedSizeBytes *int64
	ReasonCode        string
	ObservedAt        time.Time
	Version           int64
}

type Probe interface {
	Observe(Spec) Observation
}

type Admission string

const (
	AdmissionAdopted  Admission = "adopted_existing"
	AdmissionDownload Admission = "download_required"
	AdmissionFailed   Admission = "acquisition_failed"
	// AdmissionSkipped: the batch plan has nothing to place (every keyed
	// video is covered or excluded and no unknown video is routed). The
	// release is filtered with no_placeable_files; it is not a failure.
	AdmissionSkipped Admission = "skipped"
)

type AdmissionResult struct {
	Admission Admission
	Reason    string
	Matched   int
	Missing   int
}

// DecideTargets reports, per target, the observation that keeps that one
// target out of the acquisition: an unreadable target, a block collision on an
// ensure_present target, or a block collision on a replacement target. A batch
// drops only the conflicting file (target_collision); the remaining targets are
// decided together by Decide.
func DecideTargets(set Set, observations []Observation) map[string]string {
	byID := make(map[string]Observation, len(observations))
	for _, observation := range observations {
		byID[observation.TargetID] = observation
	}
	blocked := map[string]string{}
	for _, spec := range set.Targets {
		observation, ok := byID[spec.ID]
		switch {
		case !ok || observation.State == ObservationUnreadable:
			blocked[spec.TargetRef] = "target_unreadable"
		case spec.CollisionPolicy != domain.CollisionBlock || observation.State == ObservationMissing:
		case spec.Intent == IntentReplace:
			blocked[spec.TargetRef] = "replacement_collision_blocked"
		case observation.State == ObservationPresentMismatched:
			blocked[spec.TargetRef] = "target_collision_blocked"
		}
	}
	return blocked
}

// Decide applies the weak product truth only to ordinary ensure_present
// acquisition. Replacement is never short-circuited by a same-size incumbent.
// A mismatch is downloadable only when the frozen collision policy authorizes
// the eventual effect; block is a terminal pre-acquisition failure.
func Decide(set Set, observations []Observation) AdmissionResult {
	if len(set.Targets) == 0 || len(observations) != len(set.Targets) {
		return AdmissionResult{Admission: AdmissionFailed, Reason: "target_observation_incomplete"}
	}
	byID := make(map[string]Observation, len(observations))
	for _, observation := range observations {
		byID[observation.TargetID] = observation
	}
	matched := 0
	missing := 0
	for _, spec := range set.Targets {
		observation, ok := byID[spec.ID]
		if !ok || observation.State == ObservationUnreadable {
			return AdmissionResult{Admission: AdmissionFailed, Reason: "target_unreadable"}
		}
		if spec.Intent == IntentReplace {
			if spec.CollisionPolicy == domain.CollisionBlock && observation.State != ObservationMissing {
				return AdmissionResult{Admission: AdmissionFailed, Reason: "replacement_collision_blocked"}
			}
			return AdmissionResult{Admission: AdmissionDownload, Reason: "replacement_requires_new_artifact"}
		}
		switch observation.State {
		case ObservationPresentMatched:
			matched++
		case ObservationMissing:
			missing++
		case ObservationPresentMismatched:
			if spec.CollisionPolicy == domain.CollisionBlock {
				return AdmissionResult{Admission: AdmissionFailed, Reason: "target_collision_blocked"}
			}
			missing++
		default:
			return AdmissionResult{Admission: AdmissionFailed, Reason: "target_observation_invalid"}
		}
	}
	if matched == len(set.Targets) {
		return AdmissionResult{Admission: AdmissionAdopted, Reason: "all_targets_exact_size", Matched: matched}
	}
	return AdmissionResult{Admission: AdmissionDownload, Reason: "target_work_required", Matched: matched, Missing: missing}
}

type FilesystemProbe struct {
	Now func() time.Time
}

func (p FilesystemProbe) Observe(spec Spec) Observation {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	result := Observation{TargetID: spec.ID, ObservedAt: now()}
	info, err := os.Lstat(spec.TargetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.State = ObservationMissing
			result.ReasonCode = "target_missing"
			return result
		}
		result.State = ObservationUnreadable
		result.ReasonCode = "target_stat_failed"
		return result
	}
	if !info.Mode().IsRegular() {
		result.State = ObservationUnreadable
		if info.Mode()&os.ModeSymlink != 0 {
			result.ReasonCode = "target_symlink"
		} else {
			result.ReasonCode = "target_non_regular"
		}
		return result
	}
	size := info.Size()
	result.ObservedSizeBytes = &size
	if size == spec.ExpectedSizeBytes {
		result.State = ObservationPresentMatched
		result.ReasonCode = "target_size_matched"
	} else {
		result.State = ObservationPresentMismatched
		result.ReasonCode = "target_size_mismatched"
	}
	return result
}
