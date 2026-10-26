package application

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func putQBConnectionForTest(t *testing.T, ctx context.Context, svc generatedHTTPService, id string, expected int64, name string) error {
	t.Helper()
	var input generated.ConnectionInput
	if err := input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		Kind: "qbittorrent", Name: name, Enabled: true, BaseUrl: "http://127.0.0.1:1", Username: "admin",
		DownloadRoot: "/downloads", Secret: replaceConnectionSecret(t, "probe-password"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.PutConnection(ctx, id, expected, input)
	return err
}

// The runtime drives exactly one qBittorrent: a second row is refused with 409
// DOWNLOADER_ALREADY_CONFIGURED, while editing the existing row stays allowed.
func TestSecondQBittorrentConnectionIsRefusedPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	svc := newGeneratedHTTPService(NewHTTPService(NewService(db, newConnectionTestSecretStore(t, db))))
	if err := putQBConnectionForTest(t, ctx, svc, "connection-test-qb-one", 0, "QB"); err != nil {
		t.Fatalf("first downloader must save: %v", err)
	}
	err := putQBConnectionForTest(t, ctx, svc, "connection-test-qb-two", 0, "QB 2")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != http.StatusConflict || business.Code != "DOWNLOADER_ALREADY_CONFIGURED" {
		t.Fatalf("second downloader must be refused with 409 DOWNLOADER_ALREADY_CONFIGURED, got %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM connections WHERE kind = 'qbittorrent'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exactly one qBittorrent row must remain, count=%d err=%v", count, err)
	}
	if err := putQBConnectionForTest(t, ctx, svc, "connection-test-qb-one", 1, "QB renamed"); err != nil {
		t.Fatalf("editing the existing downloader must stay allowed: %v", err)
	}
}

func TestQBittorrentDownloadRootIsRequired(t *testing.T) {
	for _, root := range []string{"", "   "} {
		err := validateConnectionInput(configurationdomain.ConnectionInput{
			Kind: configurationdomain.ConnectionQBittorrent, Name: "QB", Endpoint: "http://qb.local:8080",
			QBittorrentConfig: &configurationdomain.QBittorrentConnectionConfig{Username: "admin", SaveRoot: root},
		})
		var bad *apperrors.BadRequest
		if !errors.As(err, &bad) {
			t.Fatalf("blank download_root %q must be a bad request, got %v", root, err)
		}
	}
	if err := validateConnectionInput(configurationdomain.ConnectionInput{
		Kind: configurationdomain.ConnectionQBittorrent, Name: "QB", Endpoint: "http://qb.local:8080",
		QBittorrentConfig: &configurationdomain.QBittorrentConnectionConfig{Username: "admin", SaveRoot: "/downloads"},
	}); err != nil {
		t.Fatalf("a download_root must pass: %v", err)
	}
}
