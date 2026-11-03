package filemaintenance

import (
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/testutil/maintenancestore"
)

// Maintenance entry points refuse an unwired receipt store, so every test
// executor gets one: an in-memory store whose receipts survive the executor
// instance exactly like the on-disk markers did, standing in for the
// PostgreSQL store the composition root wires in production. The PostgreSQL
// store has its own integration test in adapter/receipts.
func maintenanceTestFileExecutor() filesystem.OSFileExecutor {
	return filesystem.OSFileExecutor{Receipts: maintenancestore.New()}
}

func maintenanceTestCleanupExecutor(anchor string) filesystem.OSCleanupExecutor {
	executor := filesystem.NewOSCleanupExecutor(anchor)
	executor.Receipts = maintenancestore.New()
	return executor
}
