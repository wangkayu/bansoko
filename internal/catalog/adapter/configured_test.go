package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"bansoko/internal/catalog/application"
	"bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
)

type staticCatalogConnectionSource []configurationdomain.Connection

func (s staticCatalogConnectionSource) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return append([]configurationdomain.Connection(nil), s...), nil
}

func TestConfiguredReaderUsesSavedAniBTConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"data":{"requestedSeason":"2024-07","total":0,"byWeekday":[]}}`))
	}))
	defer server.Close()
	reader := ConfiguredReader{
		Provider: application.ProviderAniBT,
		Connections: staticCatalogConnectionSource{
			{ID: "disabled", Kind: configurationdomain.ConnectionAniBT, Endpoint: "https://disabled.invalid", State: configurationdomain.ConnectionDisabled},
			{ID: "saved", Kind: configurationdomain.ConnectionAniBT, Endpoint: server.URL, State: configurationdomain.ConnectionConfigured},
			{ID: "mikan", Kind: configurationdomain.ConnectionMikan, Endpoint: "https://mikan.invalid", State: configurationdomain.ConnectionConfigured},
		},
	}
	if _, err := reader.ReadSeason(context.Background(), "2024-07", domain.ReadHints{}); err != nil {
		t.Fatalf("configured reader did not use the saved AniBT connection: %v", err)
	}
}

// When no connection row exists for the kind, the reader must fall back to
// the official endpoint instead of returning the "not configured" selection
// error. The fallback reaches a real host, so this test only asserts the
// error is no longer the connection-selection failure; a network error from
// the official endpoint is acceptable in a sandbox.
func TestConfiguredReaderFallsBackToDefaultEndpointWhenNoConnectionRowExists(t *testing.T) {
	reader := ConfiguredReader{
		Provider: application.ProviderAniBT,
		Connections: staticCatalogConnectionSource{
			{ID: "unrelated", Kind: configurationdomain.ConnectionMikan, Endpoint: "https://mikan.invalid", State: configurationdomain.ConnectionConfigured},
		},
	}
	_, err := reader.ReadSeason(context.Background(), "2024-07", domain.ReadHints{})
	if err != nil && errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
		t.Fatalf("configured reader returned the no-connection error instead of falling back: %v", err)
	}
}

func TestConfiguredReaderRejectsAmbiguousConnections(t *testing.T) {
	reader := ConfiguredReader{
		Provider: application.ProviderAniBT,
		Connections: staticCatalogConnectionSource{
			{ID: "one", Kind: configurationdomain.ConnectionAniBT, Endpoint: "https://one.invalid", State: configurationdomain.ConnectionConfigured},
			{ID: "two", Kind: configurationdomain.ConnectionAniBT, Endpoint: "https://two.invalid", State: configurationdomain.ConnectionDegraded},
		},
	}
	// Ambiguous connections are a real configuration error and must NOT fall
	// back to the default; the user must resolve the duplicate.
	if _, err := reader.ReadSeason(context.Background(), "2024-07", domain.ReadHints{}); err == nil {
		t.Fatal("ambiguous AniBT connections were silently resolved via fallback")
	}
}
