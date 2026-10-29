package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

// Failure codes of a download cancelled before import (migration 103). Like
// FailureDownloaderTaskRemoved, the acquisition settles them as cancelled and
// they raise no download_failed notification.
const (
	// FailureProviderGroupNotAllowed: the task's provider group left the
	// subscription's selection. After the settle its Release returns to
	// filtered/provider_group_not_selected and reopens with the selection.
	FailureProviderGroupNotAllowed = "PROVIDER_GROUP_NOT_ALLOWED"
	// FailureUserCancelled: an explicit cancel of the execution record. The
	// Release keeps the cancelled task until a retry.
	FailureUserCancelled = "USER_CANCELLED"
)

func isCancelCode(code string) bool {
	return code == FailureProviderGroupNotAllowed || code == FailureUserCancelled
}

// Cancel effects: what one cancel attempt did in qBittorrent.
const (
	CancelEffectDeletedWithData = "deleted_with_data"
	CancelEffectAbsent          = "absent"
	// CancelEffectDetached: the ownership rule kept the qB task (adopted from
	// another owner, shared with another running download, or refused by the
	// data-delete safety check); nothing was deleted.
	CancelEffectDetached = "detached"
	// CancelEffectRetry: a transient failure; spent from the sync error budget.
	CancelEffectRetry = "retry"
)

// CancelRemoval is the result of one delete attempt.
type CancelRemoval struct {
	Effect string
	// Code is the completion result or failure code behind Effect.
	Code string
}

// CancelRemover deletes the qBittorrent task of a cancelled download with its
// data under the ownership rule of the download-completion remove_with_data
// action: a task another running download shares is kept, a delete the safety
// check refuses is kept, and an absent task is reported as absent.
type CancelRemover interface {
	RemoveCancelledDownload(ctx context.Context, acquisitionID string, correlation, frozenInput []byte) CancelRemoval
}

// SetCancelRemover wires the qB delete of a cancelled download. Without one a
// cancel attempt counts as a transient failure.
func (s *Service) SetCancelRemover(remover CancelRemover) {
	if s != nil {
		s.cancelRemover = remover
	}
}

type cancelGateReply struct {
	Granted bool   `json:"granted"`
	Applied bool   `json:"applied"`
	Reason  string `json:"reason"`
	Cause   string `json:"cause"`
}

// typedCancel runs a granted cancel ticket: it deletes the qB task (or keeps
// it under the ownership rule) and settles the task failed with the cause's
// code in the same owner-locked transaction that re-reads the cause.
func (s *Service) typedCancel(ctx context.Context, claim workitems.Claim, session string) (worker.Outcome, error) {
	task, err := s.loadTask(ctx, claim.Owner.ID, false)
	if err != nil {
		return worker.Outcome{}, err
	}
	removal := s.cancelRemoval(ctx, task)
	var reply stageReply
	err = s.workItems.WithTypedStageTransaction(ctx, claim, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_finish_cancel($1,$2,$3,$4,$5,$6)`,
			task.id, claim.ID, claim.FencingToken, session, removal.Effect, uuid.NewString()).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		switch removal.Effect {
		case CancelEffectDeletedWithData, CancelEffectRetry:
			// A delete ran or may have run.
			return workitems.DispositionConfirmed, nil
		default:
			return workitems.DispositionNoExternalEffect, nil
		}
	})
	if err != nil {
		return worker.Outcome{}, fmt.Errorf("finish download cancel: %w", err)
	}
	return worker.Committed(claim, workitems.OwnerResult{State: reply.State, ReasonCode: reply.ReasonCode,
		Reason: safeReason(reply.CommittedReason)}), nil
}

// cancelRemoval decides the qB side of one cancel attempt. An adopted task
// was not created by this download, so it is only detached.
func (s *Service) cancelRemoval(ctx context.Context, task taskSnapshot) CancelRemoval {
	if input, err := decodeFrozenInput(task.frozenInputRaw); err == nil && input.AdoptionProvenance != nil &&
		input.AdoptionProvenance.Kind == "unique_infohash_path" {
		return CancelRemoval{Effect: CancelEffectDetached, Code: "adopted_task"}
	}
	if s.cancelRemover == nil {
		return CancelRemoval{Effect: CancelEffectRetry, Code: "cancel_remover_unavailable"}
	}
	removal := s.cancelRemover.RemoveCancelledDownload(ctx, task.acquisitionID, task.providerRaw, task.frozenInputRaw)
	switch removal.Effect {
	case CancelEffectDeletedWithData, CancelEffectAbsent, CancelEffectDetached, CancelEffectRetry:
		return removal
	default:
		return CancelRemoval{Effect: CancelEffectRetry, Code: strings.TrimSpace(removal.Code)}
	}
}
