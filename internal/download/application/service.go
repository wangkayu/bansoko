// Package application contains the qBittorrent owner handlers.
// DownloadTask is the product owner; WorkItems only provide fenced execution.
package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/batchplan"
	"bansoko/internal/download/admission"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/events"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/templatecatalog/notification"
	"bansoko/internal/torrenttree"
	"bansoko/internal/workitems"
)

const (
	defaultMaxAttempts = 5
	defaultRetryAfter  = 5 * time.Minute
	// Keep one-shot qB observations bounded independently of the worker tick.
	// One-shot tasks are excluded from the autonomous dispatcher, so this delay
	// is the only cadence guard for their marker-owned continuation chain.
	defaultQBSyncContinuationAfter = 5 * time.Second
	// The stall window is not a constant here: the accepted input freezes it as
	// limits.stall_seconds (the Global download_timeouts setting) and
	// download_finish_sync judges it under the owner lock.
	maxReasonLength           = 2048
	maxProviderFiles          = 32
	maxProviderText           = 512
	maxProviderPathBytes      = 4096
	maxProviderCorrelationRaw = 240 * 1024
)

var (
	ErrInvalidClaim        = errors.New("qB work item claim must target a download task")
	ErrFrozenInputInvalid  = errors.New("download task frozen input is invalid")
	ErrProviderCorrelation = errors.New("download task provider correlation is missing")
)

// Provider is the narrow external qB port required by the typed handlers.
type Provider interface {
	LookupByOperationKey(context.Context, string) (domain.ProviderTask, bool, error)
	Sync(context.Context, string) (domain.ProviderTask, error)
	ClassifyProviderError(error) domain.ProviderErrorClass
}

// SnapshotProvider methods are optional so narrow fakes keep compiling.
// Production ConfiguredClient implements the full frozen-payload surface.
type SnapshotProvider interface {
	qbittorrent.SnapshotFileSubmitter
	qbittorrent.SnapshotLookup
	qbittorrent.SnapshotSyncer
}

// RuntimeConfigSource resolves the current internal qB connection projection.
// It deliberately exposes no public DTO and is separate from the provider port
// so unit tests can inject an unavailable or changing configuration source.
type RuntimeConfigSource interface {
	GetQBittorrentRuntimeConfig(context.Context) (qbittorrent.RuntimeConfig, error)
}

// Service implements qb_submit and qb_sync. The two handlers share the same
// owner transaction helpers but never create a periodic successor WorkItem.
type Service struct {
	db                    *sql.DB
	workItems             *workitems.Store
	provider              Provider
	runtimeConfig         RuntimeConfigSource
	downloadSubmittedSink plugintargetport.BusinessDeliveryEnqueuer
	retryAfter            time.Duration
	diagnostics           observabilityport.Recorder
	syncMu                sync.Mutex
	syncFailures          int
	syncChecked           bool
	logger                observabilityport.RuntimeLogger
	eventHub              *events.Hub
}

// SetDiagnosticRecorder wires the optional diagnostic episode boundary.
// Diagnostic writes are best-effort after-commit work and never block the
// business path.
func (s *Service) SetDiagnosticRecorder(recorder observabilityport.Recorder, logger observabilityport.RuntimeLogger) {
	s.diagnostics = recorder
	s.logger = logger
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

func NewService(db *sql.DB, workItems *workitems.Store, provider Provider, runtimeConfig RuntimeConfigSource) (*Service, error) {
	if db == nil {
		return nil, errors.New("download service database is nil")
	}
	if workItems == nil {
		return nil, errors.New("download service work item store is nil")
	}
	if provider == nil {
		return nil, errors.New("download service qB provider is nil")
	}
	if runtimeConfig == nil {
		return nil, errors.New("download service runtime config source is nil")
	}
	return &Service{db: db, workItems: workItems, provider: provider, runtimeConfig: runtimeConfig,
		retryAfter: defaultRetryAfter}, nil
}

// SetBusinessDeliveryEnqueuer wires the optional clean-V2 notification side
// channel. The owner package depends only on the narrow transaction port.
func (s *Service) SetBusinessDeliveryEnqueuer(enqueuer plugintargetport.BusinessDeliveryEnqueuer) {
	if s != nil {
		s.downloadSubmittedSink = enqueuer
	}
}

// Handle dispatches only the two qB owner kinds.
func (s *Service) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if s == nil || s.db == nil || s.workItems == nil || s.provider == nil || s.runtimeConfig == nil {
		return worker.Outcome{}, errors.New("download service is not configured")
	}
	if ctx == nil {
		return worker.Outcome{}, errors.New("download handler context is nil")
	}
	var outcome worker.Outcome
	var err error
	switch claim.Kind {
	case workitems.KindQBSubmit:
		outcome, err = s.handleSubmit(ctx, claim)
	case workitems.KindQBSync:
		outcome, err = s.typedSync(ctx, claim)
	default:
		return worker.Outcome{}, fmt.Errorf("unsupported qB work item kind %q", claim.Kind)
	}
	if err != nil {
		return outcome, err
	}
	// One-shot disposition: the typed external-effect result settles the
	// marker-owned admission lease. Ambiguous outcomes keep the lease so
	// reclamation stays blocked until lookup or recovery proves the qB state.
	if claim.OneShot != nil {
		outcome.Disposition = downloadDisposition(outcome)
	}
	if s.eventHub != nil {
		outcome.AfterCommit = s.eventHub.AfterCommitFor(events.TargetRSS, claim.ManagedAnimeID, outcome.AfterCommit)
	}
	return outcome, nil
}

// downloadDisposition classifies a qB handler outcome for one-shot settlement.
//
//   - Successful finishes observe the provider (lookup reconciled, submit
//     accepted, sync observed, stall backstop with known provider state):
//     confirmed.
//   - Early exits before any provider call (owner already changed or already
//     recorded, frozen input/correlation invalid, payload missing): provably no
//     external effect this attempt.
//   - Terminal provider failures carry a known external disposition (the task
//     definitively failed in qB): confirmed.
//   - Retries are timeout/retryable/unknown by definition: ambiguous.
func downloadDisposition(outcome worker.Outcome) workitems.EffectDisposition {
	switch outcome.Kind {
	case worker.OutcomeFinish:
		if outcome.TerminalState == workitems.StateSucceeded {
			return workitems.DispositionConfirmed
		}
		switch outcome.OwnerResult.ReasonCode {
		case "download_task_already_changed", "provider_task_already_recorded",
			"frozen_input_invalid", "torrent_payload_missing",
			"provider_correlation_invalid", "provider_correlation_missing":
			return workitems.DispositionNoExternalEffect
		default:
			return workitems.DispositionConfirmed
		}
	case worker.OutcomeRetry:
		return workitems.DispositionAmbiguous
	default:
		return workitems.DispositionAmbiguous
	}
}

var _ worker.Handler = (*Service)(nil)

// QBSubmitHandler restricts registration to qb_submit.
type QBSubmitHandler struct{ service *Service }

func (h QBSubmitHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if h.service == nil {
		return worker.Outcome{}, errors.New("qB submit service is nil")
	}
	if claim.Kind != workitems.KindQBSubmit {
		return worker.Outcome{}, fmt.Errorf("qB submit handler received %q", claim.Kind)
	}
	return h.service.Handle(ctx, claim)
}

// QBSyncHandler restricts registration to qb_sync.
type QBSyncHandler struct{ service *Service }

func (h QBSyncHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if h.service == nil {
		return worker.Outcome{}, errors.New("qB sync service is nil")
	}
	if claim.Kind != workitems.KindQBSync {
		return worker.Outcome{}, fmt.Errorf("qB sync handler received %q", claim.Kind)
	}
	return h.service.Handle(ctx, claim)
}

func (s *Service) SubmitHandler() worker.Handler { return QBSubmitHandler{service: s} }
func (s *Service) SyncHandler() worker.Handler   { return QBSyncHandler{service: s} }

// taskSnapshot contains only internal owner data. In particular, frozenInput
// is never returned from this package as a public read model.
type taskSnapshot struct {
	id             string
	releaseID      string
	connectionID   sql.NullString
	providerRaw    []byte
	frozenInputRaw []byte
	state          string
	idempotencyKey string
	correlationID  sql.NullString
	version        int64
}

type frozenInput struct {
	ReleaseStableItemKey string                `json:"release_stable_item_key"`
	SelectedRelease      json.RawMessage       `json:"selected_release,omitempty"`
	SelectedSeed         json.RawMessage       `json:"selected_seed,omitempty"`
	TargetFacts          json.RawMessage       `json:"target_facts,omitempty"`
	TargetSetDigest      string                `json:"target_set_digest,omitempty"`
	SourceLocatorDisplay string                `json:"source_locator_display"`
	TorrentInputID       string                `json:"torrent_input_id"`
	TorrentPayloadDigest string                `json:"torrent_payload_digest"`
	TorrentFilename      string                `json:"torrent_filename"`
	TorrentParserVersion string                `json:"torrent_parser_version"`
	TorrentVersion       string                `json:"torrent_version"`
	InfoHash             string                `json:"info_hash,omitempty"`
	InfoHashV1           string                `json:"info_hash_v1,omitempty"`
	InfoHashV2           string                `json:"info_hash_v2,omitempty"`
	ReleaseFiles         []json.RawMessage     `json:"release_files,omitempty"`
	PathSnapshot         *domain.PathSnapshot  `json:"path_snapshot,omitempty"`
	AdoptionProvenance   *admission.Provenance `json:"adoption_provenance,omitempty"`
}

// providerCorrelation is intentionally an internal DownloadTask owner
// snapshot. InfoHash is the hash returned by qB/provider inspection and is
// therefore provider_info_hash, not the RSS/torrent parser source fact stored
// in frozen_input. Public generated DTO decoding remains limited to its known
// stable_tag/info_hash/external_id fields. Files contain only bounded provider
// facts, never URLs, credentials, or opaque provider payloads.
type providerCorrelation struct {
	ExternalID string  `json:"external_id,omitempty"`
	StableTag  string  `json:"stable_tag,omitempty"`
	InfoHash   string  `json:"info_hash,omitempty"`
	Name       string  `json:"name,omitempty"`
	Progress   float64 `json:"progress,omitempty"`
	State      string  `json:"state,omitempty"`
	// ProviderState is the raw qB status behind State. State collapses
	// pauseddl/queueddl/stalleddl/downloading into one owner state, so only this
	// field can tell a stalled torrent from a deliberately held one.
	ProviderState string `json:"provider_state,omitempty"`
	// DownloadSpeed is the provider's download rate (bytes/s) at this
	// observation, kept for the execution-record display only; nothing in the
	// owner pipeline reads it.
	DownloadSpeed *int64                 `json:"download_speed,omitempty"`
	ArtifactPath  string                 `json:"artifact_path,omitempty"`
	FileCount     int                    `json:"file_count,omitempty"`
	Files         []providerFileSnapshot `json:"files,omitempty"`
	FilesComplete bool                   `json:"files_complete"`
	PathSnapshot  *domain.PathSnapshot   `json:"path_snapshot,omitempty"`
}

type providerFileSnapshot struct {
	Path      string  `json:"path"`
	SizeBytes int64   `json:"size_bytes"`
	MediaKind string  `json:"media_kind,omitempty"`
	Language  string  `json:"language,omitempty"`
	Progress  float64 `json:"progress,omitempty"`
}

func (s *Service) loadTask(ctx context.Context, taskID string, lock bool) (taskSnapshot, error) {
	if strings.TrimSpace(taskID) == "" {
		return taskSnapshot{}, ErrInvalidClaim
	}
	query := `SELECT id, release_id, qbittorrent_connection_id, provider_correlation, frozen_input, state, idempotency_key, version FROM download_tasks WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	var task taskSnapshot
	if err := s.db.QueryRowContext(ctx, query, taskID).Scan(&task.id, &task.releaseID, &task.connectionID, &task.providerRaw, &task.frozenInputRaw, &task.state, &task.idempotencyKey, &task.version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return taskSnapshot{}, fmt.Errorf("download task %q not found: %w", taskID, err)
		}
		return taskSnapshot{}, fmt.Errorf("read download task: %w", err)
	}
	return task, nil
}

func decodeFrozenInput(raw []byte) (frozenInput, error) {
	var input frozenInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return frozenInput{}, fmt.Errorf("%w: %v", ErrFrozenInputInvalid, err)
	}
	if strings.TrimSpace(input.SourceLocatorDisplay) == "" {
		return frozenInput{}, fmt.Errorf("%w: source locator display is required", ErrFrozenInputInvalid)
	}
	if strings.TrimSpace(input.TorrentInputID) == "" || strings.TrimSpace(input.TorrentPayloadDigest) == "" || strings.TrimSpace(input.TorrentFilename) == "" || strings.TrimSpace(input.TorrentParserVersion) == "" || strings.TrimSpace(input.TorrentVersion) == "" {
		return frozenInput{}, fmt.Errorf("%w: program-owned torrent payload reference is incomplete", ErrFrozenInputInvalid)
	}
	if !strings.HasPrefix(strings.TrimSpace(input.TorrentPayloadDigest), "sha256:") {
		return frozenInput{}, fmt.Errorf("%w: torrent payload digest is invalid", ErrFrozenInputInvalid)
	}
	if input.PathSnapshot != nil {
		normalized, err := qbittorrent.NormalizePathSnapshot(*input.PathSnapshot)
		if err != nil || strings.TrimSpace(normalized.RemoteRoot) == "" {
			if err == nil {
				err = errors.New("remote root is required")
			}
			return frozenInput{}, fmt.Errorf("%w: path snapshot is invalid: %v", ErrFrozenInputInvalid, err)
		}
		input.PathSnapshot = &normalized
	}
	return input, nil
}

func decodeCorrelation(raw []byte) (providerCorrelation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return providerCorrelation{}, nil
	}
	var correlation providerCorrelation
	if err := json.Unmarshal([]byte(trimmed), &correlation); err != nil {
		return providerCorrelation{}, fmt.Errorf("decode provider correlation: %w", err)
	}
	return correlation, nil
}

func hasProviderCorrelation(raw []byte) bool {
	correlation, err := decodeCorrelation(raw)
	return err == nil && strings.TrimSpace(correlation.ExternalID) != ""
}

func encodeCorrelation(task domain.ProviderTask) ([]byte, providerCorrelation, error) {
	artifactPath, artifactPathOK := providerPath(task.Path)
	if strings.TrimSpace(task.Path) == "" {
		artifactPathOK = true
	}
	correlation := providerCorrelation{
		ExternalID:    boundedSafeText(task.ID, maxProviderText),
		InfoHash:      boundedSafeText(task.Hash, maxProviderText),
		Name:          boundedSafeText(task.Name, maxProviderText),
		Progress:      clampProgress(task.Progress),
		State:         boundedSafeText(string(task.State), maxProviderText),
		ProviderState: boundedSafeText(task.ProviderState, maxProviderText),
		ArtifactPath:  artifactPath,
	}
	if task.DownloadSpeed != nil && *task.DownloadSpeed >= 0 {
		speed := *task.DownloadSpeed
		correlation.DownloadSpeed = &speed
	}
	if task.PathSnapshot != nil {
		normalized, err := qbittorrent.NormalizePathSnapshot(*task.PathSnapshot)
		if err != nil {
			return nil, providerCorrelation{}, fmt.Errorf("%w: path snapshot: %v", ErrProviderCorrelation, err)
		}
		correlation.PathSnapshot = &normalized
	}
	if strings.HasPrefix(correlation.ExternalID, "tag:") {
		correlation.StableTag = boundedSafeText(strings.TrimPrefix(correlation.ExternalID, "tag:"), maxProviderText)
	}
	if correlation.ExternalID == "" {
		return nil, providerCorrelation{}, ErrProviderCorrelation
	}

	entries, fileCount, complete := encodeProviderFiles(task)
	correlation.Files = entries
	correlation.FileCount = fileCount
	correlation.FilesComplete = complete && artifactPathOK
	if len(entries) > 0 && (!artifactPathOK || strings.TrimSpace(task.Path) == "") {
		correlation.ArtifactPath = primaryProviderPath(entries)
		artifactPathOK = correlation.ArtifactPath != ""
		correlation.FilesComplete = complete && artifactPathOK
	}
	if correlation.ArtifactPath != "" && len(entries) > 0 && !providerFilesContain(entries, correlation.ArtifactPath) {
		correlation.FilesComplete = false
	}
	if correlation.ArtifactPath != "" && !safeProviderPath(correlation.ArtifactPath, maxProviderPathBytes) {
		correlation.ArtifactPath = ""
		correlation.FilesComplete = false
	}

	raw, err := marshalCorrelation(correlation)
	if err != nil {
		return nil, providerCorrelation{}, err
	}
	return raw, correlation, nil
}

// marshalCorrelation encodes a correlation under the same size bound the column
// enforces, so a re-encode after a local field update cannot slip past it.
func marshalCorrelation(correlation providerCorrelation) ([]byte, error) {
	raw, err := json.Marshal(correlation)
	if err != nil {
		return nil, fmt.Errorf("encode provider correlation: %w", err)
	}
	if len(raw) > maxProviderCorrelationRaw {
		return nil, fmt.Errorf("%w: provider correlation is too large", ErrProviderCorrelation)
	}
	return raw, nil
}

func encodeProviderFiles(task domain.ProviderTask) ([]providerFileSnapshot, int, bool) {
	files := task.Files
	fromPaths := len(files) == 0 && len(task.Paths) > 0
	if fromPaths {
		files = make([]domain.ProviderFile, 0, len(task.Paths))
		for _, path := range task.Paths {
			files = append(files, domain.ProviderFile{Path: path})
		}
	}

	fileCount := len(files)
	complete := len(task.Files) > 0
	if fileCount > maxProviderFiles {
		complete = false
		fileCount = maxProviderFiles
		files = files[:maxProviderFiles]
	}
	entries := make([]providerFileSnapshot, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		path, pathOK := providerPath(file.Path)
		mediaKind, mediaOK := boundedProviderField(file.MediaKind, maxProviderText)
		language, languageOK := boundedProviderField(file.Language, maxProviderText)
		if !pathOK {
			complete = false
			continue
		}
		key := strings.ReplaceAll(path, "\\", "/")
		if _, exists := seen[key]; exists {
			complete = false
			continue
		}
		seen[key] = struct{}{}
		if file.SizeBytes < 0 || !mediaOK || !languageOK {
			complete = false
		}
		entries = append(entries, providerFileSnapshot{
			Path:      path,
			SizeBytes: maxInt64(file.SizeBytes, 0),
			MediaKind: mediaKind,
			Language:  language,
			Progress:  clampProgress(file.Progress),
		})
	}
	if len(entries) != fileCount {
		complete = false
	}
	if fromPaths {
		complete = false
	}
	if len(entries) == 0 {
		if fallback, ok := providerPath(task.Path); ok {
			entries = append(entries, providerFileSnapshot{Path: fallback, Progress: clampProgress(task.Progress)})
			if fileCount == 0 {
				fileCount = 1
			}
		}
		complete = false
	}
	return entries, fileCount, complete
}

func providerPath(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n\x00") {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{"passkey=", "password=", "token=", "secret=", "credential="} {
		if strings.Contains(lower, marker) {
			return "", false
		}
	}
	if strings.Contains(lower, "://") {
		return "", false
	}
	if len(trimmed) > maxProviderPathBytes {
		return trimmed[:maxProviderPathBytes], false
	}
	return trimmed, true
}

func primaryProviderPath(entries []providerFileSnapshot) string {
	best := ""
	bestRank := 3
	for _, entry := range entries {
		rank := 2
		if entry.MediaKind == "video" {
			rank = 0
		} else if entry.MediaKind == "subtitle" {
			rank = 1
		}
		path := strings.ReplaceAll(entry.Path, "\\", "/")
		if best == "" || rank < bestRank || (rank == bestRank && path < best) {
			best, bestRank = entry.Path, rank
		}
	}
	return best
}

func providerFilesContain(entries []providerFileSnapshot, path string) bool {
	path = strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	for _, entry := range entries {
		if strings.ReplaceAll(entry.Path, "\\", "/") == path {
			return true
		}
	}
	return false
}

func boundedProviderField(value string, max int) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", true
	}
	if len(trimmed) > max || unsafeProviderText(trimmed) {
		return "", false
	}
	return trimmed, true
}

func boundedSafeText(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if unsafeProviderText(trimmed) {
		return ""
	}
	if len(trimmed) > max {
		return trimmed[:max]
	}
	return trimmed
}

func unsafeProviderText(value string) bool {
	if strings.ContainsAny(value, "\r\n\x00") {
		return true
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") {
		return true
	}
	for _, marker := range []string{"passkey=", "password=", "token=", "secret=", "credential=", "cookie="} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func safeProviderPath(value string, max int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"passkey=", "password=", "token=", "secret=", "credential="} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return !strings.Contains(lower, "://")
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func frozenInfoHashCandidates(input frozenInput) []string {
	values := []string{input.InfoHash, input.InfoHashV1, input.InfoHashV2}
	if len(input.InfoHashV2) >= 40 {
		values = append(values, input.InfoHashV2[:40])
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Service) syncProvider(ctx context.Context, providerID string, snapshot *domain.PathSnapshot) (domain.ProviderTask, error) {
	if snapshot != nil {
		if provider, ok := s.provider.(qbittorrent.SnapshotSyncer); ok {
			return provider.SyncWithSnapshot(ctx, providerID, *snapshot)
		}
	}
	return s.provider.Sync(ctx, providerID)
}

func pathSnapshotFromInput(input frozenInput) *domain.PathSnapshot {
	return input.PathSnapshot
}

func pathSnapshotFromCorrelation(correlation providerCorrelation) *domain.PathSnapshot {
	return correlation.PathSnapshot
}

func currentPathSnapshot(config qbittorrent.RuntimeConfig) (domain.PathSnapshot, error) {
	snapshot, err := qbittorrent.DerivePathSnapshot(config)
	if err != nil {
		return domain.PathSnapshot{}, fmt.Errorf("%w: invalid qB path snapshot: %v", qbittorrent.ErrRuntimeConfigUnavailable, err)
	}
	return snapshot, nil
}

func frozenSnapshotFromRaw(raw []byte) (*domain.PathSnapshot, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var input struct {
		PathSnapshot *domain.PathSnapshot `json:"path_snapshot,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &input); err != nil {
		return nil, fmt.Errorf("decode frozen path snapshot: %w", err)
	}
	if input.PathSnapshot == nil {
		return nil, nil
	}
	normalized, err := qbittorrent.NormalizePathSnapshot(*input.PathSnapshot)
	if err != nil || strings.TrimSpace(normalized.RemoteRoot) == "" {
		if err == nil {
			err = errors.New("remote root is required")
		}
		return nil, fmt.Errorf("invalid frozen path snapshot: %w", err)
	}
	return &normalized, nil
}

func applyProviderSnapshot(task domain.ProviderTask, expected *domain.PathSnapshot) (domain.ProviderTask, error) {
	if expected == nil {
		if task.PathSnapshot == nil {
			return task, nil
		}
		normalized, err := qbittorrent.NormalizePathSnapshot(*task.PathSnapshot)
		if err != nil {
			return domain.ProviderTask{}, err
		}
		task.PathSnapshot = &normalized
		return task, nil
	}
	if task.PathSnapshot == nil {
		frozen := *expected
		task.PathSnapshot = &frozen
		return task, nil
	}
	normalized, err := qbittorrent.NormalizePathSnapshot(*task.PathSnapshot)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	if !qbittorrent.PathSnapshotsEqual(normalized, *expected) {
		return domain.ProviderTask{}, qbittorrent.ErrPathSnapshotChanged
	}
	task.PathSnapshot = &normalized
	return task, nil
}

func (s *Service) freezeTaskPathSnapshot(ctx context.Context, claim workitems.Claim, task taskSnapshot, input frozenInput, snapshot domain.PathSnapshot) (workitems.Claim, taskSnapshot, frozenInput, error) {
	frozen := snapshot
	input.PathSnapshot = &frozen
	raw, err := json.Marshal(input)
	if err != nil {
		return claim, task, frozenInput{}, fmt.Errorf("encode frozen path snapshot: %w", err)
	}
	if !json.Valid(raw) {
		return claim, task, frozenInput{}, errors.New("validate frozen path snapshot: encoded input is invalid JSON")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return claim, task, frozenInput{}, fmt.Errorf("begin freeze path snapshot: %w", err)
	}
	defer tx.Rollback()
	var newVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT download_freeze_path_snapshot($1,$2,$3,$4,$5)`,
		task.id, claim.ID, claim.FencingToken, claim.ExpectedOwnerVersion, raw).Scan(&newVersion); err != nil {
		return claim, task, frozenInput{}, fmt.Errorf("freeze download task path snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return claim, task, frozenInput{}, fmt.Errorf("commit frozen path snapshot: %w", err)
	}
	claim.ExpectedOwnerVersion = newVersion
	task.frozenInputRaw = raw
	task.version = newVersion
	return claim, task, input, nil
}

func syncRetryOwnerState(claim workitems.Claim, task taskSnapshot) string {
	if claim.Kind != workitems.KindQBSync {
		return "waiting_retry"
	}
	if task.state == "submitting" || task.state == "downloading" {
		return task.state
	}
	correlation, err := decodeCorrelation(task.providerRaw)
	if err != nil || strings.TrimSpace(correlation.ExternalID) == "" {
		return "waiting_retry"
	}
	state := taskStateFromProvider(domain.TaskState(correlation.State))
	if state == "submitting" || state == "downloading" {
		return state
	}
	return "waiting_retry"
}

func (s *Service) handleSubmit(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Owner.Type != workitems.OwnerDownloadTask || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, ErrInvalidClaim
	}
	if _, err := s.loadTask(ctx, claim.Owner.ID, false); err != nil {
		return worker.Outcome{}, err
	}
	outcome, err := s.typedSubmit(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeCommitted {
		return outcome, err
	}
	switch outcome.OwnerResult.State {
	case "failed":
		// Failures that reached finishUnstartedSubmission already carry the
		// diagnostic built from their cause.
		if outcome.AfterCommit == nil {
			outcome = worker.WithAfterCommit(outcome, observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, observabilityport.InternalFailure(observabilityport.InternalDownloadSubmit, "")))
		}
	case "submitting", "downloading", "completed":
		outcome = worker.WithAfterCommit(outcome, func(ctx context.Context) {
			target := s.downloaderTarget(ctx)
			for _, request := range []observabilityport.DiagnosticSuccessRequest{
				{Kind: observabilityport.KindDownloader, TargetID: target},
				observabilityport.InternalSuccess(observabilityport.InternalDownloadSubmit),
			} {
				if record := observabilityport.DiagnosticSuccessAfterCommit(s.diagnostics, s.logger, request); record != nil {
					record(ctx)
				}
			}
		})
	}
	return outcome, nil
}

func managedAnimeIDForDownloadTask(ctx context.Context, tx *sql.Tx, taskID string) (string, error) {
	var managedAnimeID string
	err := tx.QueryRowContext(ctx, `
SELECT sub.managed_anime_id
FROM download_tasks task
JOIN releases release ON release.id = task.release_id
JOIN rss_sources source ON source.id = release.rss_source_id
JOIN subscriptions sub ON sub.id = source.subscription_id
WHERE task.id = $1`, taskID).Scan(&managedAnimeID)
	if err != nil {
		return "", fmt.Errorf("resolve managed anime for download task: %w", err)
	}
	return managedAnimeID, nil
}

func (s *Service) enqueueDownloadSubmittedTx(ctx context.Context, tx *sql.Tx, task taskSnapshot, version int64, correlation providerCorrelation) error {
	// The "submitted" run-log entry is derived after commit by the
	// independent acquisition Activity consumer; only the notification
	// admission stays in this transaction.
	if s.downloadSubmittedSink == nil {
		return nil
	}
	managedAnimeID, err := managedAnimeIDForDownloadTask(ctx, tx, task.id)
	if err != nil {
		return err
	}
	var row downloadNotificationRow
	if err := tx.QueryRowContext(ctx, `
SELECT anime.title, COALESCE(anime.title_override, ''), release.raw_facts, release.parsed_facts, release.source_facts,
       input.input, connection.name,
       (SELECT snap.release_identity FROM media_fact_snapshots snap
        WHERE snap.release_id = task.release_id ORDER BY snap.created_at DESC, snap.id DESC LIMIT 1),
       (SELECT snap.release_facts->'batch_plan'->'coverage_keys' FROM media_fact_snapshots snap
        WHERE snap.release_id = task.release_id ORDER BY snap.created_at DESC, snap.id DESC LIMIT 1),
       torrent_input.payload
FROM download_tasks task
JOIN releases release ON release.id = task.release_id
JOIN rss_sources source ON source.id = release.rss_source_id
JOIN subscriptions sub ON sub.id = source.subscription_id
JOIN managed_anime anime ON anime.id = sub.managed_anime_id
JOIN acquisitions input ON input.id = task.acquisition_id
LEFT JOIN connections connection ON connection.id = task.qbittorrent_connection_id
LEFT JOIN release_torrent_inputs torrent_input ON torrent_input.release_id = task.release_id
WHERE task.id = $1`, task.id).Scan(&row.animeTitle, &row.titleOverride, &row.rawFacts, &row.parsedFacts, &row.sourceFacts,
		&row.inputSnapshot, &row.clientName, &row.releaseIdentity, &row.coverageKeys, &row.torrentPayload); err != nil {
		return fmt.Errorf("read download notification facts: %w", err)
	}
	{
		payload, err := json.Marshal(downloadSubmittedFacts(row, managedAnimeID, task, correlation))
		if err != nil {
			return fmt.Errorf("encode download notification facts: %w", err)
		}
		if err := s.downloadSubmittedSink.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), plugintargetport.BusinessDeliveryRequest{
			SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: task.id, SourceOwnerVersion: version,
			ManagedAnimeID: managedAnimeID, TriggerKind: plugintargetport.TriggerDownloadSubmitted,
			Payload: payload, ResultKey: task.idempotencyKey,
		}); err != nil {
			return fmt.Errorf("enqueue download submitted delivery: %w", err)
		}
	}
	return nil
}

// stallExemptProviderStates lists the raw qB statuses that positively mean the
// user or qB is deliberately holding or verifying the torrent, so their stillness
// is expected rather than a fault (BC 4.2). The set is deliberately narrower than
// the pre-S1b one: queued, queueddl, allocating and moving used to be exempt, but
// their stillness is exactly what this backstop exists to catch, and an unknown or
// empty status is never exempt because exemption requires positive evidence.
//
// Exemption skips one round's stall judgment only. It does not reset the progress
// clock and does not credit the paused time, so a resumed torrent can stall
// immediately and the total download window still covers the pause. Changing the
// set is a policy change and requires a new policy version, not a silent edit.
var stallExemptProviderStates = map[string]bool{
	"pauseddl":           true,
	"stoppeddl":          true,
	"checkingdl":         true,
	"checkingup":         true,
	"checkingresumedata": true,
}

// stallExempt reports whether a raw provider state excuses a lack of progress.
// An empty or unrecognized state is never exempt.
func stallExempt(providerState string) bool {
	return stallExemptProviderStates[strings.ToLower(strings.TrimSpace(providerState))]
}

func taskStateFromProvider(state domain.TaskState) string {
	switch state {
	case domain.TaskCompleted:
		return "completed"
	case domain.TaskDownloading:
		return "downloading"
	case domain.TaskSubmitted, domain.TaskQueued:
		return "submitting"
	case domain.TaskFailed:
		return "failed"
	default:
		return "submitting"
	}
}

func clampProgress(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

type downloadNotificationRow struct {
	animeTitle, titleOverride string
	rawFacts, parsedFacts     []byte
	sourceFacts               []byte
	inputSnapshot             []byte
	clientName                sql.NullString
	releaseIdentity           []byte
	// coverageKeys is the accepted batch plan's placeable coverage.
	coverageKeys []byte
	// torrentPayload is the retained bencode payload the release was accepted
	// with; nil when none is retained any more.
	torrentPayload []byte
}

// torrentTreeText renders the one-level listing of a retained bencode
// payload. An absent or unreadable payload is an empty fact, never an error:
// a release can outlive its payload and the notice still has to go out.
func torrentTreeText(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}
	metadata, err := torrent.Parse(payload)
	if err != nil {
		return nil
	}
	files := make([]torrenttree.File, 0, len(metadata.Files))
	for _, file := range metadata.Files {
		files = append(files, torrenttree.File{Path: file.Path, SizeBytes: file.SizeBytes})
	}
	if text := torrenttree.Render(files); text != "" {
		return text
	}
	return nil
}

// downloadSubmittedFacts builds the download_submitted payload. The shared
// catalog facts come from the frozen acquisition input snapshot, ep.*
// from the release's frozen episode identity, vars/rt/anibt from the frozen
// Vars output and source facts, download.client from the connection the
// task was submitted to. The group is the input snapshot's (anime.group);
// there is no parsed-title fallback. release.episodes labels the accepted
// plan's episodes ("01-04", "01, 03, 07"); a batch has no single episode, so
// ep.* are empty for it. torrent.tree is rendered here, from the retained
// bencode payload, so no render ever re-parses it.
func downloadSubmittedFacts(row downloadNotificationRow, managedAnimeID string, task taskSnapshot, correlation providerCorrelation) map[string]any {
	var raw map[string]any
	_ = json.Unmarshal(row.rawFacts, &raw)
	animeTitle := row.animeTitle
	var customTitle any
	if row.titleOverride != "" {
		animeTitle, customTitle = row.titleOverride, row.titleOverride
	}
	facts := notification.InputFacts(row.inputSnapshot)
	facts["torrent.tree"] = torrentTreeText(row.torrentPayload)
	for key, value := range notification.EpisodeFacts(releaseIdentityFact(row.releaseIdentity)) {
		facts[key] = value
	}
	var coverage []string
	_ = json.Unmarshal(row.coverageKeys, &coverage)
	plan := batchplan.Plan{CoverageKeys: coverage}
	facts["release.episodes"] = nil
	if _, episodes, ok := plan.Episodes(); ok {
		facts["release.episodes"] = batchplan.EpisodesLabel(episodes)
	}
	if len(coverage) > 1 {
		for key := range notification.EpisodeFacts(nil) {
			facts[key] = nil
		}
	}
	// vars frozen at accept; rt/anibt from the source facts this submit fixes.
	providerText := func(value string) any { return nullableFact(bounded(value, maxProviderText)) }
	for key, value := range notification.ValueFacts(notification.InputVarsOutput(row.inputSnapshot), row.sourceFacts, providerText) {
		facts[key] = value
	}
	for key, value := range notification.TitleFacts(row.inputSnapshot, providerText) {
		facts[key] = value
	}
	for key, value := range map[string]any{
		"anime.title": animeTitle, "anime.id": managedAnimeID, "anime.title_override": customTitle,
		"download.task": nullableFact(correlation.Name), "download.client": nullableFact(row.clientName.String),
		"torrent.name":     nullableFact(correlation.Name),
		"download_task_id": task.id, "release_id": task.releaseID, "release.id": task.releaseID,
		"release.title":        stringFact(raw, "title"),
		"release.published_at": nullableFact(stringFact(raw, "published_at")),
		"provider_task_id":     correlation.ExternalID, "provider_info_hash": correlation.InfoHash, "task_name": correlation.Name,
		"event.type": plugintargetport.TriggerDownloadSubmitted, "result.outcome": "success",
	} {
		facts[key] = value
	}
	return facts
}

// releaseIdentityFact adapts a media fact snapshot's EpisodeIdentity (stored
// with Go field names) to the frozen identity shape notification.EpisodeFacts
// reads.
func releaseIdentityFact(raw []byte) any {
	var identity struct {
		State      string
		RawEpisode *string
		Episode    *string
		Offset     int
	}
	if len(raw) == 0 || json.Unmarshal(raw, &identity) != nil {
		return nil
	}
	fv := func(value *string) any {
		if value == nil {
			return map[string]any{"state": "absent", "value": nil}
		}
		return map[string]any{"state": "known", "value": *value}
	}
	return map[string]any{"state": "known", "value": map[string]any{
		"state": identity.State, "raw_episode": fv(identity.RawEpisode), "episode": fv(identity.Episode), "offset": json.Number(fmt.Sprint(identity.Offset)),
	}}
}

func nullableFact(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func stringFact(facts map[string]any, key string) string {
	if value, ok := facts[key]; ok && value != nil {
		return bounded(fmt.Sprint(value), maxProviderText)
	}
	return ""
}

func bounded(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func safeReason(value string) string {
	safe, _ := redact.Apply(value, maxReasonLength, redact.Mask, redact.Redacted)
	return safe
}

func redactProviderError(err error) string {
	if err == nil {
		return "qBittorrent operation failed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "qBittorrent operation timed out; external result will be looked up before retry"
	case strings.Contains(message, "connection"), strings.Contains(message, "no usable"), strings.Contains(message, "credentials"):
		return "qBittorrent connection is unavailable"
	case strings.Contains(message, "not found"):
		return "qBittorrent task was not found"
	default:
		return "qBittorrent operation failed"
	}
}

// downloaderTarget is the diagnostic target of the configured downloader: its
// connection id, or a fixed label when the runtime configuration cannot be read.
func (s *Service) downloaderTarget(ctx context.Context) string {
	if s.runtimeConfig != nil {
		if config, err := s.runtimeConfig.GetQBittorrentRuntimeConfig(ctx); err == nil && config.ConnectionID != "" {
			return config.ConnectionID
		}
	}
	return "qbittorrent"
}

// submitFailureDiagnostic maps a failed submission onto the catalog from the
// cause in hand: outbound facts mean the downloader could not be reached,
// refused credentials mean its login is wrong, and everything else (an unreadable
// runtime configuration included) is a product fault that stays internal. The
// task is the affected unit.
func (s *Service) submitFailureDiagnostic(taskID, code string, cause error) func(context.Context) {
	if code != "qb_connection_unavailable" {
		condition := observabilityport.Condition{Kind: observabilityport.KindDownloader}
		var failure outbound.Failure
		if facts, ok := outbound.FailureOf(cause); ok {
			condition.Variant, failure = observabilityport.VariantUnreachable, facts
		} else if errors.Is(cause, qbittorrent.ErrAuthFailed) {
			condition.Variant = observabilityport.VariantAuthFailed
		}
		if condition.Variant != "" {
			return func(ctx context.Context) {
				request := observabilityport.DiagnosticFailureRequest{
					Condition: condition, Severity: observabilityport.SeverityError,
					TargetID: s.downloaderTarget(ctx), TargetName: "qBittorrent", Failure: failure, AffectedID: taskID,
				}
				if record := observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, request); record != nil {
					record(ctx)
				}
			}
		}
	}
	return observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, observabilityport.InternalFailure(observabilityport.InternalDownloadSubmit, ""))
}
