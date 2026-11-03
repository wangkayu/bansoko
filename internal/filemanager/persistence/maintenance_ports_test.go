package persistence

import (
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/testutil/maintenancestore"
)

// Maintenance entry points refuse an unwired receipt store; these test
// executors get the in-memory one, standing in for the PostgreSQL store the
// composition root wires in production.
func maintenanceTestFileExecutor() filesystem.OSFileExecutor {
	return filesystem.OSFileExecutor{Receipts: maintenancestore.New()}
}

func maintenanceTestCleanupExecutor(anchor string) filesystem.OSCleanupExecutor {
	executor := filesystem.NewOSCleanupExecutor(anchor)
	executor.Receipts = maintenancestore.New()
	return executor
}
