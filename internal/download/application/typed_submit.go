package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

func (s *Service) typedSubmit(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	session := uuid.NewString()
	var gate stageReply
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,$3,$4)`, c.Owner.ID, c.ID, c.FencingToken, session).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &gate); err != nil {
			return "", err
		}
		if gate.Reason == "SUBMIT_TASK_TERMINAL" || gate.Reason == "SUBMIT_BUDGET_EXHAUSTED" {
			return workitems.DispositionNoExternalEffect, nil
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	if !gate.Granted {
		if gate.Reason == "SUBMIT_RECONCILE_REQUIRED" {
			return s.typedSubmitRecovery(ctx, c)
		}
		return worker.Committed(c, workitems.OwnerResult{State: gate.State, ReasonCode: gate.Reason}), nil
	}
	task, err := s.loadTask(ctx, c.Owner.ID, false)
	if err != nil {
		return worker.Outcome{}, err
	}
	fail := func(code string, e error, retry bool) (worker.Outcome, error) {
		return s.finishUnstartedSubmission(ctx, c, task, session, code, safeReason(e.Error()), retry)
	}
	input, err := decodeFrozenInput(task.frozenInputRaw)
	if err != nil {
		return fail("frozen_input_invalid", err, false)
	}
	payload, err := s.loadTorrentInput(ctx, task, input)
	if err != nil {
		return fail("torrent_payload_missing", err, false)
	}
	config, err := s.runtimeConfig.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		return fail("qb_connection_unavailable", err, true)
	}
	snapshot, err := currentPathSnapshot(config)
	if err != nil {
		return fail("qb_connection_unavailable", err, true)
	}
	frozen := pathSnapshotFromInput(input)
	if frozen == nil {
		c, task, input, err = s.freezeTaskPathSnapshot(ctx, c, task, input, snapshot)
		if err != nil {
			return worker.Outcome{}, err
		}
		frozen = pathSnapshotFromInput(input)
	}
	if frozen == nil || !qbittorrent.PathSnapshotsEqual(*frozen, snapshot) {
		return fail("qb_path_snapshot_changed", qbittorrent.ErrPathSnapshotChanged, true)
	}
	preparer, ok := s.provider.(qbittorrent.SubmissionPreparer)
	if !ok {
		return fail("qb_submission_preparer_required", errors.New("qB provider lacks prepared submission support"), false)
	}
	prepared, existing, found, err := preparer.PrepareSubmission(ctx, payload.Payload, payload.Filename, *frozen, c.IdempotencyKey)
	if err != nil {
		return fail("qb_submit_prepare_failed", err, s.provider.ClassifyProviderError(err) != domain.ProviderErrorTerminal)
	}
	// Cross-owner adoption: acceptance matched exactly one retained provider
	// task by info hash under the frozen save roots (unique_infohash_path) and
	// froze it as the adoption provenance. The operation tag lookup above cannot
	// find that task (it carries the earlier owner's tag), so the submit adopts
	// it by the frozen hash instead of adding the same torrent again, which qB
	// refuses and which would leave the effect unknown.
	if !found && input.AdoptionProvenance != nil && input.AdoptionProvenance.Kind == "unique_infohash_path" && strings.TrimSpace(input.AdoptionProvenance.ProviderInfoHash) != "" {
		lookup, ok := s.provider.(interface {
			LookupByInfoHashesWithSnapshot(context.Context, []string, domain.PathSnapshot) (domain.ProviderTask, bool, error)
		})
		if !ok {
			return fail("qb_adoption_lookup_unavailable", errors.New("qB provider lacks info-hash lookup"), false)
		}
		hash := strings.ToLower(strings.TrimSpace(input.AdoptionProvenance.ProviderInfoHash))
		adopted, adoptedFound, lookupErr := lookup.LookupByInfoHashesWithSnapshot(ctx, []string{hash}, *frozen)
		if lookupErr != nil {
			return fail("qb_adoption_lookup_failed", lookupErr, s.provider.ClassifyProviderError(lookupErr) != domain.ProviderErrorTerminal)
		}
		if !adoptedFound || !strings.EqualFold(strings.TrimSpace(adopted.Hash), hash) {
			return fail("qb_adoption_task_missing", errors.New("the adopted qB task is no longer present"), false)
		}
		prepared, existing, found = nil, adopted, true
	}
	// Existing provider evidence must be recorded through the same permission and
	// receipt identity before the submit stage; it never executes an add request.
	permit := uuid.NewString()
	path, _ := json.Marshal(frozen)
	identity, _ := json.Marshal(map[string]any{"info_hash_v1": input.InfoHashV1, "info_hash_v2": input.InfoHashV2})
	// EF 4.2: the namespace is a credential-free endpoint/instance identity plus
	// the path mapping and the trusted connection reference with its version. The
	// location is stored beside its digest so the anchor stays reconcilable, and
	// the digest -- not the bare connection id -- identifies the namespace.
	namespace := qbittorrent.NewNamespaceLocation(config)
	locator, _ := json.Marshal(map[string]any{
		"operation_key": c.IdempotencyKey,
		"namespace":     namespace,
	})
	namespaceDigest := namespace.Digest()
	specBytes, _ := json.Marshal(map[string]any{"task": task.id, "payload": input.TorrentPayloadDigest, "path": frozen, "operation_key": c.IdempotencyKey, "namespace": namespaceDigest})
	digest := func(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }
	spec := digest(specBytes)
	var dispatch struct {
		Granted bool      `json:"granted"`
		Reason  string    `json:"reason"`
		Now     time.Time `json:"gate_db_now"`
		Until   time.Time `json:"attempt_until"`
	}
	start := time.Now()
	err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, task.id, c.ID, c.FencingToken, session, permit, spec, config.ConnectionID, namespaceDigest, locator, path, identity).Scan(&raw); err != nil {
			return "", err
		}
		return workitems.DispositionAmbiguous, json.Unmarshal(raw, &dispatch)
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	if !dispatch.Granted {
		return worker.Committed(c, workitems.OwnerResult{ReasonCode: dispatch.Reason}), nil
	}
	observed := existing
	effect := "confirmed"
	var closed, resolution any
	if !found {
		remaining := dispatch.Until.Sub(dispatch.Now) - time.Since(start)
		if remaining <= 0 {
			return worker.AwaitRecovery("qb_submit_attempt_window_exhausted"), nil
		}
		callCtx, cancel := context.WithTimeout(ctx, remaining)
		result, executeErr := prepared.Execute(callCtx)
		cancel()
		err = executeErr
		observed = result.Task
		if err != nil || !result.Accepted {
			effect = "unknown"
		}
	}
	observation := map[string]any{}
	if effect == "confirmed" {
		observed, err = applyProviderSnapshot(observed, frozen)
		if err == nil {
			raw, _, encodeErr := encodeCorrelation(observed)
			err = encodeErr
			observation["correlation"] = json.RawMessage(raw)
		}
		if err != nil {
			effect = "unknown"
		} else {
			closed = `{"kind":"channel_closed"}`
			resolution = `{"kind":"accepted_protocol"}`
		}
	}
	if err != nil {
		observation["error"] = redactProviderError(err)
	}
	raw, _ := json.Marshal(observation)
	err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var reply []byte
		var root string
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT acquisition_id FROM download_tasks WHERE id=$1`, task.id).Scan(&root); err != nil {
			return "", err
		}
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,$2,$3,$4,$5,$6,$7,$8,NULL,$9,$10,NULL)`, root, permit, session, spec, effect, closed, resolution, raw, c.ID, c.FencingToken).Scan(&reply); err != nil {
			return "", err
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return worker.Outcome{}, fmt.Errorf("record submission receipt: %w", err)
	}
	if effect != "confirmed" {
		return worker.AwaitRecovery("qb_submit_effect_unknown"), nil
	}
	return s.finishTypedSubmission(ctx, c, task, session, permit, observed, config.ConnectionID)
}

func (s *Service) finishUnstartedSubmission(ctx context.Context, c workitems.Claim, task taskSnapshot, session, code, reason string, retry bool) (worker.Outcome, error) {
	var reply stageReply
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_finish_unstarted_submission($1,$2,$3,$4,$5,$6,$7)`, task.id, c.ID, c.FencingToken, session, code, reason, retry).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	return worker.Committed(c, workitems.OwnerResult{State: reply.State, ReasonCode: code, Reason: reason}), nil
}
