package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"

	"bansoko/internal/backup/adapter/pgarchive"
	backupapplication "bansoko/internal/backup/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/update/adapter/pgexec"
	updateapplication "bansoko/internal/update/application"
)

// backupDatabaseURL is the DSN export and restore connect with. The serving
// pool uses the derived runtime login, which can neither read every owner-only
// table nor create databases, so backups use the schema-owner connection
// (BANSOKO_DATABASE_URL). A process started without the bootstrap serves with
// that same connection. The serving connection pool is unaffected.
func backupDatabaseURL(serving string) string {
	if dsn := strings.TrimSpace(os.Getenv("BANSOKO_DATABASE_URL")); dsn != "" {
		return dsn
	}
	return serving
}

// recoverInterruptedRestore finishes or undoes a restore a crash interrupted
// before the serving pool connects (the live database may be renamed aside
// at that moment). A deployment whose owner DSN cannot reach the server here
// only logs: there is then nothing this process could repair either.
func recoverInterruptedRestore(ctx context.Context, servingDSN string, logger observabilityport.RuntimeLogger) {
	cluster, err := pgarchive.NewCluster(backupDatabaseURL(servingDSN))
	if err != nil {
		return
	}
	recoverCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := backupapplication.RecoverInterrupted(recoverCtx, cluster, logger, time.Now()); err != nil {
		logger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:    "interrupted restore check failed",
			Component:  "backup",
			Operation:  "restore_recovery",
			Outcome:    "failure",
			ReasonCode: "backup_restore_recovery_failed",
			ErrorClass: "db",
		})
	}
}

// buildBackupService composes backup export/restore. Missing PostgreSQL client
// tools or a non-URL owner DSN are not fatal: the backup panel then reports
// the capability as unavailable.
func buildBackupService(ctx context.Context, db *sql.DB, servingDSN, schemaDir string, logger observabilityport.RuntimeLogger) (*backupapplication.Service, error) {
	ownerURL := backupDatabaseURL(servingDSN)
	var tools backupapplication.Tools
	if resolved, err := pgarchive.NewTools(ownerURL, pgexec.DefaultCandidatePaths); err != nil {
		logger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:    "backup tools or database URL unavailable; export and restore are off",
			Component:  "backup",
			Operation:  "startup",
			Outcome:    "warning",
			ReasonCode: "backup_tools_unavailable",
		})
	} else {
		tools = resolved
	}
	// Restore renames the database the serving pool uses, so the owner DSN
	// must name that same database.
	var cluster backupapplication.Cluster
	if resolved, err := pgarchive.NewCluster(ownerURL); err == nil {
		if serving, nameErr := updateapplication.PostgresDatabaseName(servingDSN); nameErr == nil && serving == resolved.Database() {
			cluster = resolved
		}
	}
	return backupapplication.NewService(backupapplication.Options{
		DB:        db,
		Tools:     tools,
		Cluster:   cluster,
		SchemaDir: schemaDir,
		Logger:    logger,
	})
}
