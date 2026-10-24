package identity

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

const pendingInstallID = "pending-install-id"

func EnsureInstallID(ctx context.Context, db *sql.DB) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin install identity transaction: %w", err)
	}
	defer tx.Rollback()
	var installID string
	if err := tx.QueryRowContext(ctx, `SELECT install_id FROM instance_bootstrap WHERE singleton_id = 1 FOR UPDATE`).Scan(&installID); err != nil {
		return "", fmt.Errorf("read install identity: %w", err)
	}
	if installID == pendingInstallID || installID == "" {
		installID = uuid.NewString()
		if _, err := tx.ExecContext(ctx, `UPDATE instance_bootstrap SET install_id = $1, version = version + 1, updated_at = NOW() WHERE singleton_id = 1`, installID); err != nil {
			return "", fmt.Errorf("persist install identity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit install identity: %w", err)
	}
	return installID, nil
}
