//go:build restorecutoverpg

package application_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/backup/adapter/pgarchive"
	backupapp "bansoko/internal/backup/application"
	"bansoko/internal/update/adapter/pgexec"
)

func TestRestoreAdmissionPG17(t *testing.T) {
	base, _ := newCutoverCluster(t)
	ctx := t.Context()
	open := func(role string) *sql.DB {
		t.Helper()
		u := base
		u.User = url.User(role)
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	execute := func(db *sql.DB, query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	admin := open(base.User.Username())
	const live, ready, aside, operation = `Live "恢复"`, `Ready "恢复"`, `Aside "恢复"`, "admission-fixture"
	const liveSQL, readySQL = `"Live ""恢复"""`, `"Ready ""恢复"""`
	execute(admin, `CREATE ROLE fixture_owner LOGIN CREATEDB CREATEROLE`)
	execute(admin, `CREATE DATABASE `+liveSQL+` OWNER fixture_owner`)
	owner := open("fixture_owner")
	if _, err := backupapp.CheckRestorePrerequisites(ctx, owner, live); err == nil {
		t.Fatal("owner without session termination authority admitted")
	}
	execute(admin, `GRANT pg_signal_backend TO fixture_owner`)
	pinned, err := backupapp.CheckRestorePrerequisites(ctx, owner, live)
	if err != nil {
		t.Fatal(err)
	}
	execute(owner, `CREATE DATABASE `+readySQL)
	for _, claim := range []string{"", "bansoko.restore-claim:other-operation"} {
		if claim != "" {
			execute(owner, `COMMENT ON DATABASE `+readySQL+` IS '`+claim+`'`)
		}
		if err := backupapp.EnsureOperationRestoreDatabase(ctx, owner, live, ready, aside, operation); err == nil {
			t.Fatalf("adopted ready database with claim %q", claim)
		}
	}
	execute(owner, `DROP DATABASE `+readySQL)
	if err := backupapp.EnsureOperationRestoreDatabase(ctx, owner, live, ready, aside, operation); err != nil {
		t.Fatal(err)
	}
	cutover, err := backupapp.PinRestoreCutover(ctx, owner, live, ready, aside, operation)
	if err != nil {
		t.Fatal(err)
	}
	execute(owner, `DROP DATABASE `+liveSQL)
	execute(owner, `CREATE DATABASE `+liveSQL)
	if err := backupapp.ProveLiveDatabase(ctx, owner, pinned); err == nil {
		t.Fatal("accepted a replacement database with the same name and owner")
	}
	if err := backupapp.ReconcileRestoreCutover(ctx, owner, cutover, operation, nil, nil); err == nil {
		t.Fatal("renamed a replacement database with the same name and owner")
	}
	if _, err := backupapp.CheckRestorePrerequisites(ctx, owner, live); err != nil {
		t.Fatalf("replacement must still be online: %v", err)
	}
}

func TestRestoreCutoverPG17(t *testing.T) {
	base, restart := newCutoverCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cluster, err := pgarchive.NewCluster(base.String())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := cluster.Open(ctx, "postgres")
	if err != nil {
		t.Fatal("cannot open disposable PG17 maintenance database")
	}
	defer admin.Close()
	exec := func(t *testing.T, db *sql.DB, statement string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	for _, point := range []string{"before first rename", "between renames", "after second rename"} {
		t.Run(point, func(t *testing.T) {
			id := "bsk_cutover_" + uuid.NewString()[:8]
			live, ready, aside, runtimeRole := id, id+"_ready", id+"_aside", id+"_app"
			exec(t, admin, `CREATE ROLE `+runtimeRole+` LOGIN`)
			t.Cleanup(func() {
				for _, name := range []string{ready, aside, live} {
					if _, err := admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
						t.Errorf("drop test database: %v", err)
					}
				}
				if _, err := admin.ExecContext(context.Background(), `DROP ROLE `+runtimeRole); err != nil {
					t.Errorf("drop test runtime role: %v", err)
				}
			})
			exec(t, admin, `CREATE DATABASE `+live)
			old, err := cluster.Open(ctx, live)
			if err != nil {
				t.Fatal("cannot open test database")
			}
			defer old.Close()
			exec(t, old, `CREATE TABLE public.previous_data (value text NOT NULL)`)
			exec(t, old, `INSERT INTO public.previous_data VALUES ('preserved')`)
			exec(t, old, `GRANT SELECT, INSERT, UPDATE ON public.previous_data TO `+runtimeRole)

			dump := filepath.Join(t.TempDir(), "previous.dump")
			ownerURL := base
			ownerURL.Path, ownerURL.RawPath = "/"+live, ""
			tools, err := pgarchive.NewTools(ownerURL.String(), pgexec.DefaultCandidatePaths)
			if err != nil {
				t.Fatal(err)
			}
			if err := tools.Dump(ctx, dump); err != nil {
				t.Fatal(err)
			}
			if err := tools.Verify(ctx, dump); err != nil {
				t.Fatal(err)
			}
			exec(t, old, `CREATE TABLE public.added_by_failed_migration (value text)`)
			if err := backupapp.EnsureOperationRestoreDatabase(ctx, admin, live, ready, aside, id); err != nil {
				t.Fatal(err)
			}
			if err := tools.RestoreInto(ctx, dump, ready); err != nil {
				t.Fatal(err)
			}
			pinned, err := backupapp.PinRestoreCutover(ctx, admin, live, ready, aside, id)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := json.Marshal(pinned)
			if err != nil {
				t.Fatal(err)
			}
			if point != "before first rename" {
				_ = old.Close()
				exec(t, admin, `ALTER DATABASE `+live+` ALLOW_CONNECTIONS false`)
				exec(t, admin, `ALTER DATABASE `+live+` RENAME TO `+aside)
			}
			if point == "after second rename" {
				exec(t, admin, `ALTER DATABASE `+ready+` ALLOW_CONNECTIONS false`)
				exec(t, admin, `ALTER DATABASE `+ready+` RENAME TO `+live)
			}

			restart(t)
			var recovered backupapp.RestoreCutover
			if err := json.Unmarshal(evidence, &recovered); err != nil {
				t.Fatal(err)
			}
			recoveryDB, err := cluster.Open(ctx, "postgres")
			if err != nil {
				t.Fatal("cannot reopen disposable maintenance database")
			}
			defer recoveryDB.Close()
			for range 2 {
				if err := backupapp.ReconcileRestoreCutover(ctx, recoveryDB, recovered, id, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			foreign := recovered
			foreign.ReadyOID++
			if err := backupapp.ReconcileRestoreCutover(ctx, recoveryDB, foreign, id, nil, nil); err == nil {
				t.Fatal("accepted unproved database identity")
			}

			runtimeURL := ownerURL
			runtimeURL.User = url.User(runtimeRole)
			app, err := sql.Open("pgx", runtimeURL.String())
			if err != nil {
				t.Fatal("cannot open disposable runtime connection")
			}
			defer app.Close()
			var value string
			var newTableAbsent bool
			if err := app.QueryRowContext(ctx, `SELECT value, to_regclass('public.added_by_failed_migration') IS NULL FROM public.previous_data`).Scan(&value, &newTableAbsent); err != nil || value != "preserved" || !newTableAbsent {
				t.Fatalf("runtime read and schema after recovery: value=%q newTableAbsent=%v err=%v", value, newTableAbsent, err)
			}
			exec(t, app, `INSERT INTO public.previous_data VALUES ('runtime write')`)
			exec(t, app, `UPDATE public.previous_data SET value = 'updated' WHERE value = 'preserved'`)
			if _, err := app.ExecContext(ctx, `DELETE FROM public.previous_data`); err == nil {
				t.Fatal("runtime was granted DELETE outside the backup ACL")
			}
		})
	}
}

// TestRestoreQuarantinePG17 pins the PostgreSQL 17 behaviour the recovery
// cutover depends on: RENAME inside a transaction block is accepted, so the
// connection policy and rename commit or roll back together, while eviction
// ends existing sessions. Replay reclaims only its own unclaimed scratch.
func TestRestoreQuarantinePG17(t *testing.T) {
	base, _ := newCutoverCluster(t)
	ctx := t.Context()
	admin, err := sql.Open("pgx", base.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	exec := func(t *testing.T, statement string) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	read := func(t *testing.T, name string) (bool, bool) {
		t.Helper()
		var allows bool
		err := admin.QueryRowContext(ctx, `SELECT datallowconn FROM pg_catalog.pg_database WHERE datname = $1`, name).Scan(&allows)
		if errors.Is(err, sql.ErrNoRows) {
			return false, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return true, allows
	}
	id := "bsk_quarantine_" + uuid.NewString()[:8]
	live, aside, blocked := id, id+"_aside", id+"_blocked"
	ready := id + "_ready"
	scratch := backupapp.RestoreScratchName(blocked, ready, id)
	other := backupapp.RestoreScratchName(blocked, ready, id+"-other")
	for _, name := range []string{live, blocked, ready, scratch, other} {
		statement := `CREATE DATABASE ` + name
		if name == scratch || name == other {
			statement += ` ALLOW_CONNECTIONS false`
		}
		exec(t, statement)
	}
	liveURL := base
	liveURL.Path = "/" + live
	client, err := sql.Open("pgx", liveURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := client.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}

	// One transaction admits no new session and publishes the rename.
	if err := backupapp.QuarantineAndRenameDatabase(ctx, admin, live, aside); err != nil {
		t.Fatalf("transactional quarantine and rename: %v", err)
	}
	if present, _ := read(t, live); present {
		t.Fatal("the quarantined database kept its serving name")
	}
	if present, allows := read(t, aside); !present || allows {
		t.Fatalf("aside database present=%v allowsConnections=%v, want a closed quarantine", present, allows)
	}

	var stillConnected bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, pid).Scan(&stillConnected); err != nil || stillConnected {
		t.Fatalf("live client was not evicted: connected=%v err=%v", stillConnected, err)
	}

	// A refused rename rolls the quarantine back: the database keeps its name
	// and stays online instead of being left half-renamed.
	if err := backupapp.QuarantineAndRenameDatabase(ctx, admin, blocked, aside); err == nil {
		t.Fatal("renamed onto an existing database name")
	}
	if present, allows := read(t, blocked); !present || !allows {
		t.Fatalf("failed rename left the database present=%v allowsConnections=%v", present, allows)
	}

	// A crash between CREATE DATABASE and the claiming transaction leaves an
	// inaccessible, unclaimed scratch database. The next admission reclaims it
	// and never adopts or drops a database that is connectable or claimed.
	exec(t, `DROP DATABASE `+ready)
	for _, mutation := range []struct{ set, reset string }{
		{`COMMENT ON DATABASE ` + scratch + ` IS 'bansoko.restore-claim:other-operation'`, `COMMENT ON DATABASE ` + scratch + ` IS NULL`},
		{`ALTER DATABASE ` + scratch + ` ALLOW_CONNECTIONS true`, `ALTER DATABASE ` + scratch + ` ALLOW_CONNECTIONS false`},
	} {
		exec(t, mutation.set)
		if err := backupapp.EnsureOperationRestoreDatabase(ctx, admin, blocked, ready, id+"_unused_aside", id); err == nil {
			t.Fatal("occupied scratch database was reclaimed")
		}
		exec(t, mutation.reset)
	}
	if err := backupapp.EnsureOperationRestoreDatabase(ctx, admin, blocked, ready, id+"_unused_aside", id); err != nil {
		t.Fatal(err)
	}
	if present, _ := read(t, scratch); present {
		t.Fatal("abandoned unclaimed scratch database survived admission")
	}
	if present, _ := read(t, other); !present {
		t.Fatal("another operation's scratch database was dropped")
	}
	if present, allows := read(t, ready); !present || !allows {
		t.Fatalf("ready database present=%v allowsConnections=%v", present, allows)
	}
}

// The test controls only the PostgreSQL cluster it initialized in t.TempDir.
// No configured application or shared integration database is ever stopped.
func newCutoverCluster(t *testing.T) (url.URL, func(*testing.T)) {
	t.Helper()
	var bin string
	for _, path := range pgexec.DefaultCandidatePaths {
		if info, err := os.Stat(filepath.Join(path, "initdb")); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			bin = path
			break
		}
	}
	if bin == "" {
		t.Fatal("PostgreSQL 17 server tools are required for cutover tests")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	tool := func(t *testing.T, name string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, filepath.Join(bin, name), args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
	tool(t, "initdb", "-D", data, "-U", "cutover_owner", "-A", "trust", "--no-locale")
	start := func(t *testing.T) {
		t.Helper()
		tool(t, "pg_ctl", "-D", data, "-l", filepath.Join(root, "postgres.log"), "-o", fmt.Sprintf("-h 127.0.0.1 -p %d -c unix_socket_directories=", port), "-w", "start")
	}
	stop := func(t *testing.T) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); err == nil {
			tool(t, "pg_ctl", "-D", data, "-m", "immediate", "-w", "stop")
		}
	}
	t.Cleanup(func() { stop(t) })
	start(t)
	base := url.URL{Scheme: "postgres", Host: fmt.Sprintf("127.0.0.1:%d", port), User: url.User("cutover_owner"), Path: "/postgres", RawQuery: "sslmode=disable"}
	return base, func(t *testing.T) {
		stop(t)
		start(t)
	}
}
