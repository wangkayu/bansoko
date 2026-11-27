//go:build restorecutoverpg

package application_test

import (
	"context"
	"database/sql"
	"encoding/json"
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
			if err := backupapp.CreateEmptyRestoreDatabase(ctx, admin, live, ready); err != nil {
				t.Fatal(err)
			}
			if err := tools.RestoreInto(ctx, dump, ready); err != nil {
				t.Fatal(err)
			}
			pinned, err := backupapp.PinRestoreCutover(ctx, admin, live, ready, aside)
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
				if err := backupapp.ReconcileRestoreCutover(ctx, recoveryDB, recovered, nil); err != nil {
					t.Fatal(err)
				}
			}
			foreign := recovered
			foreign.ReadyOID++
			if err := backupapp.ReconcileRestoreCutover(ctx, recoveryDB, foreign, nil); err == nil {
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
