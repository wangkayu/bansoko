package application

import (
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
)

// Observe only; an unknown Commit never grants permission to prepare again or
// to replay the write transaction. Identity and the entire graph are checked
// in one repeatable-read transaction, including the facts/settle obligation.
func (s *Service) confirmTypedPlanCommit(ctx context.Context, c workitems.Claim) (worker.Outcome, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return worker.Outcome{}, false, err
	}
	defer tx.Rollback()
	if err := validateObservedPlanLedger(ctx, tx, c.Owner.ID); err != nil {
		return worker.Outcome{}, false, err
	}
	if err := validateObservedSatisfactionProof(ctx, tx, c.Owner.ID); err != nil {
		return worker.Outcome{}, false, err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
		return worker.Outcome{}, false, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return worker.Outcome{}, false, nil
	}
	var reply struct {
		State string `json:"state"`
		Code  string `json:"reason_code"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return worker.Outcome{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return worker.Outcome{}, false, err
	}
	out := worker.Committed(c, workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code})
	out.Disposition = workitems.DispositionNoExternalEffect
	return out, true, nil
}
