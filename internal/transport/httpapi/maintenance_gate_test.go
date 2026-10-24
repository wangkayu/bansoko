package httpapi

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	accessapplication "bansoko/internal/accesscontrol/application"
	"bansoko/internal/runtime/maintenance"
)

func newGateTestServer(gate *maintenance.Gate) http.Handler {
	srv, err := NewServer(Dependencies{
		DB:              &sql.DB{},
		Access:          accessapplication.NewService(nil),
		Environment:     "development",
		SkipAuth:        true,
		SessionTTL:      0,
		MaintenanceGate: gate,
	})
	if err != nil {
		panic(err)
	}
	return srv.Handler()
}

// doRequest runs a request through the handler directly (no network), so a
// handler panic from the nil-DB test rig surfaces as a 500-style recovery
// rather than a connection EOF. Returns status and whether the body carried
// WRITES_REJECTED.
func doRequest(h http.Handler, method, path string) (int, bool) {
	req := httptest.NewRequest(method, "http://test"+path, nil)
	rec := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(rec, req)
	}()
	return rec.Code, bytes.Contains(rec.Body.Bytes(), []byte("WRITES_REJECTED"))
}

func TestMaintenanceRejectsProductWrites(t *testing.T) {
	gate := maintenance.NewGate()
	h := newGateTestServer(gate)
	gate.EnterMaintenance()

	writeCases := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/anime/subscribe"},
		{"POST", "/api/v1/anime/abc-1/releases"},
		{"PATCH", "/api/v1/anime/abc-1/metadata"},
		{"POST", "/api/v1/releases/r-1/retry"},
		{"POST", "/api/v1/automation/rss_refresh/run"},
		{"POST", "/api/v1/plugin-targets"},
		{"DELETE", "/api/v1/api-tokens/tok-1"},
		{"POST", "/api/v1/catalog/providers/anibt/seasons/2026-summer/refresh"},
	}
	for _, tc := range writeCases {
		status, rejected := doRequest(h, tc.method, tc.path)
		if status != http.StatusServiceUnavailable || !rejected {
			t.Errorf("%s %s during maintenance: got %d rejected=%v, want 503 WRITES_REJECTED", tc.method, tc.path, status, rejected)
		}
	}
}

func TestMaintenanceAllowsReadOnlyPaths(t *testing.T) {
	gate := maintenance.NewGate()
	h := newGateTestServer(gate)
	gate.EnterMaintenance()

	readCases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/anime"},
		{"GET", "/api/v1/observability/records"},
		{"POST", "/api/v1/anime/rss-list-view"}, // side-effect free
		{"POST", "/api/v1/parse/release-title"}, // pure parse
		{"POST", "/api/v1/auth/login"},          // auth recovery
	}
	for _, tc := range readCases {
		status, rejected := doRequest(h, tc.method, tc.path)
		if rejected {
			t.Errorf("%s %s during maintenance: unexpectedly rejected as WRITES_REJECTED (status %d)", tc.method, tc.path, status)
		}
	}
}

func TestServingAcceptsWrites(t *testing.T) {
	gate := maintenance.NewGate()
	h := newGateTestServer(gate)

	status, rejected := doRequest(h, "POST", "/api/v1/anime/subscribe")
	if rejected {
		t.Errorf("serving instance must not reject writes with WRITES_REJECTED (status %d)", status)
	}
}
