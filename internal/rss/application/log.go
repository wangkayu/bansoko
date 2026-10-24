package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// appendActivity delegates business-history persistence to the central writer.
// The caller owns the transaction, so the owner mutation and Activity append
// commit or roll back together.
func appendActivity(ctx context.Context, tx *sql.Tx, writer observabilityport.ActivityWriter, record observabilityport.ActivityRecord) error {
	if writer == nil {
		return fmt.Errorf("RSS activity writer is nil")
	}
	return writer.AppendTx(ctx, txscope.Wrap(tx), record)
}

func marshalActivityDetails(details map[string]any) (json.RawMessage, error) {
	if len(details) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("encode RSS activity details: %w", err)
	}
	return raw, nil
}
