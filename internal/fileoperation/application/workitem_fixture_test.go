package application

import (
	"bansoko/internal/testutil/disposabledb"
	"database/sql"
	"testing"
)

// workItemFixtureDB arranges historical states; services retain runtimeDB.
func workItemFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	fixtureDB := disposabledb.Owner(t)
	return fixtureDB
}
