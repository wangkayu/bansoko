package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// A deletion whose file is busy (held open over SMB/CIFS, so the capture
// rename, the purge unlink or even the observation of the file is refused) or
// whose change is not permitted is retried on a fixed schedule: every
// cleanupBusyRetryAfter, until cleanupBusyWindow has passed since the first
// refusal. A read-only filesystem is not retried. Either way the deletion
// then fails with the file left at its public path, its Activity and its
// notification.
const (
	CleanupFileBusy = "cleanup_file_busy"
	// CleanupFileRefused ends a deletion whose change a read-only filesystem
	// refused: waiting does not help, so it fails on the first refusal.
	CleanupFileRefused = "cleanup_file_refused"
	// CleanupRestoreFailed blocks a deletion that gave up after capturing
	// its file and could not move it back to its public path for a reason
	// other than a file now holding that path; deployment recovery re-admits
	// it from the intact captured object.
	CleanupRestoreFailed = "cleanup_restore_failed"
	// CleanupRestoreOccupied fails a deletion that gave up after capturing
	// its file while another file took its public path: the captured object
	// stays in the private namespace. Re-admitting it would only repeat the
	// same 24-hour window and the same refusal, so nothing retries it.
	CleanupRestoreOccupied = "cleanup_restore_occupied"
	cleanupBusyRetryAfter  = 5 * time.Minute
	cleanupBusyWindow      = 24 * time.Hour
	// cleanupBusySummary is the user-facing reason of a deletion that gave
	// up busy: an EACCES/EPERM answer may also be a real permission problem.
	cleanupBusySummary = "文件可能被占用或没有权限"
)

// cleanupFailureCopy is the user-facing reason and the notice template
// category of a deletion that gave up.
func cleanupFailureCopy(code string) (reason, category string) {
	switch code {
	case CleanupFileRefused:
		return "文件所在位置只读，未能删除", "read_only"
	case CleanupRestoreFailed:
		return "旧文件已移到同一文件夹的隐藏目录，未能放回原位置", "not_restored"
	case CleanupRestoreOccupied:
		return "旧文件已移到同一文件夹的隐藏目录，原位置已有同名文件", "path_occupied"
	}
	return cleanupBusySummary, "busy"
}

// cleanupWorkInput is the replacement_delete input snapshot. BusySince is the
// first refusal of the busy chain the item continues; every hop copies it, so
// the window is measured from the first refusal. An item created by
// admission, deployment recovery or a scan release carries none and opens a
// new window.
type cleanupWorkInput struct {
	CleanupID string     `json:"cleanup_id"`
	BusySince *time.Time `json:"busy_since,omitempty"`
}

func cleanupBusySince(claim workitems.Claim) (time.Time, bool) {
	var input cleanupWorkInput
	if len(claim.InputSnapshot) == 0 || json.Unmarshal(claim.InputSnapshot, &input) != nil || input.BusySince == nil || input.BusySince.IsZero() {
		return time.Time{}, false
	}
	return input.BusySince.UTC(), true
}

// busyNotApplied proves, inside the claim that ran the refused stage, that the
// stage left the files as frozen. A capture: nothing sits at the capture
// path (the rename into it is atomic and only this deletion makes it) and
// the source is still the frozen object at its public path, or cannot even
// be observed because an exclusive holder refuses every open. A purge: the
// captured object is still present and identical (an unlink is atomic).
func (r *cleanupRun) busyNotApplied(ctx context.Context, attempt cleanupAttemptEvidence) bool {
	s, op := r.service, r.op
	switch attempt.Stage {
	case cleanupStageCapture:
		observed, err := observeCleanupFile(ctx, s.executor, attempt.SourcePath, op.SourceFingerprint)
		if !domain.FileBusy(err) && (err != nil || !sameFrozenCleanupObservation(observed, attempt.SourceObservation)) {
			return false
		}
		_, err = observeCleanupFile(ctx, s.executor, attempt.CapturePath, op.SourceFingerprint)
		return isMissingCleanupError(err)
	case cleanupStagePurge:
		observed, err := observeCleanupFile(ctx, s.executor, attempt.CapturePath, op.SourceFingerprint)
		return err == nil && sameFrozenCleanupObservation(observed, attempt.CaptureObservation)
	}
	return false
}

// refused settles a stage whose effect syscall returned an error. Unless the
// files prove the stage did not apply, it blocks exactly like any other
// uncertain effect (failedCode). A proven-unapplied stage retires its
// invocation (its markers are removed, the persisted attempt is marked
// refused) and then: a busy refusal is retried after cleanupBusyRetryAfter
// until the window passes; a read-only filesystem gives up at once; any
// other error blocks as before.
func (r *cleanupRun) refused(ctx context.Context, attempt cleanupAttemptEvidence, failedCode string, cause error) worker.Outcome {
	reason := safeCleanupReason(cause)
	busy, readOnly := domain.FileBusy(cause), domain.FileReadOnly(cause)
	if (!busy && !readOnly) || ctx.Err() != nil || !r.busyNotApplied(ctx, attempt) {
		return r.block(failedCode, reason)
	}
	r.discardRefused(ctx, attempt)
	attempt.BusyRefused = true
	if readOnly {
		return r.giveUp(ctx, attempt, CleanupFileRefused, reason)
	}
	since, exhausted := r.busyWindow()
	if exhausted {
		return r.giveUp(ctx, attempt, CleanupFileBusy, reason)
	}
	return r.busyRetry(&attempt, since, reason)
}

// observationBusy settles a start whose source observation an exclusive
// holder refused: nothing ran, so the deletion keeps its attempt (if any)
// and waits on the same schedule, or gives up with the file in place.
func (r *cleanupRun) observationBusy(cause error) worker.Outcome {
	reason := safeCleanupReason(cause)
	since, exhausted := r.busyWindow()
	if exhausted {
		return r.settle(workitems.StateFailed, CleanupFailed, CleanupFileBusy, reason)
	}
	return r.busyRetry(nil, since, reason)
}

// busyWindow returns the first refusal of this busy chain (now when the claim
// opens it) and whether cleanupBusyWindow has passed since.
func (r *cleanupRun) busyWindow() (time.Time, bool) {
	now := r.service.now().UTC()
	since, ok := cleanupBusySince(r.claim)
	if !ok || since.After(now) {
		since = now
	}
	return since, now.Sub(since) >= cleanupBusyWindow
}

// discardRefused removes the start marker and lock the refused invocation
// left in the private namespace, so a stage retried every few minutes leaves
// nothing behind. Best effort: a marker that stays is only clutter, never a
// receipt the retired attempt is read by again.
func (r *cleanupRun) discardRefused(ctx context.Context, attempt cleanupAttemptEvidence) {
	discarder, ok := r.service.executor.(interface {
		DiscardRefusedMaintenance(context.Context, executorpkg.MaintenanceInvocation) error
	})
	if !ok {
		return
	}
	kind := cleanupCaptureInvocation
	if attempt.Stage == cleanupStagePurge {
		kind = cleanupPurgeInvocation
	}
	_ = discarder.DiscardRefusedMaintenance(ctx, cleanupInvocation(r.op, attempt, kind))
}

// giveUp ends a deletion that will not happen with its file at the public
// path: a captured object is first moved back where it was. A restore that
// reports an error is judged by the files: the captured object at the public
// path is restored; the captured object intact while another file holds the
// public path fails the deletion with the object kept hidden
// (cleanup_restore_occupied); anything else blocks with the refused attempt
// kept, so deployment recovery re-admits it from the intact captured object.
func (r *cleanupRun) giveUp(ctx context.Context, attempt cleanupAttemptEvidence, code, reason string) worker.Outcome {
	if attempt.Stage == cleanupStagePurge {
		restorer, ok := r.service.executor.(interface {
			RestoreCaptured(context.Context, string, string, domain.FileObservation) error
		})
		var err error
		if !ok {
			err = errors.New("cleanup executor cannot restore a captured file")
		} else {
			err = restorer.RestoreCaptured(ctx, attempt.CapturePath, attempt.SourcePath, attempt.CaptureObservation)
		}
		if err != nil {
			switch r.restoreRefusal(ctx, attempt) {
			case cleanupRestoreLanded:
			case cleanupRestorePathOccupied:
				return r.settleKeepingAttempt(attempt, CleanupFailed, CleanupRestoreOccupied, safeCleanupReason(err))
			default:
				return r.settleKeepingAttempt(attempt, CleanupBlocked, CleanupRestoreFailed, safeCleanupReason(err))
			}
		}
	}
	return r.settle(workitems.StateFailed, CleanupFailed, code, reason)
}

type cleanupRestoreResult int

const (
	cleanupRestoreUnproven cleanupRestoreResult = iota
	cleanupRestoreLanded
	cleanupRestorePathOccupied
)

// restoreRefusal observes the files after a restore reported an error: the
// public path holding the captured object means the move landed (a lost
// reply, or a sync or verification that failed after it); the captured object
// still intact with another file at the public path means that file took it.
func (r *cleanupRun) restoreRefusal(ctx context.Context, attempt cleanupAttemptEvidence) cleanupRestoreResult {
	if ctx.Err() != nil {
		return cleanupRestoreUnproven
	}
	executor, fingerprint := r.service.executor, r.op.SourceFingerprint
	source, sourceErr := observeCleanupFile(ctx, executor, attempt.SourcePath, fingerprint)
	if sourceErr == nil && sameFrozenCleanupObservation(source, attempt.CaptureObservation) {
		return cleanupRestoreLanded
	}
	if sourceErr != nil && !errors.Is(sourceErr, domain.ErrNotRegularFile) {
		return cleanupRestoreUnproven
	}
	captured, err := observeCleanupFile(ctx, executor, attempt.CapturePath, fingerprint)
	if err == nil && sameFrozenCleanupObservation(captured, attempt.CaptureObservation) {
		return cleanupRestorePathOccupied
	}
	return cleanupRestoreUnproven
}

// settleKeepingAttempt settles the obligation with attempt persisted first,
// in the WorkItem's terminal transaction.
func (r *cleanupRun) settleKeepingAttempt(attempt cleanupAttemptEvidence, to CleanupState, code, reason string) worker.Outcome {
	s, op, expected := r.service, r.op, r.claim.ExpectedOwnerVersion
	writer := func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		version, err := s.persistAttemptTx(ctx, scope, op.ID, expected, attempt)
		if err != nil {
			return 0, err
		}
		return s.settleWriter(op, version, to, code, reason)(ctx, scope, result)
	}
	return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: string(to), ReasonCode: code, Reason: reason}, writer)
}

func (s *CleanupService) persistAttemptTx(ctx context.Context, scope txscope.Tx, cleanupID string, expected int64, attempt cleanupAttemptEvidence) (int64, error) {
	raw, err := json.Marshal(attempt)
	if err != nil {
		return 0, err
	}
	return s.repository.PersistAttemptTx(ctx, scope, cleanupID, expected, raw)
}

// busyRetry keeps the obligation admitted and ends this item; the same
// transaction persists the refused attempt (when a stage ran) and creates the
// next item, due after cleanupBusyRetryAfter. A WorkItem's attempt budget (at
// most 100) cannot hold the 288 hops of a full window, so each hop is its own
// item.
func (r *cleanupRun) busyRetry(attempt *cleanupAttemptEvidence, since time.Time, reason string) worker.Outcome {
	s, op, expected := r.service, r.op, r.claim.ExpectedOwnerVersion
	var next int64
	writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		version := expected
		if attempt != nil {
			persisted, err := s.persistAttemptTx(ctx, scope, op.ID, version, *attempt)
			if err != nil {
				return 0, fmt.Errorf("keep refused cleanup attempt: %w", err)
			}
			version = persisted
		}
		version, err := s.repository.TransitionTx(ctx, scope, op.ID, version, string(CleanupAdmitted), CleanupFileBusy, reason)
		if err != nil {
			return 0, fmt.Errorf("keep busy cleanup admitted: %w", err)
		}
		next = version
		return version, nil
	}
	outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: string(CleanupAdmitted), ReasonCode: CleanupFileBusy, Reason: reason}, writer)
	outcome.PostTransition = func(ctx context.Context, scope txscope.Tx) error {
		if next < 1 {
			return fmt.Errorf("busy cleanup %s has no committed owner version", op.ID)
		}
		return s.enqueueNextTx(ctx, scope.SQLTx(), op.ID, next, since, cleanupBusyRetryAfter)
	}
	return outcome
}

// cleanupGaveUp reports the codes of a deletion that gave up (failed, or
// blocked with its file hidden after a restore that did not land): they carry
// the cleanup_failed Activity and the replacement_deletion_failed notice.
func cleanupGaveUp(code string) bool {
	switch code {
	case CleanupFileBusy, CleanupFileRefused, CleanupRestoreFailed, CleanupRestoreOccupied:
		return true
	}
	return false
}

// appendCleanupFailureTx records the end of a deletion that gave up in the
// caller's transaction: the cleanup_failed Activity and the
// replacement_deletion_failed notice. Unwired sinks are skipped, and so is
// the Activity of an anime without a subscription and the notice of an anime
// that no longer exists: the failure itself is still recorded.
func (s *CleanupService) appendCleanupFailureTx(ctx context.Context, scope txscope.Tx, op *ReplacementDeletion, code string) error {
	if op == nil {
		return nil
	}
	reason, category := cleanupFailureCopy(code)
	if s.activity != nil {
		subscriptionID, err := s.repository.SubscriptionIDForAnimeTx(ctx, scope, op.ManagedAnimeID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			if err := s.activity.AppendTx(ctx, scope, observabilityport.ActivityRecord{
				SubscriptionID:  subscriptionID,
				ManagedAnimeID:  op.ManagedAnimeID,
				Kind:            "cleanup_failed",
				Outcome:         "failure",
				ReasonCode:      code,
				SubjectType:     "replacement_deletion",
				SubjectID:       op.ID,
				SubjectVersion:  op.Version,
				StableResultKey: code,
				Title:           "清理失败",
				Summary:         "旧版本未删除：" + reason,
				CorrelationID:   op.CorrelationID,
				CausationType:   "replacement_decision",
				CausationID:     op.ReplacementRef,
			}); err != nil {
				return err
			}
		}
	}
	if s.notifications != nil {
		title, err := s.repository.ManagedAnimeTitleTx(ctx, scope, op.ManagedAnimeID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read deletion notice anime: %w", err)
		}
		request, err := plugintargetport.ReplacementDeletionFailedRequest(op.ID, op.Version, op.ManagedAnimeID, title, filepath.Base(op.SourcePath), reason, category)
		if err != nil {
			return fmt.Errorf("encode deletion notice: %w", err)
		}
		if err := s.notifications.EnqueueBusinessDelivery(ctx, scope, request); err != nil {
			return fmt.Errorf("enqueue deletion notice: %w", err)
		}
	}
	return nil
}

// SetBusinessDeliveryEnqueuer wires the replacement_deletion_failed notice of
// a deletion that gave up. Unset (nil) admits none.
func (s *CleanupService) SetBusinessDeliveryEnqueuer(enqueuer plugintargetport.BusinessDeliveryEnqueuer) {
	if s != nil {
		s.notifications = enqueuer
	}
}
