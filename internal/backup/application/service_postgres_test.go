package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/backup/adapter/pgarchive"
	backupapplication "bansoko/internal/backup/application"
	"bansoko/internal/runtime/migrations"
	"bansoko/internal/testutil/disposabledb"
)

// The CI PostgreSQL lane has no pg_dump/pg_restore, so these tests drive the
// real orchestration (header, checksum, scratch database, schema proof,
// session sign-out, database switch and crash recovery) with tools that stand
// in for the archive format: the "archive" names the owner account the loaded
// database gets. The real archive round trip is proven on the VM.

func repoMigrations() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "db", "migrations")
}

type fakeTools struct {
	cluster *pgarchive.Cluster
	account string
	failRun bool
}

func (f *fakeTools) Dump(_ context.Context, outPath string) error {
	return os.WriteFile(outPath, []byte("FAKE:"+f.account), 0o600)
}

func (f *fakeTools) Verify(_ context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(data, []byte("FAKE:")) {
		return errors.New("not an archive")
	}
	return nil
}

func (f *fakeTools) RestoreInto(ctx context.Context, path, database string) error {
	if f.failRun {
		return errors.New("injected load failure")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	account := strings.TrimPrefix(string(data), "FAKE:")
	db, err := f.cluster.Open(ctx, database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := (migrations.Runner{DB: db, Dir: repoMigrations()}).Apply(ctx); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO local_accounts(id, username, password_hash) VALUES ('acct-1', $1, repeat('h', 40))`, account); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO auth_sessions(id, account_id, token_hash, expires_at)
VALUES ('sess-1', 'acct-1', repeat('t', 40), CURRENT_TIMESTAMP + interval '1 day')`)
	return err
}

type fixture struct {
	runtime *sql.DB
	cluster *pgarchive.Cluster
	tools   *fakeTools
	service *backupapplication.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	runtimeDB := disposabledb.ResetDisposableSchema(t)
	cluster, err := pgarchive.NewCluster(os.Getenv("BANSOKO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	tools := &fakeTools{cluster: cluster, account: "from-backup"}
	service, err := backupapplication.NewService(backupapplication.Options{
		DB: runtimeDB, Tools: tools, Cluster: cluster, SchemaDir: repoMigrations(), TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{runtime: runtimeDB, cluster: cluster, tools: tools, service: service}
}

func (f *fixture) admin(t *testing.T) *sql.DB {
	t.Helper()
	db, err := f.cluster.Open(context.Background(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sideDatabases lists the restore databases left next to the live one.
func (f *fixture) sideDatabases(t *testing.T) []string {
	t.Helper()
	rows, err := f.admin(t).Query(`SELECT datname FROM pg_database WHERE starts_with(datname, $1) ORDER BY 1`, f.cluster.Database()+"__")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func (f *fixture) liveAccounts(t *testing.T) []string {
	t.Helper()
	db, err := f.cluster.Open(context.Background(), f.cluster.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username FROM local_accounts ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func (f *fixture) export(t *testing.T) []byte {
	t.Helper()
	export, err := f.service.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer export.Close()
	var buf bytes.Buffer
	if _, err := export.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if int64(buf.Len()) != export.Size {
		t.Fatalf("export wrote %d bytes, announced %d", buf.Len(), export.Size)
	}
	return buf.Bytes()
}

func businessCode(err error) string {
	var business *apperrors.Business
	if errors.As(err, &business) {
		return business.Code
	}
	return ""
}

func handedOff(service *backupapplication.Service) bool {
	select {
	case <-service.HandoffRequested():
		return true
	default:
		return false
	}
}

func TestBackupExportRestoreAndSwitch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	info, err := f.service.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ExportAvailable || !info.RestoreAvailable {
		t.Fatalf("info = %+v, want export and restore available", info)
	}
	file := f.export(t)
	if !bytes.HasPrefix(file, []byte(backupapplication.Magic)) {
		t.Fatal("export lacks the backup signature")
	}
	if _, err := f.runtime.Exec(`INSERT INTO local_accounts(id, username, password_hash) VALUES ('acct-live', 'live-only', repeat('h', 40))`); err != nil {
		t.Fatal(err)
	}

	accepted, err := f.service.Restore(ctx, bytes.NewReader(file))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if accepted.BackupCreatedAt.IsZero() || !handedOff(f.service) {
		t.Fatalf("restore accepted without a handoff: %+v", accepted)
	}
	if got := f.liveAccounts(t); len(got) != 1 || got[0] != "live-only" {
		t.Fatalf("live data changed before the switch: %v", got)
	}
	if _, err := f.service.Restore(ctx, bytes.NewReader(file)); businessCode(err) != "RESTORE_IN_PROGRESS" {
		t.Fatalf("second restore error = %v, want RESTORE_IN_PROGRESS", err)
	}

	if err := f.service.Activate(ctx); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := f.liveAccounts(t); len(got) != 1 || got[0] != "from-backup" {
		t.Fatalf("live accounts after restore = %v, want the backup's", got)
	}
	live, err := f.cluster.Open(ctx, f.cluster.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	var sessions int
	if err := live.QueryRow(`SELECT count(*) FROM auth_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("restored sessions = %d (%v), want every session signed out", sessions, err)
	}
	if err := (migrations.Runner{DB: live, Dir: repoMigrations()}).Verify(ctx); err != nil {
		t.Fatalf("restored database is not the running schema: %v", err)
	}
	if left := f.sideDatabases(t); len(left) != 0 {
		t.Fatalf("restore left databases behind: %v", left)
	}
}

func TestBackupRestoreRefusalsLeaveDataUnchanged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file := f.export(t)
	headerEnd := bytes.IndexByte(file[len(backupapplication.Magic):], '\n') + len(backupapplication.Magic) + 1
	payload := file[headerEnd:]
	reheader := func(mutate func(*backupapplication.Header), body []byte) []byte {
		sum := sha256.Sum256(body)
		header := backupapplication.Header{
			App: backupapplication.AppName, Format: backupapplication.FormatVersion,
			SchemaVersion: latestMigration(t), CreatedAt: time.Now().UTC().Truncate(time.Second),
			PayloadBytes: int64(len(body)), PayloadSHA256: hex.EncodeToString(sum[:]),
		}
		mutate(&header)
		encoded, err := backupapplication.EncodeHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		return append(encoded, body...)
	}
	cases := []struct {
		name string
		file []byte
		fail bool
		code string
	}{
		{"not a backup", []byte("PGDMP raw archive"), false, "BACKUP_FILE_INVALID"},
		{"older schema", reheader(func(h *backupapplication.Header) { h.SchemaVersion = "001_baseline.sql" }, payload), false, "BACKUP_SCHEMA_MISMATCH"},
		{"checksum", reheader(func(h *backupapplication.Header) { h.PayloadSHA256 = strings.Repeat("0", 64) }, payload), false, "BACKUP_FILE_INVALID"},
		{"truncated", file[:len(file)-1], false, "BACKUP_FILE_INVALID"},
		{"trailing data", append(append([]byte{}, file...), '!'), false, "BACKUP_FILE_INVALID"},
		{"not an archive", reheader(func(*backupapplication.Header) {}, []byte("garbage")), false, "BACKUP_FILE_INVALID"},
		{"load fails", file, true, "BACKUP_LOAD_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.tools.failRun = tc.fail
			defer func() { f.tools.failRun = false }()
			_, err := f.service.Restore(ctx, bytes.NewReader(tc.file))
			if code := businessCode(err); code != tc.code {
				t.Fatalf("restore error = %v (code %q), want %s", err, code, tc.code)
			}
			if handedOff(f.service) {
				t.Fatal("a refused restore requested the handoff")
			}
			if left := f.sideDatabases(t); len(left) != 0 {
				t.Fatalf("a refused restore left databases behind: %v", left)
			}
			if got := f.liveAccounts(t); len(got) != 0 {
				t.Fatalf("a refused restore changed live data: %v", got)
			}
		})
	}
}

func latestMigration(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(repoMigrations())
	if err != nil {
		t.Fatal(err)
	}
	latest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") && entry.Name() > latest {
			latest = entry.Name()
		}
	}
	return latest
}

func TestRecoverInterruptedRestore(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	stamp := now.Unix()
	mark := func(t *testing.T, f *fixture, name, account string) {
		t.Helper()
		db, err := f.cluster.Open(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`INSERT INTO local_accounts(id, username, password_hash) VALUES ('acct-m', $1, repeat('h', 40))`, account); err != nil {
			t.Fatal(err)
		}
	}
	// prepare builds a migrated database under name (a copy of the live one).
	prepare := func(t *testing.T, f *fixture, name string) {
		t.Helper()
		admin := f.admin(t)
		if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
			t.Fatal(err)
		}
		db, err := f.cluster.Open(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := (migrations.Runner{DB: db, Dir: repoMigrations()}).Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	setAside := func(t *testing.T, f *fixture, to string) {
		t.Helper()
		admin := f.admin(t)
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, f.cluster.Database())
		if _, err := admin.Exec(fmt.Sprintf(`ALTER DATABASE %q RENAME TO %q`, f.cluster.Database(), to)); err != nil {
			t.Fatal(err)
		}
	}
	side := func(f *fixture, kind string, at int64) string {
		return fmt.Sprintf("%s__%s_%d", f.cluster.Database(), kind, at)
	}

	t.Run("switch finished after the live database was set aside", func(t *testing.T) {
		f := newFixture(t)
		mark(t, f, f.cluster.Database(), "previous")
		prepare(t, f, side(f, "restored", stamp))
		mark(t, f, side(f, "restored", stamp), "restored")
		setAside(t, f, side(f, "replaced", stamp))
		if err := backupapplication.RecoverInterrupted(ctx, f.cluster, nil, now); err != nil {
			t.Fatal(err)
		}
		if got := f.liveAccounts(t); len(got) != 1 || got[0] != "restored" {
			t.Fatalf("live accounts = %v, want the restored data", got)
		}
		if left := f.sideDatabases(t); len(left) != 0 {
			t.Fatalf("recovery left databases behind: %v", left)
		}
	})

	t.Run("previous data put back when nothing was ready", func(t *testing.T) {
		f := newFixture(t)
		mark(t, f, f.cluster.Database(), "previous")
		setAside(t, f, side(f, "replaced", stamp))
		if err := backupapplication.RecoverInterrupted(ctx, f.cluster, nil, now); err != nil {
			t.Fatal(err)
		}
		if got := f.liveAccounts(t); len(got) != 1 || got[0] != "previous" {
			t.Fatalf("live accounts = %v, want the previous data", got)
		}
	})

	t.Run("a recent ready restore is switched in, a stale one dropped, a partial load dropped", func(t *testing.T) {
		f := newFixture(t)
		mark(t, f, f.cluster.Database(), "previous")
		stale := now.Add(-time.Hour).Unix()
		prepare(t, f, side(f, "restored", stale))
		mark(t, f, side(f, "restored", stale), "stale")
		prepare(t, f, side(f, "restoring", stamp))
		if err := backupapplication.RecoverInterrupted(ctx, f.cluster, nil, now); err != nil {
			t.Fatal(err)
		}
		if got := f.liveAccounts(t); len(got) != 1 || got[0] != "previous" {
			t.Fatalf("live accounts = %v, want the previous data kept", got)
		}
		if left := f.sideDatabases(t); len(left) != 0 {
			t.Fatalf("recovery left databases behind: %v", left)
		}

		prepare(t, f, side(f, "restored", stamp))
		mark(t, f, side(f, "restored", stamp), "recent")
		if err := backupapplication.RecoverInterrupted(ctx, f.cluster, nil, now); err != nil {
			t.Fatal(err)
		}
		if got := f.liveAccounts(t); len(got) != 1 || got[0] != "recent" {
			t.Fatalf("live accounts = %v, want the recent restore switched in", got)
		}
		if left := f.sideDatabases(t); len(left) != 0 {
			t.Fatalf("recovery left databases behind: %v", left)
		}
	})
}
