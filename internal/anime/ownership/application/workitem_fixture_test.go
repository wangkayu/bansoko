package application

import (
	"bansoko/internal/testutil/disposabledb"
	"database/sql"
	"testing"
)

// workItemFixtureDB arranges legacy states or probes the owner-side table fence.
// All application service actions continue on runtimeDB.
func workItemFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	fixtureDB := disposabledb.Owner(t)
	return fixtureDB
}
