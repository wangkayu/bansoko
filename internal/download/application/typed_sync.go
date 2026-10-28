package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

// SyncCodeTaskAbsent is the observation code of a poll whose successful qB
// lookup did not list the task. download_finish_sync matches it exactly.
const SyncCodeTaskAbsent = "qb_task_absent"

// FailureDownloaderTaskRemoved is the DownloadTask failure code committed on the
// second consecutive absent observation. The acquisition settles it as
// cancelled, never as failed, and it raises no download_failed notification.
const FailureDownloaderTaskRemoved = "DOWNLOADER_TASK_REMOVED"

type stageReply struct {
	Granted    bool   `json:"granted"`
	Applied    bool   `json:"applied"`
	Reason     string `json:"reason"`
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
	// CommittedReason is the terminal text the owner-held judgment committed.
	// It chooses the code and this text together under the lock, so a caller must
	// never substitute its own wording for a failed sync.
	CommittedReason   string `json:"reason_text"`
	TaskVersion       int64  `json:"task_version"`
	FirstConfirmation bool   `json:"first_confirmation"`
	Ticket            struct {
		Ordinal  int    `json:"ordinal"`
		TicketID string `json:"ticket_id"`
	} `json:"ticket"`
}

func (s *Service) typedSync(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Owner.Type != workitems.OwnerDownloadTask || claim.Owner.ID == "" {
		return worker.Outcome{}, ErrInvalidClaim
	}
	var input struct {
		Ordinal int `json:"poll_ordinal"`
	}
	if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil {
		return worker.Outcome{}, err
	}
	session := uuid.NewString()
	var gate stageReply
	err := s.workItems.WithTypedStageTransaction(ctx, claim, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_start_sync($1,$2,$3,$4,$5)`, claim.Owner.ID, claim.ID, claim.FencingToken, input.Ordinal, session).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &gate); err != nil {
			return "", err
		}
		if !gate.Granted && gate.Reason == "SYNC_TASK_TERMINAL" {
			return workitems.DispositionNoExternalEffect, nil
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	if !gate.Granted {
		return worker.Committed(claim, workitems.OwnerResult{State: gate.State, ReasonCode: gate.Reason}), nil
	}
	task, err := s.loadTask(ctx, claim.Owner.ID, false)
	if err != nil {
		return worker.Outcome{}, err
	}
	correlation, err := decodeCorrelation(task.providerRaw)
	if err != nil {
		return s.finishTypedSync(ctx, claim, task, session, input.Ordinal, domain.ProviderTask{}, "provider_correlation_invalid", safeReason(err.Error()), false)
	}
	if correlation.ExternalID == "" {
		return s.finishTypedSync(ctx, claim, task, session, input.Ordinal, domain.ProviderTask{}, "provider_correlation_missing", "qB provider task correlation is missing", false)
	}
	snapshot, err := frozenSnapshotFromRaw(task.frozenInputRaw)
	if err != nil {
		return s.finishTypedSync(ctx, claim, task, session, input.Ordinal, domain.ProviderTask{}, "frozen_input_invalid", "the frozen qB path snapshot is invalid", false)
	}
	if snapshot != nil {
		if correlated := pathSnapshotFromCorrelation(correlation); correlated != nil && !qbittorrent.PathSnapshotsEqual(*snapshot, *correlated) {
			return s.finishTypedSync(ctx, claim, task, session, input.Ordinal, domain.ProviderTask{}, "qb_path_snapshot_changed", "qBittorrent path mapping changed", true)
		}
	}
	if snapshot == nil {
		snapshot = pathSnapshotFromCorrelation(correlation)
	}
	config, err := s.runtimeConfig.GetQBittorrentRuntimeConfig(ctx)
	if err == nil && snapshot != nil {
		current, deriveErr := currentPathSnapshot(config)
		if deriveErr != nil {
			err = deriveErr
		} else if !qbittorrent.PathSnapshotsEqual(*snapshot, current) {
			err = qbittorrent.ErrPathSnapshotChanged
		}
	}
	var observed domain.ProviderTask
	if err != nil && snapshot == nil {
		observed, err = s.provider.Sync(ctx, correlation.ExternalID)
	} else if err == nil {
		observed, err = s.syncProvider(ctx, correlation.ExternalID, snapshot)
	}
	if err == nil {
		observed, err = applyProviderSnapshot(observed, snapshot)
	}
	if err != nil {
		code, reason, observationError := classifySyncError(s.provider, err)
		outcome, finishErr := s.finishTypedSync(ctx, claim, task, session, input.Ordinal, domain.ProviderTask{}, code, reason, observationError)
		return s.withSyncDiagnostics(outcome, finishErr, code, task.id, err)
	}
	outcome, finishErr := s.finishTypedSync(ctx, claim, task, session, input.Ordinal, observed, "qb_sync_succeeded", "", false)
	return s.withSyncDiagnostics(outcome, finishErr, "qb_sync_succeeded", task.id, nil)
}

// syncUnreachableThreshold is how many consecutive connection-class sync
// failures turn a flaky poll into a downloader.unreachable episode.
const syncUnreachableThreshold = 3

// withSyncDiagnostics feeds one committed sync outcome into the downloader
// detection. Only a retryable failure whose cause carries outbound facts counts
// toward the unreachable run: three in a row open the episode and every later
// one adds its task to it. Refused credentials are a definite answer, so they
// open downloader.auth_failed at once and reset the run. Any sync that reached
// qBittorrent (success or another definite answer) resets the run and closes
// the episode. The counter is in memory: a restart only delays detection by
// three polls.
func (s *Service) withSyncDiagnostics(outcome worker.Outcome, err error, code, taskID string, cause error) (worker.Outcome, error) {
	if err != nil || outcome.Kind != worker.OutcomeCommitted {
		return outcome, err
	}
	record := func(variant observabilityport.ConditionVariant, failure outbound.Failure) (worker.Outcome, error) {
		return worker.WithAfterCommit(outcome, func(ctx context.Context) {
			request := observabilityport.DiagnosticFailureRequest{
				Condition: observabilityport.Condition{Kind: observabilityport.KindDownloader, Variant: variant},
				Severity:  observabilityport.SeverityError, TargetID: s.downloaderTarget(ctx), TargetName: "qBittorrent",
				Failure: failure, AffectedID: taskID,
			}
			if record := observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, request); record != nil {
				record(ctx)
			}
		}), nil
	}
	if errors.Is(cause, qbittorrent.ErrAuthFailed) {
		s.syncMu.Lock()
		s.syncFailures = 0
		// The next success must close the episode this opens.
		s.syncChecked = false
		s.syncMu.Unlock()
		return record(observabilityport.VariantAuthFailed, outbound.Failure{})
	}
	if facts, ok := outbound.FailureOf(cause); ok && code == "qb_sync_retryable" {
		s.syncMu.Lock()
		s.syncFailures++
		open := s.syncFailures >= syncUnreachableThreshold
		s.syncMu.Unlock()
		if !open {
			return outcome, nil
		}
		return record(observabilityport.VariantUnreachable, facts)
	}
	if code != "qb_sync_succeeded" && code != SyncCodeTaskAbsent {
		return outcome, nil
	}
	s.syncMu.Lock()
	// The first success after start also closes: an episode opened by a
	// previous process has no counter to remember it.
	due := s.syncFailures > 0 || !s.syncChecked
	s.syncFailures = 0
	s.syncChecked = true
	s.syncMu.Unlock()
	if !due {
		return outcome, nil
	}
	return worker.WithAfterCommit(outcome, func(ctx context.Context) {
		if record := observabilityport.DiagnosticSuccessAfterCommit(s.diagnostics, s.logger, observabilityport.DiagnosticSuccessRequest{Kind: observabilityport.KindDownloader, TargetID: s.downloaderTarget(ctx)}); record != nil {
			record(ctx)
		}
	}), nil
}

// classifySyncError maps a failed sync observation onto the code the owner-held
// judgment reads. A successful lookup that no longer lists the task is the one
// definitive absence answer: it is reported as an observation without provider
// facts under SyncCodeTaskAbsent, which download_finish_sync counts apart from
// the error budget, so two consecutive absences end the task as deleted in the
// downloader and an error poll never does. Every other error keeps its
// existing code.
func classifySyncError(provider interface {
	ClassifyProviderError(error) domain.ProviderErrorClass
}, err error) (code, reason string, observationError bool) {
	if errors.Is(err, domain.ErrProviderTaskAbsent) {
		return SyncCodeTaskAbsent, "qBittorrent no longer lists this task", true
	}
	if provider.ClassifyProviderError(err) == domain.ProviderErrorTerminal {
		return "qb_sync_failed", redactProviderError(err), false
	}
	code = "qb_sync_retryable"
	if errors.Is(err, qbittorrent.ErrPathSnapshotChanged) {
		code = "qb_path_snapshot_changed"
	}
	if errors.Is(err, qbittorrent.ErrRuntimeConfigUnavailable) {
		code = "qb_connection_unavailable"
	}
	return code, redactProviderError(err), true
}

func (s *Service) finishTypedSync(ctx context.Context, claim workitems.Claim, task taskSnapshot, session string, ordinal int, observed domain.ProviderTask, code, reason string, observationError bool) (worker.Outcome, error) {
	state := "failed"
	var raw []byte
	var correlation providerCorrelation
	var err error
	// NULL byte pointers preserve the difference between absent evidence and zero.
	var observedBytes, observedTotal *int64
	var observedState any
	if !observationError {
		observedBytes, observedTotal = observed.ContentCompleteBytes, observed.PayloadTotalBytes
		observedState = observed.ProviderState
	}
	if observed.ID != "" {
		raw, correlation, err = encodeCorrelation(observed)
		if err != nil {
			return worker.Outcome{}, err
		}
		state = taskStateFromProvider(observed.State)
		if state == "failed" {
			code = "qb_provider_failed"
			reason = redactProviderError(errors.New(observed.Error))
		}
		// BC 4.1 replaced the float-ratio clock with the integer high-water mark, and
		// the stall verdict itself moved into the owner-held function: it is reached
		// under the root -> task -> work lock, after the frozen error-budget and
		// wall/max_polls branches, so it can decide a terminal code this handler cannot
		// foresee and cannot be overwritten by the write set. This worker only freezes
		// the observation; the reply carries the authoritative code and text.
	} else if observationError {
		state = syncRetryOwnerState(claim, task)
	}
	var inventory, fingerprints any
	if state == "completed" {
		inventoryRaw, err := json.Marshal(map[string]any{"provider_task_id": correlation.ExternalID, "provider_info_hash": correlation.InfoHash, "name": correlation.Name, "progress": correlation.Progress, "state": correlation.State, "path": correlation.ArtifactPath, "file_count": correlation.FileCount, "files_complete": correlation.FilesComplete, "files": correlation.Files})
		if err != nil {
			return worker.Outcome{}, err
		}
		fingerprintRaw, err := json.Marshal(map[string]any{"provider_task_id": correlation.ExternalID, "provider_info_hash": correlation.InfoHash, "path": correlation.ArtifactPath, "files": correlation.Files})
		if err != nil {
			return worker.Outcome{}, err
		}
		inventory = inventoryRaw
		fingerprints = fingerprintRaw
	}
	var reply stageReply
	err = s.workItems.WithTypedStageTransaction(ctx, claim, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var result []byte
		var correlationValue any
		if raw != nil {
			correlationValue = raw
		}
		tx := scope.SQLTx()
		if err := tx.QueryRowContext(ctx, `SELECT download_finish_sync($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, task.id, claim.ID, claim.FencingToken, session, ordinal, state, correlationValue, code, safeReason(reason), observationError, observedBytes, observedTotal, observedState, uuid.NewString(), uuid.NewString(), inventory, fingerprints).Scan(&result); err != nil {
			return "", err
		}
		if err := json.Unmarshal(result, &reply); err != nil {
			return "", err
		}
		// This is a read-only poll: even an unavailable observation cannot leave
		// an outstanding media mutation in this admission.
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		return worker.Outcome{}, fmt.Errorf("finish typed sync: %w", err)
	}
	// The committed code and text come from the owner-held judgment; a stall verdict this handler cannot
	// produce on its own must not be reported as whatever the handler guessed earlier.
	terminalReason := safeReason(reason)
	if reply.State == "failed" && reply.CommittedReason != "" {
		terminalReason = safeReason(reply.CommittedReason)
	}
	return worker.Committed(claim, workitems.OwnerResult{State: reply.State, ReasonCode: reply.ReasonCode, Reason: terminalReason}), nil
}
