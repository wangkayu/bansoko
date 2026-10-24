// Command disposable-db-check is the shell-facing fail-closed guard for
// destructive PostgreSQL test/script targets. It validates BANSOKO_TEST_DATABASE_URL
// against the same rules the Go test helpers use (disposabledb package) and
// exits non-zero before any mutation.
//
// Subcommands:
//
//	check          validate BANSOKO_TEST_DATABASE_URL (requires BANSOKO_TEST_DB_DISPOSABLE=1)
//	inventory      list active PG test packages; --expect-file <path> diffs
//	               the canonical list (used by scripts/ci/test-integration.sh)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bansoko/internal/runtime/database"
	"bansoko/internal/testutil/disposabledb/guard"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: disposable-db-check <check|inventory> [--expect-file PATH]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "check":
		runCheck()
	case "inventory":
		runInventory(os.Args[2:])
	case "create":
		runCreate(os.Args[2:])
	case "provision-runtime":
		runProvisionRuntime()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

// runProvisionRuntime converges the test runtime login role through the owner
// DSN so the PostgreSQL integration gate can run application tests under the real runtime
// privilege boundary. The credentials come from BANSOKO_TEST_DB_RUNTIME_USER /
// BANSOKO_TEST_DB_RUNTIME_PASSWORD; nothing sensitive is printed, so a generated
// internal credential never reaches the gate log.
func runProvisionRuntime() {
	ctx := context.Background()
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if err := guard.Check(ctx, dsn, guard.Options{RequireDisposable: true}); err != nil {
		fmt.Fprintf(os.Stderr, "disposable DB guard rejected target: %v\n", err)
		os.Exit(1)
	}
	conn, err := database.Open(ctx, database.Config{DSN: dsn})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect owner: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := database.ProvisionRuntimeRole(ctx, conn, database.RuntimeRoleConfig{
		Role:     os.Getenv("BANSOKO_TEST_DB_RUNTIME_USER"),
		Password: os.Getenv("BANSOKO_TEST_DB_RUNTIME_PASSWORD"),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "provision runtime role: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(`{"ok":true}`)
}

// runCreate creates a database via an admin (postgres maintenance) URL and
// then proves the disposable guard accepts it. Used by rehearsal scripts on
// hosts without psql/docker clients.
func runCreate(args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	dbName := fs.String("db", "", "database name to create")
	adminURL := fs.String("admin-url", "", "admin connection URL (usually the postgres maintenance database)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *dbName == "" || *adminURL == "" {
		fmt.Fprintln(os.Stderr, "usage: disposable-db-check create --db NAME --admin-url URL")
		os.Exit(2)
	}
	ctx := context.Background()
	conn, err := database.Open(ctx, database.Config{DSN: *adminURL})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect admin: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, *dbName).Scan(&exists); err != nil {
		fmt.Fprintf(os.Stderr, "check database: %v\n", err)
		os.Exit(1)
	}
	if !exists {
		quoted := strings.ReplaceAll(*dbName, `"`, `""`)
		if _, err := conn.ExecContext(ctx, `CREATE DATABASE "`+quoted+`"`); err != nil {
			fmt.Fprintf(os.Stderr, "create database: %v\n", err)
			os.Exit(1)
		}
	}
	target := *adminURL
	// Rewrite the admin URL to point at the new database (admin URL's db is
	// usually 'postgres').
	if idx := strings.LastIndex(target, "/"); idx >= 0 {
		rest := target[idx+1:]
		if q := strings.Index(rest, "?"); q >= 0 {
			target = target[:idx+1] + *dbName + rest[q:]
		} else {
			target = target[:idx+1] + *dbName
		}
	}
	if err := guard.Check(ctx, target, guard.Options{RequireDisposable: true}); err != nil {
		fmt.Fprintf(os.Stderr, "disposable DB guard rejected created target: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("{\"ok\":true,\"database\":\"%s\"}\n", *dbName)
}

func runCheck() {
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if err := guard.Check(context.Background(), dsn, guard.Options{RequireDisposable: true}); err != nil {
		fmt.Fprintf(os.Stderr, "disposable DB guard rejected target: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(`{"ok":true}`)
}

// scanPGTestPackages returns the sorted, de-duplicated set of Go packages
// whose test files are active PostgreSQL lanes: they either read
// BANSOKO_TEST_DATABASE_URL, execute DROP SCHEMA (destructive reset), or route
// through the shared disposable guard (testutil/disposabledb). These are the
// packages the canonical PostgreSQL lane must run. It scans the application
// tree (internal) and the composition/CLI commands (cmd), skipping
// testutil.
func scanPGTestPackages(root string) ([]string, error) {
	set := map[string]bool{}
	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				if info.Name() == "testutil" || strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(info.Name(), "_test.go") {
				return nil
			}
			matched, err := fileMatches(path)
			if err != nil {
				return err
			}
			if matched {
				pkg, err := packageFor(filepath.Dir(path), root)
				if err != nil {
					return err
				}
				set[pkg] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	pkgs := make([]string, 0, len(set))
	for p := range set {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs, nil
}

func fileMatches(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// testutil/animetest is the shared wrapper over the disposable guard:
		// animetest.OpenTestDB delegates to disposabledb.ResetDisposableSchema,
		// so a package whose tests reach a database only through that wrapper is
		// still a PG package. Without this marker a package that loses its last
		// direct disposabledb import is silently reclassified as database-free
		// and then fails in the unit lane on the disposable guard.
		if strings.Contains(line, "BANSOKO_TEST_DATABASE_URL") ||
			strings.Contains(line, "DROP SCHEMA") ||
			strings.Contains(line, "testutil/disposabledb") ||
			strings.Contains(line, "testutil/animetest") {
			return true, nil
		}
	}
	return false, sc.Err()
}

func packageFor(dir, root string) (string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	// Output is intended to be passed straight to `go test`, so use the
	// ./relative/path form rather than the module path.
	return "./" + filepath.ToSlash(rel), nil
}

func runInventory(args []string) {
	fs := flag.NewFlagSet("inventory", flag.ExitOnError)
	expectFile := fs.String("expect-file", "", "canonical package list to diff against")
	_ = fs.Parse(args)

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pkgs, err := scanPGTestPackages(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory scan failed: %v\n", err)
		os.Exit(1)
	}

	if *expectFile == "" {
		for _, p := range pkgs {
			fmt.Println(p)
		}
		return
	}

	raw, err := os.ReadFile(*expectFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read expect file: %v\n", err)
		os.Exit(1)
	}
	var expected []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			expected = append(expected, line)
		}
	}
	sort.Strings(expected)

	if !equalStrings(pkgs, expected) {
		fmt.Fprintln(os.Stderr, "PG test package inventory drifted from canonical list:")
		fmt.Fprintf(os.Stderr, "  scanned (%d): %v\n", len(pkgs), pkgs)
		fmt.Fprintf(os.Stderr, "  expected (%d): %v\n", len(expected), expected)
		os.Exit(1)
	}
	fmt.Printf(`{"ok":true,"packages":%d}`, len(pkgs))
	fmt.Println()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
