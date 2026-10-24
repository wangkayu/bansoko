package migrations

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An installed database is not empty when the forward migrations reach it.
// 007 replaces the installed `metadata_work_titles_check` and moves the tmdb
// titles into `title_cn`; the installed check requires `title_cn IS NULL` for
// tmdb rows, so dropping it has to happen before the UPDATE. A fresh CI
// database carries no tmdb row and cannot see the order, which is why this
// scenario is reproduced explicitly: install the migrations an existing
// deployment had applied, seed the row shape it holds, then upgrade.
func TestUpgradeMovesPopulatedTMDBTitlesAcrossTheInstalledCheck(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "006_group_dictionary.sql")

	if _, err := db.ExecContext(ctx, `
INSERT INTO metadata_work_titles (provider, media_kind, work_id, title, title_cn, original_title)
VALUES ('tmdb', 'tv', '1', '星际邮差艾拉', NULL, 'Interstellar Postgirl')`); err != nil {
		t.Fatalf("seed the tmdb row an installed database holds: %v", err)
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade an installed, populated database: %v", err)
	}

	var title, titleCN sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT title, title_cn FROM metadata_work_titles WHERE provider = 'tmdb' AND work_id = '1'`,
	).Scan(&title, &titleCN); err != nil {
		t.Fatalf("read the upgraded row: %v", err)
	}
	if title.Valid {
		t.Fatalf("tmdb title = %q after 007, want NULL: the localized title moves to title_cn", title.String)
	}
	if !titleCN.Valid || titleCN.String != "星际邮差艾拉" {
		t.Fatalf("tmdb title_cn = %v, want the value the row carried in title", titleCN)
	}
}

// The ordering is load-bearing and static, so it is also pinned without a
// database: a future edit that moves the UPDATE back above the DROP restores
// the upgrade failure on every populated database.
func TestTMDBTitleMovesOnlyAfterTheInstalledCheckIsDropped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "007_metadata_work_titles_tmdb_title_cn.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	drop := strings.Index(text, "DROP CONSTRAINT metadata_work_titles_check")
	update := strings.Index(text, "UPDATE metadata_work_titles")
	if drop < 0 || update < 0 {
		t.Fatal("007 no longer carries both the constraint drop and the title move")
	}
	if drop > update {
		t.Fatal("007 moves tmdb titles before dropping the installed check; the old check requires title_cn IS NULL, so a populated database aborts")
	}
}

// installMigrationsThrough builds the state an older release left behind:
// schema_migrations records the applied files and each file is executed in
// order. The runner cannot be used for the partial set because its final
// verification expects the complete schema.
func installMigrationsThrough(t *testing.T, db *sql.DB, last string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
CREATE TABLE schema_migrations (
    version TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatalf("create migration history: %v", err)
	}
	entries, err := os.ReadDir(migrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range migrationFiles(entries) {
		raw, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(raw)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)`,
			name, checksum(raw)); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
		if name == last {
			return
		}
	}
	t.Fatalf("migration %s not found", last)
}
