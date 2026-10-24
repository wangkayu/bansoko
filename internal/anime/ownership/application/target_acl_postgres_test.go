package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/testutil/animetest"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestTargetRuntimePrivilegeClosure(t *testing.T) {
	db := animetest.OpenTestDB(t)
	for _, table := range []string{"acquisition_targets"} {
		statements := []string{"INSERT INTO " + table + " DEFAULT VALUES", "UPDATE " + table + " SET version=version", "DELETE FROM " + table, "TRUNCATE " + table, "SELECT * FROM " + table + " FOR UPDATE NOWAIT", "SELECT * FROM " + table + " FOR SHARE NOWAIT"}
		for _, statement := range statements {
			_, err := db.ExecContext(context.Background(), statement)
			var p *pgconn.PgError
			if !errors.As(err, &p) || p.Code != "42501" {
				t.Fatalf("%s: want 42501, got %v", statement, err)
			}
		}
	}
}
