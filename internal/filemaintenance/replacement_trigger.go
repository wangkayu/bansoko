package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"bansoko/internal/observability/port"
	"bansoko/internal/workitems"
)

// TriggerReason is the typed trigger identity of one replacement reevaluation.
// Every reason funnels through the same trigger port: automatic/force/Agent
// placements share TriggerPlacementCommitted (placements has no source
// column), instance policy saves use TriggerPolicySaved, Subscription resume
// uses TriggerSubscriptionResumed, and the completed-generation replacement
// consumer (reconciliation closure child) uses TriggerGenerationCompleted.
const (
	TriggerPlacementCommitted  = "placement_committed"
	TriggerPolicySaved         = "policy_saved"
	TriggerSubscriptionResumed = "subscription_resumed"
	TriggerGenerationCompleted = "generation_completed"
)

// triggerMaxAttempts bounds the evaluate claim retries; the frozen evaluate
// owner is idempotent, so every replay converges on the same decision. It
// matches the worker registry lane policy for the FileMaintenance kinds.
const triggerMaxAttempts = 3

// ReplacementTriggerService is the after-commit replacement trigger port. It
// is the single entry for every incremental reevaluation source: it reads the
// Subscription (phase-1 active gate), resolves the candidate release, and
// creates or merges the one live replacement_evaluate WorkItem per anime
// through a stable idempotency key. It never executes settle/cleanup and is
// never called inside the owner transaction: the caller commits the owner
// truth first, then invokes the trigger; a trigger failure records an
// observable diagnostic episode and never rolls the owner back.
type ReplacementTriggerService struct {
	db        *sql.DB
	workItems *workitems.Store
	recorder  port.Recorder
	logger    port.RuntimeLogger
	now       func() time.Time
}

// NewReplacementTriggerService wires the trigger port.
func NewReplacementTriggerService(db *sql.DB, workItems *workitems.Store) *ReplacementTriggerService {
	return &ReplacementTriggerService{db: db, workItems: workItems, now: time.Now}
}

// SetDiagnostics wires the optional observable-failure recorder and structured
// runtime logger used when a trigger cannot create/merge the WorkItem.
func (s *ReplacementTriggerService) SetDiagnostics(recorder port.Recorder, logger port.RuntimeLogger) {
	if s == nil {
		return
	}
	s.recorder = recorder
	s.logger = logger
}

// TriggerPlacementCommitted fires after a verified active Placement committed:
// the candidate release is the release that owns the just-admitted placement.
func (s *ReplacementTriggerService) TriggerPlacementCommitted(ctx context.Context, animeID, releaseID string) error {
	return s.trigger(ctx, TriggerPlacementCommitted, animeID, releaseID)
}

// TriggerPolicySaved fires after an instance retention policy CAS save
// committed. The candidate is the latest verified active release-backed
// placement, so the saved policy re-freezes the current facts.
func (s *ReplacementTriggerService) TriggerPolicySaved(ctx context.Context, animeID string) error {
	return s.trigger(ctx, TriggerPolicySaved, animeID, "")
}

// TriggerSubscriptionResumed fires after a stopped -> active transition
// committed. It creates a NEW reevaluation epoch (the Subscription version
// bumped on stop/resume), reads the latest Placement facts from the paused
// period, and never replays a pre-stop decision id (the frozen decision
// digest carries the new Subscription version).
func (s *ReplacementTriggerService) TriggerSubscriptionResumed(ctx context.Context, animeID string) error {
	return s.trigger(ctx, TriggerSubscriptionResumed, animeID, "")
}

// TriggerGenerationCompleted is the replacement consumer entry of a completed
// reconciliation generation (the closure child dispatches it). It is gated by
// the same phase-1 active check as every other trigger; the neutral scan and
// alignment consumers are independent and never pass through this port.
func (s *ReplacementTriggerService) TriggerGenerationCompleted(ctx context.Context, animeID string) error {
	return s.trigger(ctx, TriggerGenerationCompleted, animeID, "")
}

// trigger is the single typed entry. Phase-1 admission: a stopped or missing
// Subscription is a safe no-op (resume creates a fresh epoch); only an active
// Subscription creates/merges the live evaluate WorkItem. The idempotency key
// is stable per anime so different independent triggers merge into the latest
// unexecuted input (CreateOrMerge refreshes input snapshot and owner shape).
func (s *ReplacementTriggerService) trigger(ctx context.Context, reason, animeID, releaseID string) error {
	if s == nil || s.db == nil || s.workItems == nil {
		err := errors.New("replacement trigger is not fully wired")
		s.recordFailure(ctx, animeID, err)
		return err
	}
	if strings.TrimSpace(animeID) == "" {
		err := errors.New("replacement trigger requires a managed anime id")
		s.recordFailure(ctx, animeID, err)
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.recordFailure(ctx, animeID, err)
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	var subVersion int64
	err = tx.QueryRowContext(ctx, `
SELECT state, version FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, animeID).Scan(&state, &subVersion)
	if errors.Is(err, sql.ErrNoRows) {
		s.logEvent(ctx, port.RuntimeEvent{
			Message:    "replacement trigger " + reason + " skipped",
			Component:  "replacement",
			Operation:  "evaluate_trigger",
			Outcome:    "skipped",
			ReasonCode: "subscription_missing",
			OwnerType:  "managed_anime",
			OwnerID:    animeID,
		})
		return nil
	}
	if err != nil {
		s.recordFailure(ctx, animeID, err)
		return err
	}
	if state != "active" {
		// Stopped: no automatic evaluate/supersede/cleanup. Force/Agent files
		// stay ordinary Placements; resume creates a new epoch.
		s.logEvent(ctx, port.RuntimeEvent{
			Message:    "replacement trigger " + reason + " skipped",
			Component:  "replacement",
			Operation:  "evaluate_trigger",
			Outcome:    "skipped",
			ReasonCode: "subscription_stopped",
			OwnerType:  "managed_anime",
			OwnerID:    animeID,
		})
		return nil
	}
	if releaseID == "" {
		releaseID, err = s.latestPlacementRelease(ctx, tx, animeID)
		if err != nil {
			s.recordFailure(ctx, animeID, err)
			return err
		}
		if releaseID == "" {
			s.logEvent(ctx, port.RuntimeEvent{
				Message:    "replacement trigger " + reason + " skipped",
				Component:  "replacement",
				Operation:  "evaluate_trigger",
				Outcome:    "skipped",
				ReasonCode: "no_release_placement",
				OwnerType:  "managed_anime",
				OwnerID:    animeID,
			})
			return nil
		}
	}
	snapshot, err := json.Marshal(map[string]any{
		"candidate_release_id": releaseID,
		"trigger":              reason,
		"subscription_version": subVersion,
	})
	if err != nil {
		s.recordFailure(ctx, animeID, err)
		return err
	}
	_, _, err = s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:          workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID},
		Kind:           workitems.KindReplacementEvaluate,
		IdempotencyKey: "replacement_evaluate:" + animeID,
		InputSnapshot:  snapshot,
		MaxAttempts:    triggerMaxAttempts,
		// The claim's expected owner version carries the frozen Subscription
		// version; EvaluateServer re-reads and re-freezes it inside its own
		// transaction regardless of this hint.
		ExpectedOwnerVersion: subVersion,
	})
	if err != nil {
		err = errors.New("create or merge replacement_evaluate work item: " + err.Error())
		s.recordFailure(ctx, animeID, err)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.recordFailure(ctx, animeID, err)
		return err
	}
	s.logEvent(ctx, port.RuntimeEvent{
		Message:    "replacement trigger " + reason + " merged",
		Component:  "replacement",
		Operation:  "evaluate_trigger",
		Outcome:    "merged",
		ReasonCode: "trigger_merged",
		OwnerType:  "managed_anime",
		OwnerID:    animeID,
	})
	if s.recorder != nil {
		identity := port.ReplacementTriggerFailure()
		if err := s.recorder.RecordDiagnosticSuccess(ctx, port.DiagnosticSuccessRequest{
			Identity: identity, ScopeID: animeID,
		}); err != nil {
			port.RecordDiagnosticError(s.logger, string(identity.Component), string(identity.Operation), animeID, string(identity.ErrorCode), "diagnostic_write_failed")
		}
	}
	return nil
}

// latestPlacementRelease resolves the release backing the most recent verified
// active file_operation placement (the candidate for policy-save/resume/
// generation triggers). Reconciliation placements have no release chain and
// can never be evaluate candidates; an empty result skips the trigger.
func (s *ReplacementTriggerService) latestPlacementRelease(ctx context.Context, tx *sql.Tx, animeID string) (string, error) {
	var releaseID sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT p.quality_facts_ref
FROM placements p
WHERE p.managed_anime_id = $1
  AND p.verification_state = 'verified' AND p.state = 'active'
  AND p.source_evidence_type = 'file_operation'
  AND p.quality_facts_ref IS NOT NULL
ORDER BY p.created_at DESC, p.id DESC
LIMIT 1`, animeID).Scan(&releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return releaseID.String, nil
}

// recordFailure makes an async orchestration failure observable: the
// diagnostic episode owner is updated best-effort and the operator log line
// always fires. The owner truth (Placement/Subscription/policy) is already
// committed and never rolled back by a trigger failure.
func (s *ReplacementTriggerService) recordFailure(ctx context.Context, animeID string, cause error) {
	if s == nil {
		return
	}
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
		if len(lastError) > 300 {
			lastError = lastError[:300]
		}
	}
	if s.recorder != nil {
		identity := port.ReplacementTriggerFailure()
		if err := s.recorder.RecordDiagnosticFailure(ctx, port.DiagnosticFailureRequest{
			Identity: identity, Severity: port.SeverityError,
			ScopeID: animeID, ManagedAnimeID: animeID, LastError: lastError,
		}); err != nil {
			port.RecordDiagnosticError(s.logger, string(identity.Component), string(identity.Operation), animeID, string(identity.ErrorCode), "diagnostic_write_failed")
		}
	}
	if s.logger != nil {
		s.logger.Error(ctx, port.RuntimeEvent{
			Message:    "replacement trigger failure recorded",
			Component:  "replacement",
			Operation:  "evaluate_trigger",
			Outcome:    "failure",
			ReasonCode: "replacement_trigger_failed",
			OwnerType:  "managed_anime",
			OwnerID:    animeID,
			ErrorClass: "replacement_trigger",
		})
	}
}

// logEvent emits one structured runtime event for a non-failure trigger
// decision. The trigger never logs raw errors; failure details live in the
// durable diagnostic episode and the ErrorClass field above.
func (s *ReplacementTriggerService) logEvent(ctx context.Context, event port.RuntimeEvent) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Info(ctx, event)
}
