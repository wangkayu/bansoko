package application

// This file re-establishes behavioural coverage for the contracts the
// disposable server phase of UPDATE-029 still owns: manifest verification and
// candidate resolution (Admit / AdmitUpdate / CheckUpdate), the anti-replay
// release-sequence floor read, and the pending-marker handoff read. Every test
// asserts the observable refusal (status code, error type, reported status) and
// that a refusal happens before any workspace effect; none of them depend on
// the journal, fence, maintenance gate or operator control plane that the
// disposable phase removed.

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/update/journal"
)

// disposableManifestPath is the immutable manifest document the fixture serves;
// the payload sibling is derived from its directory by the update source.
const disposableManifestPath = "/release-manifest.json"

// disposableClockInstant is the injected clock reading every test asserts on,
// so no assertion depends on wall time.
var disposableClockInstant = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func disposableClock() time.Time { return disposableClockInstant }

// disposableSignedManifest builds one fully signed embedded-update release
// manifest, mirroring what build-release.sh publishes: a signed outer manifest
// whose embedded segment carries the payload signature, the bootstrap floor and
// the image floor the deployment trusts.
func disposableSignedManifest(t *testing.T, releaseID string, releaseSequence int64, bootstrapMinVersion, minImageVersion string) (hardeningdomain.ReleaseManifest, ed25519.PublicKey) {
	t.Helper()
	return disposableSignedManifestWith(t, releaseID, releaseSequence, bootstrapMinVersion, minImageVersion, nil)
}

// disposableSignedManifestWith is disposableSignedManifest with a mutation
// applied before signing, for a test that needs another version, channel or
// release notes on an otherwise identical signed manifest.
func disposableSignedManifestWith(t *testing.T, releaseID string, releaseSequence int64, bootstrapMinVersion, minImageVersion string, mutate func(*hardeningdomain.ReleaseManifest)) (hardeningdomain.ReleaseManifest, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("disposable server phase payload")
	payloadDigest := hardeningdomain.HashArtifact(payload)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(privateKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := hardeningdomain.HashArtifact([]byte("disposable server phase artifact"))
	manifest := hardeningdomain.ReleaseManifest{
		SchemaVersion:          "release-manifest-1",
		ReleaseSequence:        releaseSequence,
		ReleaseID:              releaseID,
		Version:                "0.9.0-test",
		Channel:                hardeningdomain.ReleaseChannelStable,
		SourceCommit:           "commit-test",
		APIContractVersion:     "v2",
		TargetMigrationVersion: "001",
		RequiredDowntime:       true,
		BackupRequired:         true,
		Rollback:               hardeningdomain.RestoreRequired,
		Artifacts: []hardeningdomain.ArtifactRef{{
			Platform: "linux", Architecture: "amd64", Digest: artifactDigest, Size: 1, Signature: "artifact-signature",
		}},
		SBOMRef:        "sbom",
		ProvenanceRef:  "provenance",
		ArtifactDigest: artifactDigest,
		Immutable:      true,
		EmbeddedUpdate: &hardeningdomain.EmbeddedUpdateSegment{
			FormatVersion:       hardeningdomain.EmbeddedUpdateFormatV1,
			Platform:            "linux",
			Architecture:        "amd64",
			PayloadDigest:       payloadDigest,
			PayloadSize:         int64(len(payload)),
			PayloadSignature:    payloadSignature,
			BootstrapMinVersion: bootstrapMinVersion,
			MinImageVersion:     minImageVersion,
			TargetReleaseID:     releaseID,
			TargetVersion:       "0.9.0-test",
			TargetSchemaVersion: "001",
			ReleaseSequence:     releaseSequence,
		},
	}
	if mutate != nil {
		mutate(&manifest)
	}
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return manifest, publicKey
}

func disposableManifestDigest(t *testing.T, manifest hardeningdomain.ReleaseManifest) string {
	t.Helper()
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// disposableFixture serves one signed release manifest through the real HTTP
// verification path and records every request path, so a test can prove that
// discovery either reached the deployment source or never touched it. With
// blockPayload the derived payload sibling is requested but answered only when
// the test releases it, which keeps an admitted update chain deterministically
// in flight.
type disposableFixture struct {
	server   *httptest.Server
	manifest hardeningdomain.ReleaseManifest

	blockPayload   bool
	releasePayload chan struct{}
	payloadStarted chan struct{}
	payloadOnce    sync.Once

	mu       sync.Mutex
	requests []string
}

func newDisposableFixture(t *testing.T, manifest hardeningdomain.ReleaseManifest, blockPayload bool) *disposableFixture {
	t.Helper()
	fixture := &disposableFixture{
		manifest:       manifest,
		blockPayload:   blockPayload,
		releasePayload: make(chan struct{}),
		payloadStarted: make(chan struct{}),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.record(r.URL.Path)
		if r.URL.Path == disposableManifestPath {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fixture.manifest)
			return
		}
		if strings.HasSuffix(r.URL.Path, PayloadFileName) {
			fixture.payloadOnce.Do(func() { close(fixture.payloadStarted) })
			if fixture.blockPayload {
				<-fixture.releasePayload
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fixture.server.Close)
	t.Cleanup(func() { close(fixture.releasePayload) })
	return fixture
}

func (f *disposableFixture) manifestURL() string { return f.server.URL + disposableManifestPath }

func (f *disposableFixture) client() *http.Client { return f.server.Client() }

func (f *disposableFixture) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, path)
}

func (f *disposableFixture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// awaitPayloadRequest proves the admitted update chain reached the deployment
// source instead of failing before discovery.
func (f *disposableFixture) awaitPayloadRequest(t *testing.T) {
	t.Helper()
	select {
	case <-f.payloadStarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("the admitted update chain never requested %s", PayloadFileName)
	}
}

// disposableService builds the embedded-mode service under test with the
// package's fixed clock so status timestamps never depend on wall time.
func disposableService(t *testing.T, deps Deps) *Service {
	t.Helper()
	deps.UpdateMode = "embedded"
	if deps.Clock == nil {
		deps.Clock = disposableClock
	}
	service, err := NewService(deps)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func requireDisposableBusinessError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != status || business.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

// requireNoDisposableEffect proves a refusal happened before the durable update
// boundary: the updates root holds neither a staged release nor a pending
// marker, and no operation progress was recorded for the operator.
func requireNoDisposableEffect(t *testing.T, service *Service, updatesRoot string) {
	t.Helper()
	entries, err := os.ReadDir(updatesRoot)
	if err != nil {
		t.Fatalf("read updates root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused candidate wrote update workspace entries: %v", entries)
	}
	status, ok, err := service.GetUpdateStatus(context.Background())
	if err != nil {
		t.Fatalf("GetUpdateStatus: %v", err)
	}
	if ok {
		t.Fatalf("a refused candidate recorded status %+v, want none", status)
	}
}

// disposableFloorDB returns a *sql.DB whose single query answers with the
// persisted release-sequence floor. The anti-replay read is one read-only
// aggregate, so an in-process driver reproduces it exactly while keeping the
// test free of PostgreSQL: the real query path, the real Scan and the real
// comparison all run. A non-nil failure makes the read itself fail.
func disposableFloorDB(t *testing.T, floor int64, failure error) *sql.DB {
	t.Helper()
	db := sql.OpenDB(disposableFloorConnector{floor: floor, failure: failure})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var (
	_ driver.Connector      = disposableFloorConnector{}
	_ driver.Conn           = (*disposableFloorConn)(nil)
	_ driver.QueryerContext = (*disposableFloorConn)(nil)
	_ driver.Rows           = (*disposableFloorRows)(nil)
)

type disposableFloorConnector struct {
	floor   int64
	failure error
}

func (c disposableFloorConnector) Connect(context.Context) (driver.Conn, error) {
	return &disposableFloorConn{floor: c.floor, failure: c.failure}, nil
}

func (c disposableFloorConnector) Driver() driver.Driver { return disposableFloorDriver{} }

type disposableFloorDriver struct{}

func (disposableFloorDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("disposable floor driver is only usable through its connector")
}

type disposableFloorConn struct {
	floor   int64
	failure error
}

func (c *disposableFloorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("disposable floor connection does not prepare statements")
}

func (c *disposableFloorConn) Close() error { return nil }

func (c *disposableFloorConn) Begin() (driver.Tx, error) {
	return nil, errors.New("disposable floor connection does not open transactions")
}

func (c *disposableFloorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.failure != nil {
		return nil, c.failure
	}
	return &disposableFloorRows{floor: c.floor}, nil
}

type disposableFloorRows struct {
	floor     int64
	delivered bool
}

func (r *disposableFloorRows) Columns() []string { return []string{"floor"} }

func (r *disposableFloorRows) Close() error { return nil }

func (r *disposableFloorRows) Next(dest []driver.Value) error {
	if r.delivered {
		return io.EOF
	}
	r.delivered = true
	dest[0] = r.floor
	return nil
}

// TestDisposableServerPhaseAdmitUpdateRefusesStaleCandidate covers
// UPDATE_CANDIDATE_STALE: the caller's selected candidate must still be the
// candidate the signed manifest resolves to.
func TestDisposableServerPhaseAdmitUpdateRefusesStaleCandidate(t *testing.T) {
	manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v1", "base-v1")
	fixture := newDisposableFixture(t, manifest, false)
	digest := disposableManifestDigest(t, manifest)

	cases := []struct {
		name  string
		input AdmitInput
	}{
		{
			name:  "release id no longer matches the signed manifest",
			input: AdmitInput{TargetReleaseID: "release-disposable-other", ManifestDigest: digest},
		},
		{
			name:  "manifest digest no longer matches the signed manifest",
			input: AdmitInput{TargetReleaseID: manifest.ReleaseID, ManifestDigest: hardeningdomain.HashArtifact([]byte("stale disposable candidate"))},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updatesRoot := t.TempDir()
			service := disposableService(t, Deps{
				PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
				HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
				BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
			})

			status, created, err := service.AdmitUpdate(context.Background(), tc.input, "disposable-stale-candidate")
			requireDisposableBusinessError(t, err, http.StatusConflict, "UPDATE_CANDIDATE_STALE")
			if created {
				t.Fatal("a stale candidate was reported as newly admitted")
			}
			if status.OperationID != "" || status.Step != "" || status.TargetReleaseID != "" {
				t.Fatalf("stale admission reported status %+v, want none", status)
			}
			requireNoDisposableEffect(t, service, updatesRoot)
		})
	}
}

// TestDisposableServerPhaseAdmitRefusesCallerSuppliedSource covers
// UPDATE_SOURCE_MISMATCH: discovery is deployment configuration, so a
// caller-supplied manifest URL can never redirect which release is installed.
func TestDisposableServerPhaseAdmitRefusesCallerSuppliedSource(t *testing.T) {
	manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v1", "base-v1")

	t.Run("channel deployment refuses a caller-supplied manifest url", func(t *testing.T) {
		updatesRoot := t.TempDir()
		service := disposableService(t, Deps{
			PublicKey: publicKey, UpdatesRoot: updatesRoot,
			ChannelURL: "https://packages.test/channel/channel-v1.json", PackageRoot: "https://packages.test/packages",
			BuildReleaseID: "running-build", BuildVersion: "running-version",
			BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
		})

		status, created, err := service.Admit(context.Background(),
			"https://packages.test/packages/1.2.3/release-manifest.json", "disposable-channel-mismatch")
		requireDisposableBusinessError(t, err, http.StatusConflict, "UPDATE_SOURCE_MISMATCH")
		if created {
			t.Fatal("a caller-supplied manifest URL was admitted on a channel deployment")
		}
		if status.OperationID != "" || status.Step != "" {
			t.Fatalf("refused admission reported status %+v, want none", status)
		}
		requireNoDisposableEffect(t, service, updatesRoot)
	})

	t.Run("exact-manifest deployment refuses a different manifest url", func(t *testing.T) {
		fixture := newDisposableFixture(t, manifest, false)
		updatesRoot := t.TempDir()
		service := disposableService(t, Deps{
			PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
			HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
			BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
		})

		status, created, err := service.Admit(context.Background(),
			"https://packages.test/packages/9.9.9/release-manifest.json", "disposable-manifest-mismatch")
		requireDisposableBusinessError(t, err, http.StatusConflict, "UPDATE_SOURCE_MISMATCH")
		if created {
			t.Fatal("a mismatched manifest URL was admitted")
		}
		if status.OperationID != "" || status.Step != "" {
			t.Fatalf("refused admission reported status %+v, want none", status)
		}
		if requests := fixture.requestCount(); requests != 0 {
			t.Fatalf("a mismatched manifest URL still reached the configured source %d time(s)", requests)
		}
		requireNoDisposableEffect(t, service, updatesRoot)
	})
}

// TestDisposableServerPhaseAdmitUpdateRefusesSelfReplay covers
// UPDATE_TARGET_CURRENT: replaying the running release must never be admitted.
func TestDisposableServerPhaseAdmitUpdateRefusesSelfReplay(t *testing.T) {
	manifest, publicKey := disposableSignedManifest(t, "running-build", 7, "embedded-bootstrap-v1", "base-v1")
	fixture := newDisposableFixture(t, manifest, false)
	updatesRoot := t.TempDir()
	service := disposableService(t, Deps{
		PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
		HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
		BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
	})

	status, created, err := service.AdmitUpdate(context.Background(), AdmitInput{
		TargetReleaseID: manifest.ReleaseID,
		ManifestDigest:  disposableManifestDigest(t, manifest),
	}, "disposable-self-replay")
	requireDisposableBusinessError(t, err, http.StatusUnprocessableEntity, "UPDATE_TARGET_CURRENT")
	if created {
		t.Fatal("the running release was admitted as an update target")
	}
	if status.OperationID != "" || status.Step != "" {
		t.Fatalf("refused admission reported status %+v, want none", status)
	}
	requireNoDisposableEffect(t, service, updatesRoot)
}

// TestDisposableServerPhaseAdmitUpdateRefusesNonAdvancingReleaseSequence covers
// UPDATE_TARGET_STALE: the anti-replay floor read from the durable fence table
// is what decides, so a target that does not advance beyond it is refused while
// a read failure is reported as itself.
func TestDisposableServerPhaseAdmitUpdateRefusesNonAdvancingReleaseSequence(t *testing.T) {
	manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v1", "base-v1")
	fixture := newDisposableFixture(t, manifest, false)
	digest := disposableManifestDigest(t, manifest)

	refusals := []struct {
		name  string
		floor int64
	}{
		{name: "floor equals the signed sequence", floor: 7},
		{name: "floor is beyond the signed sequence", floor: 8},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			updatesRoot := t.TempDir()
			service := disposableService(t, Deps{
				DB:        disposableFloorDB(t, tc.floor, nil),
				PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
				HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
				BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
			})

			status, created, err := service.AdmitUpdate(context.Background(), AdmitInput{
				TargetReleaseID: manifest.ReleaseID,
				ManifestDigest:  digest,
			}, "disposable-sequence-floor")
			requireDisposableBusinessError(t, err, http.StatusUnprocessableEntity, "UPDATE_TARGET_STALE")
			if created {
				t.Fatal("a non-advancing release sequence was reported as admitted")
			}
			if status.OperationID != "" || status.Step != "" {
				t.Fatalf("refused admission reported status %+v, want none", status)
			}
			requireNoDisposableEffect(t, service, updatesRoot)
		})
	}

	t.Run("the persisted floor read is what decides", func(t *testing.T) {
		readFailure := errors.New("disposable floor read failed")
		updatesRoot := t.TempDir()
		service := disposableService(t, Deps{
			DB:        disposableFloorDB(t, 0, readFailure),
			PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
			HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
			BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
		})

		_, created, err := service.AdmitUpdate(context.Background(), AdmitInput{
			TargetReleaseID: manifest.ReleaseID,
			ManifestDigest:  digest,
		}, "disposable-sequence-floor-unreadable")
		if created {
			t.Fatal("an unreadable release sequence floor was reported as admitted")
		}
		if !errors.Is(err, readFailure) {
			t.Fatalf("admission error = %v, want the persisted floor read failure", err)
		}
		var business *apperrors.Business
		if errors.As(err, &business) {
			t.Fatalf("an unreadable release sequence floor was reported as %d %s", business.Status, business.Code)
		}
		requireNoDisposableEffect(t, service, updatesRoot)
	})

	t.Run("a sequence above the persisted floor is admitted", func(t *testing.T) {
		blocking := newDisposableFixture(t, manifest, true)
		updatesRoot := t.TempDir()
		service := disposableService(t, Deps{
			DB:        disposableFloorDB(t, 6, nil),
			PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: blocking.manifestURL(),
			HTTPClient: blocking.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
			BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
		})

		status, created, err := service.AdmitUpdate(context.Background(), AdmitInput{
			TargetReleaseID: manifest.ReleaseID,
			ManifestDigest:  digest,
		}, "disposable-sequence-floor-advanced")
		if err != nil || !created {
			t.Fatalf("admission = %+v, created=%v, err=%v; want an accepted target above the floor", status, created, err)
		}
		if status.Step != "accepted" || status.TargetReleaseID != manifest.ReleaseID || status.OperationID == "" {
			t.Fatalf("admitted status = %+v, want an accepted operation for %q", status, manifest.ReleaseID)
		}
		blocking.awaitPayloadRequest(t)
	})
}

// TestDisposableServerPhaseAdmitUpdateRefusesUnmeetableBootstrapFloor covers
// the two bootstrap-floor refusals admission owns: a floor newer than the
// running bootstrap, and a signed floor no base image could ever satisfy.
func TestDisposableServerPhaseAdmitUpdateRefusesUnmeetableBootstrapFloor(t *testing.T) {
	t.Run("floor newer than the running bootstrap", func(t *testing.T) {
		manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v2", "base-v1")
		fixture := newDisposableFixture(t, manifest, false)
		updatesRoot := t.TempDir()
		service := disposableService(t, Deps{
			PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
			HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
			BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
		})

		status, created, err := service.AdmitUpdate(context.Background(), AdmitInput{
			TargetReleaseID: manifest.ReleaseID,
			ManifestDigest:  disposableManifestDigest(t, manifest),
		}, "disposable-bootstrap-too-old")
		var tooOld *apperrors.BootstrapTooOldError
		if !errors.As(err, &tooOld) {
			t.Fatalf("admission error = %v, want apperrors.BootstrapTooOldError", err)
		}
		if tooOld.Required != "embedded-bootstrap-v2" || tooOld.Current != "embedded-bootstrap-v1" {
			t.Fatalf("refusal = required %q current %q, want the signed floor and the running bootstrap", tooOld.Required, tooOld.Current)
		}
		if created {
			t.Fatal("a candidate requiring a newer bootstrap was reported as admitted")
		}
		if status.OperationID != "" || status.Step != "" {
			t.Fatalf("refused admission reported status %+v, want none", status)
		}
		requireNoDisposableEffect(t, service, updatesRoot)
	})

	t.Run("floor that no base image can satisfy", func(t *testing.T) {
		manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "bootstrap-v2", "base-v1")
		fixture := newDisposableFixture(t, manifest, false)
		updatesRoot := t.TempDir()
		service := disposableService(t, Deps{
			PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
			HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
			BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
		})

		status, created, err := service.AdmitUpdate(context.Background(), AdmitInput{
			TargetReleaseID: manifest.ReleaseID,
			ManifestDigest:  disposableManifestDigest(t, manifest),
		}, "disposable-bootstrap-unparseable")
		requireDisposableBusinessError(t, err, http.StatusUnprocessableEntity, "BOOTSTRAP_COMPATIBILITY_INVALID")
		if created {
			t.Fatal("a malformed bootstrap floor was reported as admitted")
		}
		if status.OperationID != "" || status.Step != "" {
			t.Fatalf("refused admission reported status %+v, want none", status)
		}
		requireNoDisposableEffect(t, service, updatesRoot)
	})
}

// TestDisposableServerPhaseCheckUpdateRequiresBaseImageForBootstrapFloor covers
// the side-effect-free check: a candidate whose signed bootstrap floor exceeds
// this instance is reported as requiring a base image, never as applicable.
func TestDisposableServerPhaseCheckUpdateRequiresBaseImageForBootstrapFloor(t *testing.T) {
	cases := []struct {
		name               string
		runningBootstrap   string
		wantsCurrent       string
		rejectsCurrentEcho string
	}{
		{name: "running bootstrap is older", runningBootstrap: "embedded-bootstrap-v1", wantsCurrent: "embedded-bootstrap-v1"},
		{name: "running bootstrap is unusable", runningBootstrap: "bootstrap-v1", rejectsCurrentEcho: "bootstrap-v1"},
		{name: "running bootstrap is unreported", runningBootstrap: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v2", "base-v1")
			fixture := newDisposableFixture(t, manifest, false)
			updatesRoot := t.TempDir()
			service := disposableService(t, Deps{
				PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
				HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
				BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: tc.runningBootstrap,
			})

			check, err := service.CheckUpdate(context.Background())
			if err != nil {
				t.Fatalf("CheckUpdate: %v", err)
			}
			if check.Status != CheckStatus("base_image_required") || check.Compatibility.Kind != CompatibilityKind("base_image_required") {
				t.Fatalf("check = status %q kind %q, want base_image_required", check.Status, check.Compatibility.Kind)
			}
			if !strings.Contains(check.Compatibility.Reason, "embedded-bootstrap-v2") {
				t.Fatalf("reason %q does not name the required bootstrap floor", check.Compatibility.Reason)
			}
			if tc.wantsCurrent != "" && !strings.Contains(check.Compatibility.Reason, tc.wantsCurrent) {
				t.Fatalf("reason %q does not name the running bootstrap %q", check.Compatibility.Reason, tc.wantsCurrent)
			}
			if tc.rejectsCurrentEcho != "" && strings.Contains(check.Compatibility.Reason, tc.rejectsCurrentEcho) {
				t.Fatalf("reason %q echoes the unvalidated running value %q", check.Compatibility.Reason, tc.rejectsCurrentEcho)
			}
			if check.CheckedAt != disposableClockInstant {
				t.Fatalf("checked at = %s, want the injected clock reading %s", check.CheckedAt, disposableClockInstant)
			}
			entries, err := os.ReadDir(updatesRoot)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("an update check wrote update workspace entries: %v", entries)
			}
		})
	}
}

// TestDisposableServerPhaseParseEmbeddedBootstrapVersion covers the strict
// embedded-bootstrap-v<N> syntax the bootstrap floor ordering depends on.
func TestDisposableServerPhaseParseEmbeddedBootstrapVersion(t *testing.T) {
	accepted := []struct {
		value  string
		number int64
	}{
		{value: "embedded-bootstrap-v0", number: 0},
		{value: "embedded-bootstrap-v1", number: 1},
		{value: "embedded-bootstrap-v2", number: 2},
		{value: "embedded-bootstrap-v10", number: 10},
	}
	for _, tc := range accepted {
		number, err := parseEmbeddedBootstrapVersion(tc.value)
		if err != nil || number != tc.number {
			t.Fatalf("parseEmbeddedBootstrapVersion(%q) = %d, %v; want %d, nil", tc.value, number, err, tc.number)
		}
	}

	rejected := []string{
		"",
		"embedded-bootstrap-",
		"embedded-bootstrap-v",
		"embedded-bootstrap-vx",
		"embedded-bootstrap-v1x",
		"embedded-bootstrap-v1.0",
		"embedded-bootstrap-v-1",
		"embedded-bootstrap-v3 ",
		" embedded-bootstrap-v3",
		"bootstrap-v1",
		"v1",
		"base-v1",
	}
	for _, value := range rejected {
		if number, err := parseEmbeddedBootstrapVersion(value); err == nil {
			t.Fatalf("parseEmbeddedBootstrapVersion(%q) = %d, want rejection", value, number)
		}
	}
}

// TestDisposableServerPhaseEvaluateBootstrapCompatibility covers the floor
// decision: an absent requirement is satisfied, an unparseable one is a
// manifest-invalid error, and ordering is numeric rather than lexical.
func TestDisposableServerPhaseEvaluateBootstrapCompatibility(t *testing.T) {
	cases := []struct {
		name            string
		current         string
		required        string
		satisfied       bool
		manifestInvalid bool
	}{
		{name: "equal versions", current: "embedded-bootstrap-v1", required: "embedded-bootstrap-v1", satisfied: true},
		{name: "newer running bootstrap", current: "embedded-bootstrap-v3", required: "embedded-bootstrap-v1", satisfied: true},
		{name: "numeric order beats lexical order", current: "embedded-bootstrap-v10", required: "embedded-bootstrap-v9", satisfied: true},
		{name: "older running bootstrap", current: "embedded-bootstrap-v1", required: "embedded-bootstrap-v2"},
		{name: "numeric order beats lexical order for the floor", current: "embedded-bootstrap-v9", required: "embedded-bootstrap-v10"},
		{name: "missing running version", current: "", required: "embedded-bootstrap-v1"},
		{name: "garbage running version", current: "bootstrap-v1", required: "embedded-bootstrap-v1"},
		{name: "empty requirement is satisfied", current: "embedded-bootstrap-v1", required: "", satisfied: true},
		{name: "empty requirement is satisfied without a running version", current: "", required: "", satisfied: true},
		{name: "malformed requirement", current: "embedded-bootstrap-v1", required: "bootstrap-v2", manifestInvalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluateBootstrapCompatibility(tc.current, tc.required)
			if result.Satisfied != tc.satisfied {
				t.Fatalf("satisfied = %v, want %v", result.Satisfied, tc.satisfied)
			}
			if (result.ManifestInvalid != nil) != tc.manifestInvalid {
				t.Fatalf("manifest invalid = %v, want %v", result.ManifestInvalid, tc.manifestInvalid)
			}
			if result.Required != tc.required || result.Current != tc.current {
				t.Fatalf("identity = required %q current %q, want %q / %q", result.Required, result.Current, tc.required, tc.current)
			}
		})
	}
}

// TestDisposableServerPhaseCheckUpdateReportsPendingMarker covers the
// marker-authoritative read: once the handoff marker exists it is the answer,
// and discovery is not consulted again.
func TestDisposableServerPhaseCheckUpdateReportsPendingMarker(t *testing.T) {
	manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v1", "base-v1")
	fixture := newDisposableFixture(t, manifest, false)
	updatesRoot := t.TempDir()
	marker := PendingMarker{
		OperationID:       "embedded-disposable-pending-op",
		ReleaseID:         manifest.ReleaseID,
		ManifestDigest:    disposableManifestDigest(t, manifest),
		ReleaseSequence:   manifest.ReleaseSequence,
		PreviousReleaseID: "release-disposable-previous",
	}
	if err := WritePendingMarker(updatesRoot, marker); err != nil {
		t.Fatalf("WritePendingMarker: %v", err)
	}
	service := disposableService(t, Deps{
		PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: fixture.manifestURL(),
		HTTPClient: fixture.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
		BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
	})

	check, err := service.CheckUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if check.ActiveOperation == nil {
		t.Fatalf("check = %+v, want the pending operation", check)
	}
	if check.ActiveOperation.Step != "pending_backup" ||
		check.ActiveOperation.OperationID != marker.OperationID ||
		check.ActiveOperation.TargetReleaseID != marker.ReleaseID {
		t.Fatalf("active operation = %+v, want the pending marker %+v", check.ActiveOperation, marker)
	}
	if check.Candidate != nil || check.Status == "update_available" {
		t.Fatalf("check = status %q candidate %+v, want no newly resolved candidate", check.Status, check.Candidate)
	}
	if check.CheckedAt != disposableClockInstant {
		t.Fatalf("checked at = %s, want the injected clock reading %s", check.CheckedAt, disposableClockInstant)
	}
	if requests := fixture.requestCount(); requests != 0 {
		t.Fatalf("a pending operation still consulted the update source %d time(s)", requests)
	}

	status, ok, err := service.GetUpdateStatus(context.Background())
	if err != nil {
		t.Fatalf("GetUpdateStatus: %v", err)
	}
	if !ok || status.Step != "pending_backup" || status.OperationID != marker.OperationID || status.TargetReleaseID != marker.ReleaseID {
		t.Fatalf("GetUpdateStatus = %+v, ok=%v; want the pending marker %+v", status, ok, marker)
	}
}
func TestDisposableServerPhaseStatusReadsTerminalJournalAfterRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	first, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := first.Create(journal.Record{
		OperationID: "embedded-terminal-op", Channel: "embedded",
		TargetReleaseID: "release-terminal", TargetVersion: "2.0.0",
		TargetSchemaVersion: "108", TargetReleaseSequence: 9,
	})
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	for _, step := range []journal.Step{
		journal.StepFetched, journal.StepStaged, journal.StepMaintenanceEntered,
		journal.StepBackupVerified, journal.StepPendingMigration,
		journal.StepMigrationSucceeded, journal.StepPromoted,
	} {
		record, err = first.Advance(record.OperationID, record.Version, step, "", "fixture transition", nil)
		if err != nil {
			_ = first.Close()
			t.Fatalf("advance journal to %s: %v", step, err)
		}
	}
	record, err = first.Advance(record.OperationID, record.Version, journal.StepFenceClosurePending, "", "settling fence", func(r *journal.Record) {
		r.FenceClosureTarget = journal.StepSucceeded
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("advance journal to fence closure pending: %v", err)
	}
	record, err = first.Advance(record.OperationID, record.Version, journal.StepSucceeded, "", "fence closed", func(r *journal.Record) {
		r.FenceClosure = journal.FenceClosureClosed
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("advance journal to succeeded: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A new server process has no in-memory lastStatus; it must observe the
	// bootstrap's durable terminal checkpoint from the updates volume.
	service := &Service{updatesRoot: root}
	status, found, err := service.GetUpdateStatus(context.Background())
	if err != nil || !found {
		t.Fatalf("GetUpdateStatus after restart: found=%v err=%v", found, err)
	}
	if status.Step != "succeeded" || status.OperationID != record.OperationID || status.TargetReleaseID != record.TargetReleaseID || status.TargetVersion == nil || *status.TargetVersion != record.TargetVersion || status.TargetSchemaVersion == nil || *status.TargetSchemaVersion != record.TargetSchemaVersion {
		t.Fatalf("terminal status = %+v, want the durable succeeded release", status)
	}
}

// TestDisposableServerPhaseAdmitUpdateRefusesConcurrentOperation covers
// UPDATE_LOCKED: the server phase installs exactly one release before it exits,
// so a second admission while one is in flight is refused.
func TestDisposableServerPhaseAdmitUpdateRefusesConcurrentOperation(t *testing.T) {
	manifest, publicKey := disposableSignedManifest(t, "release-disposable-target", 7, "embedded-bootstrap-v1", "base-v1")
	blocking := newDisposableFixture(t, manifest, true)
	updatesRoot := t.TempDir()
	service := disposableService(t, Deps{
		PublicKey: publicKey, UpdatesRoot: updatesRoot, ManifestURL: blocking.manifestURL(),
		HTTPClient: blocking.client(), BuildReleaseID: "running-build", BuildVersion: "running-version",
		BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
	})
	input := AdmitInput{TargetReleaseID: manifest.ReleaseID, ManifestDigest: disposableManifestDigest(t, manifest)}

	status, created, err := service.AdmitUpdate(context.Background(), input, "disposable-concurrent-first")
	if err != nil || !created {
		t.Fatalf("first admission = %+v, created=%v, err=%v; want an accepted operation", status, created, err)
	}
	blocking.awaitPayloadRequest(t)

	second, created, err := service.AdmitUpdate(context.Background(), input, "disposable-concurrent-second")
	requireDisposableBusinessError(t, err, http.StatusConflict, "UPDATE_LOCKED")
	if created {
		t.Fatal("a second operation was admitted while the first was in flight")
	}
	if second.OperationID != "" || second.Step != "" {
		t.Fatalf("refused admission reported status %+v, want none", second)
	}
}
