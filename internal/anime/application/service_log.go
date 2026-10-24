package application

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

var (
	subscriptionLogURLPattern    = regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|mysql|redis|s?ftp)://[^\s'"<>]+`)
	subscriptionLogMagnetPattern = regexp.MustCompile(`(?i)magnet:[^\s'"<>]+`)
)

// WriteSubscriptionCreatedLog appends the subscription_created activity record
// inside the caller's transaction. The owning command (Subscribe/activation)
// owns the transaction and the decision that the record belongs to it.
func WriteSubscriptionCreatedLog(ctx context.Context, tx *sql.Tx, writer port.ActivityWriter, subscriptionID, managedAnimeID, title string) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	return writer.AppendTx(ctx, txscope.Wrap(tx), port.ActivityRecord{
		SubscriptionID: subscriptionID,
		ManagedAnimeID: managedAnimeID,
		Kind:           "subscription_created",
		Outcome:        "info",
		ReasonCode:     "subscription_created",
		SubjectType:    "subscription",
		SubjectID:      subscriptionID,
		SubjectVersion: 1,
		Title:          title,
		Summary:        "订阅已创建",
	})
}
