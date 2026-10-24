package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

func (s *Service) typedSubmitRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if c.Kind != workitems.KindQBSubmit || c.Owner.Type != workitems.OwnerDownloadTask {
		return worker.Outcome{}, ErrInvalidClaim
	}
	var confirmedPermit, confirmedSession string
	var confirmedObservation []byte
	confirmedErr := s.db.QueryRowContext(ctx, `SELECT t.submit_permit_id,COALESCE(CASE WHEN t.submit_lookup_ticket IS NOT NULL AND t.submit_lookup_ticket->>'work_item_id'=$2 AND ((t.submit_lookup_ticket->>'token')::bigint=$3 OR ((t.submit_lookup_ticket->>'token')::bigint<$3 AND t.submit_lookup_ticket->>'state'='completed' AND t.submit_lookup_ticket->>'result_kind'='found')) AND t.submit_lookup_ticket->>'state' IN ('issued','completed') THEN t.submit_lookup_ticket->>'executor_session' END,CASE WHEN t.submit_work_item_id=$2 AND t.submit_fencing_token=$3 THEN t.submit_executor_session END),t.submit_observation FROM download_tasks t WHERE t.id=$1 AND t.submit_effect_class='confirmed' AND t.submit_dispatch_state='closed' AND t.submit_resolution_proof IS NOT NULL AND NOT t.submit_retry_safe LIMIT 1`, c.Owner.ID, c.ID, c.FencingToken).Scan(&confirmedPermit, &confirmedSession, &confirmedObservation)
	if confirmedErr == nil {
		var observation struct {
			Correlation json.RawMessage `json:"correlation"`
		}
		if err := json.Unmarshal(confirmedObservation, &observation); err != nil {
			return worker.Outcome{}, err
		}
		correlation, err := decodeCorrelation(observation.Correlation)
		if err != nil || correlation.ExternalID == "" {
			return worker.Outcome{}, ErrProviderCorrelation
		}
		task, err := s.loadTask(ctx, c.Owner.ID, false)
		if err != nil {
			return worker.Outcome{}, err
		}
		return s.finishTypedSubmissionCorrelation(ctx, c, task, confirmedSession, confirmedPermit, observation.Correlation, correlation, "")
	}
	if !errors.Is(confirmedErr, sql.ErrNoRows) {
		return worker.Outcome{}, confirmedErr
	}
	var root, permit, spec string
	var frozenPath []byte
	err := s.db.QueryRowContext(ctx, `SELECT acquisition_id,submit_permit_id,submit_spec_digest,submit_path_snapshot FROM download_tasks WHERE id=$1 AND submit_effect_class='unknown' AND submit_dispatch_started_at IS NOT NULL AND NOT submit_retry_safe`, c.Owner.ID).Scan(&root, &permit, &spec, &frozenPath)
	if errors.Is(err, sql.ErrNoRows) {
		return worker.AwaitRecovery("qb_submit_recovery_evidence_pending"), nil
	}
	if err != nil {
		return worker.Outcome{}, err
	}
	session := uuid.NewString()
	var gate stageReply
	err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_start_submit_lookup($1,$2,$3,$4,$5)`, c.Owner.ID, c.ID, c.FencingToken, permit, session).Scan(&raw); err != nil {
			return "", err
		}
		return workitems.DispositionAmbiguous, json.Unmarshal(raw, &gate)
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	if !gate.Granted {
		return worker.Committed(c, workitems.OwnerResult{ReasonCode: gate.Reason}), nil
	}
	var snapshot domain.PathSnapshot
	if err := json.Unmarshal(frozenPath, &snapshot); err != nil {
		return worker.Outcome{}, err
	}
	task, err := s.loadTask(ctx, c.Owner.ID, false)
	if err != nil {
		return worker.Outcome{}, err
	}
	// An already-dispatched add is reconciled only by its frozen operation key.
	var observed domain.ProviderTask
	var found bool
	var lookupErr error
	if provider, ok := s.provider.(qbittorrent.SnapshotLookup); ok {
		observed, found, lookupErr = provider.LookupByOperationKeyWithSnapshot(ctx, c.IdempotencyKey, snapshot)
	} else {
		observed, found, lookupErr = s.provider.LookupByOperationKey(ctx, c.IdempotencyKey)
	}
	effect := "unknown"
	var closed, resolution any
	observation := map[string]any{"found": found}
	if lookupErr != nil {
		observation["error"] = redactProviderError(lookupErr)
	} else if found {
		observed, lookupErr = applyProviderSnapshot(observed, &snapshot)
		if lookupErr == nil {
			effect = "confirmed"
			closed = `{"kind":"channel_closed"}`
			resolution = `{"kind":"same_key_found"}`
		} else {
			observation["error"] = redactProviderError(lookupErr)
		}
	}
	if effect == "confirmed" {
		correlationRaw, _, err := encodeCorrelation(observed)
		if err != nil {
			return worker.Outcome{}, err
		}
		observation["correlation"] = json.RawMessage(correlationRaw)
	}
	observationRaw, err := json.Marshal(observation)
	if err != nil {
		return worker.Outcome{}, err
	}
	err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9,$10,$11,$12)`, root, permit, session, spec, effect, closed, resolution, observationRaw, gate.Ticket.TicketID, c.ID, c.FencingToken, gate.Ticket.Ordinal).Scan(&raw); err != nil {
			return "", err
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	if effect != "confirmed" {
		return worker.AwaitRecovery("qb_submit_effect_unknown"), nil
	}
	return s.finishTypedSubmission(ctx, c, task, session, permit, observed, "")
}

func (s *Service) finishTypedSubmission(ctx context.Context, c workitems.Claim, task taskSnapshot, session, permit string, observed domain.ProviderTask, connection string) (worker.Outcome, error) {
	raw, correlation, err := encodeCorrelation(observed)
	if err != nil {
		return worker.Outcome{}, err
	}
	return s.finishTypedSubmissionCorrelation(ctx, c, task, session, permit, raw, correlation, connection)
}

func (s *Service) finishTypedSubmissionCorrelation(ctx context.Context, c workitems.Claim, task taskSnapshot, session, permit string, raw []byte, correlation providerCorrelation, connection string) (worker.Outcome, error) {
	state := taskStateFromProvider(domain.TaskState(correlation.State))
	var reply stageReply
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		tx := scope.SQLTx()
		var result []byte
		if err := tx.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,$3,$4,$5,$6,$7,$8,$9)`, task.id, c.ID, c.FencingToken, session, permit, state, raw, connection, uuid.NewString()).Scan(&result); err != nil {
			return "", err
		}
		if err := json.Unmarshal(result, &reply); err != nil {
			return "", err
		}
		if reply.Applied && reply.FirstConfirmation {
			if err := s.enqueueDownloadSubmittedTx(ctx, tx, task, reply.TaskVersion, correlation); err != nil {
				return "", err
			}
		}
		if reply.Applied {
			return workitems.DispositionConfirmed, nil
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	return worker.Committed(c, workitems.OwnerResult{State: state, ReasonCode: reply.Reason}), nil
}
