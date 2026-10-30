package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/configuration/domain"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
)

// Issue #7 regression: GET /config/connections/{id}/qbittorrent/torrents must
// answer an unknown connection id with a typed NotFound (structured 404),
// never an unmapped internal 500. A known connection keeps its upstream
// semantics: a reachable qB passes the list through unchanged and an
// unreachable qB still fails with the stable QB_* application error instead
// of gaining a not-found classification.
func TestQBManagementListTorrentsUnknownConnectionPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	management := NewQBManagementService(service, nil)

	torrents, err := management.ListTorrents(ctx, "connection-test-missing")
	if err == nil {
		t.Fatal("unknown connection id must fail")
	}
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("unknown connection must be a typed NotFound, got %T: %v", err, err)
	}
	if notFound.Resource != "connection" || notFound.ID != "connection-test-missing" {
		t.Fatalf("unexpected not-found identity: %+v", notFound)
	}
	if torrents != nil {
		t.Fatalf("no torrents may be returned: %+v", torrents)
	}
	for _, leaked := range []string{"sql:", "Stack", "error"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("internals leaked in error %q", err.Error())
		}
	}
}

func TestQBManagementListTorrentsHappyPathPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	qbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","name":"Show EP01","state":"downloading","progress":0.5,"size":100}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbServer.Close()

	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	var input generated.ConnectionInput
	if err := input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		Kind: "qbittorrent", DownloadRoot: "/downloads", Name: "QB", Enabled: true, BaseUrl: qbServer.URL, Username: "admin",
		Secret: replaceConnectionSecret(t, "secret-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := httpService.PutConnection(ctx, "connection-test-qb-manage", 0, input); err != nil {
		t.Fatal(err)
	}

	management := NewQBManagementService(service, nil)
	torrents, err := management.ListTorrents(ctx, "connection-test-qb-manage")
	if err != nil {
		t.Fatalf("reachable qB must pass through: %v", err)
	}
	if len(torrents) != 1 || torrents[0].Hash != "abc123" || torrents[0].Name != "Show EP01" {
		t.Fatalf("torrent list mismatch: %+v", torrents)
	}
}

func TestQBManagementListTorrentsUnreachableQBKeepsUpstreamSemanticsPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	// Port 1 on loopback refuses connections immediately; this models a qB
	// instance that exists as a configured row but cannot be reached.
	var input generated.ConnectionInput
	if err := input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		Kind: "qbittorrent", DownloadRoot: "/downloads", Name: "QB Down", Enabled: true, BaseUrl: "http://127.0.0.1:1", Username: "admin",
		Secret: replaceConnectionSecret(t, "secret-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := httpService.PutConnection(ctx, "connection-test-qb-down", 0, input); err != nil {
		t.Fatal(err)
	}

	management := NewQBManagementService(service, nil)
	_, err := management.ListTorrents(ctx, "connection-test-qb-down")
	if err == nil {
		t.Fatal("unreachable qB must fail")
	}
	var notFound *apperrors.NotFound
	if errors.As(err, &notFound) {
		t.Fatalf("unreachable qB must keep upstream semantics, not gain not-found classification: %v", err)
	}
	var badRequest *apperrors.BadRequest
	if !errors.As(err, &badRequest) || badRequest.Code != "QB_UNREACHABLE" {
		// A refused login connection is the instance being down, not a
		// credential problem.
		t.Fatalf("unreachable qB must answer QB_UNREACHABLE, got %T: %v", err, err)
	}
}

func replaceConnectionSecret(t *testing.T, value string) generated.ConnectionSecret {
	t.Helper()
	var secret generated.ConnectionSecret
	if err := secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: value}); err != nil {
		t.Fatal(err)
	}
	return secret
}

// newQBManagementTorrentServer builds a fake qB upstream whose torrent/info
// lists exactly one torrent and records whether a pause/resume action fired.
func newQBManagementTorrentServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	actionFired := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","name":"Show EP01","state":"downloading","progress":0.5,"size":100}]`))
		case r.Method == http.MethodPost && (r.URL.Path == "/api/v2/torrents/pause" || r.URL.Path == "/api/v2/torrents/resume"):
			actionFired = true
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &actionFired
}

func newQBManagementTorrentConnection(t *testing.T, ctx context.Context, db *sql.DB, id, baseURL string) {
	t.Helper()
	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	var input generated.ConnectionInput
	if err := input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		Kind: "qbittorrent", DownloadRoot: "/downloads", Name: "QB", Enabled: true, BaseUrl: baseURL, Username: "admin",
		Secret: replaceConnectionSecret(t, "secret-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := httpService.PutConnection(ctx, id, 0, input); err != nil {
		t.Fatal(err)
	}
}

// Issue #6 regression: pausing a hash that does not exist on the qB instance
// must answer with a typed NotFound (structured 404), never the silent HTTP
// 204 the upstream WebUI API returns for unknown hashes.
func TestQBManagementPauseUnknownHashPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	qbServer, actionFired := newQBManagementTorrentServer(t)
	newQBManagementTorrentConnection(t, ctx, db, "connection-test-qb-manage", qbServer.URL)

	management := NewQBManagementService(NewService(db, newConnectionTestSecretStore(t, db)), nil)
	err := management.PauseTorrent(ctx, "connection-test-qb-manage", "not-a-hash")
	if err == nil {
		t.Fatal("pause of an unknown hash must fail, not answer 204")
	}
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("unknown hash must be a typed NotFound, got %T: %v", err, err)
	}
	if notFound.Resource != "torrent" || notFound.ID != "not-a-hash" {
		t.Fatalf("unexpected not-found identity: %+v", notFound)
	}
	if *actionFired {
		t.Fatal("upstream pause endpoint must not be called for an unknown hash")
	}
}

// A torrent listed by the instance keeps its success path: resume passes
// through to the upstream action and stays error-free.
func TestQBManagementResumeKnownHashHappyPathPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	qbServer, actionFired := newQBManagementTorrentServer(t)
	newQBManagementTorrentConnection(t, ctx, db, "connection-test-qb-manage", qbServer.URL)

	management := NewQBManagementService(NewService(db, newConnectionTestSecretStore(t, db)), nil)
	if err := management.ResumeTorrent(ctx, "connection-test-qb-manage", "abc123"); err != nil {
		t.Fatalf("resume of a known hash must pass through: %v", err)
	}
	if !*actionFired {
		t.Fatal("expected the upstream resume endpoint to receive the action")
	}
}

// An unreachable qB during the existence probe keeps the stable QB_* error,
// it must not gain a not-found classification from the management boundary.
func TestQBManagementResumeUnreachableProbeKeepsQBSemanticsPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	// Port 1 on loopback refuses connections immediately; this models a qB
	// instance that exists as a configured row but cannot be reached.
	newQBManagementTorrentConnection(t, ctx, db, "connection-test-qb-down", "http://127.0.0.1:1")

	management := NewQBManagementService(NewService(db, newConnectionTestSecretStore(t, db)), nil)
	err := management.ResumeTorrent(ctx, "connection-test-qb-down", "abc123")
	if err == nil {
		t.Fatal("unreachable qB must fail")
	}
	var notFound *apperrors.NotFound
	if errors.As(err, &notFound) {
		t.Fatalf("probe failure must keep QB_* semantics, not gain not-found classification: %v", err)
	}
	var badRequest *apperrors.BadRequest
	if !errors.As(err, &badRequest) || !strings.HasPrefix(badRequest.Code, "QB_") {
		t.Fatalf("probe failure must surface the stable QB_* application error, got %T: %v", err, err)
	}
}

// A torrent owned by a running acquisition is tagged in the list and every
// delete (with or without files) is refused with a typed 409 before qB is
// called; once the acquisition is terminal the same delete goes through.
func TestQBManagementDeleteProtectedWhileAcquisitionRunsPostgres(t *testing.T) {
	animetest.InstallRegistries()
	db := openConnectionTestDB(t)
	animetest.PublishRegistries(t, db)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	const hash = "1111111111111111111111111111111111111111"

	deletes := 0
	deleted := map[string]bool{}
	qbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case r.URL.Path == "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			items := make([]string, 0, 2)
			if !deleted[hash] {
				items = append(items, `{"hash":"`+hash+`","name":"Show EP01","state":"downloading","progress":0.5,"size":100}`)
			}
			if !deleted["2222222222222222222222222222222222222222"] {
				items = append(items, `{"hash":"2222222222222222222222222222222222222222","name":"Other","state":"pausedUP","progress":1,"size":1}`)
			}
			w.Write([]byte("[" + strings.Join(items, ",") + "]"))
		case r.URL.Path == "/api/v2/torrents/delete":
			deletes++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deleted[strings.ToLower(r.Form.Get("hashes"))] = true
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbServer.Close()
	const connectionID = "connection-test-qb-protect"
	newQBManagementTorrentConnection(t, ctx, db, connectionID, qbServer.URL)

	animetest.SeedReleaseOwnerChain(t, owner, "source-qb-protect")
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-qb-protect', 'source-qb-protect', 'guid:qb-protect', '{"title":"[Group] Show - 01"}', '{}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "root-qb-protect", "task-qb-protect", "release-qb-protect", "download:qb-protect", `{}`, `{}`, "downloading",
		`{"external_id":"hash:`+hash+`","info_hash":"`+hash+`"}`)
	if _, err := tx.ExecContext(ctx, `UPDATE download_tasks SET qbittorrent_connection_id=$1 WHERE id='task-qb-protect'`, connectionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	management := NewQBManagementService(NewService(db, newConnectionTestSecretStore(t, db)), nil)
	torrents, err := management.ListTorrents(ctx, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, torrent := range torrents {
		owners[torrent.Hash] = torrent.AcquisitionOwner
	}
	// The fixture accepts the root as a one-shot (any other entry kind tags as subscription).
	if owners[hash] != "one_shot" || owners["2222222222222222222222222222222222222222"] != "" {
		t.Fatalf("owner tags = %v", owners)
	}
	// Origin: the record's kind; a task with no record and no bansoko tag was added outside bansoko.
	if origins := torrentOrigins(torrents); origins[hash] != "one_shot" || origins["2222222222222222222222222222222222222222"] != "external" {
		t.Fatalf("origins = %v", origins)
	}
	for _, deleteFiles := range []bool{true, false} {
		err := management.DeleteTorrent(ctx, connectionID, strings.ToUpper(hash), deleteFiles)
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Status != http.StatusConflict || business.Code != "QB_TORRENT_IN_USE" {
			t.Fatalf("delete(files=%v) must be a typed 409, got %v", deleteFiles, err)
		}
	}
	if deletes != 0 {
		t.Fatalf("qB was called %d times for a refused delete", deletes)
	}
	// An unowned torrent is deletable while the other one is protected.
	if err := management.DeleteTorrent(ctx, connectionID, "2222222222222222222222222222222222222222", true); err != nil || deletes != 1 {
		t.Fatalf("unowned delete: err=%v deletes=%d", err, deletes)
	}

	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state='cancelled',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id='root-qb-protect'`); err != nil {
		t.Fatal(err)
	}
	torrents, err = management.ListTorrents(ctx, connectionID)
	if err != nil || torrents[0].AcquisitionOwner != "" {
		t.Fatalf("terminal acquisition must release the tag: %v %+v", err, torrents)
	}
	// A finished download keeps its origin: the record still names who created the task.
	if origins := torrentOrigins(torrents); origins[hash] != "one_shot" {
		t.Fatalf("terminal acquisition must keep the origin: %v", origins)
	}
	if err := management.DeleteTorrent(ctx, connectionID, hash, true); err != nil || deletes != 2 {
		t.Fatalf("terminal delete: err=%v deletes=%d", err, deletes)
	}
}

// A download in progress is known to the owner only by the correlation tag the
// submit attached (the infohash is recorded once the download is observed), so
// the owner tag and the delete refusal must also match the task's qB tags.
func TestQBManagementTagOwnedTorrentProtectedWhileSubmittedPostgres(t *testing.T) {
	animetest.InstallRegistries()
	db := openConnectionTestDB(t)
	animetest.PublishRegistries(t, db)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	const hash = "3333333333333333333333333333333333333333"
	const tag = "bansoko-qb:release-qb-tag"

	deletes := 0
	deleted := map[string]bool{}
	qbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case r.URL.Path == "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			items := make([]string, 0, 2)
			if !deleted[hash] {
				items = append(items, `{"hash":"`+hash+`","name":"Show EP02","state":"downloading","progress":0.3,"size":100,"tags":"other, `+tag+`"}`)
			}
			if !deleted["4444444444444444444444444444444444444444"] {
				items = append(items, `{"hash":"4444444444444444444444444444444444444444","name":"Other","state":"pausedUP","progress":1,"size":1,"tags":"bansoko-qb:release-other"}`)
			}
			w.Write([]byte("[" + strings.Join(items, ",") + "]"))
		case r.URL.Path == "/api/v2/torrents/delete":
			deletes++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deleted[strings.ToLower(r.Form.Get("hashes"))] = true
		case r.URL.Path == "/api/v2/torrents/deleteTags":
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbServer.Close()
	const connectionID = "connection-test-qb-tag"
	newQBManagementTorrentConnection(t, ctx, db, connectionID, qbServer.URL)

	animetest.SeedReleaseOwnerChain(t, owner, "source-qb-tag")
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-qb-tag', 'source-qb-tag', 'guid:qb-tag', '{"title":"[Group] Show - 02"}', '{}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	// The correlation shape the qB client writes at submit: no infohash yet.
	acquisitionfixture.SeedObservedDownload(t, tx, "root-qb-tag", "task-qb-tag", "release-qb-tag", "download:qb-tag", `{}`, `{}`, "submitting",
		`{"state":"submitted","stable_tag":"`+tag+`","external_id":"tag:`+tag+`","files_complete":false}`)
	if _, err := tx.ExecContext(ctx, `UPDATE download_tasks SET qbittorrent_connection_id=$1 WHERE id='task-qb-tag'`, connectionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	management := NewQBManagementService(NewService(db, newConnectionTestSecretStore(t, db)), nil)
	torrents, err := management.ListTorrents(ctx, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, torrent := range torrents {
		owners[torrent.Hash] = torrent.AcquisitionOwner
	}
	if owners[hash] == "" || owners["4444444444444444444444444444444444444444"] != "" {
		t.Fatalf("owner tags = %v", owners)
	}
	// A bansoko tag without a surviving record proves bansoko created the task, not for whom.
	if origins := torrentOrigins(torrents); origins[hash] != "one_shot" || origins["4444444444444444444444444444444444444444"] != "unknown" {
		t.Fatalf("origins = %v", origins)
	}
	if err := management.DeleteTorrent(ctx, connectionID, hash, false); err == nil {
		t.Fatal("delete of a tag-owned torrent must be refused")
	} else {
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Status != http.StatusConflict || business.Code != "QB_TORRENT_IN_USE" {
			t.Fatalf("delete must be a typed 409, got %v", err)
		}
	}
	if deletes != 0 {
		t.Fatalf("qB was called %d times for a refused delete", deletes)
	}
	if err := management.DeleteTorrent(ctx, connectionID, "4444444444444444444444444444444444444444", false); err != nil || deletes != 1 {
		t.Fatalf("unowned delete: err=%v deletes=%d", err, deletes)
	}
}

func torrentOrigins(torrents []domain.QBittorrentTorrentView) map[string]string {
	origins := map[string]string{}
	for _, torrent := range torrents {
		origins[torrent.Hash] = torrent.Origin
	}
	return origins
}
