package application

import (
	"context"
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
