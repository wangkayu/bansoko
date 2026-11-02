package businesslog

import (
	"context"
	"fmt"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// ReleaseBatchThreshold is the smallest bulk release evaluation that becomes a
// release_batch_processed batch. The steady-state refresh enqueues one or two
// evaluations and keeps today's standalone rows; a first evaluation of a feed
// window or a reopen of a whole group crosses the threshold and gets one
// timeline row instead of one per release. The SQL twin
// release_reopen_selected_group (db/migrations/114) applies the same number:
// change both together.
const ReleaseBatchThreshold = 3

// ReleaseBatch describes one bulk release evaluation batch: the trigger that
// scheduled it, the key every scheduled evaluation is stamped with, and the
// outcome split the head announces. Created counts the releases the trigger
// created; the rest are reevaluations.
type ReleaseBatch struct {
	SubscriptionID string
	ManagedAnimeID string
	SourceID       string
	GroupName      string
	BatchKey       string
	Trigger        string
	Created        int
	Changed        int
}

// WriteReleaseBatchTx appends the one timeline entry of a bulk release
// evaluation batch inside the caller's owner transaction, so the head and the
// scheduled evaluations commit together. The head's outcome counts are not
// written here — the evaluations have not run yet; the read projection
// aggregates them from the release_processed details that carry the batch key.
func WriteReleaseBatchTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, batch ReleaseBatch) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	record, err := releaseBatchRecord(batch)
	if err != nil {
		return err
	}
	return writer.AppendTx(ctx, scope, record)
}

// releaseBatchRecord builds the batch head. The activity writer validates the
// envelope fail-closed, so the identity columns every main-line entry carries
// together (kind, reason, subject, message) are set here in one place instead
// of at each call site; release_batch_test.go pins them without a database.
func releaseBatchRecord(batch ReleaseBatch) (observabilityport.ActivityRecord, error) {
	message := observabilityport.MessageReleaseBatchReevaluated
	if batch.Changed == 0 {
		message = observabilityport.MessageReleaseBatchFirst
	}
	details, err := MarshalDetails(map[string]any{
		"trigger": batch.Trigger,
		"created": batch.Created, "changed": batch.Changed,
	})
	if err != nil {
		return observabilityport.ActivityRecord{}, fmt.Errorf("encode release batch details: %w", err)
	}
	return observabilityport.ActivityRecord{
		SubscriptionID: batch.SubscriptionID,
		ManagedAnimeID: batch.ManagedAnimeID,
		Kind:           string(observabilityport.ActivityKindReleaseBatchProcessed),
		Outcome:        "info",
		// The writer requires a stable reason on every activity; the batch
		// head names its own kind, like rss_new_content does.
		ReasonCode:     string(observabilityport.ActivityKindReleaseBatchProcessed),
		SubjectType:    "release_batch",
		SubjectID:      batch.BatchKey,
		SubjectVersion: 1,
		DedupeKey:      string(observabilityport.ActivityKindReleaseBatchProcessed) + ":" + batch.BatchKey,
		RSSSourceID:    batch.SourceID,
		GroupRef:       batch.GroupName,
		MessageCode:    message,
		MessageParams:  map[string]any{observabilityport.ParamCount: batch.Created + batch.Changed},
		Details:        details,
	}, nil
}
