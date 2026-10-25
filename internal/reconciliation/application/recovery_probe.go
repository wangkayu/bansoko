package application

import (
	"context"
	"path/filepath"

	"bansoko/internal/library/domain"
)

// RecoveryTargetRenderer renders, under the anime's CURRENT naming rules, the
// aligned target each active placement (keyed by placement id) and each of its
// registered companions (keyed by companion id) would be re-aligned to. It is
// the same surface the files dialog's alignment chain renders through; the
// composition root injects the filemaintenance renderer so this package never
// imports filemaintenance (dependency direction: filemaintenance ->
// reconciliation only).
type RecoveryTargetRenderer interface {
	RenderPlacementTargets(ctx context.Context, animeID string, placementIDs []string) (map[string]string, error)
}

// FileObserver stats one explicit path without following symlinks and without
// reading any content: the recovery probe re-uses the scan's no-follow,
// regular-file-only observer (domain.MetadataFingerprint = size+dev+ino).
type FileObserver interface {
	ObserveFile(ctx context.Context, path string) (domain.FileObservation, error)
}

// recoveryCandidateFact is the immutable recovery candidate a scan writes into
// reconciliation_files.facts when a placement's current path AND every bounded
// provenance locator are missing but its rendered target exists at the exact
// rendered path. Disposition stays "missing": a candidate changes no ownership
// and never auto-recovers; the user's explicit confirm (POST
// /anime/{id}/files/recover) is the only path that re-attaches the file.
type recoveryCandidateFact struct {
	// PlacementID is the placement whose video was rendered to TargetPath.
	PlacementID string `json:"placement_id"`
	// TargetPath is the exact rendered, stat'ed target (absolute, cleaned).
	TargetPath string `json:"target_path"`
	// TargetFingerprint is the target file's meta fingerprint
	// (meta:size=..;dev=..;ino=..) at scan time; recover re-stats and only
	// accepts the frozen fingerprint, so an in-place replacement fails closed.
	TargetFingerprint string `json:"target_fingerprint"`
	// SizeBytes is the stat'ed target size at scan time.
	SizeBytes int64 `json:"size_bytes"`
	// Generation is the scan generation that observed the candidate.
	Generation int64 `json:"generation"`
	// Companions are the placement's registered companions stat'ed at their
	// rendered companion paths: present ones freeze id/path/fingerprint/size;
	// a missing companion is recorded with a null observation so the confirm
	// step can tell "complete" from "incomplete" recovery.
	Companions []recoveryCompanionFact `json:"companions"`
	// CompanionsComplete is false when at least one registered companion was
	// not found at its rendered path (recover still proceeds for the video;
	// the UI reports the gap).
	CompanionsComplete bool `json:"companions_complete"`
}

// recoveryCompanionFact freezes one companion's stat result at its rendered
// path (or its absence there) for the confirm step's occupancy re-check.
type recoveryCompanionFact struct {
	ID          string  `json:"id"`
	TargetPath  string  `json:"target_path"`
	State       string  `json:"state"` // present | missing | stat_failed
	Fingerprint *string `json:"fingerprint,omitempty"`
	SizeBytes   int64   `json:"size_bytes"`
}

// probeRecoveryCandidates renders the current naming targets of every all-
// locators-missing placement, stats the exact rendered video path (and each
// rendered companion path) through the no-follow observer, and returns one
// candidate per placement whose rendered target exists as a regular file.
// Every probe is a stat only: no directory enumeration, no content read, no
// byte is moved, copied, overwritten or deleted, and no ownership changes.
// A render failure for one placement degrades that placement to "no candidate"
// without failing the scan; the caller passes nil for the renderer or observer
// only in tests that do not exercise recovery.
func (s *Service) probeRecoveryCandidates(
	ctx context.Context,
	animeID string,
	generation int64,
	missing []ownedPlacement,
) []recoveryCandidateFact {
	if s.recoveryTargets == nil || s.recoveryObserver == nil || len(missing) == 0 || ctx.Err() != nil {
		return nil
	}
	ids := make([]string, 0, len(missing))
	for _, placement := range missing {
		ids = append(ids, placement.ID)
	}
	// One bounded render for the whole missing set; a renderer failure degrades
	// every candidate this run (the disposition stays missing and the next scan
	// retries) instead of failing a scan that already observed real facts.
	rendered, err := s.recoveryTargets.RenderPlacementTargets(ctx, animeID, ids)
	if err != nil || ctx.Err() != nil {
		// A renderer failure (or a cancellation surfacing as one) degrades
		// every candidate this run: the disposition stays missing and the next
		// scan retries, instead of failing a scan that already observed real
		// facts.
		return nil
	}
	targets := make(map[string]string, len(rendered))
	for key, path := range rendered {
		if key == "" || filepath.Clean(path) == "" {
			continue
		}
		targets[key] = filepath.Clean(path)
	}
	candidates := make([]recoveryCandidateFact, 0, len(missing))
	for _, placement := range missing {
		target, ok := targets[placement.ID]
		if !ok {
			continue
		}
		// The rendered target is itself a placement locator in some scenes;
		// if the scan found the video at the rendered path it would not be
		// missing at all, so a hit here means the found map missed it — the
		// candidate probe is still a stat of the exact rendered path.
		if ctx.Err() != nil {
			return candidates
		}
		observation, err := s.recoveryObserver.ObserveFile(ctx, target)
		if err != nil {
			if ctx.Err() != nil {
				return candidates
			}
			continue
		}
		candidate := recoveryCandidateFact{
			PlacementID:        placement.ID,
			TargetPath:         target,
			TargetFingerprint:  observation.Fingerprint,
			SizeBytes:          observation.SizeBytes,
			Generation:         generation,
			Companions:         probeRecoveryCompanions(ctx, s.recoveryObserver, targets, placement.Companions),
			CompanionsComplete: true,
		}
		for _, companion := range candidate.Companions {
			if companion.State != "present" {
				candidate.CompanionsComplete = false
			}
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// probeRecoveryCompanions stats each companion's rendered path; the map key is
// the companion id as rendered by RenderPlacementTargets.
func probeRecoveryCompanions(
	ctx context.Context,
	observer FileObserver,
	targets map[string]string,
	companions []ownedCompanion,
) []recoveryCompanionFact {
	if len(companions) == 0 {
		return nil
	}
	facts := make([]recoveryCompanionFact, 0, len(companions))
	for _, companion := range companions {
		fact := recoveryCompanionFact{
			ID:         companion.ID,
			TargetPath: companion.Path,
			State:      "missing",
		}
		if rendered, ok := targets[companion.ID]; ok {
			fact.TargetPath = rendered
		}
		observation, err := observer.ObserveFile(ctx, fact.TargetPath)
		switch {
		case err == nil:
			fingerprint := observation.Fingerprint
			fact.Fingerprint = &fingerprint
			fact.SizeBytes = observation.SizeBytes
			fact.State = "present"
		case ctx.Err() != nil:
			facts = append(facts, fact)
			return facts
		default:
			fact.State = "missing"
		}
		facts = append(facts, fact)
	}
	return facts
}
