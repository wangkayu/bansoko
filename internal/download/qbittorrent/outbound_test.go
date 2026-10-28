package qbittorrent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/observability/outbound"
)

func TestLoginRejectionIsAuthFailed(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"fails body": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("Fails.")) },
		"banned ip":  func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"api 403 after login": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/auth/login") {
				_, _ = w.Write([]byte("Ok."))
				return
			}
			w.WriteHeader(http.StatusForbidden)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			client := Client{Endpoint: server.URL, Username: "admin", Password: "wrong"}
			if _, err := client.Health(context.Background()); !errors.Is(err, ErrAuthFailed) {
				t.Fatalf("Health error = %v, want ErrAuthFailed", err)
			}
			if _, err := client.Sync(context.Background(), "tag:x"); !errors.Is(err, ErrAuthFailed) {
				t.Fatalf("Sync error = %v, want ErrAuthFailed", err)
			} else if client.ClassifyProviderError(err) != domain.ProviderErrorRetryable {
				t.Fatalf("auth failure must stay retryable, got %v", client.ClassifyProviderError(err))
			}
		})
	}
}

func TestTransportFailureCarriesOutboundFacts(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := server.URL
	server.Close()
	_, err := (Client{Endpoint: endpoint}).Health(context.Background())
	if err == nil {
		t.Fatal("expected a transport error")
	}
	failure, ok := outbound.FailureOf(err)
	if !ok {
		t.Fatal("transport error carries no outbound facts")
	}
	if failure.Host != "127.0.0.1" || failure.Class != outbound.ClassConnectionRefused || failure.Via != outbound.ViaDirect {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestHealthSucceedsWithGoodCredentials(t *testing.T) {
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("v5.0.0")) })
	version, err := (Client{Endpoint: server.URL, Username: "admin", Password: "secret"}).Health(context.Background())
	if err != nil || version != "v5.0.0" {
		t.Fatalf("version=%q err=%v", version, err)
	}
}
