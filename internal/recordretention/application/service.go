// Package application owns the code-defined retention janitor registry.
package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

const (
	// DefaultBatchSize bounds one cleaner transaction (one short DELETE).
	DefaultBatchSize             = 500
	DefaultSnapshotRetentionDays = 30
	// DefaultPassBudget bounds how long one pass keeps repeating batches. Every
	// cleaner always runs at least one batch per pass; a cleaner whose batch
	// came back full runs again (round-robin with the other unfinished
	// cleaners) until it comes back short or the budget is spent.
	DefaultPassBudget = 30 * time.Second
)

// CleanerID identifies one fixed retention class. IDs are code-owned and are
// never derived from a relation name or caller input.
type CleanerID string

const (
	CleanerPluginDeliveryAttempts CleanerID = "observability_plugin_delivery_attempts"
	CleanerPluginDeliveries       CleanerID = "observability_plugin_deliveries"
	CleanerSubscriptionLogEntries CleanerID = "observability_subscription_log_entries"
	CleanerAutomationDispatchRuns CleanerID = "observability_automation_dispatch_runs"
	CleanerDiagnosticEpisodes     CleanerID = "observability_diagnostic_episodes"
	CleanerProviderCatalog        CleanerID = "provider_catalog_snapshots"
	CleanerProviderGroups         CleanerID = "provider_group_snapshots"
	CleanerProviderFeeds          CleanerID = "provider_feed_snapshots"
	CleanerAnimeCandidates        CleanerID = "anime_config_candidates"
	CleanerWorkItems              CleanerID = "terminal_work_items"
	CleanerMediaPosterCache       CleanerID = "media_poster_cache"
	// CleanerDiscoveryReleases bounds discovery history (a Release that never
	// reached a download task) of inactive owners to 30 days. The ID names the
	// evidence class, not the owner relation, so the fixed registry keeps
	// Release as an owner-truth table.
	CleanerDiscoveryReleases CleanerID = "inactive_discovery_releases"
	// Maintenance-record classes (library content is permanent, maintenance
	// records expire). Each deletes only terminal rows
	// no live row or in-flight work references.
	CleanerIdempotencyRecords     CleanerID = "maintenance_idempotency_records"
	CleanerNotificationAdmissions CleanerID = "maintenance_notification_admissions"
	CleanerAlignmentOperations    CleanerID = "maintenance_alignment_operations"
	CleanerReconciliationFiles    CleanerID = "maintenance_reconciliation_files"
)

// ObservabilityRetention is the typed persistence surface for the five
// observability evidence classes.
type ObservabilityRetention interface {
	PurgePluginDeliveryAttempts(context.Context, txscope.Tx, int) (int64, error)
	PurgePluginDeliveries(context.Context, txscope.Tx, int) (int64, error)
	PurgeSubscriptionLogEntries(context.Context, txscope.Tx, int) (int64, error)
	PurgeAutomationDispatchRuns(context.Context, txscope.Tx, int) (int64, error)
	PurgeDiagnosticEpisodes(context.Context, txscope.Tx, int) (int64, error)
}

// CatalogRetention is the typed persistence surface for provider discovery
// snapshots. The two methods are intentionally separate despite sharing an
// owner service so each class has an independent transaction and event.
type CatalogRetention interface {
	PurgeExpiredProviderCatalogSnapshots(context.Context, txscope.Tx, int) (int64, error)
	PurgeExpiredProviderGroupSnapshots(context.Context, txscope.Tx, int) (int64, error)
	PurgeExpiredProviderFeedSnapshots(context.Context, txscope.Tx, int) (int64, error)
}

// AnimeCandidateCleanup is the bounded cleanup surface for candidates carrying
// an explicit cleanup authorization. Idle time and retention metadata alone are
// never eligibility.
type AnimeCandidateCleanup interface {
	PurgeEligibleAnimeConfigCandidates(context.Context, txscope.Tx, int) (int64, error)
}

// WorkItemRetention is the bounded terminal WorkItem cleanup surface.
type WorkItemRetention interface {
	PurgeExpiredTx(context.Context, txscope.Tx, int) (int64, error)
}

// MediaPosterCacheRetention is the bounded cleanup surface for durable poster
// bytes. It is intentionally separate from provider snapshot retention.
type MediaPosterCacheRetention interface {
	PurgeExpired(context.Context, txscope.Tx, int) (int64, error)
}

// DiscoveryRetention is the bounded cleanup surface for discovery history:
// Releases that never reached a download task, of a subscription stopped or a
// managed anime retired for the code-owned window. Acquisition evidence is
// outside eligibility by contract, so this is not a generic Release purge.
type DiscoveryRetention interface {
	PurgeExpiredDiscoveryReleases(context.Context, txscope.Tx, int) (int64, error)
}

// MaintenanceRetention is the bounded cleanup surface for maintenance records:
// HTTP replay records (30 days), settled notification admissions (90 days, so
// the dedupe ledger outlives the discovery history it dedupes), settled
// alignment operations of superseded scan generations and observation rows of
// scan generations no reader uses any more (30 days).
type MaintenanceRetention interface {
	PurgeExpiredIdempotencyRecords(context.Context, txscope.Tx, int) (int64, error)
	PurgeExpiredNotificationAdmissions(context.Context, txscope.Tx, int) (int64, error)
	PurgeExpiredAlignmentOperations(context.Context, txscope.Tx, int) (int64, error)
	PurgeSupersededReconciliationFiles(context.Context, txscope.Tx, int) (int64, error)
}

// Dependencies are the only inputs used to construct the fixed registry. No
// table, SQL statement, or runtime cleaner list is accepted here.
type Dependencies struct {
	Observability   ObservabilityRetention
	Catalog         CatalogRetention
	AnimeCandidates AnimeCandidateCleanup
	WorkItems       WorkItemRetention
	MediaPosters    MediaPosterCacheRetention
	Discovery       DiscoveryRetention
	Maintenance     MaintenanceRetention
}

type cleaner struct {
	id  CleanerID
	run func(context.Context, txscope.Tx, int) (int64, error)
}

// Registry is an immutable ordered set of the initial retention whitelist.
type Registry struct {
	cleaners []cleaner
}

// NewRegistry constructs the fixed registry in dependency-safe order.
func NewRegistry(dependencies Dependencies) (*Registry, error) {
	if dependencies.Observability == nil {
		return nil, errors.New("retention observability dependency is nil")
	}
	if dependencies.Catalog == nil {
		return nil, errors.New("retention catalog dependency is nil")
	}
	if dependencies.AnimeCandidates == nil {
		return nil, errors.New("retention anime candidate dependency is nil")
	}
	if dependencies.WorkItems == nil {
		return nil, errors.New("retention WorkItem dependency is nil")
	}
	if dependencies.MediaPosters == nil {
		return nil, errors.New("retention media poster dependency is nil")
	}
	if dependencies.Discovery == nil {
		return nil, errors.New("retention discovery dependency is nil")
	}
	if dependencies.Maintenance == nil {
		return nil, errors.New("retention maintenance dependency is nil")
	}
	// Terminal WorkItems (work_item_outcomes) go first; the other cleaners
	// do not depend on that order.
	cleaners := []cleaner{
		{id: CleanerWorkItems, run: dependencies.WorkItems.PurgeExpiredTx},
		{id: CleanerPluginDeliveryAttempts, run: dependencies.Observability.PurgePluginDeliveryAttempts},
		{id: CleanerPluginDeliveries, run: dependencies.Observability.PurgePluginDeliveries},
		{id: CleanerSubscriptionLogEntries, run: dependencies.Observability.PurgeSubscriptionLogEntries},
		{id: CleanerAutomationDispatchRuns, run: dependencies.Observability.PurgeAutomationDispatchRuns},
		{id: CleanerDiagnosticEpisodes, run: dependencies.Observability.PurgeDiagnosticEpisodes},
		{id: CleanerProviderCatalog, run: dependencies.Catalog.PurgeExpiredProviderCatalogSnapshots},
		{id: CleanerProviderGroups, run: dependencies.Catalog.PurgeExpiredProviderGroupSnapshots},
		{id: CleanerProviderFeeds, run: dependencies.Catalog.PurgeExpiredProviderFeedSnapshots},
		{id: CleanerAnimeCandidates, run: dependencies.AnimeCandidates.PurgeEligibleAnimeConfigCandidates},
		{id: CleanerMediaPosterCache, run: dependencies.MediaPosters.PurgeExpired},
		// After the run-log cleaner: an expired log row no longer holds its
		// release, so a release whose log expired in this same pass can go.
		{id: CleanerDiscoveryReleases, run: dependencies.Discovery.PurgeExpiredDiscoveryReleases},
		// After the discovery cleaner: a release_retry record of a release that
		// cleaner removed is already gone with it.
		{id: CleanerIdempotencyRecords, run: dependencies.Maintenance.PurgeExpiredIdempotencyRecords},
		{id: CleanerNotificationAdmissions, run: dependencies.Maintenance.PurgeExpiredNotificationAdmissions},
		// Alignments before observations: an open alignment holds its anime's
		// observation rows, a settled one does not.
		{id: CleanerAlignmentOperations, run: dependencies.Maintenance.PurgeExpiredAlignmentOperations},
		{id: CleanerReconciliationFiles, run: dependencies.Maintenance.PurgeSupersededReconciliationFiles},
	}
	return &Registry{cleaners: cleaners}, nil
}

// IDs returns the fixed whitelist in execution order.
func (r *Registry) IDs() []CleanerID {
	if r == nil {
		return nil
	}
	ids := make([]CleanerID, len(r.cleaners))
	for i, item := range r.cleaners {
		ids[i] = item.id
	}
	return ids
}

// Contains reports whether id is one of the fixed registered classes.
func (r *Registry) Contains(id CleanerID) bool {
	if r == nil {
		return false
	}
	for _, item := range r.cleaners {
		if item.id == id {
			return true
		}
	}
	return false
}

// PassResult reports each cleaner result. Count sums the cleaner's committed
// batches in the pass; a failed batch sets Error (the batch's rows stay) and
// later cleaners still run.
type PassResult struct {
	Results []CleanerResult
}

type CleanerResult struct {
	ID    CleanerID
	Count int64
	Error error
}

func (r PassResult) Failed() bool {
	for _, result := range r.Results {
		if result.Error != nil {
			return true
		}
	}
	return false
}

type txRunner interface {
	Run(context.Context, func(txscope.Tx) error) error
}

type dbTxRunner struct {
	db *sql.DB
}

func (r dbTxRunner) Run(ctx context.Context, fn func(txscope.Tx) error) error {
	if r.db == nil {
		return errors.New("retention janitor database is nil")
	}
	if fn == nil {
		return errors.New("retention janitor transaction callback is nil")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retention cleaner transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(txscope.Wrap(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit retention cleaner transaction: %w", err)
	}
	return nil
}

// Service runs registered cleaners in independent single-batch transactions.
type Service struct {
	runner    txRunner
	registry  *Registry
	logger    observabilityport.RuntimeLogger
	recorder  observabilityport.Recorder
	batchSize int
	budget    time.Duration
	now       func() time.Time
}

func NewService(db *sql.DB, registry *Registry, logger observabilityport.RuntimeLogger, recorder observabilityport.Recorder) (*Service, error) {
	if db == nil {
		return nil, errors.New("retention janitor database is nil")
	}
	return newServiceWithRunner(dbTxRunner{db: db}, registry, logger, recorder)
}

func newServiceWithRunner(runner txRunner, registry *Registry, logger observabilityport.RuntimeLogger, recorder observabilityport.Recorder) (*Service, error) {
	if runner == nil {
		return nil, errors.New("retention janitor transaction runner is nil")
	}
	if registry == nil || len(registry.cleaners) == 0 {
		return nil, errors.New("retention janitor registry is nil")
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	if recorder == nil {
		return nil, errors.New("retention janitor observability recorder is nil")
	}
	return &Service{runner: runner, registry: registry, logger: logger, recorder: recorder, batchSize: DefaultBatchSize, budget: DefaultPassBudget, now: time.Now}, nil
}

// RunPass executes every registered class, committing successful classes before
// moving on. A failure is isolated to its own transaction and does not prevent
// later classes from running.
func (s *Service) RunPass(ctx context.Context) (PassResult, error) {
	var pass PassResult
	if s == nil || s.runner == nil {
		return pass, errors.New("retention janitor transaction runner is nil")
	}
	if s.registry == nil {
		return pass, errors.New("retention janitor registry is nil")
	}
	// Round 1 runs every cleaner once, in registry order. Later rounds repeat
	// only the cleaners whose last batch was full, until each comes back short,
	// fails, or the pass budget is spent. Each batch is its own transaction; a
	// failed batch keeps the rows of the cleaner's earlier batches committed
	// and ends that cleaner for this pass.
	deadline := s.now().Add(s.budget)
	pass.Results = make([]CleanerResult, len(s.registry.cleaners))
	pending := make([]int, len(s.registry.cleaners))
	for i, item := range s.registry.cleaners {
		pass.Results[i].ID = item.id
		pending[i] = i
	}
	for len(pending) > 0 {
		var next []int
		for _, i := range pending {
			item := s.registry.cleaners[i]
			var count int64
			err := s.runner.Run(ctx, func(scope txscope.Tx) error {
				n, cleanerErr := item.run(ctx, scope, s.batchSize)
				if cleanerErr == nil {
					count = n
				}
				return cleanerErr
			})
			if err != nil {
				pass.Results[i].Error = err
				continue
			}
			pass.Results[i].Count += count
			if count >= int64(s.batchSize) {
				next = append(next, i)
			}
		}
		if ctx.Err() != nil || !s.now().Before(deadline) {
			break
		}
		pending = next
	}

	failed := pass.Failed()
	resultCode, outcome := observabilityport.RoutineResultRetentionPassCompleted, observabilityport.RoutineOutcomeSuccess
	if failed {
		resultCode, outcome = observabilityport.RoutineResultRetentionPassFailed, observabilityport.RoutineOutcomeFailure
	}
	message := fmt.Sprintf("retention sweep completed (cleaners=%d failed=%t deleted=%d)", len(pass.Results), failed, pass.deletedCount())
	var recordErr error
	if failed {
		recordErr = errors.Join(recordErr, s.recorder.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
			Identity: observabilityport.DiagnosticRetentionSweepFailure(), Severity: observabilityport.SeverityError,
			ScopeID: "global", LastError: "one or more retention cleaners failed",
		}))
	} else {
		recordErr = errors.Join(recordErr, s.recorder.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{
			Identity: observabilityport.DiagnosticRetentionSweepFailure(), ScopeID: "global",
		}))
	}
	recordErr = errors.Join(recordErr, s.recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: observabilityport.RoutineRetentionPassIdentity(outcome, resultCode), ScopeID: "global",
		Summary: message,
	}))
	level := "success"
	if failed || recordErr != nil {
		level = "failure"
	}
	event := observabilityport.RuntimeEvent{
		Message: message, Component: string(observabilityport.ComponentRetention),
		Operation: string(observabilityport.OperationSweep), Outcome: level,
		ReasonCode: string(resultCode),
	}
	if level == "failure" {
		s.logger.Error(ctx, event)
	} else {
		s.logger.Info(ctx, event)
	}
	if !failed && recordErr == nil {
		return pass, nil
	}
	var failures []error
	for _, result := range pass.Results {
		if result.Error != nil {
			failures = append(failures, fmt.Errorf("%s: %w", result.ID, result.Error))
		}
	}
	if recordErr != nil {
		failures = append(failures, fmt.Errorf("record retention observability: %w", recordErr))
	}
	return pass, errors.Join(failures...)
}

func (r PassResult) deletedCount() int64 {
	var count int64
	for _, result := range r.Results {
		count += result.Count
	}
	return count
}
