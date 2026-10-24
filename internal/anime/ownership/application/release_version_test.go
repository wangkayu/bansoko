package application

import (
	"context"
	"database/sql"
	"testing"
)

func currentReleaseVersion(t *testing.T, db *sql.DB, id string) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM releases WHERE id=$1`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}
