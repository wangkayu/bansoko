// Package businesslog contains small transaction-local writers for bounded
// product history entries shared by owner application contexts.
package businesslog

import (
	"context"
	"fmt"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// WriteMetadataAppliedTx appends the meaningful business result of a complete
// metadata application through the central Activity writer. It participates in
// the caller's owner transaction; the writer owns validation, redaction, and
// deterministic replay dedupe.
func WriteMetadataAppliedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID string, metadataVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID,
		ManagedAnimeID: managedAnimeID,
		Kind:           "metadata_applied",
		Outcome:        "success",
		ReasonCode:     "metadata_applied",
		SubjectType:    "metadata",
		SubjectID:      managedAnimeID,
		SubjectVersion: metadataVersion,
		MessageCode:    observabilityport.MessageMetadataApplied,
	})
}
