package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/jackc/pgx/v5/pgconn"
)

// A recovery observation never prepares a plan or calls a filesystem port.
// Its sole write is the planning-only transition whose locked checks are the
// authority. Terminal/live and legacy running graphs retain all evidence.
func (s *Service) observePlanRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if c.Kind != workitems.KindPostProcessPlan || c.Owner.Type != workitems.OwnerPostProcessingPlan || strings.TrimSpace(c.Owner.ID) == "" {
		return worker.Outcome{}, ErrInvalidClaim
	}
	if ctx == nil || s == nil || s.db == nil {
		return worker.Outcome{}, ErrInvalidClaim
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return worker.Outcome{}, err
	}
	defer tx.Rollback()
	var state string
	var live bool
	err = tx.QueryRowContext(ctx, `SELECT p.state,EXISTS(SELECT 1 FROM work_items w WHERE w.owner_type='postprocessing_plan' AND w.owner_id=p.id AND w.kind='postprocess_plan' AND w.state IN ('available','leased')) FROM postprocessing_plans p WHERE p.id=$1`, c.Owner.ID).Scan(&state, &live)
	if err != nil {
		return worker.Outcome{}, err
	}
	if (state == "succeeded" || state == "failed") && live {
		return blockedPlanRecovery("PLAN_TERMINAL_WORK_LIVE"), nil
	}
	if state == "succeeded" || state == "failed" {
		if err := validateObservedPlanLedger(ctx, tx, c.Owner.ID); err != nil {
			if errors.Is(err, workitems.ErrInvalidOneShotLedger) {
				return blockedPlanRecovery("PLAN_RECOVERY_GRAPH_INCONSISTENT"), nil
			}
			return worker.Outcome{}, err
		}
		if err := validateObservedSatisfactionProof(ctx, tx, c.Owner.ID); err != nil {
			return blockedPlanRecovery("PLAN_RECOVERY_GRAPH_INCONSISTENT"), err
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
			return worker.Outcome{}, err
		}
		if len(raw) > 0 && string(raw) != "null" {
			var reply struct {
				State string `json:"state"`
				Code  string `json:"reason_code"`
			}
			if err := json.Unmarshal(raw, &reply); err != nil {
				return worker.Outcome{}, err
			}
			if err := tx.Commit(); err != nil {
				return worker.Outcome{}, err
			}
			out := worker.Committed(c, workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code})
			out.Disposition = workitems.DispositionNoExternalEffect
			return out, nil
		}
		return blockedPlanRecovery("PLAN_RECOVERY_GRAPH_INCONSISTENT"), nil
	}
	if state != "queued" || !live {
		return blockedPlanRecovery("PLAN_RECOVERY_GRAPH_INCONSISTENT"), nil
	}
	if err := tx.Commit(); err != nil {
		return worker.Outcome{}, err
	}
	return s.planRecoveryTransaction(ctx, c)
}

func blockedPlanRecovery(code string) worker.Outcome {
	out := worker.AwaitRecovery(code)
	out.OwnerResult = workitems.OwnerResult{ReasonCode: code}
	return out
}

func (s *Service) planRecoveryTransaction(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	var result workitems.OwnerResult
	store := &workitems.Store{DB: s.db}
	err := store.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.postprocessing_plan_requeue_interrupted($1,$2,$3,$4)`, c.Owner.ID, c.ID, c.FencingToken, planRetryAfter.Seconds()).Scan(&raw); err != nil {
			return "", err
		}
		var reply struct {
			State string `json:"state"`
			Code  string `json:"reason_code"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		result = workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23514" {
			switch strings.TrimSpace(pg.Message) {
			case "PLAN_TERMINAL_WORK_LIVE", "PLAN_RECOVERY_GRAPH_INCONSISTENT":
				return blockedPlanRecovery(pg.Message), nil
			}
		}
		// Commit unknown is an error until an independent complete proof exists.
		// Never manufacture a Committed receipt or retry the transaction here.
		return worker.Outcome{}, err
	}
	out := worker.Committed(c, result)
	out.Disposition = workitems.DispositionNoExternalEffect
	return out, nil
}
