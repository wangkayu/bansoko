package application

import (
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

// Frozen 144/150/151 exclude non-acquisition media owners. Do not disable
// their constraints to invent a legacy retry preservation path.
func TestManualRetryMediaOwnersRequireAcquisition(t *testing.T) {
	db := animetest.OpenTestDB(t)
	animetest.SeedReleaseOwnerChain(t, db, "source-required-retry")
	animetest.SeedCompletedFileOp(t, db, "source-required-retry", 1, false, "failed")
	owner := disposabledb.Owner(t)
	defer owner.Close()
	for _, table := range []string{"download_tasks", "artifacts", "file_operations"} {
		t.Run(table, func(t *testing.T) {
			var required bool
			if err := db.QueryRow(`SELECT attnotnull FROM pg_attribute WHERE attrelid=$1::regclass AND attname='acquisition_id'`, table).Scan(&required); err != nil || !required {
				t.Fatalf("required=%v err=%v", required, err)
			}
			before := stageRetryClosureSnapshot(t, db)
			_, err := owner.Exec(`UPDATE ` + table + ` SET acquisition_id=NULL`)
			var pg *pgconn.PgError
			expected := "23502"
			if table == "download_tasks" {
				expected = "P0002"
			} // 146 BEFORE guard SELECT INTO STRICT precedes NOT NULL.
			if !errors.As(err, &pg) || (pg.Code != expected && pg.Code != "23514") {
				t.Fatalf("owner acquisition removal=%v", err)
			}
			if stageRetryClosureSnapshot(t, db) != before {
				t.Fatal("rejected acquisition removal changed graph")
			}
		})
	}
}
