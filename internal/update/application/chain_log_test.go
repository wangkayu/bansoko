package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
	observabilityport "bansoko/internal/observability/port"
)

type chainResultLogger struct {
	observabilityport.NopRuntimeLogger
	warnings []observabilityport.RuntimeEvent
}

func (l *chainResultLogger) Warn(_ context.Context, e observabilityport.RuntimeEvent) {
	l.warnings = append(l.warnings, e)
}

func TestChainFailureLogsStageAndOperationBeforeHandoff(t *testing.T) {
	logger := &chainResultLogger{}
	s := &Service{updatesRoot: t.TempDir(), logger: logger, chainRunning: true, done: make(chan struct{})}
	s.runChain(hardeningdomain.ReleaseManifest{ReleaseID: "release"}, "digest", "", "operation", nil)
	if s.chainRunning || s.lastStatus.Step != "failed" || len(logger.warnings) != 1 {
		t.Fatalf("running=%v, status=%+v, warnings=%+v", s.chainRunning, s.lastStatus, logger.warnings)
	}
	e := logger.warnings[0]
	if e.OwnerID != "operation" || e.Stage != "release_input" || e.Outcome != "failure" || e.ErrorClass != "source_invalid" || s.lastStatus.LastError == nil || e.Message != *s.lastStatus.LastError {
		t.Fatalf("status=%+v, warning=%+v", s.lastStatus, e)
	}
	restarted := &Service{updatesRoot: s.updatesRoot}
	status, found, err := restarted.GetUpdateStatus(context.Background())
	if err != nil || !found || status.OperationID != "operation" || status.Step != "failed" || status.LastError == nil || *status.LastError != e.Message {
		t.Fatalf("restarted status=%+v found=%v err=%v", status, found, err)
	}
	select {
	case <-s.done:
		t.Fatal("failed preparation requested a restart")
	default:
	}
}

func TestChainFailureClassifiesPreparationWithoutPersistingRawErrors(t *testing.T) {
	const privateText = "fixture-private-content"
	const routePath = "/fixture-private-route"
	download := []byte(privateText)
	goodDigest := fmt.Sprintf("%x", sha256.Sum256(download))
	for _, tc := range []struct {
		name, stage, class string
		status, size       int
		digest             string
		transportErr       error
		blockDownloads     bool
	}{
		{name: "not found", stage: "download_payload", class: "payload_unavailable", status: http.StatusNotFound},
		{name: "rate limited", stage: "download_payload", class: "payload_rate_limited", status: http.StatusTooManyRequests},
		{name: "upstream unavailable", stage: "download_payload", class: "payload_upstream_failure", status: http.StatusServiceUnavailable},
		{name: "DNS", stage: "download_payload", class: "dns", transportErr: &net.DNSError{Name: privateText, Err: "cannot look up host", IsNotFound: true}},
		{name: "short package", stage: "download_payload", class: "payload_size", size: len(download) + 1},
		{name: "wrong digest", stage: "download_payload", class: "payload_verification", digest: strings.Repeat("a", 64)},
		{name: "download directory blocked", stage: "download_payload", class: "storage_io", blockDownloads: true},
		{name: "verified bytes not an archive", stage: "stage_release", class: "archive_extraction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write(download)
			}))
			defer server.Close()
			client := server.Client()
			if tc.transportErr != nil {
				client.Transport = failureRoundTripper{tc.transportErr}
			}
			source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{ManifestURL: server.URL + routePath + "/manifest.json", HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			logger := &chainResultLogger{}
			s := &Service{updatesRoot: t.TempDir(), logger: logger, chainRunning: true, done: make(chan struct{})}
			if tc.blockDownloads {
				if err := os.WriteFile(filepath.Join(s.updatesRoot, "downloads"), download, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			size, digest := tc.size, tc.digest
			if size == 0 {
				size = len(download)
			}
			if digest == "" {
				digest = goodDigest
			}
			manifest := hardeningdomain.ReleaseManifest{ReleaseID: "release", EmbeddedUpdate: &hardeningdomain.EmbeddedUpdateSegment{PayloadSize: int64(size), PayloadDigest: digest}}
			s.runChain(manifest, "digest", source.ManifestURL(), "operation", source)
			if s.lastStatus.Step != "failed" || s.lastStatus.LastError == nil || len(logger.warnings) != 1 {
				t.Fatalf("status=%+v warnings=%+v", s.lastStatus, logger.warnings)
			}
			event := logger.warnings[0]
			if event.Stage != tc.stage || event.ErrorClass != tc.class || event.Outcome != "failure" || event.Message != *s.lastStatus.LastError {
				t.Fatalf("event=%+v status=%+v", event, s.lastStatus)
			}
			persisted, err := os.ReadFile(filepath.Join(s.updatesRoot, preparationFailureFile))
			if err != nil {
				t.Fatal(err)
			}
			logged, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{privateText, routePath, server.URL, s.updatesRoot} {
				if strings.Contains(string(logged), forbidden) || strings.Contains(string(persisted), forbidden) {
					t.Fatal("failure output included source or filesystem details")
				}
			}
		})
	}
}

type failureRoundTripper struct{ err error }

func (rt failureRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

func TestPreparationClassifiesWrappedStorageErrors(t *testing.T) {
	for errno, want := range map[syscall.Errno]preparationClass{syscall.ENOSPC: "storage_capacity", syscall.EACCES: "storage_permission"} {
		cause := &os.PathError{Op: "write", Path: "/fixture", Err: errno}
		err := labelPreparationError(fmt.Errorf("write payload: %w", cause), prepTransfer)
		if got := classifyPreparation(err, prepStorage); got != want {
			t.Fatalf("class = %q, want %q", got, want)
		}
	}
}
