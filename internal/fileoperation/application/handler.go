package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/events"
	"bansoko/internal/library/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Handle executes only file_execute claims. The WorkItem store owns the outer
// fencing transaction. Every file operation is planned by a committed
// post-processing plan (file_operations.postprocessing_plan_id is NOT NULL),
// so a normal claim always runs the typed publication lane. A placement
// continuation is the post-completion placement stage of an already completed
// operation: it never enters the typed effect lane, which would treat it as a
// new execution.
func (s *Service) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Kind != workitems.KindFileExecute {
		return worker.Outcome{}, fmt.Errorf("%w: %q", ErrInvalidKind, claim.Kind)
	}
	if ctx == nil {
		return worker.Outcome{}, errors.New("file operation handler context is nil")
	}
	if claim.Owner.Type != workitems.OwnerFileOperation || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, ErrInvalidClaim
	}
	if s == nil || s.db == nil {
		return worker.Outcome{}, ErrInvalidClaim
	}
	var outcome worker.Outcome
	var err error
	switch input, continuationClass := classifyPlacementContinuation(claim); continuationClass {
	case continuationNormal:
		return s.handleTypedExecution(ctx, claim)
	case continuationMalformed:
		// A claim that declares placement intent with a malformed
		// discriminator fails closed and never reaches the file-effect path.
		outcome, err = s.handleMalformedPlacementContinuation(ctx, claim)
	default:
		outcome, err = s.handlePlacementContinuation(ctx, claim, input)
	}
	if err != nil {
		return outcome, err
	}
	// One-shot disposition: the continuation result settles the marker-owned
	// admission lease. Assigned before the optional event-hub wrap so the
	// disposition never depends on hub wiring.
	if claim.OneShot != nil {
		outcome.Disposition = fileOperationDisposition(outcome)
	}
	if s.eventHub == nil {
		return outcome, nil
	}
	outcome.AfterCommit = s.eventHub.AfterCommitFor(events.TargetRSS, claim.ManagedAnimeID, outcome.AfterCommit)
	return outcome, nil
}

// fileOperationDisposition classifies a file_execute outcome for one-shot
// settlement:
//
//   - successful finish: the target was atomically published and its exact
//     size reread — confirmed;
//   - EFFECT_UNCERTAIN: the publication checkpoint cannot prove what happened
//     — ambiguous, the lease stays and cleanup remains blocked;
//   - SOURCE_MISSING with no satisfied target: provably no mutation landed —
//     no_external_effect;
//   - every other terminal failure carries a known external disposition per
//     the FileOperation contract — confirmed;
//   - retries are transient/unknown by definition — ambiguous.
func fileOperationDisposition(outcome worker.Outcome) workitems.EffectDisposition {
	switch outcome.Kind {
	case worker.OutcomeFinish:
		if outcome.TerminalState == workitems.StateSucceeded {
			return workitems.DispositionConfirmed
		}
		switch outcome.OwnerResult.ReasonCode {
		case "EFFECT_UNCERTAIN":
			return workitems.DispositionAmbiguous
		case "SOURCE_MISSING":
			return workitems.DispositionNoExternalEffect
		default:
			return workitems.DispositionConfirmed
		}
	case worker.OutcomeRetry:
		return workitems.DispositionAmbiguous
	default:
		return workitems.DispositionAmbiguous
	}
}

// RecoveryFallback keeps a file_execute claim whose handler failed or
// panicked leased for the ticketed recovery lane: the typed publication
// evidence, never an ordinary retry, decides what the interrupted attempt did.
func (s *Service) RecoveryFallback(ctx context.Context, claim workitems.Claim, _ error) (worker.Outcome, bool) {
	if ctx == nil || s == nil || s.db == nil {
		return worker.Outcome{}, false
	}
	if claim.Kind != workitems.KindFileExecute || claim.Owner.Type != workitems.OwnerFileOperation || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, false
	}
	if exists, err := s.operationExists(ctx, claim); err == nil && !exists {
		return worker.Outcome{}, false
	}
	return typedExecutionBlocked(), true
}

func (s *Service) loadOperation(ctx context.Context, operationID string) (operationRecord, error) {
	var record operationRecord
	err := scanOperation(s.db.QueryRowContext(ctx, operationQuery+" WHERE id = $1", operationID), &record)
	if errors.Is(err, sql.ErrNoRows) {
		return operationRecord{}, fmt.Errorf("%w: %q", ErrOperationNotFound, operationID)
	}
	if err != nil {
		return operationRecord{}, fmt.Errorf("read file operation: %w", err)
	}
	return record, nil
}

func observeTarget(ctx context.Context, observer FileObserver, path string, requireSize bool) (domain.FileObservation, error) {
	if sizeAware, ok := observer.(SizeAwareFileObserver); ok {
		observation, err := sizeAware.ObserveFile(ctx, path)
		if err != nil {
			return domain.FileObservation{}, err
		}
		if observation.SizeBytes < 0 {
			return domain.FileObservation{}, errors.New("size-aware file observation returned a negative size")
		}
		return observation, nil
	}
	if requireSize {
		return domain.FileObservation{}, ErrSizeAwareObserverRequired
	}
	fingerprint, err := observer.Observe(ctx, path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	return domain.FileObservation{Fingerprint: fingerprint, SizeBytes: -1}, nil
}

func validateClaimInput(claim workitems.Claim, operation operationRecord) error {
	if claim.Owner.Type != workitems.OwnerFileOperation || claim.Owner.ID != operation.id || claim.Kind != workitems.KindFileExecute {
		return ErrInvalidClaim
	}
	var input struct {
		OperationID    string `json:"operation_id"`
		ArtifactID     string `json:"artifact_id"`
		ArtifactFileID string `json:"artifact_file_id"`
	}
	if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil {
		return fmt.Errorf("invalid file execute input: %w", err)
	}
	if input.OperationID != operation.id || input.ArtifactID != operation.artifactID || input.ArtifactFileID != operation.artifactFileID {
		return ErrInvalidClaim
	}
	return nil
}

func invalidSnapshot(operation operationRecord) bool {
	var snapshot struct {
		Validation struct {
			State string `json:"state"`
		} `json:"validation"`
	}
	if json.Unmarshal(operation.renderSnapshot, &snapshot) != nil {
		return false
	}
	return snapshot.Validation.State == "blocked"
}
