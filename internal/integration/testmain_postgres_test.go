package integration

import (
	"context"
	"fmt"
	"os"
	"testing"

	"bansoko/internal/runtime/database"
	"bansoko/internal/testutil/disposabledb/guard"
)

// Integration tests are expected to run against a disposable PostgreSQL
// database. Keep the package boundary clean so submitted fake tasks from a
// crash-window test cannot affect the next package.
func TestMain(m *testing.M) {
	cleanupIntegrationDatabase()
	exitCode := m.Run()
	cleanupIntegrationDatabase()
	os.Exit(exitCode)
}

func cleanupIntegrationDatabase() {
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if err := guard.Check(context.Background(), dsn, guard.Options{RequireDisposable: true}); err != nil {
		fmt.Fprintf(os.Stderr, "integration database cleanup refused: %v\n", err)
		os.Exit(1)
	}
	db, err := database.Open(context.Background(), database.Config{DSN: dsn})
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration database cleanup open failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Do not delete owner facts here. Mark only unfinished external intents as
	// canceled so the database remains inspectable after a failed run.
	_, _ = db.ExecContext(context.Background(), `
UPDATE download_tasks
SET state = 'canceled', provider_task_id = NULL, version = version + 1, updated_at = NOW()
WHERE state IN ('submitted', 'downloading')`)
}
