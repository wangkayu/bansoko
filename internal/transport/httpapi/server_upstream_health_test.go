package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	access "bansoko/internal/accesscontrol/application"
	accessdomain "bansoko/internal/accesscontrol/domain"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
	persistence "bansoko/internal/provider/outbound/persistence"
	"bansoko/internal/runtime/readiness"
	"bansoko/internal/transport/httpapi/generated"
)

type healthReaderFunc func(context.Context) (persistence.Diagnostics, error)

func (f healthReaderFunc) Read(ctx context.Context) (persistence.Diagnostics, error) {
	return f(ctx)
}

func TestUpstreamHealthRequiresReadScope(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/upstream-health", nil)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if _, err := server.authorizeScope(request, accessdomain.Account{}, next); err == nil {
		t.Fatal("diagnostics accepted a bearer without read scope")
	}
	if _, err := server.authorizeScope(request, accessdomain.Account{Scopes: []string{"read"}}, next); err != nil {
		t.Fatalf("diagnostics rejected a read-only bearer: %v", err)
	}
}

func TestUpstreamHealthSeparatesQuotasAndBusinessResults(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name      string
		skipAuth  bool
		readError error
		status    int
	}{
		{name: "requires authentication", status: http.StatusUnauthorized},
		{name: "diagnostics", skipAuth: true, status: http.StatusOK},
		{name: "redacted read error", skipAuth: true, status: http.StatusServiceUnavailable, readError: errors.New("private database error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			server, err := NewServer(Dependencies{
				DB: &sql.DB{}, Access: access.NewService(nil), Environment: "development", SkipAuth: tc.skipAuth,
				Readiness: readiness.New(readiness.Dependencies{}),
				UpstreamHealth: healthReaderFunc(func(context.Context) (persistence.Diagnostics, error) {
					reads++
					return persistence.Diagnostics{
						Quotas:    []outbound.Health{{Bucket: outbound.AniBTMetadata, LastLimitedAt: now, CooldownUntil: now.Add(time.Minute), ReasonCode: outbound.ReasonRateLimited}},
						Refreshes: []persistence.RefreshHealth{{Provider: "anibt", Kind: "groups", LastAttemptAt: now, Outcomes: provider.Outcomes{NotIndexed: 2}}},
					}, tc.readError
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.skipAuth {
				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/health", nil))
				if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "DATABASE_UNAVAILABLE") || reads != 0 {
					t.Fatalf("public health consulted upstream or changed readiness: status=%d body=%s reads=%d", response.Code, response.Body.String(), reads)
				}
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/upstream-health", nil))
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if !tc.skipAuth && reads != 0 {
				t.Fatal("unauthenticated request reached health reader")
			}
			if tc.readError != nil && (!strings.Contains(response.Body.String(), "DATABASE_UNAVAILABLE") || strings.Contains(response.Body.String(), tc.readError.Error())) {
				t.Fatalf("unsafe or untyped error response: %s", response.Body.String())
			}
			if tc.status == http.StatusOK {
				var result generated.SystemUpstreamHealth
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Quotas) != 1 || len(result.Refreshes) != 1 || result.Quotas[0].Bucket != string(outbound.AniBTMetadata) || result.Refreshes[0].Outcomes.NotIndexed != 2 || result.Refreshes[0].LastSuccessAt != nil {
					t.Fatalf("diagnostics response=%+v err=%v", result, err)
				}
			}
		})
	}
}
