package application

import (
	"context"
	"net/http"
	"testing"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/testutil/disposabledb"
)

func TestNetworkRouteRoundTripPostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DELETE FROM connections WHERE id LIKE 'network-route-test-%'; DELETE FROM network_routes WHERE id LIKE 'network-route-test-%'; DELETE FROM secret_values WHERE purpose LIKE 'network-route:%'`)
	defer func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM connections WHERE id LIKE 'network-route-test-%'; DELETE FROM network_routes WHERE id LIKE 'network-route-test-%'; DELETE FROM secret_values WHERE purpose LIKE 'network-route:%'`)
	}()

	secrets := newConnectionTestSecretStore(t, db)
	service := NewService(db, secrets)
	direct, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "network-route-test-direct", Name: "Direct", Kind: configurationdomain.NetworkRouteDirect, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Kind != configurationdomain.NetworkRouteDirect || direct.Endpoint != "" || direct.SecretRef != "" || direct.State != configurationdomain.ConnectionConfigured {
		t.Fatalf("unexpected direct route projection: %+v", direct)
	}
	proxyRoute, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "network-route-test-proxy", Name: "Proxy", Kind: configurationdomain.NetworkRouteHTTPProxy, Enabled: true,
		Endpoint: "http://127.0.0.1:7890", Secret: configurationdomain.SecretMutation{Action: configurationdomain.SecretReplace, Value: "proxy-user:proxy-password"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if proxyRoute.Kind != configurationdomain.NetworkRouteHTTPProxy || !proxyRoute.UpdatedAt.After(direct.UpdatedAt.Add(-time.Second)) || proxyRoute.SecretRef == "" {
		t.Fatalf("unexpected proxy route projection: %+v", proxyRoute)
	}
	disabled, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "network-route-test-disabled", Name: "Disabled", Kind: configurationdomain.NetworkRouteHTTPProxy,
		Endpoint: "http://127.0.0.1:7891",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.State != configurationdomain.ConnectionDisabled {
		t.Fatalf("a route saved without enabled must stay disabled: %+v", disabled)
	}
	if _, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "network-route-test-connection", Kind: configurationdomain.ConnectionMikan,
		Name: "Mikan", Endpoint: "https://provider.example", Enabled: true, NetworkRouteID: direct.ID,
	}); err != nil {
		t.Fatalf("configured route should be selectable by a connection: %v", err)
	}
	if _, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "network-route-test-invalid-connection", Kind: configurationdomain.ConnectionMikan,
		Name: "Invalid", Endpoint: "https://provider.example", NetworkRouteID: "missing-route",
	}); err == nil {
		t.Fatal("missing route reference must be rejected")
	}
	if _, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "network-route-test-disabled-connection", Kind: configurationdomain.ConnectionMikan,
		Name: "Disabled", Endpoint: "https://provider.example", NetworkRouteID: disabled.ID,
	}); err == nil {
		t.Fatal("disabled route must not be selectable by a connection")
	}
	items, err := service.ListNetworkRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].SecretRef != "" {
		t.Fatalf("unexpected route list: %+v", items)
	}
}

// The app update channel is an ordinary GitHub connection: its endpoint is the
// base every update request is sent to (so a reverse proxy lives here) and its
// network route is the egress those requests travel through. Both are read per
// operation, so a saved edit applies to the next update without a restart.
func TestGitHubUpdateConnectionPostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DELETE FROM network_routes WHERE id LIKE 'github-connection-test-%'; DELETE FROM secret_values WHERE purpose LIKE 'network-route:%'`)
	defer func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM network_routes WHERE id LIKE 'github-connection-test-%'; DELETE FROM secret_values WHERE purpose LIKE 'network-route:%'`)
		_, _ = db.ExecContext(ctx, `UPDATE connections SET network_route_id = NULL, endpoint = 'https://raw.githubusercontent.com', state = 'configured', version = version + 1 WHERE kind = 'github'`)
	}()
	service := NewService(db, newConnectionTestSecretStore(t, db))
	// The seeded row already exists, so every save carries the version it read.
	save := func(input configurationdomain.ConnectionInput) (configurationdomain.Connection, error) {
		if current, err := service.GetConnection(ctx, input.ID); err == nil {
			input.ExpectedVersion = current.Version
		}
		return service.UpsertConnection(ctx, input)
	}

	client, gateway, err := service.UpdateConnection(ctx)
	if err != nil || client != nil || gateway != configurationdomain.DefaultGitHubEndpoint {
		t.Fatalf("seeded update connection = %v, %q, %v; want the canonical base on the deployment default", client, gateway, err)
	}
	proxyRoute, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "github-connection-test-proxy", Name: "Update proxy", Kind: configurationdomain.NetworkRouteHTTPProxy,
		Enabled: true, Endpoint: "http://127.0.0.1:7890",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The endpoint is the request-target base: a reverse proxy is configured in
	// exactly one place and comes back normalized.
	saved, err := save(configurationdomain.ConnectionInput{
		ID: "builtin-github", Kind: configurationdomain.ConnectionGitHub, Name: "GitHub",
		Endpoint: " HTTPS://ghproxy.example/raw/ ", Enabled: true, NetworkRouteID: proxyRoute.ID,
	})
	if err != nil {
		t.Fatalf("save GitHub update connection: %v", err)
	}
	if saved.Endpoint != "https://ghproxy.example/raw" || saved.NetworkRouteID != proxyRoute.ID || saved.State != configurationdomain.ConnectionConfigured {
		t.Fatalf("saved update connection = %+v", saved)
	}
	// Exactly one GitHub connection anchors the channel.
	if _, err := service.UpsertConnection(ctx, configurationdomain.ConnectionInput{
		ID: "github-connection-test-second", Kind: configurationdomain.ConnectionGitHub, Name: "Other",
		Endpoint: "https://mirror.example", Enabled: true,
	}); err == nil {
		t.Fatal("a second GitHub update connection must be refused")
	}
	client, gateway, err = service.UpdateConnection(ctx)
	if err != nil || gateway != "https://ghproxy.example/raw" {
		t.Fatalf("update connection = %v, %q, %v", client, gateway, err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatal("the selected route did not become the updater's forward proxy")
	}
	request, err := http.NewRequest(http.MethodGet, "https://raw.githubusercontent.com/example/channel/manifest.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL, err := transport.Proxy(request); err != nil || proxyURL == nil || proxyURL.Host != "127.0.0.1:7890" {
		t.Fatalf("update proxy = %v, %v", proxyURL, err)
	}
	// A route that is switched off after the fact keeps the configured base in
	// use but returns the channel to the deployment default egress. Disabled
	// routes are not selectable, exactly as for every other connection.
	if _, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: proxyRoute.ID, Name: proxyRoute.Name, Kind: configurationdomain.NetworkRouteHTTPProxy,
		Endpoint: proxyRoute.Endpoint, Enabled: false,
	}, proxyRoute.Version); err != nil {
		t.Fatalf("switch the update route off: %v", err)
	}
	if client, gateway, err := service.UpdateConnection(ctx); err != nil || client != nil || gateway != "https://ghproxy.example/raw" {
		t.Fatalf("disabled route = %v, %q, %v; want the deployment default egress", client, gateway, err)
	}
	// Disabling the connection returns the whole channel to the canonical source
	// and the deployment default, including its base URL.
	if _, err := save(configurationdomain.ConnectionInput{
		ID: "builtin-github", Kind: configurationdomain.ConnectionGitHub, Name: "GitHub",
		Endpoint: "https://ghproxy.example/raw", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if client, gateway, err := service.UpdateConnection(ctx); err != nil || client != nil || gateway != "" {
		t.Fatalf("disabled connection = %v, %q, %v; want nil", client, gateway, err)
	}
}
