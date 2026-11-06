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

// The update channel selects an existing route rather than owning one: the
// selection is one singleton row, versioned like every other owner, and the
// updater resolves it into the same client a connection would get.
func TestUpdateEgressSelectsAnExistingRoutePostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DELETE FROM network_routes WHERE id LIKE 'update-egress-test-%'; DELETE FROM secret_values WHERE purpose LIKE 'network-route:%'`)
	defer func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM network_routes WHERE id LIKE 'update-egress-test-%'; DELETE FROM secret_values WHERE purpose LIKE 'network-route:%'`)
	}()
	service := NewService(db, newConnectionTestSecretStore(t, db))

	initial, err := service.GetUpdateEgress(ctx)
	if err != nil || initial.NetworkRouteID != "" || initial.Version != 1 {
		t.Fatalf("seeded update egress = %+v, %v", initial, err)
	}
	proxyRoute, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "update-egress-test-proxy", Name: "Update proxy", Kind: configurationdomain.NetworkRouteHTTPProxy,
		Enabled: true, Endpoint: "http://127.0.0.1:7890",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// An unknown route is refused before anything is stored.
	if _, err := service.PutUpdateEgress(ctx, configurationdomain.UpdateEgressInput{
		NetworkRouteID: "update-egress-test-missing", ExpectedVersion: initial.Version,
	}); err == nil {
		t.Fatal("an unknown route must be refused")
	}
	if current, err := service.GetUpdateEgress(ctx); err != nil || current.NetworkRouteID != "" || current.Version != initial.Version {
		t.Fatalf("refused selection changed the row: %+v, %v", current, err)
	}
	// The expected version is the CAS; a stale one changes nothing.
	if _, err := service.PutUpdateEgress(ctx, configurationdomain.UpdateEgressInput{
		NetworkRouteID: proxyRoute.ID, ExpectedVersion: initial.Version + 1,
	}); err == nil {
		t.Fatal("a stale expected version must conflict")
	}
	saved, err := service.PutUpdateEgress(ctx, configurationdomain.UpdateEgressInput{
		NetworkRouteID: proxyRoute.ID, ExpectedVersion: initial.Version,
	})
	if err != nil || saved.NetworkRouteID != proxyRoute.ID || saved.Version != initial.Version+1 {
		t.Fatalf("saved update egress = %+v, %v", saved, err)
	}
	client, err := service.UpdateRouteClient(ctx)
	if err != nil {
		t.Fatal(err)
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
	// A disabled route may be selected; the updater follows the deployment
	// default until it is enabled again.
	disabled, err := service.UpsertNetworkRoute(ctx, configurationdomain.NetworkRouteInput{
		ID: "update-egress-test-disabled", Name: "Disabled", Kind: configurationdomain.NetworkRouteHTTPProxy,
		Endpoint: "http://127.0.0.1:7891",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PutUpdateEgress(ctx, configurationdomain.UpdateEgressInput{
		NetworkRouteID: disabled.ID, ExpectedVersion: saved.Version,
	}); err != nil {
		t.Fatalf("a disabled route must be selectable: %v", err)
	}
	if client, err := service.UpdateRouteClient(ctx); err != nil || client != nil {
		t.Fatalf("disabled selection = %v, %v; want the deployment default", client, err)
	}
	// Null restores the deployment default.
	reset, err := service.PutUpdateEgress(ctx, configurationdomain.UpdateEgressInput{
		ExpectedVersion: saved.Version + 1,
	})
	if err != nil || reset.NetworkRouteID != "" {
		t.Fatalf("reset update egress = %+v, %v", reset, err)
	}
	if client, err := service.UpdateRouteClient(ctx); err != nil || client != nil {
		t.Fatalf("no selection = %v, %v; want the deployment default", client, err)
	}
}
