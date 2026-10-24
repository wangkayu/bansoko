package workitems

import (
	"bansoko/internal/testutil/disposabledb"
	"database/sql"
	"testing"
)

// workItemFixtureDB is only for arranging persisted historical states.
// Services and Store operations retain the runtime handle returned by openTestDB.
func workItemFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	fixtureDB := disposabledb.Owner(t)
	return fixtureDB
}
