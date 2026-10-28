package filemaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"bansoko/internal/fileidentity"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
)

// cleanupAttemptEvidence is the durable checkpoint of one deletion stage. The
// work item id and owner version bind the stage's exact invocation receipt,
// so recovery reads it without the claim that ran it.
type cleanupAttemptEvidence struct {
	CleanupID          string                 `json:"cleanup_id"`
	Stage              string                 `json:"stage"`
	WorkItemID         string                 `json:"work_item_id"`
	OwnerVersion       int64                  `json:"owner_version"`
	SourcePath         string                 `json:"source_path"`
	SourceFingerprint  string                 `json:"source_fingerprint"`
	CapturePath        string                 `json:"capture_path"`
	SourceObservation  domain.FileObservation `json:"source_observation"`
	CaptureObservation domain.FileObservation `json:"capture_observation"`
	FencingToken       int64                  `json:"fencing_token"`
	// BusyRefused marks a stage the claim that ran it saw refused (busy)
	// and proved unapplied; its invocation was retired and its markers
	// removed, so the next claim decides from the files alone.
	BusyRefused bool `json:"busy_refused,omitempty"`
}

func decodeCleanupAttempt(raw []byte) (cleanupAttemptEvidence, error) {
	if len(raw) == 0 {
		return cleanupAttemptEvidence{}, errors.New("cleanup attempt is missing")
	}
	var evidence cleanupAttemptEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return cleanupAttemptEvidence{}, err
	}
	if evidence.Stage != cleanupStageCapture && evidence.Stage != cleanupStagePurge {
		return cleanupAttemptEvidence{}, errors.New("cleanup attempt stage is invalid")
	}
	if evidence.WorkItemID == "" || evidence.OwnerVersion < 1 || evidence.SourcePath == "" || evidence.CapturePath == "" {
		return cleanupAttemptEvidence{}, errors.New("cleanup attempt lacks its invocation identity")
	}
	if !completeCleanupObservation(evidence.SourceObservation) {
		return cleanupAttemptEvidence{}, errors.New("cleanup attempt lacks complete frozen source evidence")
	}
	if evidence.Stage == cleanupStagePurge && !completeCleanupObservation(evidence.CaptureObservation) {
		return cleanupAttemptEvidence{}, errors.New("cleanup purge attempt lacks complete captured evidence")
	}
	return evidence, nil
}

func completeCleanupObservation(observation domain.FileObservation) bool {
	return observation.Fingerprint != "" && observation.SizeBytes >= 0 && fileidentity.IsPresent(observation.Identity)
}

// cleanupInvocation is the exact receipt identity of one stage, derived only
// from the owner and its persisted attempt.
func cleanupInvocation(op *ReplacementDeletion, attempt cleanupAttemptEvidence, kind string) executorpkg.MaintenanceInvocation {
	var parsed any
	_ = json.Unmarshal(op.Attempt, &parsed)
	normalized, _ := json.Marshal(parsed)
	invocation := executorpkg.MaintenanceInvocation{
		OwnerType:    "replacement_deletion",
		OwnerID:      op.ID,
		Kind:         kind,
		WorkItemID:   attempt.WorkItemID,
		OwnerVersion: attempt.OwnerVersion,
		Attempt:      normalized,
	}
	if kind == cleanupCaptureInvocation {
		invocation.Source, invocation.Target, invocation.Fingerprint = attempt.SourcePath, attempt.CapturePath, attempt.SourceObservation.Fingerprint
	} else {
		invocation.Source, invocation.Target, invocation.Fingerprint = attempt.CapturePath, attempt.CapturePath, attempt.CaptureObservation.Fingerprint
	}
	return invocation
}

// cleanupResolution is what the receipts prove about a persisted attempt.
type cleanupResolution int

const (
	cleanupResolutionUnknown cleanupResolution = iota
	// cleanupResolutionCaptured: the capture finished; the purge is next.
	cleanupResolutionCaptured
	// cleanupResolutionPurged: the purge of the captured object returned.
	cleanupResolutionPurged
	// cleanupResolutionCaptureNotStarted: no capture marker, the source
	// still matches, nothing at the capture path.
	cleanupResolutionCaptureNotStarted
	// cleanupResolutionPurgeNotStarted: no purge marker and the captured
	// object is still present and identical.
	cleanupResolutionPurgeNotStarted
)

// resolveCleanupAttempt reads the exact receipt of the attempt's stage once.
// Absence of a file is never a receipt: a purge without its receipt stays
// unknown. A started stage whose effect provably did not apply (the source or
// the captured object is still the frozen object) resolves like an unstarted
// one, so a refused rename or unlink can run again.
func resolveCleanupAttempt(ctx context.Context, observer executorpkg.CleanupFileExecutor, receipts executorpkg.MaintenanceCleanupExecutor, op *ReplacementDeletion, attempt cleanupAttemptEvidence) cleanupResolution {
	if attempt.BusyRefused {
		// A refused, proven-unapplied stage has no receipt left to read.
		// A capture: nothing at the capture path means it never happened
		// (only this deletion renames into it); the fresh start re-observes
		// the source.
		if attempt.Stage == cleanupStagePurge {
			return resolveUnpurgedCleanup(ctx, observer, op, attempt)
		}
		if _, err := observeCleanupFile(ctx, observer, attempt.CapturePath, op.SourceFingerprint); isMissingCleanupError(err) {
			return cleanupResolutionCaptureNotStarted
		}
		return cleanupResolutionUnknown
	}
	if attempt.Stage == cleanupStagePurge {
		invocation := cleanupInvocation(op, attempt, cleanupPurgeInvocation)
		receipt, err := receipts.ReadMaintenanceReceipt(ctx, invocation)
		if err == nil && receipt.Validate(invocation) == nil {
			return cleanupResolutionPurged
		}
		if stageProvablyNotRun(err) {
			return resolveUnpurgedCleanup(ctx, observer, op, attempt)
		}
		return cleanupResolutionUnknown
	}
	invocation := cleanupInvocation(op, attempt, cleanupCaptureInvocation)
	receipt, err := receipts.ReadMaintenanceReceipt(ctx, invocation)
	if err == nil && receipt.Validate(invocation) == nil && sameFrozenCleanupObservation(receipt.Observation, attempt.SourceObservation) {
		return cleanupResolutionCaptured
	}
	if stageProvablyNotRun(err) {
		observed, observeErr := observeCleanupFile(ctx, observer, attempt.SourcePath, op.SourceFingerprint)
		_, targetErr := observeCleanupFile(ctx, observer, attempt.CapturePath, op.SourceFingerprint)
		if observeErr == nil && sameFrozenCleanupObservation(observed, attempt.SourceObservation) && isMissingCleanupError(targetErr) {
			return cleanupResolutionCaptureNotStarted
		}
	}
	return cleanupResolutionUnknown
}

// resolveUnpurgedCleanup resolves a purge proven not to have run: the
// captured object still intact means the purge is next. A give-up that moved
// the object back to its public path before its settlement committed (its
// refused markers already discarded) leaves nothing captured and the object
// where it was, exactly as before the capture.
func resolveUnpurgedCleanup(ctx context.Context, observer executorpkg.CleanupFileExecutor, op *ReplacementDeletion, attempt cleanupAttemptEvidence) cleanupResolution {
	observed, err := observeCleanupFile(ctx, observer, attempt.CapturePath, op.SourceFingerprint)
	if err == nil && sameFrozenCleanupObservation(observed, attempt.CaptureObservation) {
		return cleanupResolutionPurgeNotStarted
	}
	if isMissingCleanupError(err) {
		source, sourceErr := observeCleanupFile(ctx, observer, attempt.SourcePath, op.SourceFingerprint)
		if sourceErr == nil && sameFrozenCleanupObservation(source, attempt.CaptureObservation) {
			return cleanupResolutionCaptureNotStarted
		}
	}
	return cleanupResolutionUnknown
}

// stageProvablyNotRun reports a receipt read that proves the stage's effect
// did not happen: the invocation never wrote its start marker, or it started
// and the executor observed, under the invocation lock, that the syscall did
// not apply (a refused rename or unlink of a busy file). Either way the
// resolver still compares the paths with the frozen evidence itself.
func stageProvablyNotRun(err error) bool {
	return errors.Is(err, executorpkg.ErrMaintenanceNotStarted) || errors.Is(err, executorpkg.ErrMaintenanceNotApplied)
}

// observeCleanupFile observes a path in the mode of the frozen identity. A
// deletion freezes a metadata identity, so it uses no-follow path metadata
// and never reads the content.
func observeCleanupFile(ctx context.Context, executor executorpkg.CleanupFileExecutor, path, fingerprint string) (domain.FileObservation, error) {
	if _, metadata := metadataFingerprintValue(fingerprint); metadata {
		observer, ok := executor.(executorpkg.CleanupMetadataFileObserver)
		if !ok {
			return domain.FileObservation{}, errors.New("cleanup metadata observer is not wired")
		}
		return observer.ObserveMetadataFile(ctx, path)
	}
	return executor.Observe(ctx, path)
}

// sameFrozenCleanupObservation compares two observations of one object:
// metadata observations by size and inode (never the device, which a network
// mount may change), content observations by digest, size and identity.
func sameFrozenCleanupObservation(actual, expected domain.FileObservation) bool {
	metadata := domain.IsMetadataFingerprint(actual.Fingerprint) || domain.IsMetadataFingerprint(expected.Fingerprint)
	if metadata {
		if !domain.MetadataFingerprintsMatch(actual.Fingerprint, expected.Fingerprint) || actual.SizeBytes != expected.SizeBytes {
			return false
		}
		if !fileidentity.IsPresent(actual.Identity) || !fileidentity.IsPresent(expected.Identity) || actual.Identity.Inode != expected.Identity.Inode {
			return false
		}
		return actual.Identity.GenerationNanos == 0 || expected.Identity.GenerationNanos == 0 || actual.Identity.GenerationNanos == expected.Identity.GenerationNanos
	}
	return actual.Fingerprint == expected.Fingerprint && actual.SizeBytes == expected.SizeBytes && fileidentity.Matches(actual.Identity, expected.Identity)
}

func isMissingCleanupError(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}
