package filemaintenance

import (
	"bansoko/internal/testutil/disposabledb"
	"database/sql"
	"testing"
)

// workItemFixtureDB arranges historical WorkItem states only.
func workItemFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	fixtureDB := disposabledb.Owner(t)
	return fixtureDB
}
