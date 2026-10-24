package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/database"
)

func main() {
	runtimeLogger := observabilityapplication.NewJSONRuntimeLogger(os.Stderr)
	ctx := context.Background()
	cfg, err := config.FromFlags(os.Args[1:])
	if err != nil {
		fatalStartup(runtimeLogger, "configuration parse failed", "config_parse_failed")
	}
	if cfg.Environment != "development" && cfg.Environment != "test" {
		fatalStartup(runtimeLogger, "reset is allowed only in development or test environment", "reset_environment_forbidden")
	}
	if os.Getenv("BANSOKO_RESET_CONFIRM") != "reset" {
		fatalStartup(runtimeLogger, "reset requires BANSOKO_RESET_CONFIRM=reset", "reset_confirm_required")
	}
	db, err := database.Open(ctx, database.Config{DSN: cfg.DatabaseURL})
	if err != nil {
		fatalStartup(runtimeLogger, "database open failed", "database_open_failed")
	}
	defer db.Close()
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		fatalStartup(runtimeLogger, "database name query failed", "database_name_failed")
	}
	lowerDatabaseName := strings.ToLower(databaseName)
	if !strings.HasPrefix(lowerDatabaseName, "test") && !strings.HasPrefix(lowerDatabaseName, "dev") &&
		!strings.HasSuffix(lowerDatabaseName, "_test") && !strings.HasSuffix(lowerDatabaseName, "_dev") {
		fatalStartup(runtimeLogger, "reset target must be a test or dev database", "reset_target_forbidden")
	}

	// Every relation and function in public belongs to the schema baseline and
	// its forward migrations (the runner refuses any other relation), so the
	// reset drops all of them; the next bansoko-migrate rebuilds from 001.
	rows, err := db.QueryContext(ctx, `
SELECT c.relname, c.relkind
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
  AND NOT EXISTS (SELECT 1 FROM pg_depend AS d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')`)
	if err != nil {
		fatalStartup(runtimeLogger, "relations list failed", "relations_list_failed")
	}
	dropSets := map[string]map[string]struct{}{
		"TABLE":             {},
		"VIEW":              {},
		"MATERIALIZED VIEW": {},
		"FOREIGN TABLE":     {},
		"SEQUENCE":          {},
	}
	for rows.Next() {
		var relation, relkind string
		if err := rows.Scan(&relation, &relkind); err != nil {
			_ = rows.Close()
			fatalStartup(runtimeLogger, "relations scan failed", "relations_scan_failed")
		}
		switch relkind {
		case "r", "p":
			dropSets["TABLE"][relation] = struct{}{}
		case "v":
			dropSets["VIEW"][relation] = struct{}{}
		case "m":
			dropSets["MATERIALIZED VIEW"][relation] = struct{}{}
		case "f":
			dropSets["FOREIGN TABLE"][relation] = struct{}{}
		case "S":
			dropSets["SEQUENCE"][relation] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		fatalStartup(runtimeLogger, "relations iteration failed", "relations_iterate_failed")
	}
	_ = rows.Close()
	for _, kind := range []string{"VIEW", "MATERIALIZED VIEW", "FOREIGN TABLE", "TABLE", "SEQUENCE"} {
		relations := make([]string, 0, len(dropSets[kind]))
		for relation := range dropSets[kind] {
			relations = append(relations, relation)
		}
		sort.Strings(relations)
		if len(relations) == 0 {
			continue
		}
		if _, err := db.ExecContext(ctx, `DROP `+kind+` IF EXISTS `+strings.Join(quoteTables(relations), ", ")+` CASCADE`); err != nil {
			fatalStartup(runtimeLogger, "relations drop failed", "relations_drop_failed")
		}
	}
	if _, err := db.ExecContext(ctx, `
DO $$
DECLARE fn regprocedure;
BEGIN
  FOR fn IN
    SELECT p.oid::regprocedure
    FROM pg_proc AS p
    JOIN pg_namespace AS n ON n.oid = p.pronamespace
    WHERE n.nspname = 'public'
      AND NOT EXISTS (SELECT 1 FROM pg_depend AS d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
  LOOP
    EXECUTE format('DROP ROUTINE IF EXISTS %s CASCADE', fn);
  END LOOP;
END
$$`); err != nil {
		fatalStartup(runtimeLogger, "baseline functions drop failed", "functions_drop_failed")
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*)
        FROM pg_class AS c
        JOIN pg_namespace AS n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
          AND NOT EXISTS (SELECT 1 FROM pg_depend AS d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e'))
     + (SELECT COUNT(*)
        FROM pg_proc AS p
        JOIN pg_namespace AS n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public'
          AND NOT EXISTS (SELECT 1 FROM pg_depend AS d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e'))`).Scan(&remaining); err != nil {
		fatalStartup(runtimeLogger, "reset verification failed", "reset_verify_failed")
	}
	if remaining != 0 {
		fatalStartup(runtimeLogger, fmt.Sprintf("reset left %d objects", remaining), "reset_incomplete")
	}
	runtimeLogger.Info(ctx, observabilityport.RuntimeEvent{
		Message:    "reset tables in guarded database",
		Component:  "reset",
		Operation:  "reset",
		Outcome:    "info",
		ReasonCode: "reset_completed",
		OwnerID:    databaseName,
	})
}

// fatalStartup reports a startup failure as a structured runtime event and
// exits with status 1. The message and reason code are stable identifiers;
// raw error details, DSNs and paths are intentionally omitted.
func fatalStartup(logger observabilityport.RuntimeLogger, message, reasonCode string) {
	logger.Error(context.Background(), observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "reset",
		Operation:  "startup",
		Outcome:    "failure",
		ReasonCode: reasonCode,
		ErrorClass: "startup_fatal",
	})
	os.Exit(1)
}

func quoteTables(tables []string) []string {
	quoted := make([]string, len(tables))
	for i, table := range tables {
		quoted[i] = `"public"."` + strings.ReplaceAll(table, `"`, `""`) + `"`
	}
	return quoted
}
