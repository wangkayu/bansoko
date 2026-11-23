package migrations

import (
	"context"
	"testing"
)

// Migration 159 adds auth_mode to the qBittorrent typed config and tightens the
// shape check: the key set gains auth_mode, credentials keeps a non-empty
// username, api_key requires it empty, and a secret is mandatory. Rows written
// before the key existed authenticate with username and password, so the
// migration backfills them as credentials before the new check is installed.
func TestUpgradeBackfillsQBittorrentAuthMode(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "158_api_token_admin_scope.sql")

	if _, err := db.ExecContext(ctx, `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('conn-qb-legacy', 'qbittorrent', 'QB Legacy', 'http://qb.local', 'secret-legacy', 'configured',
	'{"username":"admin","category":"","save_root":"/downloads","downloader_root":"","user_tags":[]}'::jsonb)`); err != nil {
		t.Fatalf("seed the pre-159 qBittorrent row an installed database holds: %v", err)
	}

	applyMigrationsThrough(t, db, "159_qbittorrent_auth_mode.sql")

	var authMode string
	if err := db.QueryRowContext(ctx,
		`SELECT config ->> 'auth_mode' FROM connections WHERE id = 'conn-qb-legacy'`,
	).Scan(&authMode); err != nil {
		t.Fatalf("read the upgraded qBittorrent row: %v", err)
	}
	if authMode != "credentials" {
		t.Fatalf("auth_mode = %q after 159, want the credentials backfill", authMode)
	}

	// The rebuilt check accepts the api_key shape and refuses the old
	// auth_mode-less shape, so an upgrade that skipped the backfill fails here.
	if _, err := db.ExecContext(ctx, `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('conn-qb-upgraded-apikey', 'qbittorrent', 'QB Upgraded APIKey', 'http://qb2.local', 'secret-x', 'configured',
	'{"auth_mode":"api_key","username":"","category":"","save_root":"/downloads","downloader_root":"","user_tags":[]}'::jsonb)`); err != nil {
		t.Fatalf("the 159 check should accept an api_key config: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('conn-qb-upgraded-oldshape', 'qbittorrent', 'QB Upgraded OldShape', 'http://qb3.local', 'secret-x', 'configured',
	'{"username":"admin","category":"","save_root":"/downloads","downloader_root":"","user_tags":[]}'::jsonb)`); err == nil {
		t.Fatal("the 159 check should refuse a qBittorrent config without auth_mode")
	}
}
