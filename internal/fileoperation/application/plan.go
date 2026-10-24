package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	librarydomain "bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// The dedicated post-processing plan stage. A verified artifact no
// longer couples its success transaction to naming/target planning: the
// artifact-success transaction persists one queued postprocessing_plans row
// plus its claimable postprocess_plan WorkItem, and the naming engine
// (prepareForArtifactTxWithRef, the single shared implementation) executes in
// the plan stage's own transaction. Plan failures are bounded-retryable and
// terminally recorded on the plan owner without rewriting the verified
// artifact fact. Manual planning retry/replan is retired; only the fixed
// interrupted recovery transition may requeue within the original budget.

const (
	planOutcomeCompleted   = "plan_completed"
	planOutcomeFailed      = "plan_failed"
	planRetryAfter         = 30 * time.Second
	planTerminalRetryAfter = 0
	// planBudgetExhaustedCode is the typed terminal failure when the
	// business attempt budget (max attempts or frozen wall) is spent.
	planBudgetExhaustedCode = "PLAN_BUDGET_EXHAUSTED"
)

type postProcessingPlanRow struct {
	id              string
	artifactID      string
	artifactVersion int64
	oneShotID       sql.NullString
	state           string
	inputDigest     string
	version         int64
}

func scanPlanRow(row interface{ Scan(...any) error }) (*postProcessingPlanRow, error) {
	var plan postProcessingPlanRow
	if err := row.Scan(&plan.id, &plan.artifactID, &plan.artifactVersion, &plan.oneShotID, &plan.state, &plan.inputDigest, &plan.version); err != nil {
		return nil, err
	}
	return &plan, nil
}

func nullablePlanText(oneShot *workitems.OneShotRef) any {
	if oneShot == nil {
		return nil
	}
	return oneShot.MarkerID
}

// handlePostProcessPlan executes one plan claim: naming/target planning from
// the frozen verified artifact via the shared prepare implementation. The plan
// owner state mirrors the WorkItem outcome; a terminal failure records the
// typed outcome on the plan without touching the verified artifact fact.
func (s *Service) handlePostProcessPlan(ctx context.Context, claim workitems.Claim) (runtimeworker.Outcome, error) {
	if claim.Kind != workitems.KindPostProcessPlan {
		return runtimeworker.Outcome{}, fmt.Errorf("%w: %q", ErrInvalidKind, claim.Kind)
	}
	if ctx == nil {
		return runtimeworker.Outcome{}, errors.New("post-processing plan handler context is nil")
	}
	if claim.Owner.Type != workitems.OwnerPostProcessingPlan || strings.TrimSpace(claim.Owner.ID) == "" {
		return runtimeworker.Outcome{}, fmt.Errorf("%w: post-processing plan claim owner mismatch", workitems.ErrInvalidSpec)
	}
	if s == nil || s.db == nil {
		return runtimeworker.Outcome{}, errors.New("file operation service is not configured")
	}

	plan, err := s.loadPlan(ctx, claim.Owner.ID)
	if err != nil {
		return runtimeworker.Outcome{}, err
	}
	if plan.state == "succeeded" || plan.state == "failed" {
		return blockedPlanRecovery("PLAN_TERMINAL_WORK_LIVE"), nil
	}

	var oneShot *workitems.OneShotRef
	if claim.OneShot != nil && plan.oneShotID.Valid && claim.OneShot.MarkerID == plan.oneShotID.String {
		oneShot = claim.OneShot
	} else if plan.oneShotID.Valid {
		return runtimeworker.Outcome{}, fmt.Errorf("%w: plan declares one-shot %q but the claim does not carry it", workitems.ErrInvalidOneShotClaim, plan.oneShotID.String)
	}

	// Attempt-budget gate: the checks above cost only the claim; capture and
	// naming read the filesystem and cost one business attempt, bounded by the
	// ticket's attempt_until. Exhaustion is the typed terminal plan failure.
	ticket, gateErr := s.workItems.StartStageAttempt(ctx, claim, "postprocess_plan:"+uuid.NewString())
	if errors.Is(gateErr, workitems.ErrStageBudgetExhausted) {
		return s.typedPlanError(ctx, claim, &apperrors.Business{Code: planBudgetExhaustedCode, Message: "the planning attempt budget is exhausted"}, true)
	}
	if gateErr != nil {
		return runtimeworker.Outcome{}, gateErr
	}
	prepareCtx, cancelPrepare := context.WithDeadline(ctx, ticket.AttemptUntil)
	defer cancelPrepare()
	batch, prepareErr := s.captureArtifactBatch(prepareCtx, plan)
	if prepareErr == nil {
		prepareErr = s.buildArtifactBatch(prepareCtx, batch, oneShot)
	}
	if prepareErr == nil {
		out, commitErr := s.commitTypedPlan(ctx, claim, batch)
		var pg *pgconn.PgError
		if errors.As(commitErr, &pg) && pg.Code == "23514" && pg.Message == "PLAN_CAPTURE_CHANGED" {
			return s.typedPlanError(ctx, claim, fmt.Errorf("%w: locked capture changed", errPlanCaptureChanged), false)
		}
		return out, commitErr
	}
	prepareErr = prepareAdmissionError(prepareErr)
	if errors.Is(prepareErr, workitems.ErrStaleClaim) || errors.Is(prepareErr, workitems.ErrOwnerRetired) {
		return runtimeworker.Outcome{}, prepareErr
	}
	var business *apperrors.Business
	deterministic := errors.As(prepareErr, &business)
	return s.typedPlanError(ctx, claim, prepareErr, deterministic)
}

func (s *Service) loadPlan(ctx context.Context, planID string) (*postProcessingPlanRow, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, artifact_id, artifact_version, one_shot_id, state, input_digest, version
FROM postprocessing_plans WHERE id = $1`, planID)
	plan, err := scanPlanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: post-processing plan %q", workitems.ErrNotFound, planID)
	}
	if err != nil {
		return nil, fmt.Errorf("read post-processing plan: %w", err)
	}
	return plan, nil
}

func planTerminalCode(err error) string {
	if errors.Is(err, errPlanCaptureChanged) {
		return "PLAN_CAPTURE_CHANGED"
	}
	if errors.Is(err, errTargetObservationUnavailable) {
		return librarydomain.CodeTargetUnreadable
	}
	var business *apperrors.Business
	if errors.As(err, &business) && strings.TrimSpace(business.Code) != "" {
		return boundedCode(business.Code, planOutcomeFailed)
	}
	return planOutcomeFailed
}

// PostProcessingPlanHandler adapts the plan stage to the worker Handler
// interface so the composition root registers it for KindPostProcessPlan.
type PostProcessingPlanHandler struct {
	service *Service
	hub     *events.Hub
	logger  observabilityport.RuntimeLogger
}

func (s *Service) PostProcessingPlanHandler() PostProcessingPlanHandler {
	return PostProcessingPlanHandler{service: s, logger: observabilityport.NopRuntimeLogger{}}
}

func (h PostProcessingPlanHandler) Handle(ctx context.Context, claim workitems.Claim) (runtimeworker.Outcome, error) {
	outcome, err := h.service.handlePostProcessPlan(ctx, claim)
	if err != nil || h.service == nil {
		return outcome, err
	}
	// The plan stage performs no external effect: every terminal outcome is a
	// provable no-external-effect disposition, so a one-shot marker lease
	// settles without blocking reclamation.
	if claim.OneShot != nil && outcome.Disposition == "" {
		outcome.Disposition = workitems.DispositionNoExternalEffect
	}
	if h.service.eventHub == nil {
		return outcome, nil
	}
	outcome.AfterCommit = h.service.eventHub.AfterCommitFor(events.TargetRSS, claim.ManagedAnimeID, outcome.AfterCommit)
	return outcome, nil
}
