// Package application contains the Artifact owner handler.
// Artifact inspection records downloaded-file facts; FileOperation owns the
// later naming and filesystem effect.
package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	terminaldomain "bansoko/internal/acquisition/terminal/domain"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/download/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/events"
	"bansoko/internal/filekinds"
	librarydomain "bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	"bansoko/internal/redact"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"

	"github.com/google/uuid"
)

const (
	defaultRetryAfter = 5 * time.Minute
	maxPathLength     = 4096
	maxReasonLength   = 2048
	maxFingerprintLen = 256
	// maxInspectionFiles is the torrent manifest bound: inspection accepts
	// every file a parsed .torrent may list.
	maxInspectionFiles = catalog.MaxTorrentManifestFiles
	// maxFrozenInputBytes is the stored bound of download_tasks.frozen_input
	// (pg_column_size <= 1048576); a stored input is never refused here.
	maxFrozenInputBytes = 1 << 20
	maxMediaFactLen     = 32

	// inspectBudgetExhaustedCode is the typed terminal failure when the
	// business attempt budget (max attempts or frozen wall) is spent.
	inspectBudgetExhaustedCode = "ARTIFACT_INSPECT_BUDGET_EXHAUSTED"
)

var (
	ErrInvalidKind             = errors.New("artifact handler received an unsupported work item kind")
	ErrInvalidClaim            = errors.New("artifact inspection claim is invalid")
	ErrArtifactNotFound        = errors.New("artifact not found")
	ErrArtifactOwnerMismatch   = errors.New("artifact inspection owner does not match the artifact")
	ErrArtifactVersionConflict = errors.New("artifact version changed before inspection completed")
	ErrDownloadNotCompleted    = errors.New("download task is not completed")
	ErrArtifactPathMissing     = errors.New("completed download has no artifact path")
	ErrArtifactFactsInvalid    = errors.New("artifact inspector returned invalid facts")
	ErrArtifactSizeMismatch    = errors.New("artifact inspected size does not match the frozen release file size")
	ErrArtifactPathUnsafe      = errors.New("artifact path is unsafe or contains credentials")
	ErrProviderFilesIncomplete = errors.New("provider file snapshot is incomplete")
	ErrFrozenParserEvidence    = errors.New("media fact snapshot parser evidence is unusable")
)

// ArtifactInspector is the small filesystem port needed by this owner.
// artifact/adapter.OSArtifactInspector is the production implementation; the
// port remains here so the application owner does not depend on its adapter.
type ArtifactInspector interface {
	Inspect(context.Context, string) (domain.ArtifactFacts, error)
}

// MediaProber reads the execution-time media facts (resolution, codecs,
// container) from a local video file. A failure never fails the inspection:
// the facts stay empty and the handler logs a bounded diagnostic.
// artifact/adapter.OSMediaProber (MediaInfo CLI) is the production
// implementation.
type MediaProber interface {
	Probe(context.Context, string) (domain.MediaFacts, error)
}

// VerifiedArtifactRequest identifies the artifact whose verified files should
// be prepared by a downstream owner.
type VerifiedArtifactRequest struct {
	ArtifactID string
}

// Service implements the typed artifact_inspect WorkItem handler.
type Service struct {
	db         *sql.DB
	inspector  ArtifactInspector
	now        func() time.Time
	eventHub   *events.Hub
	mediaFacts facts.MediaRepository
	// prober and logger are optional: without a prober the media facts stay
	// empty, which is the documented fail-closed state.
	prober MediaProber
	logger observabilityport.RuntimeLogger
	// settleDelay is how long the handler waits before re-inspecting the
	// primary file to prove the artifact is stable. Download clients report
	// completion at progress>=0.999, which can race the final piece flush;
	// freezing an unstable fingerprint would poison downstream FileOperation
	// with an unrecoverable SOURCE_CHANGED. Zero disables the settle check.
	settleDelay time.Duration
}

const (
	// defaultSettleDelay matches the real-world window between qB reporting
	// completion and the file being fully flushed on disk.
	defaultSettleDelay = 2 * time.Second
	// unstableRetryAfter is the retry backoff when the primary file changed
	// during inspection: the file needs a moment to settle, not five minutes.
	unstableRetryAfter = 10 * time.Second
)

// NewService constructs the Artifact owner service. The optional clock is used
// by tests and keeps persisted observation timestamps deterministic.
func NewService(db *sql.DB, inspector ArtifactInspector, clocks ...func() time.Time) (*Service, error) {
	if db == nil {
		return nil, errors.New("artifact service database is nil")
	}
	if inspector == nil {
		return nil, errors.New("artifact service inspector is nil")
	}
	now := func() time.Time { return time.Now().UTC() }
	if len(clocks) > 0 && clocks[0] != nil {
		now = clocks[0]
	}
	// The media facts store writes each file's identity into the Release
	// snapshot; SetMediaFactsStore may replace it.
	return &Service{db: db, inspector: inspector, now: now, settleDelay: defaultSettleDelay, mediaFacts: facts.NewStore(db)}, nil
}

// SetMediaFactsStore replaces the per-file facts owner (NewService wires the
// PostgreSQL store over the service database).
func (s *Service) SetMediaFactsStore(store facts.MediaRepository) {
	if s == nil {
		return
	}
	s.mediaFacts = store
}

// SetMediaProber wires the optional media fact reader and the logger that
// receives its bounded failure diagnostics (nil logger discards them).
func (s *Service) SetMediaProber(prober MediaProber, logger observabilityport.RuntimeLogger) {
	if s == nil {
		return
	}
	s.prober = prober
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	s.logger = logger
}

// MediaProberReady reports whether execution-time media facts are wired.
// Composition tests use it to prove the production graph carries a prober.
func (s *Service) MediaProberReady() bool {
	return s != nil && s.prober != nil
}

// SetSettleDelay overrides the primary-file settle window. Tests use zero to
// skip the wait or a small value to exercise the unstable retry path; the
// production default is defaultSettleDelay.
func (s *Service) SetSettleDelay(delay time.Duration) {
	if s == nil {
		return
	}
	s.settleDelay = delay
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

var _ worker.Handler = (*Service)(nil)

// Handle accepts only artifact_inspect. It performs filesystem inspection
// before returning an owner writer; the writer re-locks and validates the
// Artifact inside WorkItem's fenced transaction.
func (s *Service) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	outcome, err := s.handle(ctx, claim)
	if err != nil || s == nil {
		return outcome, err
	}
	// Inspection is a read-only owner step: it freezes observed facts but
	// performs no mutating external effect, so every one-shot outcome settles
	// its admission lease as no_external_effect. Assigned before the optional
	// event-hub wrap so the disposition never depends on hub wiring.
	if claim.OneShot != nil {
		outcome.Disposition = workitems.DispositionNoExternalEffect
	}
	if s.eventHub == nil {
		return outcome, nil
	}
	outcome.AfterCommit = s.eventHub.AfterCommitFor(events.TargetRSS, claim.ManagedAnimeID, outcome.AfterCommit)
	return outcome, nil
}

func (s *Service) handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Kind != workitems.KindArtifactInspect {
		return worker.Outcome{}, fmt.Errorf("%w: %q", ErrInvalidKind, claim.Kind)
	}
	if s == nil || s.db == nil || s.inspector == nil {
		return worker.Outcome{}, errors.New("artifact service is not configured")
	}
	if ctx == nil {
		return worker.Outcome{}, errors.New("artifact handler context is nil")
	}
	if claim.Owner.Type != workitems.OwnerArtifact || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, ErrInvalidClaim
	}

	snapshot, loadErr := s.loadArtifactSnapshot(ctx, claim.Owner.ID)
	if loadErr != nil {
		return worker.Outcome{}, loadErr
	}

	// A replay after another worker already committed verification must not
	// parse its frozen input or inspect/write the filesystem again.
	if snapshot.state == "verified" {
		return worker.AwaitRecovery("ARTIFACT_VERIFIED_WORK_LIVE"), nil
	}

	input, inputErr := decodeInspectInput(claim.InputSnapshot)
	if inputErr != nil {
		return s.failInspection(ctx, claim, "artifact_input_invalid", "artifact inspection input is invalid")
	}
	snapshot, err := s.resolveArtifactSnapshot(ctx, claim.Owner.ID, input)
	if err != nil {
		if errors.Is(err, ErrArtifactOwnerMismatch) && snapshot.id != "" {
			return s.failInspection(ctx, claim, "artifact_owner_mismatch", safeFailureReason(err))
		}
		return worker.Outcome{}, err
	}
	if err := s.validateInput(snapshot, input); err != nil {
		return s.failInspection(ctx, claim, "artifact_owner_mismatch", safeFailureReason(err))
	}

	if snapshot.taskState != "completed" {
		if snapshot.taskState == "failed" || snapshot.taskState == "canceled" {
			return s.failInspection(ctx, claim, "download_not_completed", "the download task did not complete")
		}
		return s.retryInspection(ctx, claim, "download_not_completed", "the download task is not complete yet", defaultRetryAfter.Seconds())
	}

	correlation, correlationErr := decodeProviderCorrelation(snapshot.providerCorrelation)
	if correlationErr != nil {
		return s.failInspection(ctx, claim, "provider_correlation_invalid", "the download provider correlation is invalid")
	}
	input, rootErr := applyProviderPathSnapshot(input, correlation)
	if rootErr != nil {
		return s.failInspection(ctx, claim, "artifact_path_invalid", safeFailureReason(rootErr))
	}

	// The file list parsed from the .torrent file (stored in frozen_input)
	// is the authoritative source.  qB's /torrents/files API is incomplete
	// for BitTorrent v2 torrents and is not used for file enumeration.
	frozen, frozenErr := decodeFrozenInput(snapshot.frozenInput)
	if frozenErr != nil {
		return s.failInspection(ctx, claim, "frozen_input_invalid", "the frozen download input is invalid")
	}
	targets, pathErr := resolveArtifactFilesFromFrozenInput(input, frozen, correlation)
	if pathErr != nil {
		code := "artifact_path_invalid"
		if errors.Is(pathErr, ErrProviderFilesIncomplete) {
			code = "provider_files_incomplete"
		}
		return s.failInspection(ctx, claim, code, safeFailureReason(pathErr))
	}
	if len(targets) == 0 {
		return s.failInspection(ctx, claim, "provider_files_incomplete", "frozen input has no release files")
	}

	// Gate: everything above is a DB-only check and costs only
	// the claim; from here the run reads the filesystem and costs one business
	// attempt. The ticket bounds the I/O by its attempt_until.
	ticket, gateErr := (&workitems.Store{DB: s.db}).StartStageAttempt(ctx, claim, "artifact_inspect:"+uuid.NewString())
	if errors.Is(gateErr, workitems.ErrStageBudgetExhausted) {
		return s.failInspection(ctx, claim, inspectBudgetExhaustedCode, "the inspection attempt budget is exhausted")
	}
	if gateErr != nil {
		return worker.Outcome{}, gateErr
	}
	inspectCtx, cancelInspect := context.WithDeadline(ctx, ticket.AttemptUntil)
	defer cancelInspect()

	facts := make([]domain.ArtifactFacts, len(targets))
	var firstInspectErr error
	var firstInspectTerminal bool
	for index, target := range targets {
		inspected, inspectErr := s.inspector.Inspect(inspectCtx, target.path)
		if inspectErr != nil {
			if firstInspectErr == nil {
				firstInspectErr = inspectErr
				firstInspectTerminal = terminalInspectionError(inspectErr)
			}
			continue
		}
		facts[index] = inspected
	}
	if firstInspectErr != nil {
		reasonCode := "artifact_inspect_failed"
		reason := redactInspectionError(firstInspectErr)
		if firstInspectTerminal {
			return s.failInspection(ctx, claim, reasonCode, reason)
		}
		return s.retryInspection(ctx, claim, reasonCode, reason, defaultRetryAfter.Seconds())
	}

	// A download client reports completion at progress>=0.999, which can race
	// the final piece flush: the first stat may observe a primary file that
	// is still being written. Re-stat the primary file after a short settle
	// window and retry when its size or modification time changed, instead
	// of freezing an unstable observation (evidence: E01 SOURCE_CHANGED was
	// unrecoverable because frozen inputs are immutable).
	if s.settleDelay > 0 {
		primaryIndex := -1
		for index, target := range targets {
			if target.primary {
				primaryIndex = index
				break
			}
		}
		if primaryIndex < 0 {
			primaryIndex = 0
		}
		time.Sleep(s.settleDelay)
		rechecked, recheckErr := s.inspector.Inspect(inspectCtx, targets[primaryIndex].path)
		if recheckErr != nil || !samePathObservation(rechecked, facts[primaryIndex]) {
			reasonCode := "artifact_unstable"
			reason := "artifact file changed during inspection; retrying after settle"
			if recheckErr != nil {
				reason = redactInspectionError(recheckErr)
			}
			return s.retryInspection(ctx, claim, reasonCode, reason, unstableRetryAfter.Seconds())
		}
	}

	s.probeMediaFacts(inspectCtx, snapshot.id, targets, facts)

	plan, planErr := buildInspectionPlanForFiles(snapshot, input, targets, facts, s.currentTime())
	if planErr != nil {
		return s.failInspection(ctx, claim, "artifact_facts_invalid", safeFailureReason(planErr))
	}
	return s.commitInspection(ctx, claim, plan)
}

// ArtifactInspectHandler restricts registration to artifact_inspect when a
// composition root wants a typed wrapper instead of registering Service.
type ArtifactInspectHandler struct{ service *Service }

func (h ArtifactInspectHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if h.service == nil {
		return worker.Outcome{}, errors.New("artifact inspection service is nil")
	}
	if claim.Kind != workitems.KindArtifactInspect {
		return worker.Outcome{}, fmt.Errorf("%w: %q", ErrInvalidKind, claim.Kind)
	}
	return h.service.Handle(ctx, claim)
}

// InspectHandler returns the typed artifact_inspect wrapper.
func (s *Service) InspectHandler() worker.Handler {
	return ArtifactInspectHandler{service: s}
}

type inspectInput struct {
	DownloadTaskID string `json:"download_task_id"`
	ArtifactID     string `json:"artifact_id"`
	LocalRoot      string `json:"local_root"`
	ProviderTaskID string `json:"provider_task_id"`
	ArtifactPath   string `json:"artifact_path"`
}

type providerCorrelation struct {
	ExternalID           string                 `json:"external_id"`
	ArtifactPath         string                 `json:"artifact_path"`
	State                string                 `json:"state"`
	FileCount            int                    `json:"file_count"`
	Files                []providerFileSnapshot `json:"files"`
	FilesComplete        bool                   `json:"files_complete"`
	PathSnapshot         *pathSnapshot          `json:"path_snapshot,omitempty"`
	FilesPresent         bool                   `json:"-"`
	FilesCompletePresent bool                   `json:"-"`
}

type pathSnapshot struct {
	RemoteRoot      string `json:"remote_root"`
	ApplicationRoot string `json:"application_root"`
}

type providerFileSnapshot struct {
	Path      string  `json:"path"`
	SizeBytes int64   `json:"size_bytes"`
	MediaKind string  `json:"media_kind"`
	Language  string  `json:"language"`
	Progress  float64 `json:"progress"`
}

type artifactInspectionTarget struct {
	path         string
	mediaKind    string
	language     string
	expectedSize *int64
	sourceHash   string
	primary      bool
	// media holds the execution-time media facts read from the file's own
	// streams; empty fields are omitted from artifact_files.facts.
	media domain.MediaFacts
	// firstLevel is read from the torrent manifest path (never from the local
	// path segments the download client produced).
	firstLevel bool
}

// frozenReleaseFile mirrors the JSON shape of storedReleaseFile in
// rss/application without importing that package (which would create a
// circular dependency).  The fields must stay in sync.
type frozenReleaseFile struct {
	SourcePath string         `json:"source_path"`
	Filename   string         `json:"filename"`
	SizeBytes  *int64         `json:"size_bytes,omitempty"`
	MediaKind  string         `json:"media_kind,omitempty"`
	SourceHash string         `json:"source_hash,omitempty"`
	Facts      map[string]any `json:"facts,omitempty"`
}

// frozenDownloadInput mirrors the JSON shape of internalFrozenDownloadInput
// stored in download_tasks.frozen_input.  Only the fields needed by
// artifact inspection are decoded.
type frozenDownloadInput struct {
	InfoHash       string              `json:"info_hash,omitempty"`
	TorrentVersion string              `json:"torrent_version,omitempty"`
	ReleaseFiles   []frozenReleaseFile `json:"release_files,omitempty"`
}

type artifactSnapshot struct {
	id                  string
	downloadTaskID      string
	state               string
	version             int64
	taskState           string
	inventorySnapshot   []byte
	providerCorrelation []byte
	frozenInput         []byte
	correlationID       sql.NullString
}

type artifactFilePlan struct {
	id           string
	fileRef      string
	relativePath string
	sourcePath   string
	mediaKind    string
	sizeBytes    int64
	fingerprint  []byte
	facts        []byte
	firstLevel   bool
}

type inspectionPlan struct {
	// inventoryBase holds the pending provider facts and the observation
	// time; commitInspection adds the bound summary (counts and digest).
	inventoryBase map[string]any
	file          artifactFilePlan
	files         []artifactFilePlan
	verifiedAt    time.Time
}

func (plan inspectionPlan) filesOrFallback() []artifactFilePlan {
	if len(plan.files) > 0 {
		return plan.files
	}
	return []artifactFilePlan{plan.file}
}

type failurePlanData struct {
	state       string
	failureCode string
	reason      string
}

func decodeInspectInput(raw json.RawMessage) (inspectInput, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return inspectInput{}, nil
	}
	var input inspectInput
	if err := json.Unmarshal([]byte(trimmed), &input); err != nil {
		return inspectInput{}, fmt.Errorf("decode artifact inspection input: %w", err)
	}
	return input, nil
}

func (s *Service) loadArtifactSnapshot(ctx context.Context, artifactID string) (artifactSnapshot, error) {
	var snapshot artifactSnapshot
	var correlation []byte
	var frozenInput []byte
	err := s.db.QueryRowContext(ctx, `
SELECT a.id, a.download_task_id, a.state, a.version, a.inventory_snapshot, dt.state,
       COALESCE(dt.provider_correlation, '{}'::jsonb),
       COALESCE(dt.frozen_input, '{}'::jsonb)
FROM artifacts AS a
JOIN download_tasks AS dt ON dt.id = a.download_task_id
WHERE a.id = $1`, strings.TrimSpace(artifactID)).Scan(
		&snapshot.id, &snapshot.downloadTaskID, &snapshot.state, &snapshot.version,
		&snapshot.inventorySnapshot, &snapshot.taskState, &correlation, &frozenInput)
	if errors.Is(err, sql.ErrNoRows) {
		return artifactSnapshot{}, fmt.Errorf("%w: %q", ErrArtifactNotFound, artifactID)
	}
	if err != nil {
		return artifactSnapshot{}, fmt.Errorf("read artifact owner: %w", err)
	}
	snapshot.providerCorrelation = append([]byte(nil), correlation...)
	snapshot.frozenInput = append([]byte(nil), frozenInput...)
	return snapshot, nil
}

// resolveArtifactSnapshot always loads the Artifact identified by the claimed
// owner. When only a download task is present in the frozen input, it also
// resolves the pending Artifact for that task so validation can reject a
// mismatched owner before any filesystem inspection occurs.
func (s *Service) resolveArtifactSnapshot(ctx context.Context, ownerID string, input inspectInput) (artifactSnapshot, error) {
	ownerID = strings.TrimSpace(ownerID)
	snapshot, loadErr := s.loadArtifactSnapshot(ctx, ownerID)
	if loadErr != nil {
		return artifactSnapshot{}, loadErr
	}
	if strings.TrimSpace(input.ArtifactID) == "" {
		taskID := strings.TrimSpace(input.DownloadTaskID)
		if taskID == "" {
			taskID = snapshot.downloadTaskID
		}
		var pendingID string
		err := s.db.QueryRowContext(ctx, `
SELECT id
FROM artifacts
WHERE download_task_id = $1 AND state = 'pending'`, taskID).Scan(&pendingID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return snapshot, fmt.Errorf("resolve pending artifact: %w", err)
		}
		if err == nil && pendingID != ownerID {
			return snapshot, fmt.Errorf("%w: pending artifact %q is not owner %q", ErrArtifactOwnerMismatch, pendingID, ownerID)
		}
	}
	return snapshot, nil
}

func (s *Service) validateInput(snapshot artifactSnapshot, input inspectInput) error {
	if strings.TrimSpace(input.ArtifactID) != "" && strings.TrimSpace(input.ArtifactID) != snapshot.id {
		return fmt.Errorf("%w: input artifact_id is not the claimed artifact", ErrArtifactOwnerMismatch)
	}
	if strings.TrimSpace(input.DownloadTaskID) != "" && strings.TrimSpace(input.DownloadTaskID) != snapshot.downloadTaskID {
		return fmt.Errorf("%w: input download_task_id does not belong to the claimed artifact", ErrArtifactOwnerMismatch)
	}
	correlation, err := decodeProviderCorrelation(snapshot.providerCorrelation)
	if err != nil {
		return fmt.Errorf("%w: provider correlation cannot be decoded", ErrArtifactOwnerMismatch)
	}
	if providerTaskID := strings.TrimSpace(input.ProviderTaskID); providerTaskID != "" {
		if strings.TrimSpace(correlation.ExternalID) == "" || providerTaskID != strings.TrimSpace(correlation.ExternalID) {
			return fmt.Errorf("%w: input provider_task_id does not match the download task", ErrArtifactOwnerMismatch)
		}
	}
	return nil
}

func decodeProviderCorrelation(raw []byte) (providerCorrelation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return providerCorrelation{}, nil
	}
	if len(trimmed) > 262144 {
		return providerCorrelation{}, errors.New("provider correlation is too large")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("provider correlation must be a JSON object")
		}
		return providerCorrelation{}, err
	}
	var correlation providerCorrelation
	if err := json.Unmarshal([]byte(trimmed), &correlation); err != nil {
		return providerCorrelation{}, err
	}
	_, correlation.FilesPresent = fields["files"]
	_, correlation.FilesCompletePresent = fields["files_complete"]
	return correlation, nil
}

// decodeFrozenInput parses the frozen_input JSON stored on the download task
// at evaluation time.  It contains the info_hash and release_files that were
// parsed from the .torrent file during RSS acquisition — the authoritative
// file list for artifact inspection.
func decodeFrozenInput(raw []byte) (frozenDownloadInput, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return frozenDownloadInput{}, nil
	}
	if len(trimmed) > maxFrozenInputBytes {
		return frozenDownloadInput{}, errors.New("frozen input is too large")
	}
	var input frozenDownloadInput
	if err := json.Unmarshal([]byte(trimmed), &input); err != nil {
		return frozenDownloadInput{}, fmt.Errorf("decode frozen input: %w", err)
	}
	return input, nil
}

func applyProviderPathSnapshot(input inspectInput, correlation providerCorrelation) (inspectInput, error) {
	if correlation.PathSnapshot == nil {
		return input, nil
	}
	root := strings.TrimSpace(correlation.PathSnapshot.ApplicationRoot)
	if root == "" {
		// Legacy frozen rows may lack application_root; under the same-path
		// convention the qB-reported root is the application root.
		root = strings.TrimSpace(correlation.PathSnapshot.RemoteRoot)
	}
	if root == "" || !safePathValue(root) || !filepath.IsAbs(root) {
		return inspectInput{}, fmt.Errorf("%w: provider application root is invalid", ErrArtifactPathUnsafe)
	}
	root = filepath.Clean(root)
	if input.LocalRoot != "" && !safePathValue(input.LocalRoot) {
		return inspectInput{}, ErrArtifactPathUnsafe
	}
	input.LocalRoot = root
	return input, nil
}

// resolveArtifactFilesFromFrozenInput builds inspection targets from the
// release_files that were parsed from the .torrent file during RSS
// acquisition and stored in frozen_input.  This is the authoritative file
// list - it does not depend on qB's /torrents/files API which is incomplete
// for v2 torrents.  The correlation.ArtifactPath (from qB) is still used as
// a hint for primary selection when available.
func resolveArtifactFilesFromFrozenInput(input inspectInput, frozen frozenDownloadInput, correlation providerCorrelation) ([]artifactInspectionTarget, error) {
	var targets []artifactInspectionTarget
	if len(frozen.ReleaseFiles) == 0 {
		// Frozen release_files can be empty even though the provider actually
		// downloaded and completed the file: acquisition-time torrent metadata
		// enrichment may have failed while the payload row was preserved by an
		// earlier successful refresh (evidence: slime E79 froze with an empty
		// list and its artifact stayed permanently failed). For v1 torrents the
		// persisted provider correlation snapshot carries the authoritative
		// qB file list, so rebuild the inspection targets from it instead of
		// freezing the artifact as a terminal failure.
		if strings.TrimSpace(frozen.TorrentVersion) == "v1" && len(correlation.Files) > 0 {
			// The provider snapshot keeps a bounded file list (the download
			// sync marks it files_complete=false when it cut the list); a
			// truncated one cannot stand in for the torrent's files.
			if (correlation.FilesCompletePresent && !correlation.FilesComplete) || correlation.FileCount > len(correlation.Files) {
				return nil, fmt.Errorf("%w: the download client file list is truncated", ErrProviderFilesIncomplete)
			}
			var err error
			targets, err = buildTargetsFromProviderCorrelation(input, correlation)
			if err != nil {
				return nil, err
			}
			if len(targets) == 0 {
				return nil, nil
			}
		} else {
			return nil, nil
		}
	} else {
		if len(frozen.ReleaseFiles) > maxInspectionFiles {
			return nil, fmt.Errorf("%w: the release lists more than %d files", ErrProviderFilesIncomplete, maxInspectionFiles)
		}
		var err error
		targets, err = buildTargetsFromReleaseFiles(input, frozen.ReleaseFiles)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, nil
		}
	}

	// Use correlation.ArtifactPath as the primary hint when it resolves to
	// one of our frozen-input targets.  Otherwise fall back to heuristic
	// selection (largest video file).
	primary := strings.TrimSpace(correlation.ArtifactPath)
	if primary != "" && correlation.PathSnapshot != nil {
		primary = translateProviderRoot(correlation.PathSnapshot, primary)
	}
	if primary != "" {
		primaryPath, err := resolveArtifactPathValue(input, primary)
		if err == nil {
			primary = filepath.ToSlash(filepath.Clean(primaryPath))
		} else {
			primary = ""
		}
	}
	primaryIndex := -1
	if primary != "" {
		for index, target := range targets {
			if filepath.ToSlash(filepath.Clean(target.path)) == primary {
				primaryIndex = index
				break
			}
		}
	}
	if primaryIndex < 0 {
		primaryIndex = primaryArtifactTargetIndex(targets)
	}
	if primaryIndex < 0 {
		return nil, ErrProviderFilesIncomplete
	}
	targets[primaryIndex].primary = true
	return targets, nil
}

func buildTargetsFromReleaseFiles(input inspectInput, files []frozenReleaseFile) ([]artifactInspectionTarget, error) {
	targets := make([]artifactInspectionTarget, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file.SourcePath)
		if path == "" {
			path = strings.TrimSpace(file.Filename)
		}
		if path == "" {
			return nil, ErrProviderFilesIncomplete
		}
		resolved, err := resolveArtifactPathValue(input, path)
		if err != nil {
			return nil, err
		}
		key := filepath.ToSlash(filepath.Clean(resolved))
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("%w: duplicate release file path", ErrProviderFilesIncomplete)
		}
		seen[key] = struct{}{}
		var expectedSize *int64
		if file.SizeBytes != nil && *file.SizeBytes >= 0 {
			size := *file.SizeBytes
			expectedSize = &size
		}
		language := ""
		if file.Facts != nil {
			if lang, ok := file.Facts["language"].(string); ok {
				language = strings.TrimSpace(lang)
			}
		}
		targets = append(targets, artifactInspectionTarget{
			path:         resolved,
			mediaKind:    normalizeMediaKind(file.MediaKind, resolved),
			language:     language,
			expectedSize: expectedSize,
			sourceHash:   strings.TrimSpace(file.SourceHash),
			firstLevel:   filekinds.FirstLevel(path),
		})
	}
	return targets, nil
}

// buildTargetsFromProviderCorrelation rebuilds inspection targets from the
// qB correlation snapshot when frozen release_files are unavailable. The
// snapshot records provider-root paths (e.g. /downloads/...), so they are
// translated to the application root before resolution.
func buildTargetsFromProviderCorrelation(input inspectInput, correlation providerCorrelation) ([]artifactInspectionTarget, error) {
	targets := make([]artifactInspectionTarget, 0, len(correlation.Files))
	seen := make(map[string]struct{}, len(correlation.Files))
	for _, file := range correlation.Files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			return nil, ErrProviderFilesIncomplete
		}
		if correlation.PathSnapshot != nil {
			path = translateProviderRoot(correlation.PathSnapshot, path)
		}
		resolved, err := resolveArtifactPathValue(input, path)
		if err != nil {
			return nil, err
		}
		key := filepath.ToSlash(filepath.Clean(resolved))
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("%w: duplicate provider file path", ErrProviderFilesIncomplete)
		}
		seen[key] = struct{}{}
		var expectedSize *int64
		if file.SizeBytes > 0 {
			size := file.SizeBytes
			expectedSize = &size
		}
		targets = append(targets, artifactInspectionTarget{
			path:         resolved,
			mediaKind:    normalizeMediaKind(file.MediaKind, resolved),
			language:     strings.TrimSpace(file.Language),
			expectedSize: expectedSize,
			// No frozen manifest exists on this fallback and the provider
			// reports absolute paths, so the level is the depth below the
			// download root: <root>/<torrent.name>/<file> or <root>/<file>.
			firstLevel: firstLevelUnderRoot(input.LocalRoot, resolved),
		})
	}
	return targets, nil
}

// firstLevelUnderRoot reports whether an absolute artifact path is first level
// below the download root (the torrent's own folder or file). A path outside
// the root has no level evidence and is not first level (fail closed).
func firstLevelUnderRoot(localRoot, resolved string) bool {
	localRoot = strings.TrimSpace(localRoot)
	if localRoot == "" {
		return false
	}
	for _, root := range []string{filepath.Clean(localRoot), canonicalArtifactRoot(localRoot)} {
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == "." || !pathWithinRoot(root, resolved) {
			continue
		}
		return filekinds.FirstLevel(filepath.ToSlash(rel))
	}
	return false
}

// translateProviderRoot rewrites a provider-root path (e.g. /downloads/...)
// onto the application root recorded in the path snapshot, which is the tree
// the server's filesystem can actually see.
func translateProviderRoot(snapshot *pathSnapshot, path string) string {
	remoteRoot := strings.TrimSpace(snapshot.RemoteRoot)
	appRoot := strings.TrimSpace(snapshot.ApplicationRoot)
	if remoteRoot == "" || appRoot == "" || remoteRoot == appRoot {
		return path
	}
	remoteRoot = filepath.Clean(remoteRoot)
	if rel, ok := strings.CutPrefix(path, remoteRoot); ok {
		rel = strings.TrimPrefix(rel, string(filepath.Separator))
		return filepath.Join(appRoot, rel)
	}
	return path
}

func resolveArtifactPathValue(input inspectInput, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ErrArtifactPathMissing
	}
	if !safePathValue(path) {
		return "", ErrArtifactPathUnsafe
	}
	root := strings.TrimSpace(input.LocalRoot)
	if root != "" && !safePathValue(root) {
		return "", ErrArtifactPathUnsafe
	}
	if !filepath.IsAbs(path) {
		if root == "" {
			return "", fmt.Errorf("%w: relative artifact path has no local root", ErrArtifactPathUnsafe)
		}
		path = filepath.Join(root, path)
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%w: resolve artifact path", ErrArtifactPathUnsafe)
	}
	if !safePathValue(resolved) {
		return "", ErrArtifactPathUnsafe
	}
	if root != "" {
		root, err = filepath.Abs(filepath.Clean(root))
		if err != nil {
			return "", fmt.Errorf("%w: resolve local root", ErrArtifactPathUnsafe)
		}
		// The frozen application root may traverse a mount-level symlink (for
		// example macOS /tmp -> /private/tmp), and the OS inspector reports the
		// fully resolved path. When the artifact file exists and is not itself
		// a symlink, resolve in canonical space so the inspector's returned
		// path matches the plan target; otherwise keep the raw path so a
		// missing file or a symlinked artifact surfaces through the
		// inspector's own checks.
		if info, statErr := os.Lstat(resolved); statErr == nil && info.Mode()&os.ModeSymlink == 0 {
			if canonical, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
				if !pathWithinRoot(canonicalArtifactRoot(root), canonical) {
					return "", fmt.Errorf("%w: artifact is outside the local root", ErrArtifactPathUnsafe)
				}
				return canonical, nil
			}
		}
		if !pathWithinRoot(root, resolved) {
			return "", fmt.Errorf("%w: artifact is outside the local root", ErrArtifactPathUnsafe)
		}
	}
	return resolved, nil
}

// canonicalArtifactRoot resolves the frozen local root through symlinks when
// it exists on this host. It mirrors the file-operation layer's library
// anchor canonicalization so relative facts stay consistent with the
// inspector's resolved paths. A root that does not exist locally is kept as
// written: the inspector then reports the normal missing-file error.
func canonicalArtifactRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(root)
}

func primaryArtifactTargetIndex(targets []artifactInspectionTarget) int {
	bestIndex := -1
	bestRank := 3
	bestPath := ""
	for index, target := range targets {
		rank := 2
		if target.mediaKind == "video" {
			rank = 0
		} else if target.mediaKind == "subtitle" {
			rank = 1
		}
		path := filepath.ToSlash(filepath.Clean(target.path))
		if bestIndex < 0 || rank < bestRank || (rank == bestRank && path < bestPath) {
			bestIndex, bestRank, bestPath = index, rank, path
		}
	}
	return bestIndex
}

func buildInspectionPlan(snapshot artifactSnapshot, input inspectInput, facts domain.ArtifactFacts, now time.Time) (inspectionPlan, error) {
	return buildInspectionPlanForFiles(snapshot, input, []artifactInspectionTarget{{
		path:       facts.Path,
		primary:    true,
		firstLevel: true,
	}}, []domain.ArtifactFacts{facts}, now)
}

func buildInspectionPlanForFiles(snapshot artifactSnapshot, input inspectInput, targets []artifactInspectionTarget, facts []domain.ArtifactFacts, now time.Time) (inspectionPlan, error) {
	if len(targets) == 0 || len(targets) != len(facts) || len(targets) > maxInspectionFiles {
		return inspectionPlan{}, fmt.Errorf("%w: inspected file set is invalid", ErrArtifactFactsInvalid)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	files := make([]artifactFilePlan, 0, len(facts))
	seenSources := make(map[string]struct{}, len(facts))
	seenRelative := make(map[string]struct{}, len(facts))
	primaryCount := 0
	for index, inspected := range facts {
		target := targets[index]
		expectedSize := target.expectedSize
		if expectedSize != nil && *expectedSize < 0 {
			expectedSize = nil
		}
		path := strings.TrimSpace(inspected.Path)
		if path == "" {
			return inspectionPlan{}, fmt.Errorf("%w: path is empty", ErrArtifactFactsInvalid)
		}
		if !safePathValue(path) {
			return inspectionPlan{}, ErrArtifactPathUnsafe
		}
		expectedPath, err := filepath.Abs(filepath.Clean(target.path))
		if err != nil || filepath.Clean(expectedPath) != filepath.Clean(path) {
			return inspectionPlan{}, fmt.Errorf("%w: inspector returned a different path", ErrArtifactFactsInvalid)
		}
		if inspected.SizeBytes < 0 {
			return inspectionPlan{}, fmt.Errorf("%w: size is negative", ErrArtifactFactsInvalid)
		}
		if expectedSize != nil && inspected.SizeBytes != *expectedSize {
			return inspectionPlan{}, fmt.Errorf("%w: %w: expected %d bytes, inspected %d bytes for %s", ErrArtifactFactsInvalid, ErrArtifactSizeMismatch, *expectedSize, inspected.SizeBytes, filepath.ToSlash(filepath.Clean(target.path)))
		}
		if (inspected.Device == 0) != (inspected.Inode == 0) {
			return inspectionPlan{}, fmt.Errorf("%w: file identity is incomplete", ErrArtifactFactsInvalid)
		}
		// Path-level fingerprint: size plus device/inode, never a content hash.
		fingerprintValue := librarydomain.MetadataFingerprint(inspected.SizeBytes, librarydomain.FileIdentity{Device: inspected.Device, Inode: inspected.Inode})
		sourcePath, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return inspectionPlan{}, fmt.Errorf("%w: resolve source path", ErrArtifactFactsInvalid)
		}
		if !safePathValue(sourcePath) {
			return inspectionPlan{}, ErrArtifactPathUnsafe
		}
		relativePath, err := relativeArtifactPath(input.LocalRoot, sourcePath)
		if err != nil {
			return inspectionPlan{}, err
		}
		normalizedSource := filepath.ToSlash(filepath.Clean(sourcePath))
		if _, exists := seenSources[normalizedSource]; exists {
			return inspectionPlan{}, fmt.Errorf("%w: duplicate source path", ErrArtifactFactsInvalid)
		}
		if _, exists := seenRelative[relativePath]; exists {
			return inspectionPlan{}, fmt.Errorf("%w: duplicate relative path", ErrArtifactFactsInvalid)
		}
		seenSources[normalizedSource] = struct{}{}
		seenRelative[relativePath] = struct{}{}

		mediaKind := normalizeMediaKind(inspected.MediaKind, sourcePath)
		fileRef := "primary"
		if !target.primary {
			fileRef = stableArtifactFileRef(normalizedSource)
		} else {
			primaryCount++
		}
		observedAt := inspected.ObservedAt
		if observedAt.IsZero() {
			observedAt = now
		}
		observedAt = observedAt.UTC()
		fingerprintRaw, err := marshalObject(map[string]any{
			"algorithm":  fingerprintAlgorithmMetadata,
			"value":      fingerprintValue,
			"size_bytes": inspected.SizeBytes,
		})
		if err != nil {
			return inspectionPlan{}, fmt.Errorf("marshal artifact fingerprint: %w", err)
		}
		fileFactsValue := map[string]any{
			"source":      "artifact_inspect",
			"media_kind":  mediaKind,
			"observed_at": observedAt.Format(time.RFC3339Nano),
		}
		if !inspected.ModifiedAt.IsZero() {
			fileFactsValue["modified_at"] = inspected.ModifiedAt.UTC().Format(time.RFC3339Nano)
		}
		if language := strings.TrimSpace(target.language); language != "" {
			fileFactsValue["language"] = bounded(language, 512)
		}
		if providerKind := strings.TrimSpace(target.mediaKind); providerKind != "" && providerKind != mediaKind {
			fileFactsValue["provider_media_kind"] = normalizeMediaKind(providerKind, sourcePath)
		}
		if expectedSize != nil {
			fileFactsValue["provider_size_bytes"] = *expectedSize
		}
		if sourceHash := strings.TrimSpace(target.sourceHash); sourceHash != "" && safeToken(sourceHash) {
			fileFactsValue["provider_source_hash"] = bounded(sourceHash, maxFingerprintLen)
		}
		for key, value := range target.media.Entries() {
			fileFactsValue[key] = bounded(value, maxMediaFactLen)
		}
		fileFacts, err := marshalObject(fileFactsValue)
		if err != nil {
			return inspectionPlan{}, fmt.Errorf("marshal artifact file facts: %w", err)
		}
		files = append(files, artifactFilePlan{
			id:           stableID("artifact-file", snapshot.id, fileRef),
			fileRef:      fileRef,
			relativePath: relativePath,
			sourcePath:   sourcePath,
			mediaKind:    mediaKind,
			sizeBytes:    inspected.SizeBytes,
			fingerprint:  fingerprintRaw,
			facts:        fileFacts,
			firstLevel:   target.firstLevel,
		})
	}
	if primaryCount != 1 {
		return inspectionPlan{}, fmt.Errorf("%w: exactly one primary file is required", ErrArtifactFactsInvalid)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].fileRef == "primary" {
			return true
		}
		if files[j].fileRef == "primary" {
			return false
		}
		return files[i].fileRef < files[j].fileRef
	})

	correlation, _ := decodeProviderCorrelation(snapshot.providerCorrelation)
	var primaryFile artifactFilePlan
	for _, file := range files {
		if file.fileRef == "primary" {
			primaryFile = file
		}
	}
	// The pending provider snapshot keeps its scalar facts; any file list it
	// carried is dropped. Per-file data lives in artifact_files rows; the
	// committed snapshots carry counts and a digest only (commitInspection
	// completes them once the manifest references are bound).
	inventory := sanitizeInventory(snapshot.inventorySnapshot)
	if inventory == nil {
		inventory = make(map[string]any)
	}
	for _, key := range inventoryListKeys {
		delete(inventory, key)
	}
	observedAt := now
	if primaryFacts := primaryFile.facts; len(primaryFacts) > 0 {
		var primaryFactsValue map[string]any
		if json.Unmarshal(primaryFacts, &primaryFactsValue) == nil {
			if value, ok := primaryFactsValue["observed_at"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
					observedAt = parsed.UTC()
				}
			}
		}
	}
	inventory["observed_at"] = observedAt.Format(time.RFC3339Nano)
	if providerID := safeIdentifier(correlation.ExternalID); providerID != "" {
		inventory["provider_task_id"] = providerID
	}
	if correlation.FileCount >= 0 && correlation.FileCount <= maxInspectionFiles {
		inventory["provider_file_count"] = correlation.FileCount
	}
	if correlation.FilesPresent || correlation.FilesCompletePresent {
		inventory["provider_files_complete"] = correlation.FilesComplete
	}
	return inspectionPlan{
		inventoryBase: inventory,
		file:          primaryFile,
		files:         files,
		verifiedAt:    now,
	}, nil
}

// samePathObservation is the settle rule: the primary file kept its size,
// modification time and (when recorded) inode across the settle window.
func samePathObservation(left, right domain.ArtifactFacts) bool {
	if left.SizeBytes != right.SizeBytes || !left.ModifiedAt.Equal(right.ModifiedAt) {
		return false
	}
	if left.Inode != 0 && right.Inode != 0 {
		return left.Inode == right.Inode
	}
	return true
}

func stableArtifactFileRef(normalizedSourcePath string) string {
	hash := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(normalizedSourcePath))))
	return "file-" + hex.EncodeToString(hash[:])
}

func relativeArtifactPath(root, sourcePath string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		base := filepath.Base(sourcePath)
		if base == "." || base == string(filepath.Separator) || base == "" {
			return "", fmt.Errorf("%w: source basename is empty", ErrArtifactFactsInvalid)
		}
		return filepath.ToSlash(base), nil
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("%w: resolve local root", ErrArtifactFactsInvalid)
	}
	// The inspector resolves mount-level symlinks (for example macOS /tmp ->
	// /private/tmp); canonicalize the root the same way so relative facts
	// stay inside it. A root that does not exist locally is kept as written.
	if resolvedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = filepath.Clean(resolvedRoot)
	}
	rel, err := filepath.Rel(rootAbs, sourcePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: artifact is outside the local root", ErrArtifactPathUnsafe)
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || strings.HasPrefix(rel, "/") || strings.Contains(rel, "../") || strings.Contains(rel, "\x00") || len(rel) > maxPathLength {
		return "", fmt.Errorf("%w: relative path is invalid", ErrArtifactPathUnsafe)
	}
	return rel, nil
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// sanitizeInventory keeps only bounded JSON values from the pending provider
// snapshot. Artifact inspection never copies an opaque provider payload into a
// durable owner row, and credential-bearing URL strings are dropped entirely.
func sanitizeInventory(raw []byte) map[string]any {
	if len(raw) == 0 || len(raw) > 2*1024*1024 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil
	}
	sanitized, ok := sanitizeInventoryValue(value, 0)
	if !ok {
		return nil
	}
	object, ok := sanitized.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func sanitizeInventoryValue(value any, depth int) (any, bool) {
	if depth > 5 {
		return nil, false
	}
	switch typed := value.(type) {
	case nil, bool, float64:
		return typed, true
	case string:
		if len(typed) > maxPathLength || sensitiveText(typed) {
			return nil, false
		}
		return typed, true
	case []any:
		if len(typed) > 256 {
			typed = typed[:256]
		}
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			clean, ok := sanitizeInventoryValue(item, depth+1)
			if ok {
				result = append(result, clean)
			}
		}
		return result, true
	case map[string]any:
		result := make(map[string]any, len(typed))
		count := 0
		for key, item := range typed {
			if count >= 256 || !safeInventoryKey(key) {
				continue
			}
			clean, ok := sanitizeInventoryValue(item, depth+1)
			if !ok {
				continue
			}
			result[key] = clean
			count++
		}
		return result, true
	default:
		return nil, false
	}
}

func safeInventoryKey(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.Contains(lower, "passkey") && !strings.Contains(lower, "password") &&
		!strings.Contains(lower, "token") && !strings.Contains(lower, "secret") &&
		!strings.Contains(lower, "credential") && !strings.Contains(lower, "cookie")
}

func sensitiveText(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"passkey=", "password=", "token=", "secret=", "credential="} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for key := range parsed.Query() {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "passkey" || key == "password" || key == "token" || key == "secret" || key == "credential" {
			return true
		}
	}
	return false
}

func marshalObject(value map[string]any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > 2*1024*1024 {
		return nil, errors.New("artifact JSON snapshot is too large")
	}
	return raw, nil
}

func safePathValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxPathLength || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "passkey=") || strings.Contains(lower, "password=") || strings.Contains(lower, "token=") {
		return false
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.User != nil || parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "ftp") {
		return false
	}
	return true
}

func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeToken(value string) bool {
	if strings.ContainsAny(value, "\r\n\x00") || strings.Contains(value, "http://") || strings.Contains(value, "https://") {
		return false
	}
	return !strings.Contains(strings.ToLower(value), "passkey=") && !strings.Contains(strings.ToLower(value), "token=")
}

func safeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || !safeToken(value) {
		return ""
	}
	return value
}

func normalizeMediaKind(value, path string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "video":
		return "video"
	case "subtitle":
		return "subtitle"
	case "other":
		return "other"
	}
	return filekinds.ClassifyCurrent(path)
}

func stableID(prefix string, values ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(hash[:])
}

func bounded(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func safeReason(value string) string {
	safe, _ := redact.Apply(value, maxReasonLength, redact.Mask, redact.Redacted)
	return safe
}

func safeFailureReason(err error) string {
	if err == nil {
		return "artifact inspection failed"
	}
	switch {
	case errors.Is(err, ErrArtifactOwnerMismatch):
		return "artifact inspection owner does not match the claimed artifact"
	case errors.Is(err, ErrArtifactPathMissing):
		return "the completed download did not expose an artifact path"
	case errors.Is(err, ErrArtifactPathUnsafe):
		return "the artifact path is unsafe"
	case errors.Is(err, ErrProviderFilesIncomplete):
		return "the provider file snapshot is incomplete"
	case errors.Is(err, ErrArtifactFactsInvalid):
		return "the artifact inspector returned incomplete facts"
	default:
		return "artifact inspection failed"
	}
}

func terminalInspectionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "symlink") || strings.Contains(message, "outside") ||
		strings.Contains(message, "unsafe") || strings.Contains(message, "must be a non-empty regular file")
}

func redactInspectionError(err error) string {
	if err == nil {
		return "artifact inspection failed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "artifact inspection timed out"
	case strings.Contains(message, "permission"), strings.Contains(message, "access denied"), strings.Contains(message, "open "):
		return "downloaded artifact is not readable"
	case strings.Contains(message, "symlink"), strings.Contains(message, "outside"), strings.Contains(message, "unsafe"):
		return "artifact path is unsafe"
	default:
		return "artifact inspection failed"
	}
}

// frozenParserEvidence is the parser provenance frozen on one media fact
// snapshot: the selected kind plus the exact config that produced the frozen
// Release evidence. nil Config is a real state (see frozenFileFactsSelection).
type frozenParserEvidence struct {
	Kind        string
	Config      json.RawMessage
	Fingerprint string
}

// frozenFileFactsSelection rebuilds the parser selection frozen on a Release
// media fact snapshot. Per-file recognition must run exactly this selection:
// reading current instance_parser_policies would silently mix two profiles
// when the instance policy changed between Release evaluation and Artifact
// inspection.
//
// Three frozen states are distinguished, and the current policy is never one of
// them:
//
//  1. Config present — the exact frozen profile; validated through the same
//     registry the Release evidence used. The computed config fingerprint must
//     equal the persisted fingerprint: a config/fingerprint mismatch means the
//     frozen evidence contradicts itself and fails closed instead of guessing.
//     A missing fingerprint with a present config is equally unprovable.
//  2. Config NULL with a fingerprint — only the canonical nil-config digest is
//     admissible (an empty profile is encoded as NULL config, and the
//     fingerprint proves an empty profile was hashed). Any other non-NULL
//     fingerprint with NULL config contradicts the frozen evidence and fails
//     closed; the adapter's documented defaults apply to the admissible row.
//  3. Config NULL and fingerprint NULL — a row written before the frozen
//     config existed. The only writer at that time hardcoded the bundled
//     regex parser with no config, so that provable same-source value is
//     reconstructed for kind regex. Any other kind contradicts the
//     frozen evidence and fails closed instead of guessing.
func frozenFileFactsSelection(evidence frozenParserEvidence, offset int) (parser.Selection, error) {
	kind := parser.ParserKind(strings.TrimSpace(evidence.Kind))
	if kind == "" {
		return parser.Selection{}, fmt.Errorf("%w: media fact snapshot carries no frozen parser kind", ErrFrozenParserEvidence)
	}
	config := json.RawMessage(bytes.TrimSpace(evidence.Config))
	hasConfig := len(config) > 0 && string(config) != "null"
	persisted := strings.TrimSpace(evidence.Fingerprint)
	switch {
	case hasConfig:
		selection, err := parser.NewSelection(kind, config, offset)
		if err != nil {
			return parser.Selection{}, fmt.Errorf("%w: frozen parser %q config is unusable: %v", ErrFrozenParserEvidence, kind, err)
		}
		if persisted == "" {
			return parser.Selection{}, fmt.Errorf("%w: frozen parser %q config carries no config fingerprint to verify against", ErrFrozenParserEvidence, kind)
		}
		if persisted != selection.ConfigFingerprint {
			return parser.Selection{}, fmt.Errorf("%w: frozen parser %q config fingerprint mismatch: snapshot carries %q, computed %q", ErrFrozenParserEvidence, kind, persisted, selection.ConfigFingerprint)
		}
		return selection, nil
	case persisted != "":
		if persisted != parser.NilConfigFingerprint() {
			return parser.Selection{}, fmt.Errorf("%w: frozen parser %q carries fingerprint %q with NULL config: only the canonical nil-config digest is admissible without config", ErrFrozenParserEvidence, kind, persisted)
		}
		selection, err := parser.NewSelection(kind, nil, offset)
		if err != nil {
			return parser.Selection{}, fmt.Errorf("%w: frozen parser %q is unusable: %v", ErrFrozenParserEvidence, kind, err)
		}
		return selection, nil
	case kind == parser.ParserRegex:
		return parser.NewSelection(parser.ParserRegex, nil, offset)
	default:
		return parser.Selection{}, fmt.Errorf("%w: snapshot parser %q predates frozen parser config", ErrFrozenParserEvidence, kind)
	}
}

// fileFactsContext is the frozen resolution context for one artifact's files:
// the release's media fact snapshot, the frozen effective offset, the frozen
// metadata media kind/season binding, and the parser selection frozen on that
// snapshot.
type fileFactsContext struct {
	SnapshotID      string
	SnapshotBinding json.RawMessage
	EffectiveOffset int
	// MediaKind is the media kind frozen on the snapshot ("movie"
	// short-circuits every file to the canonical movie:0 identity); a legacy
	// snapshot without a frozen binding decodes to the TV default. Current
	// anime_metadata is never consulted.
	MediaKind string
	// FileKindsVersion is the file kind list frozen on the snapshot.
	FileKindsVersion string
	Parser           frozenParserEvidence
	// Selected is the batch plan's frozen manual file selection (one-shot):
	// a selected video at any depth is parsed like a first-level one. nil is
	// the product default.
	Selected *filekinds.Selection
	// Manifest is the frozen plan's manifest paths, which map a local
	// relative path back to its manifest path for the selection.
	Manifest map[string]struct{}
}

// fileFactsContextQuery is the artifact file-facts resolution query. The parser
// evidence, the effective offset, and the metadata media kind/season binding
// come exclusively from the frozen snapshot (the binding inside
// snap.release_facts): no column of anime_metadata,
// instance_parser_policies or rss_sources.episode_offset may supply
// them, because reading current owners here would silently mix two
// profiles/offsets/bindings when an instance policy, metadata row, or link
// offset changed between Release evaluation and Artifact inspection.
const fileFactsContextQuery = `
SELECT snap.id,
       snap.release_facts,
       snap.release_identity,
       snap.parser_kind,
       snap.parser_config,
       snap.parser_config_fingerprint,
       jsonb_build_object('release_facts',snap.release_facts,'release_identity',snap.release_identity,'parser_kind',snap.parser_kind,'parser_config',snap.parser_config,'parser_config_fingerprint',snap.parser_config_fingerprint)
FROM artifacts AS art
JOIN download_tasks AS dt ON dt.id = art.download_task_id
JOIN releases AS rel ON rel.id = dt.release_id
LEFT JOIN LATERAL (
    SELECT id, release_facts, release_identity, parser_kind, parser_config, parser_config_fingerprint
    FROM media_fact_snapshots
    WHERE release_id = rel.id
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) AS snap ON true
WHERE art.id = $1`

func (s *Service) loadFileFactsContext(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, artifactID string) (*fileFactsContext, error) {
	var (
		snapshotID                       sql.NullString
		parserKind, parserFingerprint    sql.NullString
		releaseFactsRaw, parserConfigRaw []byte
		snapshotBinding                  []byte
		releaseIdentityRaw               []byte
	)
	err := query.QueryRowContext(ctx, fileFactsContextQuery, artifactID).
		Scan(&snapshotID, &releaseFactsRaw, &releaseIdentityRaw,
			&parserKind, &parserConfigRaw, &parserFingerprint, &snapshotBinding)
	if err != nil {
		return nil, err
	}
	mediaKind, _, _ := facts.DecodeFrozenMediaBinding(releaseFactsRaw)
	context := &fileFactsContext{
		MediaKind:       mediaKind,
		SnapshotBinding: json.RawMessage(snapshotBinding),
		Parser: frozenParserEvidence{
			Kind:        parserKind.String,
			Config:      parserConfigRaw,
			Fingerprint: parserFingerprint.String,
		},
	}
	if snapshotID.Valid {
		context.SnapshotID = snapshotID.String
	}
	context.EffectiveOffset = frozenReleaseOffset(releaseFactsRaw, releaseIdentityRaw)
	var frozenKinds struct {
		FileKindsVersion string `json:"file_kinds_version"`
		BatchPlan        *struct {
			SelectedPaths []string `json:"selected_paths"`
			Files         []struct {
				SourcePath string `json:"source_path"`
			} `json:"files"`
		} `json:"batch_plan"`
	}
	if len(releaseFactsRaw) > 0 && json.Unmarshal(releaseFactsRaw, &frozenKinds) == nil {
		context.FileKindsVersion = strings.TrimSpace(frozenKinds.FileKindsVersion)
		if frozenKinds.BatchPlan != nil && frozenKinds.BatchPlan.SelectedPaths != nil {
			context.Selected = filekinds.NewSelection(frozenKinds.BatchPlan.SelectedPaths)
			context.Manifest = make(map[string]struct{}, len(frozenKinds.BatchPlan.Files))
			for _, file := range frozenKinds.BatchPlan.Files {
				context.Manifest[filekinds.NormalizePath(file.SourcePath)] = struct{}{}
			}
		}
	}
	return context, nil
}

// selectedArtifactFile reports whether an artifact file is in the frozen manual
// selection. The selection holds manifest paths; the local relative path is
// mapped back to its manifest path by the one shared rule
// (filekinds.MapManifestPath over the frozen plan's files), so a client that
// did not create the torrent folder still maps exactly. Without a selection
// nothing matches.
func selectedArtifactFile(selection *filekinds.Selection, manifest map[string]struct{}, relativePath string) bool {
	if selection == nil {
		return false
	}
	path, ok := filekinds.MapManifestPath(manifest, filepath.ToSlash(relativePath))
	return ok && selection.Has(path)
}

// frozenReleaseOffset reads the one effective offset directly from the frozen
// snapshot columns. The current link offset is deliberately not consulted: the
// snapshot is the only offset authority for per-file recognition.
func frozenReleaseOffset(releaseFactsRaw, releaseIdentityRaw []byte) int {
	if len(releaseFactsRaw) > 0 && string(releaseFactsRaw) != "null" {
		var frozenFacts facts.ReleaseFacts
		if err := json.Unmarshal(releaseFactsRaw, &frozenFacts); err == nil && frozenFacts.ParserProvenance != nil {
			return frozenFacts.ParserProvenance.Offset
		}
	}
	// Legacy snapshot without frozen provenance: the frozen identity still
	// carries the effective offset it resolved with.
	if identity, err := facts.UnmarshalIdentity(releaseIdentityRaw); err == nil {
		return identity.Offset
	}
	return 0
}

// persistFileFactsTx parses every relevant artifact filename with the parser
// selection frozen on the Release media fact snapshot, resolves one independent
// EpisodeIdentity per video, and freezes the EffectiveFileFacts rows into that
// snapshot. Missing/unsupported file evidence produces an unresolved identity
// with no coverage — it never invents an episode. Subtitle and other files
// carry evidence but no identity. A movie binding short-circuits every file to
// the canonical movie:0 identity regardless of filename digits. A selection or
// parse failure is returned as an explicit error: an unusable frozen parser
// must not silently become "this artifact has no episode facts". Offset is not
// applied by the parser; the identity resolver applies it exactly once.
func (s *Service) prepareFileFacts(ctx context.Context, artifactID string, files []artifactFilePlan) (string, []map[string]any, error) {
	if s.mediaFacts == nil {
		return "", nil, nil
	}
	context, err := s.loadFileFactsContext(ctx, s.db, artifactID)
	if err != nil {
		return "", nil, err
	}
	if context.SnapshotID == "" {
		return "", nil, nil
	}
	selection, err := frozenFileFactsSelection(context.Parser, context.EffectiveOffset)
	if err != nil {
		return "", nil, err
	}
	prepared := make([]map[string]any, 0, len(files))
	registry := parser.DefaultRegistry()
	for _, file := range files {
		name := filepath.Base(file.relativePath)
		// Only a first-level video of the frozen kind list (or, under a
		// frozen one-shot manual selection, a selected video at any depth)
		// is parsed. Every
		// other file keeps an evidence row but an unresolved identity: a
		// nested file, a subtitle or another kind never gets an episode.
		eligible := (file.firstLevel || selectedArtifactFile(context.Selected, context.Manifest, file.relativePath)) && filekinds.Classify(context.FileKindsVersion, name) == filekinds.KindVideo
		input := episodeidentity.EpisodeIdentityInput{
			MediaKind: context.MediaKind,
			FileRef:   file.fileRef,
			// The frozen offset is the one offset the Release evaluation
			// resolved with; it is applied exactly once and the current link
			// offset is never re-read.
			Offset: context.EffectiveOffset,
		}
		var fileEvidence episodeidentity.EpisodeEvidence
		if eligible {
			parsed, err := parser.ParseEpisode(ctx, registry,
				parser.ParserSelection{ID: string(selection.Kind()), Config: selection.Parser.Config},
				parser.EpisodeParseInput{Kind: parser.InputFileName, Text: name})
			if err != nil {
				return "", nil, fmt.Errorf("parse artifact file %q with frozen parser %q: %w", name, selection.Kind(), err)
			}
			fileEvidence = parser.Evidence(parsed, parser.InputFileName)
		} else {
			// A movie binding must not short-circuit an ignored file to movie:0.
			input.MediaKind = "tv"
			fileEvidence = episodeidentity.NewMissingEvidence(episodeidentity.EpisodeSourceFileParser, string(parser.InputFileName))
		}
		input.FileEvidence = fileEvidence
		identity := episodeidentity.NewResolver().Resolve(input)
		// A resolved identity is frozen only in its registered shape: one key
		// (IdentityV1) or a multi-episode file's consecutive keys (IdentityV2).
		if err := terminaldomain.ValidateResolvedIdentity(identity); err != nil {
			return "", nil, fmt.Errorf("frozen episode identity of %q is invalid: %w", name, err)
		}
		evidence := facts.FileEvidence{
			Name:      name,
			MediaKind: file.mediaKind,
			SizeBytes: file.sizeBytes,
		}
		effective := facts.EffectiveFileFacts{
			Identity:       identity,
			MappingVersion: facts.MappingVersion,
		}
		if file.mediaKind == "video" {
			effective.Quality = qualityFactsFromEvidence(evidence, fileEvidence)
		}
		prepared = append(prepared, map[string]any{
			"file_ref": file.fileRef, "media_kind": file.mediaKind, "size_bytes": file.sizeBytes,
			"source_fingerprint": fingerprintValue(file.fingerprint), "file_evidence": evidence, "effective_facts": effective,
			"snapshot_binding": context.SnapshotBinding,
		})
	}
	return context.SnapshotID, prepared, nil
}

// qualityFactsFromEvidence builds the normalized typed quality snapshot from
// the parsed file evidence. Only normalized values are carried; unknown or
// absent dimensions stay empty and score zero.
func qualityFactsFromEvidence(evidence facts.FileEvidence, parsed episodeidentity.EpisodeEvidence) facts.QualityFacts {
	quality := facts.QualityFacts{
		SizeBytes: evidence.SizeBytes,
		States:    map[string]string{},
	}
	_ = parsed
	return quality
}

// fingerprintValue extracts the bare metadata value ("meta:size=...") from
// the artifact fingerprint envelope. media_fact_files.source_fingerprint is
// bounded to 128 chars and the commit binds it to the envelope's value, so
// the value, not the whole envelope, is stored.
func fingerprintValue(raw []byte) string {
	var envelope struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Value)
}
