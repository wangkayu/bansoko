package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

// The database catalog is the restore journal. A restore works on databases
// named after the deployment database:
//
//	<db>__restoring_<unix>  being loaded; never used, always dropped
//	<db>__restored_<unix>   loaded and verified; waiting for the switch
//	<db>__replaced_<unix>   the previous live database during the switch
//
// Renames are atomic, so after any crash the names alone say how far a
// restore got (RecoverInterrupted).
const (
	kindRestoring = "restoring"
	kindRestored  = "restored"
	kindReplaced  = "replaced"

	maintenanceDatabase = "postgres"

	// rollForwardWindow bounds how old a ready restore may be and still be
	// switched in at startup; an older one is dropped instead of silently
	// replacing data written since.
	rollForwardWindow = 15 * time.Minute
	switchAttempts    = 3
)

var databaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)

func validDatabaseName(name string) bool { return databaseNamePattern.MatchString(name) }

func sideName(database, kind string, stamp int64) string {
	return fmt.Sprintf("%s__%s_%d", database, kind, stamp)
}

// parseSideName recognises a restore database of database.
func parseSideName(database, name string) (string, int64, bool) {
	rest, ok := strings.CutPrefix(name, database+"__")
	if !ok {
		return "", 0, false
	}
	for _, kind := range []string{kindRestoring, kindRestored, kindReplaced} {
		digits, ok := strings.CutPrefix(rest, kind+"_")
		if !ok || digits == "" {
			continue
		}
		stamp, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || stamp <= 0 || strconv.FormatInt(stamp, 10) != digits {
			continue
		}
		return kind, stamp, true
	}
	return "", 0, false
}

// quoteIdent quotes a database name already proven by databaseNamePattern (or
// built from one by sideName).
func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func quoteLiteral(value string) string { return `'` + strings.ReplaceAll(value, `'`, `''`) + `'` }

// canSwapDatabase reports whether the connected role may create, rename and
// drop the deployment database: a superuser, or its owner holding CREATEDB.
func canSwapDatabase(ctx context.Context, admin *sql.DB, database string) (bool, error) {
	var super, createdb, owner bool
	err := admin.QueryRowContext(ctx, `
SELECT r.rolsuper, r.rolcreatedb, COALESCE(d.datdba = r.oid, false)
FROM pg_roles r LEFT JOIN pg_database d ON d.datname = $1
WHERE r.rolname = current_user`, database).Scan(&super, &createdb, &owner)
	if err != nil {
		return false, err
	}
	return super || (createdb && owner), nil
}

// createLike creates an empty database with the encoding and collation of the
// deployment database.
func createLike(ctx context.Context, admin *sql.DB, name, like string) error {
	var encoding, collate, ctype string
	if err := admin.QueryRowContext(ctx, `
SELECT pg_encoding_to_char(encoding), datcollate, datctype FROM pg_database WHERE datname = $1`, like).
		Scan(&encoding, &collate, &ctype); err != nil {
		return err
	}
	_, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s`,
		quoteIdent(name), quoteLiteral(encoding), quoteLiteral(collate), quoteLiteral(ctype)))
	return err
}

func dropDatabase(ctx context.Context, admin *sql.DB, name string) error {
	_, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
	return err
}

func renameDatabase(ctx context.Context, admin *sql.DB, from, to string) error {
	_, err := admin.ExecContext(ctx, `ALTER DATABASE `+quoteIdent(from)+` RENAME TO `+quoteIdent(to))
	return err
}

// terminateConnections ends the other sessions on database so it can be
// renamed. Sessions of roles this role may not signal are left; the rename
// then fails and nothing changes.
func terminateConnections(ctx context.Context, admin *sql.DB, database string) {
	_, _ = admin.ExecContext(ctx, `
SELECT pg_terminate_backend(pid) FROM pg_stat_activity
WHERE datname = $1 AND pid <> pg_backend_pid()`, database)
}

// swapIn makes ready the deployment database: the live one is renamed aside,
// ready takes its name, the old one is dropped. If ready cannot take the name
// the live database is put back and ready is dropped.
func swapIn(ctx context.Context, admin *sql.DB, database, ready string, stamp int64) error {
	replaced := sideName(database, kindReplaced, stamp)
	var err error
	for attempt := 0; attempt < switchAttempts; attempt++ {
		terminateConnections(ctx, admin, database)
		if err = renameDatabase(ctx, admin, database, replaced); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		_ = dropDatabase(ctx, admin, ready)
		return fmt.Errorf("set the current database aside: %w", err)
	}
	if err := renameDatabase(ctx, admin, ready, database); err != nil {
		if backErr := renameDatabase(ctx, admin, replaced, database); backErr != nil {
			return errors.Join(fmt.Errorf("activate restored database: %w", err), fmt.Errorf("put the current database back: %w", backErr))
		}
		_ = dropDatabase(ctx, admin, ready)
		return fmt.Errorf("activate restored database: %w", err)
	}
	// The switch is done; a failed drop only leaves disk space behind, which
	// the next start reclaims.
	_ = dropDatabase(ctx, admin, replaced)
	return nil
}

// RecoverInterrupted finishes or undoes a restore a crash interrupted. It runs
// at startup before the serving pool connects:
//   - the live database is missing but set aside as replaced: switch in its
//     ready restored database if there is one, else put it back;
//   - a ready restored database newer than rollForwardWindow whose schema
//     matches the live one: switch it in (the owner confirmed it and the
//     process stopped before the switch); an older one is dropped;
//   - databases being loaded and replaced leftovers are dropped.
func RecoverInterrupted(ctx context.Context, cluster Cluster, logger observabilityport.RuntimeLogger, now time.Time) error {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	database := cluster.Database()
	if !validDatabaseName(database) {
		return nil
	}
	admin, err := cluster.Open(ctx, maintenanceDatabase)
	if err != nil {
		return err
	}
	defer admin.Close()
	names, liveExists, err := restoreDatabases(ctx, admin, database)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	note := func(message, reason string) {
		logger.Warn(ctx, observabilityport.RuntimeEvent{
			Message: message, Component: "backup", Operation: "restore_recovery", Outcome: "warning", ReasonCode: reason,
		})
	}
	if !liveExists {
		replaced, ok := newest(database, names, kindReplaced)
		if !ok {
			return errors.New("the deployment database is missing and no replaced copy exists")
		}
		ready := sideName(database, kindRestored, replaced)
		if names[ready] {
			if err := renameDatabase(ctx, admin, ready, database); err != nil {
				return err
			}
			delete(names, ready)
			note("interrupted restore finished: restored data switched in", "backup_restore_recovered_forward")
		} else {
			if err := renameDatabase(ctx, admin, sideName(database, kindReplaced, replaced), database); err != nil {
				return err
			}
			delete(names, sideName(database, kindReplaced, replaced))
			note("interrupted restore undone: previous data put back", "backup_restore_recovered_back")
		}
	} else if stamp, ok := newest(database, names, kindRestored); ok {
		ready := sideName(database, kindRestored, stamp)
		age := now.Sub(time.Unix(stamp, 0))
		if age >= 0 && age <= rollForwardWindow && sameSchema(ctx, cluster, database, ready) {
			if err := swapIn(ctx, admin, database, ready, stamp); err != nil {
				return err
			}
			delete(names, ready)
			delete(names, sideName(database, kindReplaced, stamp))
			note("interrupted restore finished: restored data switched in", "backup_restore_recovered_forward")
		}
	}
	for name := range names {
		if err := dropDatabase(ctx, admin, name); err != nil {
			return err
		}
	}
	return nil
}

// restoreDatabases lists the restore databases of database and whether the
// live database exists.
func restoreDatabases(ctx context.Context, admin *sql.DB, database string) (map[string]bool, bool, error) {
	rows, err := admin.QueryContext(ctx, `SELECT datname FROM pg_database`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	names := map[string]bool{}
	live := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, false, err
		}
		if name == database {
			live = true
			continue
		}
		if _, _, ok := parseSideName(database, name); ok {
			names[name] = true
		}
	}
	return names, live, rows.Err()
}

func newest(database string, names map[string]bool, kind string) (int64, bool) {
	var best int64
	found := false
	for name := range names {
		gotKind, stamp, ok := parseSideName(database, name)
		if !ok || gotKind != kind {
			continue
		}
		if !found || stamp > best {
			best, found = stamp, true
		}
	}
	return best, found
}

func sameSchema(ctx context.Context, cluster Cluster, a, b string) bool {
	versionOf := func(name string) string {
		db, err := cluster.Open(ctx, name)
		if err != nil {
			return ""
		}
		defer db.Close()
		version, err := latestSchemaVersion(ctx, db)
		if err != nil {
			return ""
		}
		return version
	}
	left := versionOf(a)
	return left != "" && left == versionOf(b)
}
