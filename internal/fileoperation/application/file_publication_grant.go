package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// filePublicationGrant returns true only after this new CAS has committed.
// A commit error discards the acknowledgement; a reread never grants replay.
func (s *Service) filePublicationGrant(ctx context.Context, c workitems.Claim, permit string, session domain.PublicationSession, step string) (bool, error) {
	granted, _, err := s.filePublicationGrantDeadline(ctx, c, permit, session, step)
	return granted, err
}

func (s *Service) filePublicationGrantDeadline(ctx context.Context, c workitems.Claim, permit string, session domain.PublicationSession, step string) (bool, time.Time, error) {
	if s == nil || s.db == nil || ctx == nil || c.Kind != workitems.KindFileExecute || session.ExecutorSession != session.BootNonce+":"+session.CallNonce {
		return false, time.Time{}, ErrInvalidClaim
	}
	store, err := workitems.NewStore(s.db, 5, nil)
	if err != nil {
		return false, time.Time{}, err
	}
	var reply struct {
		Granted      bool      `json:"granted"`
		Permit       string    `json:"permit_id"`
		Session      string    `json:"executor_session"`
		Step         string    `json:"step"`
		GateDBNow    time.Time `json:"gate_db_now"`
		AttemptUntil time.Time `json:"attempt_until"`
	}
	callStarted := time.Now()
	err = store.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if step == "" {
			err = scope.SQLTx().QueryRowContext(ctx, `SELECT public.file_start_dispatch($1,$2,$3,$4,$5)`, c.Owner.ID, c.ID, c.FencingToken, permit, session.ExecutorSession).Scan(&raw)
		} else {
			err = scope.SQLTx().QueryRowContext(ctx, `SELECT public.file_start_step($1,$2,$3,$4,$5,$6)`, c.Owner.ID, c.ID, c.FencingToken, permit, session.ExecutorSession, step).Scan(&raw)
		}
		if err != nil {
			return workitems.DispositionAmbiguous, err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return workitems.DispositionAmbiguous, err
		}
		if reply.Granted && (reply.Permit != permit || reply.Session != session.ExecutorSession || reply.Step != step) {
			return workitems.DispositionAmbiguous, fmt.Errorf("publication grant identity mismatch")
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return false, time.Time{}, err
	}
	if !reply.Granted {
		return false, time.Time{}, nil
	}
	deadline, err := publicationGrantDeadline(callStarted, reply.GateDBNow, reply.AttemptUntil, time.Now())
	if err != nil {
		return false, time.Time{}, err
	}
	return true, deadline, nil
}
