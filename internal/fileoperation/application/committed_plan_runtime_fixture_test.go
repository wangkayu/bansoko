package application

import (
	"database/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
)

func useRuntimePlanObserver(t *testing.T, s *Service) {
	t.Helper()
	dsn := os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("BANSOKO_TEST_DATABASE_URL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = "bansoko_runtime"
	db := sql.OpenDB(stdlib.GetConnector(*cfg))
	t.Cleanup(func() { db.Close() })
	var role string
	if err := db.QueryRow(`SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "bansoko_runtime" {
		t.Fatal(role)
	}
	s.db = db
}
