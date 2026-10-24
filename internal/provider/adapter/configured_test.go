package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
)

type staticConnectionSource []configurationdomain.Connection

func (s staticConnectionSource) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return append([]configurationdomain.Connection(nil), s...), nil
}

func TestConfiguredGroupReaderUsesCurrentProviderConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"bgmId":1,"total":0,"groups":[]}}`))
	}))
	defer server.Close()
	reader := ConfiguredGroupReader{
		Provider: providerdomain.ProviderAniBT,
		Connections: staticConnectionSource{
			{ID: "disabled", Kind: configurationdomain.ConnectionAniBT, Endpoint: "https://disabled.invalid", State: configurationdomain.ConnectionDisabled},
			{ID: "current", Kind: configurationdomain.ConnectionAniBT, Endpoint: server.URL, State: configurationdomain.ConnectionConfigured},
			{ID: "mikan", Kind: configurationdomain.ConnectionMikan, Endpoint: "https://mikan.invalid", State: configurationdomain.ConnectionConfigured},
		},
	}
	result, err := reader.ReadGroups(context.Background(), "1")
	if err != nil || result.Provider != providerdomain.ProviderAniBT || result.Groups == nil {
		t.Fatalf("configured reader did not use AniBT connection: %+v err=%v", result, err)
	}
}

func TestConfiguredGroupReaderRejectsAmbiguousConnectionsAndMissingRouteResolver(t *testing.T) {
	reader := ConfiguredGroupReader{Provider: providerdomain.ProviderMikan, Connections: staticConnectionSource{
		{ID: "one", Kind: configurationdomain.ConnectionMikan, State: configurationdomain.ConnectionConfigured},
		{ID: "two", Kind: configurationdomain.ConnectionMikan, State: configurationdomain.ConnectionDegraded},
	}}
	// Ambiguous connections are a real configuration error and must NOT fall
	// back to the default; the user must resolve the duplicate.
	if _, err := reader.ReadGroups(context.Background(), "1"); err == nil {
		t.Fatal("ambiguous Mikan connections were silently selected")
	}
	reader.Connections = staticConnectionSource{{ID: "route", Kind: configurationdomain.ConnectionMikan, Endpoint: "https://mikan.invalid", NetworkRouteID: "route", State: configurationdomain.ConnectionConfigured}}
	if _, err := reader.ReadGroups(context.Background(), "1"); err == nil {
		t.Fatal("configured network route was silently ignored")
	}
}

// When no connection row exists for the provider kind, the reader must fall
// back to the official endpoint instead of returning the "not configured"
// selection error. The fallback reaches a real host, so this test only
// asserts the error is no longer the connection-selection failure; a network
// error from the official endpoint is acceptable in a sandbox.
func TestConfiguredGroupReaderFallsBackToDefaultEndpointWhenNoConnectionRowExists(t *testing.T) {
	reader := ConfiguredGroupReader{
		Provider: providerdomain.ProviderAniBT,
		Connections: staticConnectionSource{
			{ID: "unrelated", Kind: configurationdomain.ConnectionMikan, Endpoint: "https://mikan.invalid", State: configurationdomain.ConnectionConfigured},
		},
	}
	_, err := reader.ReadGroups(context.Background(), "1")
	if err != nil && errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
		t.Fatalf("configured reader returned the no-connection error instead of falling back: %v", err)
	}
}
