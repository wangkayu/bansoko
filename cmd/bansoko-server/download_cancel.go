package main

import (
	"context"

	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/downloadcompletion"
)

// cancelRemover lends the download-completion remove_with_data action to the
// cancel of a download that has not reached import, so both delete a qB task
// with its data under one ownership rule.
type cancelRemover struct{ completion *downloadcompletion.Service }

func (r cancelRemover) RemoveCancelledDownload(ctx context.Context, acquisitionID string, correlation, frozenInput []byte) downloadapplication.CancelRemoval {
	outcome := r.completion.RemoveCancelled(ctx, acquisitionID, correlation, frozenInput)
	return downloadapplication.CancelRemoval{Effect: cancelEffect(outcome), Code: outcome.Code}
}

func cancelEffect(outcome downloadcompletion.Outcome) string {
	switch {
	case outcome.Success && outcome.Code == downloadcompletion.ResultRemovedData:
		return downloadapplication.CancelEffectDeletedWithData
	case outcome.Success && outcome.Code == downloadcompletion.ResultTaskAbsent:
		return downloadapplication.CancelEffectAbsent
	case outcome.Retryable:
		return downloadapplication.CancelEffectRetry
	default:
		// Shared with another running download, a safety refusal, a task
		// that is not the recorded one, or no recorded torrent identity: the
		// qB task is kept and the download is only detached from it.
		return downloadapplication.CancelEffectDetached
	}
}
