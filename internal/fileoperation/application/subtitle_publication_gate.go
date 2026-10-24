package application

import (
	"context"
	"database/sql"
)

// NOWAIT prevents owner-first callers from waiting against the file execution
// WorkItem -> owner -> artifact order. They retry the surrounding transaction.
func requireFileArtifactQuiescenceTx(ctx context.Context, tx *sql.Tx, fileID string) error {
	var artifactID string
	if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM artifact_files WHERE id=$1`, fileID).Scan(&artifactID); err != nil {
		return err
	}
	return requireArtifactQuiescenceTx(ctx, tx, artifactID)
}
func requireArtifactQuiescenceTx(ctx context.Context, tx *sql.Tx, artifactID string) error {
	var id string
	return tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE id=$1 FOR UPDATE NOWAIT`, artifactID).Scan(&id)
}
