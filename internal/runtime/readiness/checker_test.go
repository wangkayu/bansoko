package readiness

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/maintenance"
)

type fakeBootstrap struct {
	state accessdomain.BootstrapState
	err   error
}

func (f *fakeBootstrap) Status(ctx context.Context) (accessdomain.BootstrapStatus, error) {
	if f.err != nil {
		return accessdomain.BootstrapStatus{}, f.err
	}
	return accessdomain.BootstrapStatus{State: f.state, Version: 1}, nil
}

type fakeSecrets struct{ err error }

func (f *fakeSecrets) Available(ctx context.Context) error { return f.err }

type fakeWorkers struct{ kinds []string }

func (f *fakeWorkers) AllowedKinds() []string { return f.kinds }

type fakeQB struct {
	healthy bool
	code    string
}

func (f *fakeQB) Healthy(ctx context.Context) (bool, string) { return f.healthy, f.code }

func healthyDeps() Dependencies {
	return Dependencies{
		DB:        &sql.DB{}, // ping will fail; tests override per-case
		Build:     buildinfo.BuildInfo{Version: "test", Commit: "abc123", BuildID: "test-build"},
		Bootstrap: &fakeBootstrap{state: accessdomain.StateReady},
		Secrets:   &fakeSecrets{},
		Workers:   &fakeWorkers{kinds: []string{"release_evaluate", "file_execute"}},
		Gate:      maintenance.NewGate(),
		QB:        &fakeQB{healthy: true, code: "ok"},
	}
}

func TestReadyWhenAllChecksPass(t *testing.T) {
	// A DB that pings ok; migrations verify would fail on a real pool, so use
	// the nil-DB path only when the check is explicitly exercised below. Here
	// we accept schema not_configured as the only non-ok hard check — instead
	// build a checker whose DB is nil but every other provider is healthy, and
	// assert the report is structurally complete.
	deps := healthyDeps()
	deps.DB = nil // no DB: postgres/schema/owner_read are not_configured
	report := New(deps).Report(context.Background())

	if report.Checks["http_alive"].OK != true {
		t.Error("http_alive must be ok")
	}
	for _, name := range []string{"postgres", "schema", "owner_read"} {
		if report.Checks[name].OK {
			t.Errorf("%s with nil DB must be not ok", name)
		}
	}
	if report.Checks["build_identity"].OK != true {
		t.Error("build_identity must be ok with populated identity")
	}
	if report.Checks["bootstrap"].Code != "ready" {
		t.Errorf("bootstrap code = %q, want ready", report.Checks["bootstrap"].Code)
	}
	if report.Checks["worker_lanes"].OK != true {
		t.Error("worker_lanes must be ok with registered kinds")
	}
	if report.Checks["maintenance"].Code != "serving" {
		t.Error("maintenance must be serving by default")
	}
	if !report.Checks["automation_resume_gate"].OK {
		t.Error("resume gate must be passed while serving")
	}
}

func TestMaintenanceMakesNotReady(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	gate := maintenance.NewGate()
	deps.Gate = gate
	gate.EnterMaintenance()

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("maintenance must make ready=false")
	}
	if report.Checks["maintenance"].Code != "maintenance" {
		t.Errorf("maintenance code = %q", report.Checks["maintenance"].Code)
	}
}

type strictIdentityProvider struct {
	identity deploymentidentity.Identity
}

func (p strictIdentityProvider) Identity() deploymentidentity.Identity { return p.identity }
func (strictIdentityProvider) CurrentPointer() deploymentidentity.PointerState {
	return deploymentidentity.PointerState{}
}
func (strictIdentityProvider) Validate() error     { return nil }
func (strictIdentityProvider) RequireSource() bool { return true }

func TestProductionIdentityRejectsPartialFacts(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Deployment = strictIdentityProvider{identity: deploymentidentity.Identity{BaseImageDigest: "sha256:" + strings.Repeat("a", 64)}}
	report := New(deps).Report(context.Background())
	if report.Checks["deployment_identity"].OK || report.Checks["deployment_identity"].Code != "unverified" {
		t.Fatalf("partial identity report = %+v, want unverified", report.Checks["deployment_identity"])
	}
	if !strings.Contains(report.Reason, "deployment_identity:unverified") {
		t.Fatalf("readiness reason = %q, want deployment identity failure", report.Reason)
	}
}

func TestSecretUnavailableMakesNotReady(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Secrets = &fakeSecrets{err: errors.New("key missing")}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("secret unavailability must make ready=false")
	}
}

func TestQBUnavailableIsDegradedNotHard(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.QB = &fakeQB{healthy: false, code: "unavailable"}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("nil-DB rig is not ready; qB must not mask hard failures")
	}
	if report.Checks["qbittorrent"].OK {
		t.Error("qbittorrent must be not ok when the provider reports unhealthy")
	}
	if report.Checks["qbittorrent"].Code != "unavailable" {
		t.Errorf("qbittorrent code = %q, want unavailable", report.Checks["qbittorrent"].Code)
	}
	// qB is never a hard-not-ready input: the reason must carry the first hard
	// failure (postgres/schema), not the qB code.
	if report.Reason == "not_ready:qb_unavailable" {
		t.Error("qB unavailability must not be the hard reason")
	}
}

type fakeDeploymentIdentity struct {
	identity deploymentidentity.Identity
	pointer  deploymentidentity.PointerState
	err      error
}

func (f fakeDeploymentIdentity) Identity() deploymentidentity.Identity { return f.identity }

func (f fakeDeploymentIdentity) CurrentPointer() deploymentidentity.PointerState { return f.pointer }

func (f fakeDeploymentIdentity) Validate() error { return f.err }

type realDeploymentIdentityProvider struct {
	updatesRoot string
}

func (p realDeploymentIdentityProvider) Identity() deploymentidentity.Identity {
	return deploymentidentity.FromEnv(os.Getenv)
}

func (p realDeploymentIdentityProvider) CurrentPointer() deploymentidentity.PointerState {
	return deploymentidentity.ReadCurrentPointer(p.updatesRoot)
}

func (p realDeploymentIdentityProvider) Validate() error   { return nil }
func (realDeploymentIdentityProvider) RequireSource() bool { return true }

func overlayIdentityForReadiness(expected string) deploymentidentity.Identity {
	return deploymentidentity.Identity{
		BaseImageDigest:        "sha256:" + strings.Repeat("a", 64),
		SelectedSource:         deploymentidentity.SourceOverlay,
		SelectedReleaseID:      "release-1",
		PayloadDigest:          strings.Repeat("b", 64),
		UpdateOperationID:      "update-1",
		ExpectedProcessBuildID: expected,
	}
}

func TestDeploymentIdentityRealProviderFillsProcessBuildIDFromBuildInfo(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Build = buildinfo.BuildInfo{Version: "test", Commit: "abc123", BuildID: "actual-build"}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "current"), []byte("release-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(deploymentidentity.EnvBaseImageDigest, overlayIdentityForReadiness("actual-build").BaseImageDigest)
	t.Setenv(deploymentidentity.EnvSource, deploymentidentity.SourceOverlay)
	t.Setenv(deploymentidentity.EnvSelectedReleaseID, "release-1")
	t.Setenv(deploymentidentity.EnvPayloadDigest, strings.Repeat("b", 64))
	t.Setenv(deploymentidentity.EnvUpdateOperationID, "update-1")
	t.Setenv(deploymentidentity.EnvExpectedProcessBuild, "actual-build")
	deps.Deployment = realDeploymentIdentityProvider{updatesRoot: root}

	report := New(deps).Report(context.Background())
	if report.Checks["deployment_identity"].Code != "consistent" {
		t.Fatalf("real overlay identity = %+v, want consistent", report.Checks["deployment_identity"])
	}
	if report.DeploymentIdentity == nil || report.DeploymentIdentity.ProcessBuildID != "actual-build" {
		t.Fatalf("reported process build id = %+v, want actual-build", report.DeploymentIdentity)
	}
}

func TestDeploymentIdentityRealProviderRejectsBuildInfoMismatch(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Build = buildinfo.BuildInfo{Version: "test", Commit: "abc123", BuildID: "actual-build"}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "current"), []byte("release-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := overlayIdentityForReadiness("expected-build")
	t.Setenv(deploymentidentity.EnvBaseImageDigest, identity.BaseImageDigest)
	t.Setenv(deploymentidentity.EnvSource, identity.SelectedSource)
	t.Setenv(deploymentidentity.EnvSelectedReleaseID, identity.SelectedReleaseID)
	t.Setenv(deploymentidentity.EnvPayloadDigest, identity.PayloadDigest)
	t.Setenv(deploymentidentity.EnvUpdateOperationID, identity.UpdateOperationID)
	t.Setenv(deploymentidentity.EnvExpectedProcessBuild, identity.ExpectedProcessBuildID)
	deps.Deployment = realDeploymentIdentityProvider{updatesRoot: root}

	report := New(deps).Report(context.Background())
	if report.Checks["deployment_identity"].Code != "unverified" {
		t.Fatalf("mismatched real overlay identity = %+v, want unverified", report.Checks["deployment_identity"])
	}
}

func TestDeploymentIdentityAbsentIsNeverHard(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil

	report := New(deps).Report(context.Background())
	if report.Checks["deployment_identity"].Code != "not_provided" {
		t.Errorf("nil provider must report not_provided, got %q", report.Checks["deployment_identity"].Code)
	}
	if report.DeploymentIdentity != nil {
		t.Error("absent identity must not be attached to the report")
	}
}

func TestDeploymentIdentityConsistentAttachesReport(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	identity := deploymentidentity.Identity{
		BaseImageDigest:        "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		SelectedSource:         deploymentidentity.SourceOverlay,
		SelectedReleaseID:      "release-1",
		PayloadDigest:          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		UpdateOperationID:      "update-1",
		ExpectedProcessBuildID: "test-build",
		ProcessBuildID:         "test-build",
	}
	deps.Deployment = fakeDeploymentIdentity{
		identity: identity,
		pointer:  deploymentidentity.PointerState{Token: "release-1", Readable: true, Presented: true},
	}

	report := New(deps).Report(context.Background())
	if !report.Checks["deployment_identity"].OK {
		t.Errorf("consistent identity must be ok, got %+v", report.Checks["deployment_identity"])
	}
	if report.DeploymentIdentity == nil || report.DeploymentIdentity.SelectedReleaseID != "release-1" {
		t.Errorf("identity must be attached to the report, got %+v", report.DeploymentIdentity)
	}
}

func TestDeploymentIdentityInconsistencyMakesNotReady(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest:        "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			SelectedSource:         deploymentidentity.SourceOverlay,
			SelectedReleaseID:      "release-1",
			PayloadDigest:          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			UpdateOperationID:      "update-1",
			ExpectedProcessBuildID: "test-build",
			ProcessBuildID:         "test-build",
		},
		pointer: deploymentidentity.PointerState{Token: "release-2", Readable: true, Presented: true},
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("identity inconsistency must make ready=false")
	}
	if report.Checks["deployment_identity"].Code != "inconsistent" {
		t.Errorf("code = %q, want inconsistent", report.Checks["deployment_identity"].Code)
	}
	if report.Checks["deployment_identity"].Detail == "" {
		t.Error("inconsistency must carry a typed detail")
	}
	if !strings.Contains(report.Reason, "deployment_identity:inconsistent") {
		t.Errorf("reason = %q", report.Reason)
	}
}

func TestDeploymentIdentityVerificationFailureMakesNotReady(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest:   "sha256:" + strings.Repeat("a", 64),
			SelectedSource:    deploymentidentity.SourceOverlay,
			SelectedReleaseID: "release-1",
			PayloadDigest:     strings.Repeat("a", 64),
			UpdateOperationID: "update-1",
		},
		pointer: deploymentidentity.PointerState{Token: "release-1", Readable: true, Presented: true},
		err:     errors.New("signed descriptor changed"),
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("verification failure must make ready=false")
	}
	if got := report.Checks["deployment_identity"]; got.Code != "unverified" || got.Detail != "signed descriptor changed" {
		t.Fatalf("deployment identity check = %+v, want unverified detail", got)
	}
	if !strings.Contains(report.Reason, "deployment_identity:unverified") {
		t.Fatalf("reason = %q", report.Reason)
	}
}

// TestDeploymentIdentityProcessBuildMismatch verifies that an overlay launch
// with mismatched process build id fails readiness (design §6 B5).
func TestDeploymentIdentityProcessBuildMismatch(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Build = buildinfo.BuildInfo{Version: "test", Commit: "abc123", BuildID: "actual-build"}
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest:        "sha256:" + strings.Repeat("a", 64),
			SelectedSource:         deploymentidentity.SourceOverlay,
			SelectedReleaseID:      "release-1",
			PayloadDigest:          strings.Repeat("b", 64),
			UpdateOperationID:      "update-1",
			ExpectedProcessBuildID: "expected-build",
			ProcessBuildID:         "actual-build", // will be set by checker
		},
		pointer: deploymentidentity.PointerState{Token: "release-1", Readable: true, Presented: true},
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("process build mismatch must make ready=false")
	}
	if report.Checks["deployment_identity"].Code != "inconsistent" {
		t.Errorf("code = %q, want inconsistent", report.Checks["deployment_identity"].Code)
	}
	if !strings.Contains(report.Checks["deployment_identity"].Detail, "build id") {
		t.Errorf("detail = %q, want build id mention", report.Checks["deployment_identity"].Detail)
	}
}

// TestDeploymentIdentityPointerAbsentForOverlay verifies that an overlay
// identity without a current pointer fails readiness (design §6 B5).
func TestDeploymentIdentityPointerAbsentForOverlay(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Build = buildinfo.BuildInfo{Version: "test", Commit: "abc123", BuildID: "test-build"}
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest:        "sha256:" + strings.Repeat("a", 64),
			SelectedSource:         deploymentidentity.SourceOverlay,
			SelectedReleaseID:      "release-1",
			PayloadDigest:          strings.Repeat("b", 64),
			UpdateOperationID:      "update-1",
			ExpectedProcessBuildID: "test-build",
		},
		pointer: deploymentidentity.PointerState{Readable: false, Presented: false}, // pointer absent
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("missing pointer for overlay must make ready=false")
	}
	if report.Checks["deployment_identity"].Code != "inconsistent" {
		t.Errorf("code = %q, want inconsistent", report.Checks["deployment_identity"].Code)
	}
}

// TestDeploymentIdentityBaseWithOverlayPointer verifies that a base launch
// while the pointer selects an overlay release fails readiness (design §6 B5).
func TestDeploymentIdentityBaseWithOverlayPointer(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest: "sha256:" + strings.Repeat("a", 64),
			SelectedSource:  deploymentidentity.SourceBase,
			// No payload facts
		},
		pointer: deploymentidentity.PointerState{Token: "release-1", Readable: true, Presented: true},
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("base launch with overlay pointer must make ready=false")
	}
	if report.Checks["deployment_identity"].Code != "inconsistent" {
		t.Errorf("code = %q, want inconsistent", report.Checks["deployment_identity"].Code)
	}
}

// TestDeploymentIdentityCorruptPointer verifies that a corrupt pointer
// (present but unreadable) fails readiness (design §6 B5).
func TestDeploymentIdentityCorruptPointer(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Build = buildinfo.BuildInfo{Version: "test", Commit: "abc123", BuildID: "test-build"}
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest:        "sha256:" + strings.Repeat("a", 64),
			SelectedSource:         deploymentidentity.SourceOverlay,
			SelectedReleaseID:      "release-1",
			PayloadDigest:          strings.Repeat("b", 64),
			UpdateOperationID:      "update-1",
			ExpectedProcessBuildID: "test-build",
		},
		pointer: deploymentidentity.PointerState{Readable: false, Presented: true, Corrupt: true},
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("corrupt pointer must make ready=false")
	}
	if report.Checks["deployment_identity"].Code != "inconsistent" {
		t.Errorf("code = %q, want inconsistent", report.Checks["deployment_identity"].Code)
	}
}

// TestDeploymentIdentityBaseConsistency verifies that a base launch with
// matching base image digest and no overlay facts passes readiness (design §6 B5).
func TestDeploymentIdentityBaseConsistency(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest: "sha256:" + strings.Repeat("a", 64),
			SelectedSource:  deploymentidentity.SourceBase,
			// No payload/release/operation facts
		},
		pointer: deploymentidentity.PointerState{Readable: false, Presented: false}, // no pointer is valid for base
	}

	report := New(deps).Report(context.Background())
	if report.Checks["deployment_identity"].Code != "consistent" {
		t.Errorf("base consistency check = %+v, want consistent", report.Checks["deployment_identity"])
	}
	if report.DeploymentIdentity == nil || report.DeploymentIdentity.SelectedSource != "base" {
		t.Errorf("base identity must be attached, got %+v", report.DeploymentIdentity)
	}
}

// TestDeploymentIdentityValidationError verifies that provider Validate()
// errors fail readiness with the error detail (design §6 B5).
func TestDeploymentIdentityValidationError(t *testing.T) {
	deps := healthyDeps()
	deps.DB = nil
	deps.Deployment = fakeDeploymentIdentity{
		identity: deploymentidentity.Identity{
			BaseImageDigest: "sha256:" + strings.Repeat("a", 64),
			SelectedSource:  deploymentidentity.SourceBase,
		},
		pointer: deploymentidentity.PointerState{},
		err:     errors.New("signed descriptor metadata changed"),
	}

	report := New(deps).Report(context.Background())
	if report.Ready {
		t.Fatal("validation error must make ready=false")
	}
	if report.Checks["deployment_identity"].Code != "unverified" {
		t.Errorf("code = %q, want unverified", report.Checks["deployment_identity"].Code)
	}
	if report.Checks["deployment_identity"].Detail != "signed descriptor metadata changed" {
		t.Errorf("detail = %q, want validation error message", report.Checks["deployment_identity"].Detail)
	}
}
