package application

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

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

// RecoveryOccupancyChecker reports whether any of the exact paths is already
// owned by ANOTHER active placement/companion relation or held by an
// unfinished owner. The scan-side probe refuses to freeze a candidate for an
// occupied target (fail closed: an occupied unit is never an executable
// candidate). The composition root injects the SQL implementation over the
// same ownership surface the file manager enforces; tests pass a fake.
type RecoveryOccupancyChecker interface {
	RecoveryTargetsOccupied(ctx context.Context, paths []string, exceptPlacementID string) (bool, error)
}

// recoveryCandidateFact is the immutable recovery candidate a scan writes into
// reconciliation_files.facts when a placement's current path AND every bounded
// provenance locator are missing, its rendered target exists as a regular file
// with the placement's frozen expected size, the COMPLETE companion unit sits
// at its rendered paths with the registered sizes, and no other owner holds
// any of those paths. Disposition stays "missing": a candidate changes no
// ownership and never auto-recovers; the user's explicit confirm (POST
// /anime/{id}/files/recover) is the only path that re-attaches the file.
//
// An incomplete or size-mismatched or occupied unit freezes NOTHING (not a
// "partial" candidate): there is no executable candidate to offer, only the
// plain missing disposition the next scan re-decides.
type recoveryCandidateFact struct {
	// PlacementID is the placement whose video was rendered to TargetPath.
	PlacementID string `json:"placement_id"`
	// PlacementVersion is the placement row's version at scan time; the
	// confirm demands it (or exactly its successor on a replay) so a row
	// changed between scan and confirm refuses as placement_changed.
	PlacementVersion int64 `json:"placement_version"`
	// Generation is the scan generation that observed the candidate (also the
	// row's own generation; frozen for the confirm's exact-generation read).
	Generation int64 `json:"generation"`
	// OldPhysicalPath is the placement's frozen current path at scan time; the
	// confirm demands the row still sits there, so a move between scan and
	// confirm refuses as placement_changed.
	OldPhysicalPath string `json:"old_physical_path"`
	// TargetPath is the exact rendered, stat'ed target (absolute, cleaned),
	// and it always differs from every already-missing locator.
	TargetPath string `json:"target_path"`
	// TargetFingerprint is the target file's meta fingerprint
	// (meta:size=..;dev=..;ino=..) at scan time; the confirm re-stats and
	// matches with the shared size+inode semantics (dev excluded), so an
	// in-place replacement fails closed while an NFS remount does not.
	TargetFingerprint string `json:"target_fingerprint"`
	// SizeBytes is the stat'ed target size at scan time (also equal to the
	// placement's frozen expected size: an unknown expected size never
	// freezes a candidate — no size, no guess).
	SizeBytes int64 `json:"size_bytes"`
	// Companions is the COMPLETE companion unit stat'ed at its rendered
	// paths: every active companion relation is present with its registered
	// size. The confirm recovers video + companions atomically or nothing.
	Companions []recoveryCompanionFact `json:"companions"`
}

// recoveryCompanionFact freezes one companion's stat result at its rendered
// path plus the row version the confirm's CAS update demands.
type recoveryCompanionFact struct {
	ID              string `json:"id"`
	Version         int64  `json:"version"`
	TargetPath      string `json:"target_path"`
	Fingerprint     string `json:"fingerprint"`
	SizeBytes       int64  `json:"size_bytes"`
}

// probeRecoveryCandidates renders the current naming targets of every all-
// locators-missing placement, stats the exact rendered video path (and each
// rendered companion path) through the no-follow observer, and returns one
// candidate per placement whose COMPLETE unit qualifies: rendered target not
// among the missing locators, regular file with the frozen expected size,
// every companion at its rendered path with its registered size, and no other
// owner on any unit path. Every probe is a stat only: no directory
// enumeration, no content read, no byte is moved, copied, overwritten or
// deleted, and no ownership changes. A render failure for one run degrades
// that run to "no candidate" without failing the scan; the caller passes nil
// ports only in tests that do not exercise recovery.
func (s *Service) probeRecoveryCandidates(
	ctx context.Context,
	animeID string,
	generation int64,
	missing []ownedPlacement,
) []recoveryCandidateFact {
	if s.recoveryTargets == nil || s.recoveryObserver == nil || s.recoveryOccupancy == nil ||
		len(missing) == 0 || ctx.Err() != nil {
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
		if ctx.Err() != nil {
			return candidates
		}
		candidate, ok := probeOneRecoveryCandidate(ctx, s.recoveryObserver, targets, placement, generation)
		if !ok {
			continue
		}
		paths := []string{candidate.TargetPath}
		for _, companion := range candidate.Companions {
			paths = append(paths, companion.TargetPath)
		}
		occupied, err := s.recoveryOccupancy.RecoveryTargetsOccupied(ctx, paths, placement.ID)
		if err != nil || ctx.Err() != nil {
			// An occupancy read failure degrades this run: never freeze a
			// candidate whose ownership surface could not be read.
			return candidates
		}
		if occupied {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// probeOneRecoveryCandidate freezes one placement's candidate when the whole
// unit qualifies; ok=false leaves the placement with the plain missing
// disposition (no partial candidate, no gap to report).
func probeOneRecoveryCandidate(
	ctx context.Context,
	observer FileObserver,
	targets map[string]string,
	placement ownedPlacement,
	generation int64,
) (recoveryCandidateFact, bool) {
	target, ok := targets[placement.ID]
	if !ok {
		return recoveryCandidateFact{}, false
	}
	// The target must differ from every already-missing locator: a hit at a
	// locator path is the scan's own found-set business, never a candidate.
	for _, locator := range placement.LocatorPaths {
		if sameCleanPath(locator, target) {
			return recoveryCandidateFact{}, false
		}
	}
	// Expected size first (the placement identity's own priority: metadata
	// fingerprint, then typed fingerprint size, then the operation size). No
	// recorded size means no candidate: the frozen size is what makes the
	// observed file adoptable instead of just co-located.
	size, known := placement.frozenExpectedSize()
	if !known {
		return recoveryCandidateFact{}, false
	}
	if ctx.Err() != nil {
		return recoveryCandidateFact{}, false
	}
	observation, err := observer.ObserveFile(ctx, target)
	if err != nil {
		// Missing, non-regular or unreadable target: no candidate this run.
		return recoveryCandidateFact{}, false
	}
	if observation.SizeBytes != size {
		return recoveryCandidateFact{}, false
	}
	companions, ok := probeRecoveryCompanions(ctx, observer, targets, placement.Companions)
	if !ok {
		return recoveryCandidateFact{}, false
	}
	return recoveryCandidateFact{
		PlacementID:       placement.ID,
		PlacementVersion:  placement.Version,
		Generation:        generation,
		OldPhysicalPath:   filepath.Clean(placement.PhysicalPath),
		TargetPath:        target,
		TargetFingerprint: observation.Fingerprint,
		SizeBytes:         observation.SizeBytes,
		Companions:        companions,
	}, true
}

// probeRecoveryCompanions stats each companion's rendered path. One missing,
// unreadable or size-mismatched companion fails the WHOLE unit (ok=false):
// the video is never recoverable without its complete bound unit.
func probeRecoveryCompanions(
	ctx context.Context,
	observer FileObserver,
	targets map[string]string,
	companions []ownedCompanion,
) ([]recoveryCompanionFact, bool) {
	facts := make([]recoveryCompanionFact, 0, len(companions))
	for _, companion := range companions {
		target, ok := targets[companion.ID]
		if !ok {
			// No rendered path for a registered companion: the unit is
			// incomplete by construction.
			return nil, false
		}
		if sameCleanPath(companion.Path, target) {
			// The companion still sits at its current path; the scan's own
			// companion observation already decided its fate.
			return nil, false
		}
		if ctx.Err() != nil {
			return nil, false
		}
		observation, err := observer.ObserveFile(ctx, target)
		if err != nil || observation.SizeBytes != companion.SizeBytes {
			return nil, false
		}
		facts = append(facts, recoveryCompanionFact{
			ID:          companion.ID,
			Version:     companion.Version,
			TargetPath:  target,
			Fingerprint: observation.Fingerprint,
			SizeBytes:   observation.SizeBytes,
		})
	}
	return facts, true
}

// sameCleanPath compares two paths after cleaning (the scan's own path
// identity for explicit stat targets).
func sameCleanPath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}

// frozenExpectedSize derives the placement's frozen expected size with the
// same priority the calibration identity uses: a metadata fingerprint's size,
// then a typed fingerprint envelope's size_bytes, then the producing file
// operation's observed/expected target size. known=false means nothing
// recorded a size: no candidate, no guess.
func (p ownedPlacement) frozenExpectedSize() (int64, bool) {
	for _, value := range []string{p.TargetFingerprint, p.SourceFingerprint} {
		if facts, ok := domain.ParseMetadataFingerprint(value); ok {
			return facts.SizeBytes, true
		}
	}
	for _, value := range []string{p.TargetFingerprint, p.SourceFingerprint} {
		if size, ok := typedEnvelopeSize(value); ok {
			return size, true
		}
	}
	if p.OperationSize >= 0 {
		return p.OperationSize, true
	}
	return 0, false
}

// typedEnvelopeSize reads size_bytes from a typed fingerprint envelope
// ({"algorithm":...,"value":...,"size_bytes":n}).
func typedEnvelopeSize(stored string) (int64, bool) {
	trimmed := strings.TrimSpace(stored)
	if !strings.HasPrefix(trimmed, "{") {
		return 0, false
	}
	var envelope struct {
		SizeBytes *int64 `json:"size_bytes"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.SizeBytes == nil || *envelope.SizeBytes < 0 {
		return 0, false
	}
	return *envelope.SizeBytes, true
}
