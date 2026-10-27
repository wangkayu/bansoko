package filemaintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// A deletion whose capture rename or purge unlink was refused because the
// file is busy (held open over SMB/CIFS) or the change is not permitted is
// retried on a fixed schedule: every cleanupBusyRetryAfter, until
// cleanupBusyWindow has passed since the first refusal. Then the obligation
// fails with CleanupFileBusy, its Activity and its notification.
const (
	CleanupFileBusy       = "cleanup_file_busy"
	cleanupBusyRetryAfter = 5 * time.Minute
	cleanupBusyWindow     = 24 * time.Hour
	// cleanupBusySummary is the user-facing reason of an exhausted busy
	// deletion: an EACCES/EPERM answer may also be a real permission problem.
	cleanupBusySummary = "文件可能被占用或没有权限"
)

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
// stage left the files exactly as frozen: a capture's source still sits at its
// public path as the frozen object with nothing at the capture path, or a
// purge's captured object is still present and identical. Rename and unlink
// are atomic, so the effect did not happen and the stage may run again.
func (r *cleanupRun) busyNotApplied(ctx context.Context, attempt cleanupAttemptEvidence) bool {
	s, op := r.service, r.op
	switch attempt.Stage {
	case cleanupStageCapture:
		observed, err := observeCleanupFile(ctx, s.executor, attempt.SourcePath, op.SourceFingerprint)
		if err != nil || !sameFrozenCleanupObservation(observed, attempt.SourceObservation) {
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

// busy settles a stage whose effect syscall answered busy. An effect that is
// not provably unapplied blocks exactly like any other uncertain effect
// (failedCode). A provably unapplied one is retried after
// cleanupBusyRetryAfter, or fails once the window since the first refusal has
// passed.
func (r *cleanupRun) busy(ctx context.Context, attempt cleanupAttemptEvidence, failedCode string, cause error) worker.Outcome {
	reason := safeCleanupReason(cause)
	if !r.busyNotApplied(ctx, attempt) {
		return r.block(failedCode, reason)
	}
	now := r.service.now().UTC()
	since, ok := cleanupBusySince(r.claim)
	if !ok || since.After(now) {
		since = now
	}
	if now.Sub(since) >= cleanupBusyWindow {
		return r.settle(workitems.StateFailed, CleanupFailed, CleanupFileBusy, reason)
	}
	return r.busyRetry(since, reason)
}

// busyRetry keeps the obligation admitted with its attempt (the started
// receipt resolves as not applied on the next claim) and ends this item; the
// same transaction creates the next item, due after cleanupBusyRetryAfter. A
// WorkItem's attempt budget (at most 100) cannot hold the 288 hops of a full
// window, so each hop is its own item.
func (r *cleanupRun) busyRetry(since time.Time, reason string) worker.Outcome {
	s, op, expected := r.service, r.op, r.claim.ExpectedOwnerVersion
	var next int64
	writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		version, err := s.repository.TransitionTx(ctx, scope, op.ID, expected, string(CleanupAdmitted), CleanupFileBusy, reason)
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

// appendCleanupFailureTx records the end of a busy deletion in the caller's
// transaction: the cleanup_failed Activity and the replacement_deletion_failed
// notice. Unwired sinks are skipped.
func (s *CleanupService) appendCleanupFailureTx(ctx context.Context, scope txscope.Tx, op *ReplacementDeletion) error {
	if op == nil {
		return nil
	}
	if s.activity != nil {
		subscriptionID, err := s.repository.SubscriptionIDForAnimeTx(ctx, scope, op.ManagedAnimeID)
		if err != nil {
			return err
		}
		if err := s.activity.AppendTx(ctx, scope, observabilityport.ActivityRecord{
			SubscriptionID:  subscriptionID,
			ManagedAnimeID:  op.ManagedAnimeID,
			Kind:            "cleanup_failed",
			Outcome:         "failure",
			ReasonCode:      CleanupFileBusy,
			SubjectType:     "replacement_deletion",
			SubjectID:       op.ID,
			SubjectVersion:  op.Version,
			StableResultKey: CleanupFileBusy,
			Title:           "清理失败",
			Summary:         "旧版本未删除：" + cleanupBusySummary,
			CorrelationID:   op.CorrelationID,
			CausationType:   "replacement_decision",
			CausationID:     op.ReplacementRef,
		}); err != nil {
			return err
		}
	}
	if s.notifications != nil {
		title, err := s.repository.ManagedAnimeTitleTx(ctx, scope, op.ManagedAnimeID)
		if err != nil {
			return fmt.Errorf("read deletion notice anime: %w", err)
		}
		request, err := plugintargetport.ReplacementDeletionFailedRequest(op.ID, op.Version, op.ManagedAnimeID, title, filepath.Base(op.SourcePath), cleanupBusySummary)
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
// a deletion whose busy retries ran out. Unset (nil) admits none.
func (s *CleanupService) SetBusinessDeliveryEnqueuer(enqueuer plugintargetport.BusinessDeliveryEnqueuer) {
	if s != nil {
		s.notifications = enqueuer
	}
}
