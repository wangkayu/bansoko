package workitems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	"github.com/jackc/pgx/v5/pgconn"
)

// The fixed start primitives
// count one business attempt per real inspection/planning run. The gate sits
// before the stage's first read of external state, so:
//   - a crash before the gate consumed only the WorkItem claim attempt;
//   - a crash after the gate consumed a business attempt (the ticket stays in
//     the index and the next claim supersedes it — superseded grants nothing);
//   - the same (work, token) is never re-issued (ErrStageAttemptAlreadyIssued);
//   - wall/max exhaustion is ErrStageBudgetExhausted, which the stage turns
//     into its typed terminal failure. No work is created, no budget reset.
var (
	ErrStageAttemptAlreadyIssued = errors.New("stage attempt already issued for this claim token")
	ErrStageBudgetExhausted      = errors.New("stage business attempt budget exhausted")
)

// StageAttemptTicket mirrors the submit_attempt_ticket jsonb. It is a computation
// budget for one claim token, never permission to mutate anything.
type StageAttemptTicket struct {
	WorkItemID      string    `json:"work_item_id"`
	Token           int64     `json:"token"`
	Ordinal         int       `json:"ordinal"`
	ExecutorSession string    `json:"executor_session"`
	StartedAt       time.Time `json:"started_at"`
	AttemptUntil    time.Time `json:"attempt_until"`
	State           string    `json:"state"`
}

type stageAttemptPrimitive struct {
	owner, sql, exhausted, stale string
}

// The primitive is fixed by the claim kind; callers never name SQL.
var stageAttemptPrimitives = map[string]stageAttemptPrimitive{
	KindArtifactInspect: {owner: OwnerArtifact, sql: `SELECT public.artifact_start_attempt($1,$2,$3,$4)`, exhausted: "INSPECT_BUDGET_EXHAUSTED", stale: "INSPECT_STAGE_STALE_CLAIM"},
	KindPostProcessPlan: {owner: OwnerPostProcessingPlan, sql: `SELECT public.plan_start_attempt($1,$2,$3,$4)`, exhausted: "PLAN_BUDGET_EXHAUSTED", stale: "PLAN_STAGE_STALE_CLAIM"},
}

// StartStageAttempt issues the business attempt ticket for an inspect or
// plan claim in its own typed stage transaction (one-shot marker first; the
// admission lease is not consumed). session identifies the executor run that
// is about to do the stage I/O (1..128 bytes).
func (s *Store) StartStageAttempt(ctx context.Context, c Claim, session string) (StageAttemptTicket, error) {
	p, ok := stageAttemptPrimitives[c.Kind]
	if s == nil || s.DB == nil || ctx == nil || !ok || c.Owner.Type != p.owner || c.Owner.ID == "" || c.ID == "" || len(session) < 1 || len(session) > 128 {
		return StageAttemptTicket{}, ErrInvalidSpec
	}
	var grant struct {
		Granted    bool               `json:"granted"`
		Ticket     StageAttemptTicket `json:"ticket"`
		ReasonCode string             `json:"reason_code"`
	}
	err := s.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, p.sql, c.Owner.ID, c.ID, c.FencingToken, session).Scan(&raw); err != nil {
			return DispositionAmbiguous, err
		}
		if err := json.Unmarshal(raw, &grant); err != nil {
			return DispositionAmbiguous, err
		}
		if !grant.Granted {
			// Nothing was written; roll the transaction back.
			return DispositionAmbiguous, ErrStageAttemptAlreadyIssued
		}
		t := grant.Ticket
		if t.WorkItemID != c.ID || t.Token != c.FencingToken || t.Ordinal < 1 || t.Ordinal > 100 || t.ExecutorSession != session || t.State != "issued" || !t.AttemptUntil.After(t.StartedAt) {
			return DispositionAmbiguous, fmt.Errorf("invalid stage attempt grant")
		}
		return DispositionAmbiguous, nil
	})
	var pg *pgconn.PgError
	switch {
	case err == nil:
		return grant.Ticket, nil
	case errors.As(err, &pg) && pg.Code == "23514" && pg.Message == p.exhausted:
		return StageAttemptTicket{}, fmt.Errorf("%w: %s", ErrStageBudgetExhausted, p.exhausted)
	case errors.As(err, &pg) && pg.Code == "23514" && pg.Message == p.stale:
		return StageAttemptTicket{}, fmt.Errorf("%w: %s", ErrStaleClaim, p.stale)
	}
	return StageAttemptTicket{}, err
}

// Claim (WorkItem) attempt budgets of the inspect and plan stages. The work
// items are created inside SQL with these literals, and the recovery requeue
// judges exhaustion by the claim budget, so the business attempt budget
// (KindPolicy MaxAttempts → limits snapshot → the migration DDL DEFAULT) must
// never exceed these. Changing this ordering requires a forward migration
// that moves recovery onto the business budget first.
// Tripwire: TestProductionLimitsBusinessBudgetWithinClaimBudget.
const (
	ArtifactInspectClaimMaxAttempts = 5
	PostProcessPlanClaimMaxAttempts = 5
)
