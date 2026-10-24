package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// fileAttemptTicket is a computation budget, never permission to mutate a file.
// Dispatch and every mutating step require their own clearly committed grant.
type fileAttemptTicket struct {
	WorkItemID      string    `json:"work_item_id"`
	Token           int64     `json:"token"`
	Ordinal         int       `json:"ordinal"`
	ExecutorSession string    `json:"executor_session"`
	StartedAt       time.Time `json:"started_at"`
	AttemptUntil    time.Time `json:"attempt_until"`
	State           string    `json:"state"`
}

type fileAttemptGrant struct {
	Granted    bool              `json:"granted"`
	Ticket     fileAttemptTicket `json:"ticket"`
	ReasonCode string            `json:"reason_code"`
}

// startFileAttempt returns no grant on a failed or uncertain commit. Reading an
// already issued ticket is useful for diagnosis but never authorizes replay.
// Ambiguous keeps the one-shot admission open for the later file disposition.
func (s *Service) startFileAttempt(ctx context.Context, c workitems.Claim, session string) (fileAttemptGrant, error) {
	if s == nil || s.db == nil || ctx == nil || c.Kind != workitems.KindFileExecute || c.Owner.Type != workitems.OwnerFileOperation {
		return fileAttemptGrant{}, ErrInvalidClaim
	}
	store, err := workitems.NewStore(s.db, 5, nil)
	if err != nil {
		return fileAttemptGrant{}, err
	}
	var grant fileAttemptGrant
	err = store.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.file_start_attempt($1,$2,$3,$4)`, c.Owner.ID, c.ID, c.FencingToken, session).Scan(&raw); err != nil {
			return workitems.DispositionAmbiguous, err
		}
		if err := json.Unmarshal(raw, &grant); err != nil {
			return workitems.DispositionAmbiguous, err
		}
		t := grant.Ticket
		if grant.Granted && (t.WorkItemID != c.ID || t.Token != c.FencingToken || t.Ordinal < 1 || t.Ordinal > 100 || t.ExecutorSession != session || t.State != "issued" || !t.AttemptUntil.After(t.StartedAt)) {
			return workitems.DispositionAmbiguous, fmt.Errorf("invalid file attempt grant")
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return fileAttemptGrant{}, err
	}
	return grant, nil
}
