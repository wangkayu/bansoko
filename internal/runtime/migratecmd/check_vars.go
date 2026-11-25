package migratecmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"bansoko/internal/runtime/database"
	preflight "bansoko/internal/runtime/migrations/varspreflight"
)

type checkVarsReport struct {
	OK   bool            `json:"ok"`
	Hits []preflight.Hit `json:"hits"`
}

// runCheckVars implements `bansoko-server migrate check-vars`: scans every
// editable naming and notification template location for `vars.*` references
// before a 0.3 upgrade. Exits 0 when clean, 1 when any editable template still
// references Vars, and 2 on invalid arguments or database failure.
func runCheckVars(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-vars", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit machine-readable JSON output")
	dbURLFlag := fs.String("database-url", "", "PostgreSQL connection URL (overrides BANSOKO_DATABASE_URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "check-vars: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	dsn := checkVarsDSN(getenv, *dbURLFlag)
	if strings.TrimSpace(dsn) == "" {
		fmt.Fprintln(stderr, "check-vars: BANSOKO_DATABASE_URL or --database-url is required")
		return 2
	}
	db, err := database.Open(ctx, database.Config{DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		fmt.Fprintf(stderr, "check-vars: database open failed (%s)\n", safeMigrationErrorClass(err))
		return 2
	}
	defer db.Close()

	hits, err := preflight.Scan(ctx, db)
	if err != nil {
		fmt.Fprintf(stderr, "check-vars: scan failed (%s)\n", safeMigrationErrorClass(err))
		return 2
	}
	return writeCheckVarsReport(hits, *asJSON, stdout, stderr)
}

func checkVarsDSN(getenv func(string) string, flagValue string) string {
	if dsn := strings.TrimSpace(flagValue); dsn != "" {
		return dsn
	}
	return ownerDSN(getenv, "")
}

func writeCheckVarsReport(hits []preflight.Hit, asJSON bool, stdout, stderr io.Writer) int {
	if hits == nil {
		hits = []preflight.Hit{}
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(checkVarsReport{OK: len(hits) == 0, Hits: hits}); err != nil {
			fmt.Fprintf(stderr, "check-vars: encode report failed: %v\n", err)
			return 2
		}
		if len(hits) > 0 {
			return 1
		}
		return 0
	}
	if len(hits) == 0 {
		fmt.Fprintln(stdout, "check-vars: ok (no scanned templates reference naming Vars)")
		return 0
	}
	fmt.Fprintf(stderr, "check-vars: found %d template reference(s) to naming Vars; replace editable templates or drain affected acquisitions before upgrading to 0.3:\n", len(hits))
	for _, hit := range hits {
		fmt.Fprintf(stderr, "  - %s\n", hit.String())
	}
	return 1
}
