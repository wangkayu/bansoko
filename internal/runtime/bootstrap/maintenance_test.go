package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/transport/httpapi/generated"
	embeddedupdate "bansoko/internal/update/journal"
)

func TestMaintenanceHandlerExposesOnlyAnonymousHealthReads(t *testing.T) {
	root := t.TempDir()
	journalWithSteps(t, root, "maintenance-op", embeddedupdate.StepFetched)
	handler := maintenanceHandler(filepath.Join(root, journalFileName), "", "", func() time.Time {
		return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	})

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
	}{
		{name: "livez", method: http.MethodGet, path: "/api/v1/livez", wantStatus: http.StatusOK},
		{name: "readyz", method: http.MethodGet, path: "/api/v1/readyz", wantStatus: http.StatusServiceUnavailable},
		{name: "livez mutation", method: http.MethodPost, path: "/api/v1/livez", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "readyz mutation", method: http.MethodDelete, path: "/api/v1/readyz", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "business read", method: http.MethodGet, path: "/api/v1/anime", wantStatus: http.StatusNotFound},
		{name: "update status", method: http.MethodGet, path: "/api/v1/update/status", wantStatus: http.StatusNotFound},
		{name: "bootstrap", method: http.MethodGet, path: "/bootstrap/status", wantStatus: http.StatusNotFound},
		{name: "unknown mutation", method: http.MethodPost, path: "/api/v1/anime", wantStatus: http.StatusNotFound},
		{name: "trailing path", method: http.MethodGet, path: "/api/v1/livez/extra", wantStatus: http.StatusNotFound},
		{name: "double slash", method: http.MethodGet, path: "/api//v1/livez", wantStatus: http.StatusNotFound},
		{name: "dot segment", method: http.MethodGet, path: "/api/v1/./livez", wantStatus: http.StatusNotFound},
		{name: "encoded segment", method: http.MethodGet, path: "/api/v1/%6civez", wantStatus: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if allow := recorder.Header().Get("Allow"); allow != tc.wantAllow {
				t.Fatalf("Allow = %q, want %q", allow, tc.wantAllow)
			}
		})
	}

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/api/v1/livez", nil))
	var liveReport generated.LivezReport
	if err := json.Unmarshal(live.Body.Bytes(), &liveReport); err != nil {
		t.Fatal(err)
	}
	if liveReport.Status != generated.LivezReportStatusAlive {
		t.Fatalf("live status = %q", liveReport.Status)
	}

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil))
	var report generated.ReadinessReport
	if err := json.Unmarshal(ready.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	check, found := report.Checks["maintenance"]
	if report.Ready || report.Reason != "not_ready:maintenance" || !found || check.Ok ||
		check.Code == nil || *check.Code != "maintenance" || check.Detail == nil ||
		*check.Detail != "step=fetched operation=maintenance-op" {
		t.Fatalf("unexpected readiness report: %+v", report)
	}
	if report.DeploymentIdentity != nil {
		t.Fatal("parked responder must not claim a serving deployment identity")
	}
}

func TestMaintenanceHandlerReadFailureIsFixedAndSanitized(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, journalFileName)
	secret := "postgres://owner:password@db/private/path"
	if err := os.WriteFile(journalPath, []byte("{broken "+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := maintenanceHandler(journalPath, "fetched", "fallback-op", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "maintenance state unavailable") {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(body, secret) || strings.Contains(body, "broken") || strings.Contains(body, "fallback-op") {
		t.Fatalf("read failure leaked unverified detail: %q", body)
	}
}

func TestMaintenanceHandlerDoesNotMutateDurableStateOrTakeJournalLock(t *testing.T) {
	root := t.TempDir()
	journalWithSteps(t, root, "lock-free-op", embeddedupdate.StepFetched, embeddedupdate.StepStaged)
	journalPath := filepath.Join(root, journalFileName)
	pointerPath := filepath.Join(root, currentPointerName)
	fencePath := filepath.Join(root, "fence-evidence")
	if err := os.WriteFile(pointerPath, []byte("release-before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fencePath, []byte("open\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeJournal := mustReadFile(t, journalPath)
	beforePointer := mustReadFile(t, pointerPath)
	beforeFence := mustReadFile(t, fencePath)

	held, err := embeddedupdate.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	handler := maintenanceHandler(journalPath, "", "", nil)
	for _, path := range []string{"/api/v1/livez", "/api/v1/readyz", "/api/v1/update/status"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	}
	if after := mustReadFile(t, journalPath); !bytes.Equal(beforeJournal, after) {
		t.Fatal("maintenance HTTP mutated the journal")
	}
	if after := mustReadFile(t, pointerPath); !bytes.Equal(beforePointer, after) {
		t.Fatal("maintenance HTTP mutated the current pointer")
	}
	if after := mustReadFile(t, fencePath); !bytes.Equal(beforeFence, after) {
		t.Fatal("maintenance HTTP mutated fence evidence")
	}
}

func TestMaintenanceListenAddressUsesDeploymentSetting(t *testing.T) {
	t.Setenv(envListenAddress, "127.0.0.1:19090")
	if got := maintenanceListenAddress(); got != "127.0.0.1:19090" {
		t.Fatalf("listen address = %q", got)
	}
	t.Setenv(envListenAddress, "")
	if got := maintenanceListenAddress(); got != defaultListenAddress {
		t.Fatalf("default listen address = %q", got)
	}
}

func TestMaintenanceResponderListenFailureReturnsFailClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv(envListenAddress, listener.Addr().String())
	if _, err := startMaintenanceResponder(t.TempDir(), "fetched", "maintenance-op"); err == nil {
		t.Fatal("maintenance responder accepted an unavailable listen address")
	}
}

func TestMaintenanceResponderLifecycleReportsPendingAndBlockedStates(t *testing.T) {
	for _, tc := range []struct {
		step string
		op   string
	}{
		{step: "pending_backup", op: "marker-operation"},
		{step: string(embeddedupdate.StepPendingMigration), op: "handoff-operation"},
		{step: string(embeddedupdate.StepRollbackBlocked), op: "blocked-operation"},
	} {
		t.Run(tc.step, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			_ = listener.Close()
			t.Setenv(envListenAddress, address)
			stop, err := startMaintenanceResponder(t.TempDir(), tc.step, tc.op)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			client := &http.Client{Timeout: time.Second}
			for attempt := 0; attempt < 20; attempt++ {
				response, getErr := client.Get("http://" + address + "/api/v1/readyz")
				if getErr == nil {
					defer response.Body.Close()
					if response.StatusCode != http.StatusServiceUnavailable {
						t.Fatalf("readyz status=%d, want 503", response.StatusCode)
					}
					body, readErr := io.ReadAll(response.Body)
					if readErr != nil || !strings.Contains(string(body), "step="+tc.step) || !strings.Contains(string(body), "operation="+tc.op) {
						t.Fatalf("readyz body=%q err=%v", body, readErr)
					}
					break
				}
				if attempt == 19 {
					t.Fatalf("readyz unavailable: %v", getErr)
				}
				time.Sleep(10 * time.Millisecond)
			}
			response, err := client.Get("http://" + address + "/api/v1/livez")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("livez status=%d, want 200", response.StatusCode)
			}
			response, err = client.Get("http://" + address + "/api/v1/anime")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("business route status=%d, want 404", response.StatusCode)
			}
			if err := stop(); err != nil {
				t.Fatalf("stop responder before handoff: %v", err)
			}
			if _, err := net.DialTimeout("tcp", address, 50*time.Millisecond); err == nil {
				t.Fatal("responder still owns port after stop")
			}
		})
	}
}
