package businesslog

import (
	"context"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// NotReplacedReason says why a replace decision will never take effect. It is
// frozen into details.reason_code of the decision's one not_replaced entry.
type NotReplacedReason string

const (
	// NotReplacedDownloadFailed: the candidate's acquisition failed before
	// its download completed.
	NotReplacedDownloadFailed NotReplacedReason = "download_failed"
	// NotReplacedImportFailed: the download completed but the acquisition
	// failed afterwards (verification or import).
	NotReplacedImportFailed NotReplacedReason = "import_failed"
	// NotReplacedCancelled: the candidate's acquisition was cancelled.
	NotReplacedCancelled NotReplacedReason = "cancelled"
	// NotReplacedSuperseded: the decision went stale when it was to take
	// effect (settlement revalidation, or the release claimed existing files).
	NotReplacedSuperseded NotReplacedReason = "superseded"
)

// ReplacementNotReplacedReasonCode is the entry reason code of every
// not_replaced line.
const ReplacementNotReplacedReasonCode = "not_replaced"

// NotReplaced is one replace decision that will never take effect.
type NotReplaced struct {
	SubscriptionID, ManagedAnimeID string
	DecisionID                     string
	DecisionVersion                int64
	// ReleaseID links the line to the candidate Release's run (empty: none).
	ReleaseID string
	// RunContext is the Release run context (RunContext); may be nil.
	RunContext                 map[string]any
	Reason                     NotReplacedReason
	CorrelationID              string
	CausationType, CausationID string
	// OccurredAt is the fact time; zero means now.
	OccurredAt time.Time
}

// WriteReplacementNotReplacedTx appends the one not_replaced line of a replace
// decision in the caller's transaction. The dedupe key is the decision itself
// (replacement:<decision_id>:not_replaced), so whichever writer observes the
// decision's end first records it and every replay or second observer is a
// no-op.
func WriteReplacementNotReplacedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, line NotReplaced) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	values := map[string]any{}
	for key, value := range line.RunContext {
		values[key] = value
	}
	values["reason_code"] = string(line.Reason)
	details, err := MarshalDetails(values)
	if err != nil {
		return fmt.Errorf("encode not-replaced details: %w", err)
	}
	version := line.DecisionVersion
	if version < 1 {
		version = 1
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: line.SubscriptionID, ManagedAnimeID: line.ManagedAnimeID,
		Kind: string(observabilityport.ActivityKindReplacementDecided), Outcome: "info",
		ReasonCode: ReplacementNotReplacedReasonCode, SubjectType: "replacement_decision",
		SubjectID: line.DecisionID, SubjectVersion: version,
		DedupeKey:   "replacement:" + line.DecisionID + ":not_replaced",
		MessageCode: observabilityport.MessageReplacementNotReplaced,
		ReleaseID:   line.ReleaseID, Details: details,
		CorrelationID: line.CorrelationID, CausationType: line.CausationType, CausationID: line.CausationID,
		OccurredAt: line.OccurredAt,
	})
}
