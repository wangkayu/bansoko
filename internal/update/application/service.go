// Package application implements disposable signed-release staging and the
// pending-marker handoff to the bootstrap.
package application

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/apperrors"
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/update/journal"
)

const (
	Channel         = "embedded"
	PayloadFileName = "embedded-update-v1.tar.gz"
	publicKeySize   = ed25519.PublicKeySize
)

var ErrSelfReplay = errors.New("target release matches the running binary")

// BackupExecutor is the PostgreSQL backup/restore adapter contract consumed by PID1 bootstrap.
type BackupExecutor interface {
	Dump(ctx context.Context, env []string, outPath string) error
	Restore(ctx context.Context, env []string, dumpPath, targetDatabase string) error
}

// Deps contains only inputs used to verify and stage a signed release. DB is
// used for a read-only anti-replay floor lookup; the updater never owns writes.
type Deps struct {
	DB                        *sql.DB
	PublicKey                 ed25519.PublicKey
	UpdatesRoot               string
	ManifestURL               string
	ChannelURL                string
	PackageRoot               string
	Production                bool
	HTTPClient                *http.Client
	BuildReleaseID            string
	BuildVersion              string
	BuildCompatibilityVersion string
	BuildBootstrapVersion     string
	Clock                     func() time.Time
	UpdateMode                string
	// Diagnostics and Logger are optional: the update-index reachability
	// condition is recorded only when a recorder is wired.
	Diagnostics observabilityport.Recorder
	Logger      observabilityport.RuntimeLogger
}

type Service struct {
	db                                                                 *sql.DB
	publicKey                                                          ed25519.PublicKey
	updatesRoot, manifestURL                                           string
	source                                                             *UpdateHTTPSource
	channel                                                            *channelResolver
	packageRoot                                                        string
	packageConfig                                                      UpdateHTTPSourceConfig
	buildID, buildVersion, buildCompatibilityVersion, bootstrapVersion string
	clock                                                              func() time.Time
	mu                                                                 sync.Mutex
	chainRunning                                                       bool
	lastStatus                                                         Status
	done                                                               chan struct{}
	doneOnce                                                           sync.Once
	diagnostics                                                        observabilityport.Recorder
	logger                                                             observabilityport.RuntimeLogger
}

func NewService(deps Deps) (*Service, error) {
	if strings.TrimSpace(deps.UpdateMode) != "embedded" {
		return nil, fmt.Errorf("embedded update service requires BANSOKO_UPDATE_MODE=embedded (current mode: %q)", deps.UpdateMode)
	}
	if len(deps.PublicKey) != publicKeySize {
		return nil, errors.New("embedded update service requires a valid ed25519 public key")
	}
	if strings.TrimSpace(deps.UpdatesRoot) == "" {
		return nil, errors.New("embedded update service requires the updates root")
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	config := UpdateHTTPSourceConfig{Production: deps.Production, HTTPClient: client}
	manifestURL, channelURL, packageRoot := strings.TrimSpace(deps.ManifestURL), strings.TrimSpace(deps.ChannelURL), strings.TrimSpace(deps.PackageRoot)
	if manifestURL != "" && channelURL != "" {
		return nil, fmt.Errorf("%w: %s and %s are mutually exclusive", ErrUpdateSourceURL, EnvManifestURL, EnvChannelURL)
	}
	var source *UpdateHTTPSource
	if manifestURL != "" {
		exact := config
		exact.ManifestURL = manifestURL
		var err error
		source, err = NewUpdateHTTPSource(exact)
		if err != nil {
			return nil, err
		}
	}
	var resolver *channelResolver
	if channelURL != "" {
		if packageRoot == "" {
			return nil, fmt.Errorf("%w: %s is required when %s is configured", ErrChannelNotConfigured, EnvPackageRoot, EnvChannelURL)
		}
		var err error
		resolver, err = newChannelResolver(channelURL, packageRoot, config, deps.PublicKey)
		if err != nil {
			return nil, err
		}
	}
	clock := deps.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{db: deps.DB, publicKey: deps.PublicKey, updatesRoot: deps.UpdatesRoot, manifestURL: manifestURL, source: source, channel: resolver, packageRoot: packageRoot, packageConfig: config, buildID: strings.TrimSpace(deps.BuildReleaseID), buildVersion: strings.TrimSpace(deps.BuildVersion), buildCompatibilityVersion: strings.TrimSpace(deps.BuildCompatibilityVersion), bootstrapVersion: deps.BuildBootstrapVersion, clock: clock, done: make(chan struct{}), diagnostics: deps.Diagnostics, logger: deps.Logger}, nil
}

func (s *Service) ExitRequested() <-chan struct{} { return s.done }
func (s *Service) requestExit()                   { s.doneOnce.Do(func() { close(s.done) }) }
func (s *Service) Running() bool                  { s.mu.Lock(); defer s.mu.Unlock(); return s.chainRunning }
func (s *Service) configured() bool               { return s != nil && (s.source != nil || s.channel != nil) }

func (s *Service) resolve(ctx context.Context) (resolution, error) {
	if s.channel != nil {
		return s.channel.resolve(ctx)
	}
	if s.source == nil {
		return resolution{}, &apperrors.Business{Status: 503, Code: "SERVICE_NOT_CONFIGURED", Message: "update manifest source is not configured."}
	}
	manifest, digest, raw, err := FetchAndVerifyManifestFromSourceRaw(ctx, s.source, s.publicKey)
	if err != nil {
		return resolution{}, err
	}
	return resolution{manifest: manifest, digest: digest, raw: raw, manifestURL: s.source.ManifestURL(), source: s.source}, nil
}

func (s *Service) Admit(ctx context.Context, inputManifestURL, idempotencyKey string) (Status, bool, error) {
	if s.channel != nil && inputManifestURL != "" {
		return Status{}, false, &apperrors.Business{Status: 409, Code: "UPDATE_SOURCE_MISMATCH", Message: "This deployment resolves its release through the configured update channel."}
	}
	if s.source != nil && inputManifestURL != "" && inputManifestURL != s.source.ManifestURL() {
		return Status{}, false, &apperrors.Business{Status: 409, Code: "UPDATE_SOURCE_MISMATCH", Message: "The supplied manifest source is not the configured update source."}
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return Status{}, false, &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key header is required."}
	}
	return s.admit(ctx, idempotencyKey, "", "")
}

func (s *Service) admit(ctx context.Context, idempotencyKey, targetReleaseID, targetDigest string) (Status, bool, error) {
	if !s.configured() {
		return Status{}, false, &apperrors.Business{Status: 503, Code: "SERVICE_NOT_CONFIGURED", Message: "update manifest source is not configured."}
	}
	s.mu.Lock()
	if s.chainRunning {
		s.mu.Unlock()
		return Status{}, false, &apperrors.Business{Status: 409, Code: "UPDATE_LOCKED", Message: "An update is already in progress."}
	}
	s.chainRunning = true
	s.mu.Unlock()
	fail := func(err error) (Status, bool, error) {
		s.mu.Lock()
		s.chainRunning = false
		detail := err.Error()
		s.lastStatus = Status{Step: "failed", LastError: &detail}
		s.mu.Unlock()
		return Status{}, false, err
	}
	resolved, err := s.resolve(ctx)
	if err != nil {
		return fail(err)
	}
	manifest, digest, manifestURL := resolved.manifest, resolved.digest, resolved.manifestURL
	if (targetReleaseID != "" || targetDigest != "") && (targetReleaseID != manifest.ReleaseID || targetDigest != digest) {
		return fail(&apperrors.Business{Status: 409, Code: "UPDATE_CANDIDATE_STALE", Message: "The checked update candidate is no longer the signed manifest target."})
	}
	if s.buildID != "" && manifest.ReleaseID == s.buildID {
		return fail(&apperrors.Business{Status: 422, Code: "UPDATE_TARGET_CURRENT", Message: ErrSelfReplay.Error()})
	}
	if required, e := manifest.EffectiveMinImageVersion(); e != nil {
		return fail(fmt.Errorf("embedded update image compatibility rejected: %w", e))
	} else if required != "" && s.buildCompatibilityVersion != "" {
		compatible, ce := imageCompatibilityAtLeast(s.buildCompatibilityVersion, required)
		if ce != nil {
			return fail(&apperrors.Business{Status: 422, Code: "IMAGE_COMPATIBILITY_INVALID", Message: ce.Error()})
		}
		if !compatible {
			return fail(&apperrors.ImageCompatibilityError{Required: required, Current: s.buildCompatibilityVersion})
		}
	}
	requiredBootstrap, e := manifest.EffectiveBootstrapMinVersion()
	if e != nil {
		return fail(fmt.Errorf("embedded update bootstrap compatibility rejected: %w", e))
	}
	bootstrap := evaluateBootstrapCompatibility(s.bootstrapVersion, requiredBootstrap)
	if bootstrap.ManifestInvalid != nil {
		return fail(&apperrors.Business{Status: 422, Code: "BOOTSTRAP_COMPATIBILITY_INVALID", Message: bootstrap.ManifestInvalid.Error()})
	}
	if !bootstrap.Satisfied {
		return fail(&apperrors.BootstrapTooOldError{Required: bootstrap.Required, Current: bootstrap.Current})
	}
	if manifest.EmbeddedUpdate == nil {
		return fail(errors.New("signed manifest has no embedded update segment"))
	}
	if manifest.ReleaseSequence < 1 {
		return fail(errors.New("signed update target has no positive release sequence"))
	}
	if s.db != nil {
		var floor int64
		if e = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM embedded_update_locks").Scan(&floor); e != nil {
			return fail(fmt.Errorf("read update release sequence floor: %w", e))
		}
		if manifest.ReleaseSequence <= floor {
			return fail(&apperrors.Business{Status: 422, Code: "UPDATE_TARGET_STALE", Message: "The signed update target does not advance beyond an already accepted release."})
		}
	}
	opID := "embedded-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := s.clock().UTC()
	version, schema := manifest.Version, manifest.TargetMigrationVersion
	status := Status{OperationID: opID, Channel: Channel, Step: "accepted", TargetReleaseID: manifest.ReleaseID, TargetVersion: &version, TargetSchemaVersion: &schema, CreatedAt: now, UpdatedAt: now}
	s.mu.Lock()
	s.lastStatus = status
	s.mu.Unlock()
	go s.runChain(manifest, digest, manifestURL, opID)
	return status, true, nil
}
func persistSignedManifest(root string, manifest hardeningdomain.ReleaseManifest) error {
	if err := ensureOperationDir(root, root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(root, filepath.Join(root, SignedManifestEntryPath), data, 0o600)
}

func (s *Service) setStatus(status Status) { s.mu.Lock(); s.lastStatus = status; s.mu.Unlock() }
func (s *Service) finishChain()            { s.mu.Lock(); s.chainRunning = false; s.mu.Unlock() }
func (s *Service) packageSourceFor(manifestURL string) (*UpdateHTTPSource, error) {
	if strings.TrimSpace(manifestURL) == "" {
		return nil, fmt.Errorf("%w: manifest URL is required", ErrUpdateSourceURL)
	}
	if s.source != nil && manifestURL == s.source.ManifestURL() {
		return s.source, nil
	}
	return ResolvePackageSource(manifestURL, s.packageRoot, s.packageConfig)
}

func imageCompatibilityAtLeast(current, required string) (bool, error) {
	a, e := parseBaseCompatibilityVersion(current)
	if e != nil {
		return false, e
	}
	b, e := parseBaseCompatibilityVersion(required)
	if e != nil {
		return false, e
	}
	return a >= b, nil
}
func parseBaseCompatibilityVersion(value string) (int64, error) {
	value = strings.TrimSpace(value)
	const prefix = "base-v"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, fmt.Errorf("invalid base image compatibility version %q; expected base-v<N>", value)
	}
	number, e := strconv.ParseInt(value[len(prefix):], 10, 64)
	if e != nil || number < 0 {
		return 0, fmt.Errorf("invalid base image compatibility version %q; expected base-v<N>", value)
	}
	return number, nil
}

type bootstrapCompatibility struct {
	Required, Current string
	Satisfied         bool
	ManifestInvalid   error
}

func evaluateBootstrapCompatibility(current, required string) bootstrapCompatibility {
	result := bootstrapCompatibility{Required: required, Current: current}
	requiredNumber, requiredErr := parseEmbeddedBootstrapVersion(required)
	if requiredErr != nil && required != "" {
		result.ManifestInvalid = requiredErr
		return result
	}
	currentNumber, currentErr := parseEmbeddedBootstrapVersion(current)
	result.Satisfied = required == "" || (currentErr == nil && currentNumber >= requiredNumber)
	return result
}
func (c bootstrapCompatibility) Reason() string {
	if _, err := parseEmbeddedBootstrapVersion(c.Current); err != nil {
		return fmt.Sprintf("This candidate requires %s; this instance does not report a usable bootstrap version. Switch to a base image that carries the required bootstrap.", c.Required)
	}
	return fmt.Sprintf("This candidate requires %s; this instance runs %s. Switch to a base image that carries the required bootstrap.", c.Required, c.Current)
}
func parseEmbeddedBootstrapVersion(value string) (int64, error) {
	const prefix = "embedded-bootstrap-v"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, fmt.Errorf("invalid bootstrap version %q; expected embedded-bootstrap-v<N>", value)
	}
	n, err := strconv.ParseInt(value[len(prefix):], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid bootstrap version %q; expected embedded-bootstrap-v<N>", value)
	}
	return n, nil
}

func (s *Service) AdmitUpdate(ctx context.Context, input AdmitInput, idempotencyKey string) (Status, bool, error) {
	if strings.TrimSpace(input.TargetReleaseID) == "" || strings.TrimSpace(input.ManifestDigest) == "" {
		return Status{}, false, &apperrors.BadRequest{Code: "UPDATE_CANDIDATE_REQUIRED", Message: "target_release_id and manifest_digest are required."}
	}
	return s.admit(ctx, idempotencyKey, input.TargetReleaseID, input.ManifestDigest)
}

// updateDiagnosticTarget is the single target of the update-index condition.
const updateDiagnosticTarget = "update"

// CheckUpdate reads the update index. An unreachable index repeats into one
// open diagnostic; a successful index fetch closes it. Verification refusals
// carry no outbound failure and are not reachability problems.
func (s *Service) CheckUpdate(ctx context.Context) (Check, error) {
	result, err := s.checkUpdate(ctx)
	s.recordIndexReachability(ctx, result, err)
	return result, err
}

func (s *Service) recordIndexReachability(ctx context.Context, result Check, err error) {
	if s.diagnostics == nil {
		return
	}
	logger := s.logger
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	if err == nil {
		// Only a completed check proves the index was reachable.
		if result.Status == "" || result.Status == "check_failed" {
			return
		}
		observabilityport.DiagnosticSuccessAfterCommit(s.diagnostics, logger, observabilityport.DiagnosticSuccessRequest{Kind: observabilityport.KindUpdate, TargetID: updateDiagnosticTarget})(ctx)
		return
	}
	failure, ok := outbound.FailureOf(err)
	if !ok {
		return
	}
	observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, logger, observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindUpdate, Variant: observabilityport.VariantUnreachable},
		Severity:  observabilityport.SeverityWarning, TargetID: updateDiagnosticTarget, TargetName: updateDiagnosticTarget, Failure: failure,
	})(ctx)
}

func (s *Service) checkUpdate(ctx context.Context) (Check, error) {
	checked := s.clock().UTC()
	result := Check{CheckedAt: checked, Current: Current{ReleaseID: s.buildID, Version: s.buildVersion, ImageCompatibility: s.buildCompatibilityVersion, ReleaseNotes: s.currentReleaseNotes()}, Compatibility: Compatibility{Kind: "incompatible", Reason: "No compatible candidate has been checked."}}
	if marker, ok, err := s.readPendingStatus(); err != nil || ok {
		result.ActiveOperation = nil
		if ok {
			result.ActiveOperation = &marker
		}
		return result, err
	}
	if !s.configured() {
		return result, &apperrors.Business{Status: 503, Code: "SERVICE_NOT_CONFIGURED", Message: "update manifest source is not configured."}
	}
	resolved, err := s.resolve(ctx)
	if err != nil {
		result.Status = "check_failed"
		return result, err
	}
	manifest, digest := resolved.manifest, resolved.digest
	candidate := &Candidate{ReleaseID: manifest.ReleaseID, Version: manifest.Version, ReleaseSequence: manifest.ReleaseSequence, PublishedAt: manifest.PublishedAt, TargetMigrationVersion: manifest.TargetMigrationVersion, RequiredDowntime: manifest.RequiredDowntime, BackupRequired: manifest.BackupRequired, RollbackClass: string(manifest.Rollback), ManifestDigest: digest}
	if manifest.EmbeddedUpdate != nil {
		candidate.PayloadSize = manifest.EmbeddedUpdate.PayloadSize
	}
	candidate.ReleaseNotes = hardeningdomain.ReleaseNotesNewerThan(s.buildVersion, manifest.ReleaseNotes)
	result.Candidate = candidate
	required, e := manifest.EffectiveMinImageVersion()
	if e != nil {
		result.Status = "check_failed"
		return result, e
	}
	result.Compatibility.MinImageVersion = required
	compatible := true
	if required != "" && s.buildCompatibilityVersion != "" {
		compatible, e = imageCompatibilityAtLeast(s.buildCompatibilityVersion, required)
		if e != nil {
			result.Status = "check_failed"
			return result, &apperrors.Business{Status: 422, Code: "IMAGE_COMPATIBILITY_INVALID", Message: e.Error()}
		}
	}
	requiredBootstrap, e := manifest.EffectiveBootstrapMinVersion()
	if e != nil {
		result.Status = "check_failed"
		return result, e
	}
	bootstrap := evaluateBootstrapCompatibility(s.bootstrapVersion, requiredBootstrap)
	if bootstrap.ManifestInvalid != nil {
		result.Status = "check_failed"
		return result, &apperrors.Business{Status: 422, Code: "BOOTSTRAP_COMPATIBILITY_INVALID", Message: bootstrap.ManifestInvalid.Error()}
	}
	switch {
	case !compatible || !bootstrap.Satisfied:
		result.Status = "base_image_required"
		result.Compatibility.Kind = "base_image_required"
		if !bootstrap.Satisfied {
			result.Compatibility.Reason = bootstrap.Reason()
		} else {
			result.Compatibility.Reason = "A newer base image is required."
		}
	case manifest.ReleaseID == s.buildID:
		result.Status = "up_to_date"
		result.Candidate = nil
		result.Compatibility.Kind = "overlay"
		result.Compatibility.Reason = "The current release is up to date."
	default:
		result.Status = "update_available"
		result.Compatibility.Kind = "overlay"
		result.Compatibility.Reason = "The candidate can be applied as an embedded overlay."
	}
	s.mu.Lock()
	if s.chainRunning {
		active := s.lastStatus
		result.ActiveOperation = &active
	}
	s.mu.Unlock()
	return result, nil
}
func (s *Service) GetUpdateStatus(ctx context.Context) (Status, bool, error) {
	if status, ok, err := s.readPendingStatus(); err != nil || ok {
		return status, ok, err
	}
	// A previous terminal journal must not hide this process's new admission.
	s.mu.Lock()
	if s.lastStatus.OperationID != "" {
		status := s.lastStatus
		s.mu.Unlock()
		return status, true, nil
	}
	s.mu.Unlock()
	record, found, err := journal.Snapshot(filepath.Join(s.updatesRoot, "journal.json"))
	if err != nil {
		return Status{}, false, fmt.Errorf("read durable embedded update status: %w", err)
	}
	if found {
		var lastError *string
		if record.LastError != "" {
			value := record.LastError
			lastError = &value
		}
		version, schema := record.TargetVersion, record.TargetSchemaVersion
		return Status{OperationID: record.OperationID, Channel: record.Channel, Step: string(record.Step), TargetReleaseID: record.TargetReleaseID, TargetVersion: &version, TargetSchemaVersion: &schema, LastError: lastError, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}, true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastStatus.OperationID != "" {
		return s.lastStatus, true, nil
	}
	return Status{}, false, nil
}
func (s *Service) GetEmbeddedStatus(ctx context.Context) (Status, bool, error) {
	return s.GetUpdateStatus(ctx)
}
func (s *Service) readPendingStatus() (Status, bool, error) {
	path := filepath.Join(s.updatesRoot, PendingMarkerFileName)
	marker, err := ReadPendingMarker(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("read pending update marker: %w", err)
	}
	return Status{OperationID: marker.OperationID, Channel: Channel, Step: "pending_backup", TargetReleaseID: marker.ReleaseID}, true, nil
}
