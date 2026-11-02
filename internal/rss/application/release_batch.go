package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// pendingReleaseEvaluation is one release the refresh wants evaluated, with the
// release version to schedule against and whether the refresh created it.
type pendingReleaseEvaluation struct {
	releaseID string
	version   int64
	created   bool
}

// enqueueReleaseEvaluations schedules the refresh's release evaluations. A
// bulk enqueue (>= businesslog.ReleaseBatchThreshold items) first writes one
// release_batch_processed head entry and stamps every scheduled evaluation
// with its batch key: the timeline keeps a single row for the batch, each
// release_processed detail entry carries the batch key, and the primary event
// view hides the details behind the head. The head's outcome counts are not
// written here — the evaluations have not run yet; the read projection
// aggregates them from the detail entries.
func (s *Service) enqueueReleaseEvaluations(ctx context.Context, tx *sql.Tx, snapshot sourceSnapshot, pending []pendingReleaseEvaluation) error {
	if len(pending) == 0 {
		return nil
	}
	const trigger = "rss_refresh"
	var batchKey string
	if len(pending) >= businesslog.ReleaseBatchThreshold {
		batchKey = uuid.NewString()
		created := 0
		for _, item := range pending {
			if item.created {
				created++
			}
		}
		if err := businesslog.WriteReleaseBatchTx(ctx, txscope.Wrap(tx), s.activityWriter, businesslog.ReleaseBatch{
			SubscriptionID: snapshot.subscriptionID,
			ManagedAnimeID: snapshot.managedAnimeID,
			SourceID:       snapshot.sourceID,
			GroupName:      snapshot.groupName,
			BatchKey:       batchKey,
			Trigger:        trigger,
			Created:        created,
			Changed:        len(pending) - created,
		}); err != nil {
			return fmt.Errorf("append release batch head: %w", err)
		}
	}
	for _, item := range pending {
		if err := createOrMergeReleaseEvaluationRaw(ctx, tx, s.workItems, item.releaseID, item.version, trigger, batchKey); err != nil {
			return fmt.Errorf("schedule release evaluation: %w", err)
		}
	}
	return nil
}

// releaseEvaluationBatchKey reads the batch a release evaluation claim was
// enqueued into; empty means a standalone evaluation whose rows stay in the
// timeline. The work item merge replaces the snapshot, so the latest enqueue's
// batch owns the entry.
func releaseEvaluationBatchKey(snapshot json.RawMessage) string {
	var input struct {
		BatchKey string `json:"batch_key"`
	}
	if len(snapshot) == 0 || json.Unmarshal(snapshot, &input) != nil {
		return ""
	}
	return strings.TrimSpace(input.BatchKey)
}

// evaluationSnapshot encodes the trigger and optional batch key of one
// scheduled release evaluation.
func evaluationSnapshot(trigger, batchKey string) json.RawMessage {
	if batchKey == "" {
		return json.RawMessage(`{"trigger":"` + trigger + `"}`)
	}
	return json.RawMessage(`{"trigger":"` + trigger + `","batch_key":"` + batchKey + `"}`)
}

// CreateOrMergeReleaseEvaluation enqueues a release_evaluate WorkItem for a
// release if no live (available/leased) one exists. trigger is recorded in the
// input snapshot for observability (for example "rss_refresh"). Its SQL twin
// is release_reopen_selected_group (migration 103, trigger "group_selected"):
// the same work_item_merge spec, run when a provider group enters the
// selection at commit.
func CreateOrMergeReleaseEvaluation(ctx context.Context, scope txscope.Tx, store *workitems.Store, releaseID string, releaseVersion int64, trigger string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("release evaluation transaction is nil")
	}
	return createOrMergeReleaseEvaluationRaw(ctx, tx, store, releaseID, releaseVersion, trigger, "")
}
