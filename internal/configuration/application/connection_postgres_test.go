package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"bansoko/internal/testutil/disposabledb"

	"bansoko/internal/apperrors"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/runtime/migrations"
	"bansoko/internal/transport/httpapi/generated"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// After migration 010 the bgm kind is accepted by the CHECK and an rss_http
// connection is end-to-end saveable and listable; a connection secret remove
// persists as clear.

func newConnectionTestSecretStore(t *testing.T, db *sql.DB) *DatabaseSecretStore {
	t.Helper()
	store, err := NewDatabaseSecretStore(db, []byte("connection-test-secret-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openConnectionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// Every PG test starts from a freshly reset disposable schema: tests in
	// this package must never observe rows left by other tests (the shared
	// BANSOKO_TEST_DATABASE_URL is a scratch target, not a shared fixture).
	db := disposabledb.ResetDisposableSchema(t)
	_, currentFile, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	if err := (migrations.Runner{DB: db, Dir: filepath.Join(root, "db", "migrations")}).Apply(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ownerTestDB returns the schema-owner handle for a test-only DDL path (for
// example installing an injected failure trigger). Application tests otherwise
// run as the segregated runtime role.
func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.Owner(t)
}

// cleanupConnectionTestRows removes connection-test-% rows in FK dependency
// order. Errors must stay visible: a silently failing DELETE leaks rows across
// runs and later upserts hit version conflicts.
func cleanupConnectionTestRows(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`DELETE FROM connections WHERE id LIKE 'connection-test-%'`,
		`DELETE FROM network_routes WHERE id LIKE 'connection-test-%'`,
		`DELETE FROM secret_values WHERE purpose LIKE '%connection-test-%' OR purpose LIKE 'qbittorrent:%' OR purpose LIKE 'network-route:%'`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("cleanup %q: %v", stmt, err)
		}
	}
}

func TestBGMConnectionRoundTripPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	stored, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "connection-test-bgm", Kind: configurationdomain.ConnectionBGM,
		Name: "BGM 反代", Endpoint: "https://bgm-proxy.local", Enabled: true,
	})
	if err != nil {
		t.Fatalf("bgm connection must satisfy the kind CHECK: %v", err)
	}
	if stored.Kind != configurationdomain.ConnectionBGM || stored.Endpoint != "https://bgm-proxy.local" || stored.SecretRef != "" {
		t.Fatalf("unexpected bgm projection: %+v", stored)
	}

	// The HTTP projection must list bgm connections without error.
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	connections, err := httpService.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, connection := range connections {
		if bgm, err := connection.AsBGMConnection(); err == nil && bgm.Id == "connection-test-bgm" {
			found = true
			if bgm.BaseUrl != "https://bgm-proxy.local" || !bgm.Enabled {
				t.Fatalf("unexpected bgm HTTP view: %+v", bgm)
			}
		}
	}
	if !found {
		t.Fatal("bgm connection is missing from the HTTP list projection")
	}
}

func TestJackettConnectionRoundTripPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	put := func(expected int64, secret generated.ConnectionSecret) (generated.JackettConnection, error) {
		t.Helper()
		var input generated.ConnectionInput
		if err := input.FromJackettConnectionInput(generated.JackettConnectionInput{
			Kind: "jackett", Name: "Jackett", Enabled: true, BaseUrl: "http://jackett.local:9117", Secret: secret,
		}); err != nil {
			t.Fatal(err)
		}
		connection, err := httpService.PutConnection(ctx, "connection-test-jackett", expected, input)
		if err != nil {
			return generated.JackettConnection{}, err
		}
		return connection.AsJackettConnection()
	}
	secretOf := func(mode string, value string) generated.ConnectionSecret {
		t.Helper()
		var secret generated.ConnectionSecret
		var err error
		switch mode {
		case "replace":
			err = secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: value})
		case "keep":
			err = secret.FromConnectionSecretKeep(generated.ConnectionSecretKeep{Mode: "keep"})
		default:
			err = secret.FromConnectionSecretRemove(generated.ConnectionSecretRemove{Mode: "remove"})
		}
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}

	created, err := put(0, secretOf("replace", "jackett-secret"))
	if err != nil {
		t.Fatalf("Jackett connection must save end to end: %v", err)
	}
	if !created.CredentialConfigured || created.BaseUrl != "http://jackett.local:9117" {
		t.Fatalf("unexpected Jackett create view: %+v", created)
	}
	kept, err := put(created.Version, secretOf("keep", ""))
	if err != nil || !kept.CredentialConfigured {
		t.Fatalf("Jackett keep must retain its credential: view=%+v err=%v", kept, err)
	}
	if _, err := put(kept.Version, secretOf("remove", "")); err == nil {
		t.Fatal("Jackett accepted removal of its required credential")
	}
	loaded, err := service.GetConnection(ctx, "connection-test-jackett")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SecretRef == "" {
		t.Fatal("failed credential removal changed the saved Jackett secret")
	}
	if _, err := db.ExecContext(ctx, `UPDATE connections SET config = '{"indexer":"example"}' WHERE id = 'connection-test-jackett'`); err == nil {
		t.Fatal("Jackett connection accepted indexer configuration")
	}
}

func TestRSSHTTPConnectionRoundTripPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	var input generated.ConnectionInput
	if err := input.FromRSSHTTPConnectionInput(generated.RSSHTTPConnectionInput{
		Kind: "rss_http", Name: "RSS 传输", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	stored, err := httpService.PutConnection(ctx, "connection-test-rss", 0, input)
	if err != nil {
		t.Fatalf("rss_http connection must save end to end: %v", err)
	}
	rss, err := stored.AsRSSHTTPConnection()
	if err != nil {
		t.Fatal(err)
	}
	if rss.Name != "RSS 传输" || !rss.Enabled {
		t.Fatalf("unexpected rss_http view: %+v", rss)
	}

	loaded, err := service.GetConnection(ctx, "connection-test-rss")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Kind != configurationdomain.ConnectionHTTPFeed || loaded.HTTPFeedConfig == nil || loaded.HTTPFeedConfig.AuthMode != configurationdomain.HTTPFeedAuthNone {
		t.Fatalf("unexpected persisted rss_http connection: %+v", loaded)
	}
}

func TestConnectionSecretClearPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	put := func(expected int64, secret generated.ConnectionSecret) generated.Connection {
		t.Helper()
		var input generated.ConnectionInput
		if err := input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
			Kind: "qbittorrent", DownloadRoot: "/downloads", Name: "QB", Enabled: true, BaseUrl: "http://qb.local:8080", Username: "u", Secret: secret,
		}); err != nil {
			t.Fatal(err)
		}
		connection, err := httpService.PutConnection(ctx, "connection-test-qb", expected, input)
		if err != nil {
			t.Fatal(err)
		}
		return connection
	}
	secretOf := func(mode string, value string) generated.ConnectionSecret {
		t.Helper()
		var secret generated.ConnectionSecret
		var err error
		switch mode {
		case "replace":
			err = secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: value})
		case "keep":
			err = secret.FromConnectionSecretKeep(generated.ConnectionSecretKeep{Mode: "keep"})
		default:
			err = secret.FromConnectionSecretRemove(generated.ConnectionSecretRemove{Mode: "remove"})
		}
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	credentialConfigured := func(connection generated.Connection) bool {
		t.Helper()
		qb, err := connection.AsQBittorrentConnection()
		if err != nil {
			t.Fatal(err)
		}
		return qb.CredentialConfigured
	}

	created := put(0, secretOf("replace", "first-password"))
	if !credentialConfigured(created) {
		t.Fatal("replace must store the credential")
	}
	kept := put(createdVersion(t, created), secretOf("keep", ""))
	if !credentialConfigured(kept) {
		t.Fatal("keep must retain the credential")
	}
	cleared := put(createdVersion(t, kept), secretOf("remove", ""))
	if credentialConfigured(cleared) {
		t.Fatal("remove must clear the credential")
	}
	loaded, err := service.GetConnection(ctx, "connection-test-qb")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SecretRef != "" {
		t.Fatalf("expected cleared secret_ref, got %q", loaded.SecretRef)
	}
	// keep after clear stays clear.
	stillCleared := put(createdVersion(t, cleared), secretOf("keep", ""))
	if credentialConfigured(stillCleared) {
		t.Fatal("keep must not resurrect a cleared credential")
	}
}

func createdVersion(t *testing.T, connection generated.Connection) int64 {
	t.Helper()
	qb, err := connection.AsQBittorrentConnection()
	if err != nil {
		t.Fatal(err)
	}
	return qb.Version
}

// The connection enabled write path works: disable and re-enable persist and
// read back, and the anibt/mikan HTTP read model returns route_id.
func TestConnectionEnabledAndRouteRoundTripPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))
	route, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "connection-test-route", Name: "Direct", Kind: configurationdomain.NetworkRouteDirect, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	putAnibt := func(expected int64, enabled bool) generated.AniBTConnection {
		t.Helper()
		routeID := route.ID
		var input generated.ConnectionInput
		if err := input.FromAniBTConnectionInput(generated.AniBTConnectionInput{
			Kind: "anibt", Name: "AniBT", Enabled: enabled, BaseUrl: "https://anibt.example", RouteId: &routeID,
		}); err != nil {
			t.Fatal(err)
		}
		connection, err := httpService.PutConnection(ctx, "connection-test-anibt", expected, input)
		if err != nil {
			t.Fatal(err)
		}
		view, err := connection.AsAniBTConnection()
		if err != nil {
			t.Fatal(err)
		}
		return view
	}

	created := putAnibt(0, true)
	if !created.Enabled {
		t.Fatal("enabled=true must round-trip")
	}
	if created.RouteId == nil || *created.RouteId != route.ID {
		t.Fatalf("anibt view must carry route_id: %+v", created)
	}

	disabled := putAnibt(created.Version, false)
	if disabled.Enabled {
		t.Fatal("enabled=false must disable the connection")
	}
	loaded, err := service.GetConnection(ctx, "connection-test-anibt")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != configurationdomain.ConnectionDisabled {
		t.Fatalf("domain state must be disabled, got %q", loaded.State)
	}

	if reEnabled := putAnibt(disabled.Version, true); !reEnabled.Enabled {
		t.Fatal("re-enable must round-trip")
	}

	// mikan exercises the same read-model path as anibt.
	mikanRouteID := route.ID
	var mikanInput generated.ConnectionInput
	if err := mikanInput.FromMikanConnectionInput(generated.MikanConnectionInput{
		Kind: "mikan", Name: "Mikan", Enabled: true, BaseUrl: "https://mikan.example", RouteId: &mikanRouteID,
	}); err != nil {
		t.Fatal(err)
	}
	mikanConnection, err := httpService.PutConnection(ctx, "connection-test-mikan", 0, mikanInput)
	if err != nil {
		t.Fatal(err)
	}
	mikan, err := mikanConnection.AsMikanConnection()
	if err != nil {
		t.Fatal(err)
	}
	if mikan.RouteId == nil || *mikan.RouteId != route.ID {
		t.Fatalf("mikan view must carry route_id: %+v", mikan)
	}
}

// The route credential is a three-state secret: replace composes one
// user:pass secret, keep retains it, remove clears it. A replace without a
// username is a 400, not an unmapped 500.
func TestNetworkRouteSecretTristatePostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	secrets := newConnectionTestSecretStore(t, db)
	service := NewService(db, secrets)
	httpService := newGeneratedHTTPService(NewHTTPService(service))

	secretOf := func(mode string, value string) *generated.ConnectionSecret {
		t.Helper()
		var secret generated.ConnectionSecret
		var err error
		switch mode {
		case "replace":
			err = secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: value})
		case "keep":
			err = secret.FromConnectionSecretKeep(generated.ConnectionSecretKeep{Mode: "keep"})
		default:
			err = secret.FromConnectionSecretRemove(generated.ConnectionSecretRemove{Mode: "remove"})
		}
		if err != nil {
			t.Fatal(err)
		}
		return &secret
	}
	put := func(expected int64, username *string, secret *generated.ConnectionSecret) generated.NetworkRoute {
		t.Helper()
		var input generated.NetworkRouteInput
		if err := input.FromHTTPProxyRouteInput(generated.HTTPProxyRouteInput{
			Kind: "http_proxy", Name: "代理", Enabled: true, ProxyUrl: "http://proxy.local:7890",
			Username: username, Secret: secret,
		}); err != nil {
			t.Fatal(err)
		}
		route, err := httpService.PutNetworkRoute(ctx, "connection-test-route", expected, input)
		if err != nil {
			t.Fatal(err)
		}
		return route
	}
	credentialConfigured := func(route generated.NetworkRoute) bool {
		t.Helper()
		proxy, err := route.AsHTTPProxyRoute()
		if err != nil {
			t.Fatal(err)
		}
		return proxy.CredentialConfigured
	}
	versionOf := func(route generated.NetworkRoute) int64 {
		t.Helper()
		proxy, err := route.AsHTTPProxyRoute()
		if err != nil {
			t.Fatal(err)
		}
		return proxy.Version
	}

	username := "proxy-user"
	created := put(0, &username, secretOf("replace", "p4ss"))
	if !credentialConfigured(created) {
		t.Fatal("replace must store the route credential")
	}
	stored, err := service.GetNetworkRoute(ctx, "connection-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if resolved, ok := secrets.Resolve(stored.SecretRef); !ok || resolved != "proxy-user:p4ss" {
		t.Fatalf("route credential must persist as user:pass, got %q (ok=%v)", resolved, ok)
	}

	kept := put(versionOf(created), nil, secretOf("keep", ""))
	if !credentialConfigured(kept) {
		t.Fatal("keep must retain the route credential")
	}
	// keep does not rewrite: the username field is ignored in the keep state and
	// the stored user:pass is preserved as-is.
	stored, err = service.GetNetworkRoute(ctx, "connection-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if resolved, ok := secrets.Resolve(stored.SecretRef); !ok || resolved != "proxy-user:p4ss" {
		t.Fatalf("keep must not rewrite the stored credential, got %q (ok=%v)", resolved, ok)
	}

	cleared := put(versionOf(kept), nil, secretOf("remove", ""))
	if credentialConfigured(cleared) {
		t.Fatal("remove must clear the route credential")
	}
	stored, err = service.GetNetworkRoute(ctx, "connection-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SecretRef != "" {
		t.Fatalf("expected cleared secret_ref, got %q", stored.SecretRef)
	}

	// A replace without a username is a 400 ROUTE_SECRET_INVALID, not an
	// unmapped 500.
	var badInput generated.NetworkRouteInput
	if err := badInput.FromHTTPProxyRouteInput(generated.HTTPProxyRouteInput{
		Kind: "http_proxy", Name: "代理", Enabled: true, ProxyUrl: "http://proxy.local:7890",
		Secret: secretOf("replace", "p4ss"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := httpService.PutNetworkRoute(ctx, "connection-test-route", 0, badInput); err == nil {
		t.Fatal("replace without username must be rejected")
	} else {
		var badRequest *apperrors.BadRequest
		if !errors.As(err, &badRequest) || badRequest.Code != "ROUTE_SECRET_INVALID" {
			t.Fatalf("expected ROUTE_SECRET_INVALID bad request, got %v", err)
		}
	}
}

// TestConnectionTestRecordsHealthOnConnectionRowPostgres proves the live
// /config/connections/{id}/test write path: the retired connection_health
// table is gone, so a probe only updates the connection row itself (state,
// health_version). It previously always failed on the missing table. A probe
// must NOT bump the optimistic-locking owner version, otherwise one
// TestConnection call would invalidate every pending If-Match save.
func TestConnectionTestRecordsHealthOnConnectionRowPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	stored, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "connection-test-health", Kind: configurationdomain.ConnectionBGM,
		Name: "BGM 健康", Endpoint: "https://bgm-health.local", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.TestConnection(ctx, "connection-test-health", stubConnectionTester{snapshot: configurationdomain.HealthSnapshot{
		Kind: configurationdomain.ConnectionBGM, State: configurationdomain.ConnectionConfigured, Code: "ok",
	}})
	if err != nil {
		t.Fatalf("test connection must not depend on the retired health table: %v", err)
	}
	if snapshot.ConnectionID != "connection-test-health" {
		t.Fatalf("unexpected snapshot identity: %+v", snapshot)
	}
	loaded, err := service.GetConnection(ctx, "connection-test-health")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != configurationdomain.ConnectionConfigured || loaded.HealthVersion != stored.HealthVersion+1 || loaded.Version != stored.Version {
		t.Fatalf("health probe must bump health_version only, not owner version: before=%+v after=%+v", stored, loaded)
	}
}

type stubConnectionTester struct {
	snapshot configurationdomain.HealthSnapshot
}

func (s stubConnectionTester) Test(context.Context, configurationdomain.Connection) configurationdomain.HealthSnapshot {
	return s.snapshot
}

// TestSeedDefaultConnectionsIsIdempotent proves the server-startup seed is
// safe to run repeatedly: it inserts the built-in provider/metadata rows on a
// fresh schema, never overwrites a user's existing rows, and leaves
// qBittorrent/rss_http alone.
func TestSeedDefaultConnectionsIsIdempotent(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	// Remove any built-in seed rows from prior runs so the test starts clean.
	// User-created rows for these kinds are also removed because the seed
	// treats an existing same-kind row as "already configured". qBittorrent and
	// rss_http are never seeded, so any leftover rows from other tests are
	// removed too: the final "seed must not create qBittorrent/rss_http"
	// assertion must be deterministic against this test's own actions.
	nonSeedKinds := []string{"mikan", "anibt", "bgm", "tmdb", "qbittorrent", "rss_http"}
	deleteAllConnectionKinds := func() {
		for _, kind := range nonSeedKinds {
			if _, err := db.ExecContext(ctx, `DELETE FROM connections WHERE kind = $1`, kind); err != nil {
				t.Fatalf("cleanup kind %s: %v", kind, err)
			}
		}
	}
	deleteAllConnectionKinds()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() {
		// Drop the built-in seed rows first so the network-route FK is clear
		// before cleanupConnectionTestRows deletes the connection-test route.
		deleteAllConnectionKinds()
		cleanupConnectionTestRows(t, ctx, db)
	})

	service := NewService(db, newConnectionTestSecretStore(t, db))

	if err := service.SeedDefaultConnections(ctx); err != nil {
		t.Fatalf("first seed must succeed on a fresh schema: %v", err)
	}
	// After the first seed every built-in kind must have exactly one row with
	// the official endpoint, a direct route (empty network_route_id) and an
	// enabled state.
	for _, seed := range configurationdomain.DefaultConnectionSeeds() {
		loaded, err := service.GetConnection(ctx, seed.ID)
		if err != nil {
			t.Fatalf("seeded %s connection must be readable: %v", seed.Kind, err)
		}
		if loaded.Endpoint != seed.Endpoint || loaded.NetworkRouteID != "" || loaded.State != configurationdomain.ConnectionConfigured {
			t.Fatalf("seeded %s connection projection mismatch: %+v", seed.Kind, loaded)
		}
	}

	// A user edit (custom base URL + proxy route) must survive a second seed.
	const routeID = "connection-test-route"
	if _, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: routeID, Name: "seed-test-proxy", Kind: configurationdomain.NetworkRouteHTTPProxy, Endpoint: "http://proxy.local:8080", Enabled: true,
	}, 0); err != nil {
		t.Fatalf("create test network route: %v", err)
	}
	seededMikan, err := service.GetConnection(ctx, "builtin-mikan")
	if err != nil {
		t.Fatalf("read seeded mikan before edit: %v", err)
	}
	if _, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "builtin-mikan", Kind: configurationdomain.ConnectionMikan, Name: "Mikan 反代",
		Endpoint: "https://mikan-proxy.local", Enabled: true, NetworkRouteID: routeID,
		ExpectedVersion: seededMikan.Version,
	}); err != nil {
		t.Fatalf("user edit of seeded mikan connection must succeed: %v", err)
	}

	if err := service.SeedDefaultConnections(ctx); err != nil {
		t.Fatalf("second seed must be a no-op for configured kinds: %v", err)
	}
	edited, err := service.GetConnection(ctx, "builtin-mikan")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Endpoint != "https://mikan-proxy.local" || edited.NetworkRouteID != routeID {
		t.Fatalf("second seed overwrote the user's mikan edit: %+v", edited)
	}
	// Other kinds keep their seeded defaults.
	for _, seed := range configurationdomain.DefaultConnectionSeeds() {
		if seed.Kind == configurationdomain.ConnectionMikan {
			continue
		}
		kept, err := service.GetConnection(ctx, seed.ID)
		if err != nil {
			t.Fatalf("seeded %s connection disappeared after second seed: %v", seed.Kind, err)
		}
		if kept.Endpoint != seed.Endpoint {
			t.Fatalf("second seed changed %s endpoint: %+v", seed.Kind, kept)
		}
	}

	// A disabled built-in row must also survive reseeding. Disabling keeps a
	// valid name/endpoint (validation rejects blanks) but flips Enabled off.
	seededAniBT, err := service.GetConnection(ctx, "builtin-anibt")
	if err != nil {
		t.Fatalf("read seeded anibt before disable: %v", err)
	}
	if _, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "builtin-anibt", Kind: configurationdomain.ConnectionAniBT, Name: seededAniBT.Name,
		Endpoint: seededAniBT.Endpoint, Enabled: false, ExpectedVersion: seededAniBT.Version,
	}); err != nil {
		t.Fatalf("disable seeded anibt connection: %v", err)
	}
	if err := service.SeedDefaultConnections(ctx); err != nil {
		t.Fatalf("third seed must be a no-op when a disabled row exists: %v", err)
	}
	disabled, err := service.GetConnection(ctx, "builtin-anibt")
	if err != nil {
		t.Fatal(err)
	}
	if disabled.State != configurationdomain.ConnectionDisabled {
		t.Fatalf("third seed re-enabled the user's disabled anibt connection: %+v", disabled)
	}

	// qBittorrent must never be seeded.
	connections, err := service.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, connection := range connections {
		if connection.Kind == configurationdomain.ConnectionQBittorrent || connection.Kind == configurationdomain.ConnectionHTTPFeed {
			t.Fatalf("seed must not create a %s connection: %+v", connection.Kind, connection)
		}
	}
}

// The HTTP connection-test entry must resolve routes through the same path
// the runtime uses. A tester without a ClientResolver would report
// network_route_invalid for a connection with an explicit route even though
// the matcher can use that route. The httptest fake proxy receives
// absolute-form requests, proving the probe traffic goes through the
// mounted route.
func TestConnectionTestResolvesProxyRoutePostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.RequestURI, "http://tmdb.invalid/3") {
			t.Errorf("proxy must receive absolute-form request, got %q", r.RequestURI)
		}
		proxied.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(proxy.Close)

	service := NewService(db, newConnectionTestSecretStore(t, db))
	httpService := newGeneratedHTTPService(NewHTTPService(service))

	if _, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "connection-test-route", Name: "假代理", Kind: configurationdomain.NetworkRouteHTTPProxy, Endpoint: proxy.URL, Enabled: true,
	}, 0); err != nil {
		t.Fatal(err)
	}

	var secret generated.ConnectionSecret
	if err := secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: "dummy-key"}); err != nil {
		t.Fatal(err)
	}
	routeID := generated.ID("connection-test-route")
	var connInput generated.ConnectionInput
	if err := connInput.FromTMDBConnectionInput(generated.TMDBConnectionInput{
		Kind: "tmdb", Name: "TMDB 测试", Enabled: true, BaseUrl: "http://tmdb.invalid/3",
		RouteId: &routeID, Secret: secret,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := httpService.PutConnection(ctx, "connection-test-tmdb", 0, connInput); err != nil {
		t.Fatal(err)
	}

	result, err := httpService.TestConnection(ctx, "connection-test-tmdb")
	if err != nil {
		t.Fatal(err)
	}
	if result.Health != "ready" {
		t.Fatalf("routed connection test must be ready, got %+v", result)
	}
	if proxied.Load() == 0 {
		t.Fatal("test probe must go through the attached proxy route")
	}
}

// Regression guard (S1 E-stage finding): the production composition root wires
// the same HTTPService as the qB provider source and as the download
// service's runtime config source. When the adapter stopped projecting the
// connection's internal owner reference, every typed submit dispatch wrote an
// empty connection_ref and the 124 CHECK rejected the evidence row, so typed
// submit was unusable under the real wiring. The projection must therefore stay
// non-empty and must name the configured connection row.
func TestHTTPServiceQBittorrentRuntimeConfigPropagatesConnectionIDPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	service := NewService(db, newConnectionTestSecretStore(t, db))
	var input generated.ConnectionInput
	if err := input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		Kind: "qbittorrent", DownloadRoot: "/downloads", Name: "QB", Enabled: true, BaseUrl: "http://127.0.0.1:1", Username: "admin",
		Secret: replaceConnectionSecret(t, "probe-password"),
	}); err != nil {
		t.Fatal(err)
	}
	httpAdapter := NewHTTPService(service)
	generatedView := newGeneratedHTTPService(httpAdapter)
	if _, err := generatedView.PutConnection(ctx, "connection-test-qb-runtime", 0, input); err != nil {
		t.Fatal(err)
	}

	config, err := httpAdapter.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		t.Fatalf("configured qBittorrent runtime config must resolve: %v", err)
	}
	if config.ConnectionID != "connection-test-qb-runtime" {
		t.Fatalf("adapter must project the connection owner reference, got %q", config.ConnectionID)
	}
	if config.ConnectionVersion <= 0 {
		t.Fatalf("adapter must project the connection row version, got %d", config.ConnectionVersion)
	}
	if config.Endpoint != "http://127.0.0.1:1" || config.Username != "admin" {
		t.Fatalf("adapter must still project endpoint and username: %+v", config)
	}

	ownerConfig, err := service.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		t.Fatalf("owner projection must resolve: %v", err)
	}
	if ownerConfig.ConnectionID != config.ConnectionID || ownerConfig.ConnectionVersion != config.ConnectionVersion {
		t.Fatalf("owner and adapter projections must agree on the connection reference and version: owner=%+v adapter=%+v",
			ownerConfig, config)
	}
}
