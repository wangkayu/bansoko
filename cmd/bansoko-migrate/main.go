package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/identity"
	"bansoko/internal/runtime/migrations"
)

func main() {
	runtimeLogger := observabilityapplication.NewJSONRuntimeLogger(os.Stderr)
	ctx := context.Background()
	if len(os.Args) > 1 && os.Args[1] == "rotate-secret-key" {
		exitProcess(runRotateSecretKey(ctx, os.Getenv, os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "rotate-session-secret" {
		exitProcess(runRotateSessionSecret(ctx, os.Getenv, os.Stdout, os.Stderr))
		return
	}
	cfg, err := config.FromFlags(os.Args[1:])
	if err != nil {
		fatalStartup(runtimeLogger, "configuration parse failed", "config_parse_failed")
	}
	// Migrate always runs with the schema owner: BANSOKO_DATABASE_URL. The
	// serving pool prefers the derived runtime connection
	// (BANSOKO_RUNTIME_DATABASE_URL); that preference must not leak into
	// migration, which needs DDL and CREATEROLE. The --database-url flag only
	// applies when the environment has no owner DSN.
	db, err := database.Open(ctx, database.Config{DSN: ownerDSN(os.Getenv, cfg.DatabaseURL)})
	if err != nil {
		fatalStartup(runtimeLogger, "database open failed", "database_open_failed")
	}
	defer db.Close()
	runtimeRole, err := runtimeRoleFromEnv(os.Getenv, cfg.Environment)
	if err != nil {
		fatalStartup(runtimeLogger, "runtime database connection is not configured: start through /app/bootstrap, which derives it from BANSOKO_DATABASE_URL", "runtime_role_config_missing")
	}
	if runtimeRole != nil {
		// The baseline creates the runtime group role and provisioning
		// converges the login role: both need CREATEROLE. Checking first turns
		// an external account without it into one actionable error.
		if err := database.AssertCanManageRoles(ctx, db); err != nil {
			if errors.Is(err, database.ErrCannotManageRoles) {
				fatalStartup(runtimeLogger, "the account in BANSOKO_DATABASE_URL cannot create roles: grant it CREATEROLE (ALTER ROLE <account> CREATEROLE) or use an account that has it", "owner_createrole_missing")
			}
			fatalStartup(runtimeLogger, "role privilege check failed", "owner_privilege_check_failed")
		}
	}
	if err := (migrations.Runner{DB: db, Dir: cfg.SchemaDir}).Apply(ctx); err != nil {
		fatalStartup(runtimeLogger, migrationFailureSummary(err), "migration_apply_failed")
	}
	// The schema owner holds DDL. The runtime login role is provisioned here,
	// from the owner connection, and is only ever a member of the fixed
	// `bansoko_runtime` group role the baseline creates: separate credentials
	// for a role that cannot alter triggers, run DDL or mint cleanup receipts.
	if runtimeRole != nil {
		if err := database.ProvisionRuntimeRole(ctx, db, *runtimeRole); err != nil {
			fatalStartup(runtimeLogger, "runtime role provisioning failed", "runtime_role_provision_failed")
		}
	}
	installID, err := identity.EnsureInstallID(ctx, db)
	if err != nil {
		fatalStartup(runtimeLogger, "install id failed", "install_id_failed")
	}
	runtimeLogger.Info(ctx, observabilityport.RuntimeEvent{
		Message:    "schema ready",
		Component:  "migrate",
		Operation:  "apply",
		Outcome:    "info",
		ReasonCode: "schema_ready",
		OwnerID:    installID,
	})
}

var exitProcess = os.Exit

// ownerDSN is the connection migration runs with: the schema-owner
// BANSOKO_DATABASE_URL. The flag value is only a fallback for environments
// without the variable.
func ownerDSN(getenv func(string) string, flagValue string) string {
	if dsn := strings.TrimSpace(getenv("BANSOKO_DATABASE_URL")); dsn != "" {
		return dsn
	}
	return flagValue
}

// runtimeRoleFromEnv reads the runtime login role to converge from the
// internal BANSOKO_RUNTIME_DATABASE_URL the bootstrap derives from the owner
// connection and the generated password. Outside production a missing value
// means a single-connection development database (nil, nil); production
// refuses so a deployment can never keep serving as the schema owner by
// omission.
func runtimeRoleFromEnv(getenv func(string) string, environment string) (*database.RuntimeRoleConfig, error) {
	runtimeDSN := strings.TrimSpace(getenv(bootsecrets.EnvRuntimeDatabaseURL))
	if runtimeDSN == "" {
		if environment == "production" {
			return nil, fmt.Errorf("%s is required in production: the runtime must not share the schema owner role", bootsecrets.EnvRuntimeDatabaseURL)
		}
		return nil, nil
	}
	role, password, err := bootsecrets.RuntimeCredentials(runtimeDSN)
	if err != nil {
		return nil, err
	}
	return &database.RuntimeRoleConfig{Role: role, Password: password}, nil
}

func migrationFailureSummary(err error) string {
	if err == nil {
		return "migration apply failed"
	}
	var applyErr *migrations.MigrationApplyError
	if errors.As(err, &applyErr) {
		return fmt.Sprintf("migration %s failed: %s (%s)", applyErr.Operation, applyErr.Version, safeMigrationErrorClass(applyErr.Err))
	}
	return fmt.Sprintf("migration apply failed (%s)", safeMigrationErrorClass(err))
}

func safeMigrationErrorClass(err error) string {
	if err == nil {
		return "migration_error"
	}
	for _, item := range []struct {
		target error
		class  string
	}{
		{migrations.ErrChecksumMismatch, "checksum_mismatch"},
		{migrations.ErrInvalidMigrationSet, "invalid_migration_set"},
		{migrations.ErrUnsupportedSchema, "schema_mismatch"},
		{migrations.ErrSchemaIdentity, "schema_mismatch"},
	} {
		if errors.Is(err, item.target) {
			return item.class
		}
	}

	// Inspect only the deepest cause for non-sentinel errors. Wrapper text is
	// context, not the error being classified, and must not override its class.
	deepest := err
	for {
		cause := errors.Unwrap(deepest)
		if cause == nil {
			break
		}
		deepest = cause
	}
	if class := migrationErrorTextClass(deepest.Error()); class != "" {
		return class
	}
	return "migration_error"
}

func migrationErrorTextClass(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "insufficient privilege"):
		return "permission_denied"
	case strings.Contains(lower, "duplicate"), strings.Contains(lower, "already exists"):
		return "constraint_conflict"
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "connect:"):
		return "database_unavailable"
	default:
		return ""
	}
}

// fatalStartup reports a startup failure as a structured runtime event and
// exits with status 1. Messages must be stable, bounded and free of raw errors,
// DSNs, SQL, credentials and private paths.
func fatalStartup(logger observabilityport.RuntimeLogger, message, reasonCode string) {
	logger.Error(context.Background(), observabilityport.RuntimeEvent{
		Message:    safeStartupMessage(message),
		Component:  "migrate",
		Operation:  "startup",
		Outcome:    "failure",
		ReasonCode: reasonCode,
		ErrorClass: "startup_fatal",
	})
	exitProcess(1)
}

func safeStartupMessage(message string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	lower := strings.ToLower(message)
	for _, marker := range []string{"postgres://", "postgresql://", "password", "dsn", "secret", "token", "authorization", "select ", "insert ", "update ", "delete ", "/private/", "/home/", "/users/"} {
		if strings.Contains(lower, marker) {
			return "startup failed (details redacted)"
		}
	}
	message = strings.TrimSpace(message)
	if len(message) > 512 {
		message = message[:512]
		for len(message) > 0 && !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return message
}
