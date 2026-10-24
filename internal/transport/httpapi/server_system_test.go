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

	accessapplication "bansoko/internal/accesscontrol/application"
	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/maintenance"
	"bansoko/internal/runtime/readiness"
)

type systemTestBootstrap struct{}

func (systemTestBootstrap) Status(context.Context) (accessdomain.BootstrapStatus, error) {
	return accessdomain.BootstrapStatus{State: accessdomain.StateReady}, nil
}

type systemTestSecrets struct{}

func (systemTestSecrets) Available(context.Context) error { return nil }

type systemTestWorkers struct{}

func (systemTestWorkers) AllowedKinds() []string { return []string{"release_evaluate"} }

func TestNewProductionServerRequiresGlobalSections(t *testing.T) {
	_, err := NewProductionServer(Dependencies{
		DB:          &sql.DB{},
		Access:      accessapplication.NewService(nil),
		Environment: "production",
	})
	if err == nil || !strings.Contains(err.Error(), "global sections service is nil") {
		t.Fatalf("NewProductionServer error = %v, want required global sections dependency", err)
	}
}

type systemTestDeployment struct {
	identity deploymentidentity.Identity
	pointer  deploymentidentity.PointerState
	err      error
}

func (d systemTestDeployment) Identity() deploymentidentity.Identity { return d.identity }

func (d systemTestDeployment) CurrentPointer() deploymentidentity.PointerState { return d.pointer }

func (d systemTestDeployment) Validate() error { return d.err }

func TestEmbeddedUpdateHandlersFailClosedWhenUnwired(t *testing.T) {
	server := &Server{}
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{name: "check", handler: server.CheckUpdate},
		{name: "status", handler: server.GetUpdateStatus},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/update", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", response.Code)
			}
			if !strings.Contains(response.Body.String(), "SERVICE_NOT_CONFIGURED") {
				t.Fatalf("body = %q, want SERVICE_NOT_CONFIGURED", response.Body.String())
			}
		})
	}
}

func TestGetReadyzReturnsJSON503WithIdentityReason(t *testing.T) {
	validDigest := strings.Repeat("a", 64)
	checker := readiness.New(readiness.Dependencies{
		DB:        nil,
		Build:     buildinfo.BuildInfo{Commit: "commit", BuildID: "build"},
		Bootstrap: systemTestBootstrap{},
		Secrets:   systemTestSecrets{},
		Workers:   systemTestWorkers{},
		Gate:      maintenance.NewGate(),
		Deployment: systemTestDeployment{
			identity: deploymentidentity.Identity{
				BaseImageDigest:   "sha256:" + validDigest,
				SelectedSource:    deploymentidentity.SourceOverlay,
				SelectedReleaseID: "release-1",
				PayloadDigest:     validDigest,
				UpdateOperationID: "update-1",
			},
			pointer: deploymentidentity.PointerState{Token: "release-2", Readable: true, Presented: true},
		},
	})
	server, err := NewServer(Dependencies{
		DB:          &sql.DB{},
		Access:      accessapplication.NewService(nil),
		Readiness:   checker,
		SkipAuth:    true,
		Environment: "development",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/readyz", nil)
	response := httptest.NewRecorder()
	server.GetReadyz(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", response.Code)
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("readyz content type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	var body struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if body.Ready || !strings.Contains(body.Reason, "deployment_identity:inconsistent") {
		t.Fatalf("readyz body = %+v, want identity inconsistency", body)
	}
}

func TestGetReadyzIncludesDeploymentVerificationFailureDetail(t *testing.T) {
	checker := readiness.New(readiness.Dependencies{
		DB:        nil,
		Build:     buildinfo.BuildInfo{Commit: "commit", BuildID: "build"},
		Bootstrap: systemTestBootstrap{},
		Secrets:   systemTestSecrets{},
		Workers:   systemTestWorkers{},
		Gate:      maintenance.NewGate(),
		Deployment: systemTestDeployment{
			identity: deploymentidentity.Identity{BaseImageDigest: "sha256:" + strings.Repeat("a", 64), SelectedSource: deploymentidentity.SourceBase},
			err:      errors.New("descriptor metadata changed"),
		},
	})
	server, err := NewServer(Dependencies{DB: &sql.DB{}, Access: accessapplication.NewService(nil), Readiness: checker, SkipAuth: true, Environment: "development"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.GetReadyz(response, httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/readyz", nil))
	var body struct {
		Ready  bool `json:"ready"`
		Checks map[string]struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ready || body.Checks["deployment_identity"].Code != "unverified" || body.Checks["deployment_identity"].Detail != "descriptor metadata changed" {
		t.Fatalf("readyz body = %+v, want deployment verification evidence", body)
	}
}
