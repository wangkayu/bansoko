package workitems

// Finite one-shot reclamation and fail-closed cleanup.
//
// The marker (one_shot_acquisitions) is the only reclamation authority:
// origin=oneshot and state=stopped are provenance/automation-intent facts and
// are NEVER selected by any query here, so persistent Agent skeletons and
// stopped Subscriptions are unreachable. Reclamation shares the marker-first
// lock order with the effect gate and every downstream insertion; it proceeds
// only when the admission ledger is empty, active_effect_count=0, no related
// WorkItem is available or leased, and the anchored owner graph validates by
// exact identity. Anything unprovable leaves cleanup_blocked with a bounded
// reason; failed rechecks extend the deadline within the hard maximum.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
	"github.com/lib/pq"
)

// One-shot marker states (mirrors migration 065).
const (
	OneShotStateRunning        = "running"
	OneShotStateReclaimPending = "reclaim_pending"
	OneShotStateCleanupBlocked = "cleanup_blocked"
	OneShotStateReclaiming     = "reclaiming"
	OneShotStateCleaned        = "cleaned"
)

const (
	defaultOneShotBatchSize = 10
	// bounded reason fields mirror the marker column CHECKs.
	maxOneShotReasonCode = 128
	maxOneShotReason     = 1024
)

// oneShotAdoptedReleaseVerdict is the RSS/one-shot Release verdict written when
// the exact selected target set already existed and no download chain was
// created. It is the only verdict allowed with a NULL marker download task.
const oneShotAdoptedReleaseVerdict = "adopted_existing"

// OneShotReclaimResult summarizes one bounded RunPass pass.
type OneShotReclaimResult struct {
	Drained   int // running -> reclaim_pending
	Blocked   int // proof failures -> cleanup_blocked (or deadline extended)
	Reclaimed int // reclaim_pending -> reclaiming -> cleaned
	Rechecked int // cleanup_blocked rechecks evaluated
	Purged    int // cleaned markers physically deleted after retention
}

// OneShotReclaimService processes finite one-shot markers in bounded
// database-time batches. It shares only scheduling shape with evidence
// record retention; its predicate and owner scope are one-shot-specific.
type OneShotReclaimService struct {
	DB            *sql.DB
	Logger        observabilityport.RuntimeLogger
	BatchSize     int
	RetentionDays int
	// Now is the evaluation instant of the root detail-expiry predicate the
	// reclaim checks. Nil means the database clock. It moves only the instant
	// the expiry is evaluated at; every written row records the database clock.
	Now func() time.Time
}

func (s *OneShotReclaimService) evaluationNow() sql.NullTime {
	if s.Now == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: s.Now(), Valid: true}
}

// oneShotRootsQuery selects the acquisition roots of one marker ($1): the root
// of the marker's download task, or a root whose origin is the marker owner.
const oneShotRootsQuery = `SELECT r.id FROM acquisitions r JOIN one_shot_acquisitions m ON m.id=$1
WHERE r.origin_managed_anime_ref=m.managed_anime_id OR EXISTS(SELECT 1 FROM download_tasks d WHERE d.acquisition_id=r.id AND d.id=m.download_task_id)`

// oneShotClassifiedOwnerTables are the tables with a managed_anime_id column
// whose rows of a temporary one-shot owner the reclaim deletes (or that
// cascade with the owner or are the marker itself). A row of the owner in any
// other such table is an unclassified dependent and blocks the reclaim.
var oneShotClassifiedOwnerTables = []string{
	"work_items", "work_item_outcomes", "subscriptions", "subscription_log_entries", "idempotency_records",
	"anime_config_overrides", "anime_business_configs", "anime_metadata",
	"placements",
	// A companion is a row of one of the owner's Placements: a bound subtitle
	// registered by the Placement's own admission. It is deleted before the
	// Placement it hangs off; coverage cascades with that Placement.
	"placement_companions",
	"replacement_decisions", "reconciliation_files", "reconciliation_scans",
	"notification_admissions",
	"routine_activities",
	"one_shot_acquisitions",
}

// oneShotNeverOwnerTables are tables that carry a managed_anime_id column but
// provably never hold a row of a temporary one-shot owner, so reclaim neither
// deletes them nor treats one as a blocker. The classification test requires
// every public table with that exact column to appear in exactly one of this
// list, oneShotClassifiedOwnerTables, or oneShotKnownBlockingTables.
//
// Empty today: every table that carries the exact column is either reclaimed
// or is reachable-but-not-yet-deletable (below). Two owner-shaped tables are
// outside the closed set because their column is not named managed_anime_id:
// managed_anime is the owner row itself (deleted last), and
// anime_config_candidates carries activated_managed_anime_id, which is
// non-NULL only while a durable (subscribable) owner is activated — a oneshot
// draft can never be subscribed or activated (422 ORIGIN_NOT_SUBSCRIBABLE),
// so it never points at a temporary owner.
var oneShotNeverOwnerTables = []string{}

// oneShotKnownBlockingTables are tables that can hold a row of a temporary
// one-shot owner but are deliberately NOT deleted by reclaim, so the marker
// fails closed with an unclassified dependent. Each is written by a path the
// one-shot flow does not fence, and deleting its owner row while the row's
// WorkItem is still leasable would break an in-flight external effect: the
// quiescence proof (hasLiveWorkItemsTx) covers only download-task, release,
// artifact, and file-operation work items. A one-shot that reached one of
// these stays cleanup_blocked until that owner path gets its own admission
// fence; this list is the honest record, not a claim that the rows are gone.
var oneShotKnownBlockingTables = []string{
	// Alignment admission is deliberately not Subscription-gated: a manual scan
	// of a stopped instance still admits alignment operations, and an
	// alignment_operations row can name the owner's placement or companion. Its
	// alignment_execute WorkItem is outside the one-shot quiescence proof.
	"alignment_operations",
	// Cleanup admission is downstream of a replacement decision, which requires
	// an active Subscription. A one-shot mount is created stopped, but no path
	// fences a state patch on it, so replacement_deletions is not provably absent;
	// its cleanup_execute WorkItem is outside the quiescence proof.
	"replacement_deletions",
	// Offset migration admission is reachable through PatchRSSSourceOffset: the
	// one-shot view exposes the temporary ManagedAnime id and the route only
	// requires an active lifecycle, which the temporary owner has. Plans hang
	// off their generation and also name the placeholder RSS source, so the
	// source cannot be deleted without the plan rows.
	"offset_migration_generations",
	"offset_migration_plans",
	// download_submitted resolves the temporary ManagedAnime through the
	// download task's owner chain and the business-delivery sink is wired
	// globally, so a configured target creates a plugin_deliveries row carrying
	// the owner id. A plugin_deliver WorkItem is outside the quiescence proof.
	"plugin_deliveries",
}

// oneShotIndependentTables are owner tables that never belong to a managed
// anime (no managed_anime_id column) and so never join a temporary owner
// graph: the file manager's user operations act on paths, and the placement
// a release names is never a one-shot's (one-shot files are not released).
// They are listed so the classification stays explicit; a test proves they
// carry no managed_anime_id column.
//
// download_completions is keyed by the acquisition root, which a
// reclaim only compacts and never deletes; it carries no managed_anime_id.
var oneShotIndependentTables = []string{"file_manager_operations", "download_completions"}

// NewOneShotReclaimService constructs the service. A non-positive batch size
// uses the default; retention outside [1,30] days falls back to the default.
func NewOneShotReclaimService(db *sql.DB, logger observabilityport.RuntimeLogger) (*OneShotReclaimService, error) {
	if db == nil {
		return nil, errors.New("one-shot reclaim service database is nil")
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	batch := defaultOneShotBatchSize
	if batch <= 0 {
		batch = defaultOneShotBatchSize
	}
	return &OneShotReclaimService{DB: db, Logger: logger, BatchSize: batch, RetentionDays: DefaultRetentionDays}, nil
}

// RunPass executes one bounded pass: drain terminal chains out of running,
// reclaim due reclaim_pending markers, recheck due cleanup_blocked markers,
// and purge expired cleaned markers.
func (s *OneShotReclaimService) RunPass(ctx context.Context) (OneShotReclaimResult, error) {
	var result OneShotReclaimResult
	drained, blocked, err := s.drainCandidates(ctx)
	if err != nil {
		return result, err
	}
	result.Drained = drained
	result.Blocked += blocked

	reclaimed, blocked, err := s.reclaimDue(ctx)
	if err != nil {
		return result, err
	}
	result.Reclaimed = reclaimed
	result.Blocked += blocked

	rechecked, extended, err := s.recheckBlocked(ctx)
	if err != nil {
		return result, err
	}
	result.Rechecked = rechecked
	result.Blocked += extended

	purged, err := s.purgeCleaned(ctx)
	if err != nil {
		return result, err
	}
	result.Purged = purged
	return result, nil
}

func (s *OneShotReclaimService) logEvent(ctx context.Context, operation, reasonCode, message string) {
	if s == nil || s.Logger == nil {
		return
	}
	s.Logger.Info(ctx, observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "workitems",
		Operation:  operation,
		Outcome:    "info",
		ReasonCode: reasonCode,
	})
}

func (s *OneShotReclaimService) logError(ctx context.Context, operation, reasonCode, message string) {
	if s == nil || s.Logger == nil {
		return
	}
	s.Logger.Error(ctx, observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "workitems",
		Operation:  operation,
		Outcome:    "failure",
		ReasonCode: reasonCode,
		ErrorClass: "oneshot_reclaim",
	})
}

// oneShotDueIDs selects one bounded batch of marker ids in the given states
// using database time. The batch read is advisory only: each marker is
// re-locked FOR UPDATE inside its own processing transaction, which is where
// concurrent server instances actually serialize.
func (s *OneShotReclaimService) oneShotDueIDs(ctx context.Context, states []string, due bool) ([]string, error) {
	queries := persistence.New(s.DB)
	batchSize := int32(s.BatchSize)
	switch {
	case !due && len(states) == 1 && states[0] == OneShotStateRunning:
		return queries.ListRunningOneShotMarkers(ctx, batchSize)
	case due && len(states) == 1 && states[0] == OneShotStateReclaimPending:
		return queries.ListDueReclaimPendingOneShotMarkers(ctx, batchSize)
	case due && len(states) == 1 && states[0] == OneShotStateCleanupBlocked:
		return queries.ListDueCleanupBlockedOneShotMarkers(ctx, batchSize)
	default:
		return nil, fmt.Errorf("unsupported one-shot marker selection states=%v due=%t", states, due)
	}
}

// drainCandidates moves terminal, fully-drained running markers to
// reclaim_pending with their computed terminal outcome. Chains that are not
// terminal yet are skipped; terminal chains whose reclamation proof fails
// become cleanup_blocked.
func (s *OneShotReclaimService) drainCandidates(ctx context.Context) (drained, blocked int, err error) {
	ids, err := s.oneShotDueIDs(ctx, []string{OneShotStateRunning}, false)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		processed, blockErr := s.drainOne(ctx, id)
		if blockErr != nil {
			return drained, blocked, blockErr
		}
		switch processed {
		case "drained":
			drained++
		case "blocked":
			blocked++
		}
	}
	return drained, blocked, nil
}

func (s *OneShotReclaimService) drainOne(ctx context.Context, markerID string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin one-shot drain: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return "", nil // purged concurrently
	}
	if err != nil {
		return "", err
	}
	if row.State != OneShotStateRunning {
		return "", nil // raced into another state
	}
	graph, graphErr := s.loadAnchorsTx(ctx, tx, markerID)
	if graphErr != nil {
		// Anchor corruption is typed: block with a bounded actionable reason.
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "anchor_validation_failed", boundedText(graphErr.Error(), maxOneShotReason)); updateErr != nil {
			return "", updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_drain", "cleanup_blocked", "one-shot chain blocked: anchor validation failed")
		return "blocked", nil
	}
	outcome, outcomeErr := s.evaluateChainTx(ctx, tx, graph)
	if outcomeErr != nil {
		// Unprovable quiescence or unsettled owners: keep running when work
		// may still be live, otherwise block with the bounded reason.
		live, liveErr := s.hasLiveWorkItemsTx(ctx, tx, graph)
		if liveErr != nil {
			return "", liveErr
		}
		if live || len(row.Leases) > 0 {
			return "", nil // still running normally; retry next pass
		}
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "chain_unsettled", boundedText(outcomeErr.Error(), maxOneShotReason)); updateErr != nil {
			return "", updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_drain", "cleanup_blocked", "one-shot chain blocked: "+boundedText(outcomeErr.Error(), 160))
		return "blocked", nil
	}
	if len(row.Leases) > 0 {
		if _, updateErr := s.blockMarkerWithOutcomeTx(ctx, tx, markerID, "admission_ledger_not_drained", "the admission ledger must be empty before reclamation", outcome.outcome); updateErr != nil {
			return "", updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_drain", "cleanup_blocked", "one-shot chain terminal but admission ledger is not drained")
		return "blocked", nil
	}
	if live, liveErr := s.hasLiveWorkItemsTx(ctx, tx, graph); liveErr != nil {
		return "", liveErr
	} else if live {
		return "", nil
	}
	if err := s.setTerminalPendingTx(ctx, tx, markerID, outcome); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit one-shot drain: %w", err)
	}
	s.logEvent(ctx, "oneshot_drain", "reclaim_pending", "one-shot chain drained to reclaim_pending (outcome="+outcome.outcome+")")
	return "drained", nil
}

// reclaimDue processes reclaim_pending markers whose reclaim deadline has
// passed: revalidate, then raise the epoch into reclaiming, delete ONLY the
// anchored temporary database owner graph leaf-first, and mark cleaned — all
// in one transaction. A crash rolls back to reclaim_pending.
func (s *OneShotReclaimService) reclaimDue(ctx context.Context) (reclaimed, blocked int, err error) {
	ids, err := s.oneShotDueIDs(ctx, []string{OneShotStateReclaimPending}, true)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		done, blockErr := s.reclaimOne(ctx, id)
		if blockErr != nil {
			return reclaimed, blocked, blockErr
		}
		if done {
			reclaimed++
		} else {
			blocked++
		}
	}
	return reclaimed, blocked, nil
}

func (s *OneShotReclaimService) reclaimOne(ctx context.Context, markerID string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin one-shot reclamation: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.State != OneShotStateReclaimPending {
		return false, nil
	}
	// Marker expiration never grants deletion of independent acquisition details:
	// a running root, or a settled one whose detail_until has not passed, keeps
	// the marker retained with nothing written. This read takes no
	// management/root locks; the marker is the stable prefix.
	var retained bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acquisitions r WHERE r.id IN (`+oneShotRootsQuery+`) AND r.record_state='full' AND (r.state='running' OR r.detail_until>COALESCE($2::timestamptz,CURRENT_TIMESTAMP)))`, markerID, s.evaluationNow()).Scan(&retained); err != nil {
		return false, fmt.Errorf("observe retained acquisition details: %w", err)
	}
	if retained {
		return false, nil
	}
	graph, graphErr := s.loadAnchorsTx(ctx, tx, markerID)
	if graphErr != nil {
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "anchor_validation_failed", boundedText(graphErr.Error(), maxOneShotReason)); updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_reclaim", "cleanup_blocked", "reclamation blocked: anchor validation failed")
		return false, nil
	}
	// The temporary owner row is now the serialization point shared with every
	// effect entry (the lifecycle lives on managed_anime). Lock it before any
	// child delete so reclamation keeps the marker -> owner -> child order and
	// cannot invert against a concurrent acceptance that holds the owner row.
	if graph.animeID != "" {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id = $1 FOR UPDATE`, graph.animeID); err != nil {
			return false, fmt.Errorf("lock temporary owner for reclamation: %w", err)
		}
	}
	if _, outcomeErr := s.evaluateChainTx(ctx, tx, graph); outcomeErr != nil {
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "chain_unsettled", boundedText(outcomeErr.Error(), maxOneShotReason)); updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_reclaim", "cleanup_blocked", "reclamation blocked: "+boundedText(outcomeErr.Error(), 160))
		return false, nil
	}
	if len(row.Leases) > 0 {
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "admission_ledger_not_drained", "the admission ledger must be empty before reclamation"); updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		return false, nil
	}
	if outcomeErr := s.proveQuiescentTx(ctx, tx, graph); outcomeErr != nil {
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "quiescence_unproven", boundedText(outcomeErr.Error(), maxOneShotReason)); updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_reclaim", "cleanup_blocked", "reclamation blocked: "+boundedText(outcomeErr.Error(), 160))
		return false, nil
	}
	if table, err := unclassifiedOwnerDependentTx(ctx, tx, graph.animeID); err != nil {
		return false, err
	} else if table != "" {
		if _, err := s.blockMarkerTx(ctx, tx, markerID, "unclassified_dependent", "the temporary owner still has rows in "+table); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit one-shot cleanup_blocked: %w", err)
		}
		s.logEvent(ctx, "oneshot_reclaim", "cleanup_blocked", "reclamation blocked: unclassified dependent in "+table)
		return false, nil
	}
	// Fenced drain phase: raise the epoch so any late worker's gate rejects
	// with ONE_SHOT_RECLAIMING before it can start an external effect.
	result, err := persistence.New(tx).EnterOneShotReclaiming(ctx, markerID)
	if err != nil {
		return false, fmt.Errorf("enter one-shot reclaiming: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return false, fmt.Errorf("read one-shot reclaiming result: %w", err)
	} else if affected != 1 {
		return false, ErrOneShotMarkerMissing
	}
	// T20 compact of the marker's settled roots, evaluated with the same
	// instant as the retained check above. The pins the temporary owner holds on
	// root rows go first. Anything but a compacted root rolls the whole reclaim
	// back unchanged.
	if compacted, err := s.compactOneShotRootsTx(ctx, tx, markerID, graph); err != nil {
		return false, err
	} else if !compacted {
		return false, nil
	}
	if err := deleteTemporaryOwnerGraphTx(ctx, tx, graph); err != nil {
		return false, err
	}
	result, err = persistence.New(tx).MarkOneShotCleaned(ctx, markerID)
	if err != nil {
		return false, fmt.Errorf("mark one-shot cleaned: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return false, fmt.Errorf("read one-shot cleaned result: %w", err)
	} else if affected != 1 {
		return false, ErrOneShotMarkerMissing
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit one-shot reclamation: %w", err)
	}
	s.logEvent(ctx, "oneshot_reclaim", "cleaned", "one-shot temporary owner graph reclaimed leaf-first")
	return true, nil
}

// recheckBlocked re-evaluates due cleanup_blocked markers. A passing proof
// returns the marker to reclaim_pending with a fresh deadline; a failing
// proof extends the deadline within the hard maximum from terminal_at.
func (s *OneShotReclaimService) recheckBlocked(ctx context.Context) (rechecked, extended int, err error) {
	ids, err := s.oneShotDueIDs(ctx, []string{OneShotStateCleanupBlocked}, true)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		passed, blockErr := s.recheckOne(ctx, id)
		if blockErr != nil {
			return rechecked, extended, blockErr
		}
		rechecked++
		if passed {
			// counted under reclaimed-style progress, not an extension
			continue
		}
		extended++
	}
	return rechecked, extended, nil
}

func (s *OneShotReclaimService) recheckOne(ctx context.Context, markerID string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin one-shot recheck: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.State != OneShotStateCleanupBlocked {
		return false, nil
	}
	graph, graphErr := s.loadAnchorsTx(ctx, tx, markerID)
	if graphErr == nil {
		// A marker may have become cleanup_blocked because an owner was still
		// settling. Rechecking the ledger and live WorkItems alone is not enough:
		// prove the complete terminal chain again before returning to
		// reclaim_pending.
		_, evaluateErr := s.evaluateChainTx(ctx, tx, graph)
		if evaluateErr == nil && len(row.Leases) == 0 && s.proveQuiescentTx(ctx, tx, graph) == nil {
			deadline, err := s.reclaimDeadlineTx(ctx, tx)
			if err != nil {
				return false, err
			}
			result, err := persistence.New(tx).UnblockOneShotMarker(ctx, persistence.UnblockOneShotMarkerParams{
				RetentionUntil: sql.NullTime{Time: deadline, Valid: true},
				MarkerID:       markerID,
			})
			if err != nil {
				return false, fmt.Errorf("unblock one-shot marker: %w", err)
			}
			if affected, err := result.RowsAffected(); err != nil {
				return false, fmt.Errorf("read unblocked one-shot marker result: %w", err)
			} else if affected != 1 {
				return false, ErrOneShotMarkerMissing
			}
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("commit one-shot recheck: %w", err)
			}
			s.logEvent(ctx, "oneshot_recheck", "reclaim_pending", "cleanup_blocked recheck passed; marker returned to reclaim_pending")
			return true, nil
		}
	}
	// Failed recheck: extend the deadline within the hard maximum.
	result, err := persistence.New(tx).ExtendOneShotCleanupBlocked(ctx, persistence.ExtendOneShotCleanupBlockedParams{
		RetentionDays:    int32(s.RetentionDays),
		MaxRetentionDays: int32(MaxRetentionDays),
		MarkerID:         markerID,
	})
	if err != nil {
		return false, fmt.Errorf("extend one-shot cleanup_blocked deadline: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return false, fmt.Errorf("read one-shot deadline extension result: %w", err)
	} else if affected != 1 {
		return false, ErrOneShotMarkerMissing
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit one-shot recheck extension: %w", err)
	}
	s.logEvent(ctx, "oneshot_recheck", "deadline_extended", "cleanup_blocked recheck failed; deadline extended within the hard maximum")
	return false, nil
}

// purgeCleaned physically deletes cleaned markers whose reclaim window has passed.
// After purge the replay record is gone: the request key may be reused for a
// new admission by design.
func (s *OneShotReclaimService) purgeCleaned(ctx context.Context) (int, error) {
	result, err := persistence.New(s.DB).PurgeExpiredCleanedOneShotMarkers(ctx, int32(s.BatchSize))
	if err != nil {
		return 0, fmt.Errorf("purge cleaned one-shot markers: %w", err)
	}
	purged, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read one-shot purge result: %w", err)
	}
	if purged > 0 {
		s.logEvent(ctx, "oneshot_purge", "purge_completed", fmt.Sprintf("expired cleaned one-shot markers purged (count=%d)", purged))
	}
	return int(purged), nil
}

// oneShotGraph is the exact-identity view of the anchored temporary owner
// graph. Every downstream enumeration keys off these anchors — never title,
// URL, path, group, or provider identity.
type oneShotGraph struct {
	markerID       string
	animeID        string
	subscriptionID string
	rssSourceID    string
	releaseID      string
	// releaseVerdict is the frozen Release verdict; it distinguishes an
	// adopted exact target set (no download task) from a dangling anchor.
	releaseVerdict string
	downloadTaskID string
	artifactIDs    []string
	fileOpIDs      []string
}

// loadAnchorsTx resolves ManagedAnime <- Subscription <- RSSSource <- Release
// -> DownloadTask through the stored anchor ids and their typed relationships,
// then enumerates Artifact/FileOperation owners by exact download_task_id /
// correlation identity. Any missing, extra, dangling, or relationship-
// mismatched anchor is corruption.
func (s *OneShotReclaimService) loadAnchorsTx(ctx context.Context, tx *sql.Tx, markerID string) (oneShotGraph, error) {
	graph := oneShotGraph{markerID: markerID}
	row, err := persistence.New(tx).ResolveOneShotAnchors(ctx, markerID)
	if errors.Is(err, sql.ErrNoRows) {
		return graph, errors.New("anchored temporary owner graph does not resolve by exact identity")
	}
	if err != nil {
		return graph, fmt.Errorf("resolve one-shot anchors: %w", err)
	}
	graph.animeID = row.ManagedAnimeID
	graph.subscriptionID = row.SubscriptionID
	graph.rssSourceID = row.RssSourceID
	graph.releaseID = row.ReleaseID
	graph.releaseVerdict = row.Verdict
	graph.downloadTaskID = row.DownloadTaskID
	graph.artifactIDs, err = persistence.New(tx).ListAnchoredArtifacts(ctx, graph.downloadTaskID)
	if err != nil {
		return graph, fmt.Errorf("enumerate anchored artifacts: %w", err)
	}
	// File operations hang off artifact files of the anchored artifacts AND
	// carry the media correlation; both sets must agree exactly. The pgx
	// database/sql driver delivers PostgreSQL arrays as opaque strings, so the
	// enumeration serializes through jsonb text and decodes explicitly instead
	// of scanning an array value into []string.
	correlatedRaw, err := persistence.New(tx).ListCorrelatedAnchoredFileOperationsJSON(ctx, persistence.ListCorrelatedAnchoredFileOperationsJSONParams{
		MarkerID:    markerID,
		ArtifactIds: graph.artifactIDs,
	})
	if err != nil {
		return graph, fmt.Errorf("enumerate anchored file operations: %w", err)
	}
	fileOpIDs, decodeErr := decodeAnchoredIDList(correlatedRaw)
	if decodeErr != nil {
		return graph, fmt.Errorf("enumerate anchored file operations: %w", decodeErr)
	}
	graph.fileOpIDs = fileOpIDs
	viaArtifactsRaw, err := persistence.New(tx).ListArtifactAnchoredFileOperationsJSON(ctx, graph.artifactIDs)
	if err != nil {
		return graph, fmt.Errorf("cross-check anchored file operations: %w", err)
	}
	viaArtifacts, decodeErr := decodeAnchoredIDList(viaArtifactsRaw)
	if decodeErr != nil {
		return graph, fmt.Errorf("cross-check anchored file operations: %w", decodeErr)
	}
	if !sameOrderedIDs(viaArtifacts, graph.fileOpIDs) {
		return graph, errors.New("file operation enumeration disagrees between correlation and artifact anchors")
	}
	return graph, nil
}

// decodeAnchoredIDList decodes one ordered anchor ID list returned from SQL as
// JSON text ('["a","b"]', or '[]' when empty).
func decodeAnchoredIDList(raw string) ([]string, error) {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("decode anchored id list json %q: %w", boundedText(raw, 128), err)
	}
	return ids, nil
}

// sameOrderedIDs compares the complete ordered anchor lists. Matching only
// cardinality would allow one foreign file operation to replace a real
// artifact-owned operation and pass reclamation validation.
func sameOrderedIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// oneShotOutcome is the computed terminal result for the drained chain.
type oneShotOutcome struct {
	outcome          string
	reasonCode       string
	reason           string
	retentionFromNow bool
	// failedStage is the first failed stage of the anchored chain, derived in
	// the same order the read projection uses (file_execution -> planning ->
	// artifact -> download). Empty for a succeeded outcome.
	failedStage string
}

// evaluateChainTx proves OSA-10 completion: DownloadTask terminal, every
// created Artifact verified-or-known-failure, every created FileOperation
// completed-or-known-failure. qB disposition knowledge is guaranteed by the
// drained admission ledger (every effect settled confirmed/no_external_effect).
func (s *OneShotReclaimService) evaluateChainTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) (oneShotOutcome, error) {
	if graph.downloadTaskID == "" {
		// No download chain: the only legitimate shape is an adopted exact
		// target set, which is already satisfied and needed no qB work. Any
		// other verdict without a resolvable download task is a dangling
		// anchor and stays unprovable instead of being reported as success.
		if graph.releaseVerdict != oneShotAdoptedReleaseVerdict {
			return oneShotOutcome{}, fmt.Errorf("anchored release verdict %q has no download task", graph.releaseVerdict)
		}
		if err := s.assertOwnersSettledTx(ctx, tx, graph); err != nil {
			return oneShotOutcome{}, err
		}
		return oneShotOutcome{outcome: "succeeded", reasonCode: "one_shot_completed", reason: "one-shot acquisition adopted the existing exact target set"}, nil
	}
	task, err := persistence.New(tx).GetAnchoredDownloadTaskState(ctx, graph.downloadTaskID)
	if err != nil {
		return oneShotOutcome{}, fmt.Errorf("read anchored download task: %w", err)
	}
	taskState := task.State
	taskFailure := task.FailureReason
	if taskState != "completed" && taskState != "failed" {
		return oneShotOutcome{}, fmt.Errorf("download task is still %q", taskState)
	}
	if err := s.assertOwnersSettledTx(ctx, tx, graph); err != nil {
		return oneShotOutcome{}, err
	}
	outcome := oneShotOutcome{outcome: "succeeded", reasonCode: "one_shot_completed", reason: "one-shot acquisition completed"}
	if taskState != "completed" {
		outcome.outcome = "failed"
		outcome.reasonCode = "one_shot_failed"
		outcome.reason = boundedText(taskFailure, maxOneShotReason)
		if outcome.reason == "" {
			outcome.reason = "one-shot acquisition failed"
		}
		outcome.failedStage = "download"
	}
	// Outcome aggregation: settled (no longer running) is not
	// succeeded. A failed artifact, plan, or file operation in the anchored
	// chain is a real failure of the requested acquisition even when the
	// download task itself completed, and must surface as a failed terminal
	// outcome.
	if outcome.outcome == "succeeded" {
		failure, err := s.firstAnchoredStageFailureTx(ctx, tx, graph)
		if err != nil {
			return oneShotOutcome{}, err
		}
		if failure != nil {
			outcome.outcome = "failed"
			outcome.reasonCode = "one_shot_failed"
			outcome.reason = boundedText(failure.reason, maxOneShotReason)
			if outcome.reason == "" {
				outcome.reason = "one-shot acquisition failed in " + failure.stage
			}
			outcome.failedStage = failure.stage
		}
	}
	return outcome, nil
}

// firstAnchoredStageFailure reports the first failed artifact, plan, or file
// operation in the anchored chain, or nil when every settled owner succeeded.
// The stage order mirrors the read projection's failed-stage derivation: the
// file execution outcome first, then the planning stage, then the artifact
// fact itself.
type anchoredStageFailure struct {
	stage  string
	reason string
}

func (s *OneShotReclaimService) firstAnchoredStageFailureTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) (*anchoredStageFailure, error) {
	if len(graph.fileOpIDs) > 0 {
		fileOperationRows, err := persistence.New(tx).ListAnchoredFileOperationStates(ctx, graph.fileOpIDs)
		if err != nil {
			return nil, fmt.Errorf("read anchored file operations for outcome: %w", err)
		}
		for _, row := range fileOperationRows {
			if row.State == "failed" {
				return &anchoredStageFailure{stage: "file_execution", reason: "file operation failed"}, nil
			}
		}
	}
	planRows, err := persistence.New(tx).ListAnchoredPlanStates(ctx, graph.artifactIDs)
	if err != nil {
		return nil, fmt.Errorf("read anchored post-processing plans for outcome: %w", err)
	}
	for _, row := range planRows {
		if row.State == "failed" {
			return &anchoredStageFailure{stage: "planning", reason: "post-processing plan failed"}, nil
		}
	}
	artifactRows, err := persistence.New(tx).ListAnchoredArtifactStates(ctx, graph.artifactIDs)
	if err != nil {
		return nil, fmt.Errorf("read anchored artifacts for outcome: %w", err)
	}
	for _, row := range artifactRows {
		if row.State == "failed" {
			return &anchoredStageFailure{stage: "artifact", reason: "artifact verification failed"}, nil
		}
	}
	return nil, nil
}

// assertOwnersSettledTx verifies every anchored Artifact and FileOperation is
// terminal; a non-terminal owner without live work is an anomaly that blocks
// reclamation instead of guessing.
func (s *OneShotReclaimService) assertOwnersSettledTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) error {
	artifactRows, err := persistence.New(tx).ListAnchoredArtifactStates(ctx, graph.artifactIDs)
	if err != nil {
		return fmt.Errorf("read anchored artifacts: %w", err)
	}
	for _, row := range artifactRows {
		if row.State != "verified" && row.State != "failed" {
			return fmt.Errorf("artifact %s is still %q", row.ID, row.State)
		}
	}
	if len(graph.fileOpIDs) == 0 {
		return nil
	}
	fileOperationRows, err := persistence.New(tx).ListAnchoredFileOperationStates(ctx, graph.fileOpIDs)
	if err != nil {
		return fmt.Errorf("read anchored file operations: %w", err)
	}
	for _, row := range fileOperationRows {
		if row.State != "completed" && row.State != "failed" {
			return fmt.Errorf("file operation %s is still %q", row.ID, row.State)
		}
	}
	return nil
}

// proveQuiescentTx is the reclamation-time eligibility proof: no live
// WorkItem for any anchored owner and no continuation obligation (there is no
// autonomous one-shot dispatcher, so live items are the only obligations).
func (s *OneShotReclaimService) proveQuiescentTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) error {
	live, err := s.hasLiveWorkItemsTx(ctx, tx, graph)
	if err != nil {
		return err
	}
	if live {
		return errors.New("a related WorkItem is still available or leased")
	}
	return nil
}

func (s *OneShotReclaimService) hasLiveWorkItemsTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) (bool, error) {
	live, err := persistence.New(tx).HasLiveOneShotWorkItems(ctx, persistence.HasLiveOneShotWorkItemsParams{
		DownloadTaskID:   graph.downloadTaskID,
		ReleaseID:        graph.releaseID,
		ArtifactIds:      graph.artifactIDs,
		FileOperationIds: graph.fileOpIDs,
	})
	if err != nil {
		return false, fmt.Errorf("check live one-shot work items: %w", err)
	}
	return live, nil
}

// setTerminalPendingTx writes the computed terminal evidence plus the
// reclaim deadline while moving running -> reclaim_pending. The ledger
// must already be empty (DB CHECK enforces the field matrix).
func (s *OneShotReclaimService) setTerminalPendingTx(ctx context.Context, tx *sql.Tx, markerID string, outcome oneShotOutcome) error {
	deadline, err := s.reclaimDeadlineTx(ctx, tx)
	if err != nil {
		return err
	}
	result, err := persistence.New(tx).DrainOneShotMarkerToReclaimPending(ctx, persistence.DrainOneShotMarkerToReclaimPendingParams{
		TerminalOutcome:    sql.NullString{String: outcome.outcome, Valid: true},
		TerminalReasonCode: sql.NullString{String: boundedText(outcome.reasonCode, maxOneShotReasonCode), Valid: true},
		TerminalReason:     sql.NullString{String: boundedText(outcome.reason, maxOneShotReason), Valid: true},
		RetentionUntil:     sql.NullTime{Time: deadline, Valid: true},
		MarkerID:           markerID,
	})
	if err != nil {
		return fmt.Errorf("drain one-shot marker to reclaim_pending: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read drained one-shot marker result: %w", err)
	} else if affected != 1 {
		return ErrOneShotMarkerMissing
	}
	// The attempts model: the current attempt receives its one-way
	// terminal evidence in the same transaction, so the immutable history and
	// the marker projection cannot drift. The CAS (state='running') makes the
	// write idempotent for an already-terminal attempt and skips legacy rows
	// whose pointer predates the attempts backfill. The failed stage is derived
	// from the same anchored-chain order the read projection uses.
	if _, err := tx.ExecContext(ctx, `
UPDATE one_shot_execution_attempts
SET state = $2,
    failed_stage = NULLIF($5, ''),
    outcome_code = $3,
    outcome_reason = $4,
    terminal_at = CURRENT_TIMESTAMP,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = (SELECT current_attempt_id FROM one_shot_acquisitions WHERE id = $1)
  AND state = 'running'`,
		markerID, outcome.outcome,
		boundedText(outcome.reasonCode, maxOneShotReasonCode),
		boundedText(outcome.reason, maxOneShotReason),
		boundedText(outcome.failedStage, maxOneShotReasonCode)); err != nil {
		return fmt.Errorf("terminalize current one-shot execution attempt: %w", err)
	}
	return nil
}

// blockMarkerTx moves a failing marker into cleanup_blocked with a bounded
// actionable reason and a fresh recheck deadline.
func (s *OneShotReclaimService) blockMarkerTx(ctx context.Context, tx *sql.Tx, markerID, reasonCode, reason string) (int64, error) {
	return s.blockMarkerWithOutcomeTx(ctx, tx, markerID, reasonCode, reason, "")
}

// blockMarkerWithOutcomeTx moves a marker into cleanup_blocked while retaining
// the marker's terminal outcome when one is already known. A caller that has
// just completed the terminal-chain proof may provide that outcome so a
// drained-but-unsettled marker is not mislabeled as failed. The new deadline
// is capped at terminal_at + MaxRetentionDays, matching the hard-max recheck
// policy even when a stale reclaim_pending marker is blocked during
// reclamation.
func (s *OneShotReclaimService) blockMarkerWithOutcomeTx(ctx context.Context, tx *sql.Tx, markerID, reasonCode, reason, terminalOutcome string) (int64, error) {
	if terminalOutcome == "" {
		terminalOutcome = StateFailed
	}
	if terminalOutcome != StateSucceeded && terminalOutcome != StateFailed {
		return 0, fmt.Errorf("%w: invalid one-shot terminal outcome %q", ErrInvalidSpec, terminalOutcome)
	}
	deadline, err := persistence.New(tx).ComputeOneShotCleanupDeadline(ctx, persistence.ComputeOneShotCleanupDeadlineParams{
		RetentionDays:    int32(s.RetentionDays),
		MaxRetentionDays: int32(MaxRetentionDays),
		MarkerID:         markerID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrOneShotMarkerMissing
	}
	if err != nil {
		return 0, fmt.Errorf("compute one-shot cleanup deadline: %w", err)
	}
	result, err := persistence.New(tx).BlockOneShotMarker(ctx, persistence.BlockOneShotMarkerParams{
		TerminalOutcome:    sql.NullString{String: terminalOutcome, Valid: true},
		TerminalReasonCode: sql.NullString{String: boundedText(reasonCode, maxOneShotReasonCode), Valid: true},
		TerminalReason:     sql.NullString{String: boundedText(reason, maxOneShotReason), Valid: true},
		RetentionUntil:     sql.NullTime{Time: deadline, Valid: true},
		MarkerID:           markerID,
	})
	if err != nil {
		return 0, fmt.Errorf("block one-shot marker: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read blocked one-shot marker result: %w", err)
	}
	if affected != 1 {
		return 0, ErrOneShotMarkerMissing
	}
	return affected, nil
}

// reclaimDeadlineTx computes the reclamation deadline from PostgreSQL database
// time so batches stay consistent across server instances.
func (s *OneShotReclaimService) reclaimDeadlineTx(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	deadline, err := persistence.New(tx).ComputeOneShotReclaimDeadline(ctx, int32(s.RetentionDays))
	if err != nil {
		return time.Time{}, fmt.Errorf("compute one-shot reclaim deadline: %w", err)
	}
	return deadline, nil
}

// unclassifiedOwnerDependentTx returns the first table outside the classified
// set that still holds a row of the temporary owner, or "".
func unclassifiedOwnerDependentTx(ctx context.Context, tx *sql.Tx, animeID string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.table_name FROM information_schema.columns c
JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name AND t.table_type='BASE TABLE'
WHERE c.table_schema='public' AND c.column_name='managed_anime_id' AND NOT (c.table_name=ANY($1::text[]))
ORDER BY c.table_name`, pq.Array(oneShotClassifiedOwnerTables))
	if err != nil {
		return "", fmt.Errorf("list owner dependent tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, table)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, table := range tables {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+pq.QuoteIdentifier(table)+` WHERE managed_anime_id=$1)`, animeID).Scan(&exists); err != nil {
			return "", fmt.Errorf("observe owner dependents in %s: %w", table, err)
		}
		if exists {
			return table, nil
		}
	}
	return "", nil
}

// compactOneShotRootsTx compacts every root of the marker with trigger
// detail_expired. It reports false (and the caller rolls back) when a root is
// not compactable at the evaluation instant. It runs after the marker entered
// reclaiming in this transaction: compact accepts the temporary Release that
// still names the root's task only because deleteTemporaryOwnerGraphTx deletes
// that Release before commit (otherwise compact reports release_bound).
func (s *OneShotReclaimService) compactOneShotRootsTx(ctx context.Context, tx *sql.Tx, markerID string, graph oneShotGraph) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM placement_obligations WHERE file_operation_id IN (SELECT o.id FROM file_operations o WHERE o.acquisition_id IN (`+oneShotRootsQuery+`))`, markerID); err != nil {
		return false, fmt.Errorf("delete one-shot placement_obligations: %w", err)
	}
	if err := persistence.New(tx).DeleteOneShotIdempotencyRecords(ctx, sql.NullString{String: graph.releaseID, Valid: true}); err != nil {
		return false, fmt.Errorf("delete one-shot idempotency_records: %w", err)
	}
	rows, err := tx.QueryContext(ctx, oneShotRootsQuery+` ORDER BY r.id`, markerID)
	if err != nil {
		return false, fmt.Errorf("list one-shot roots: %w", err)
	}
	var roots []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		roots = append(roots, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	now := s.evaluationNow()
	for _, root := range roots {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT acquisition_compact($1,'detail_expired',$2)`, root, now).Scan(&raw); err != nil {
			return false, fmt.Errorf("compact one-shot root %s: %w", root, err)
		}
		var out struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return false, err
		}
		if out.Status != "compacted" && out.Status != "already_compact" {
			s.logEvent(ctx, "oneshot_reclaim", "retained", "reclamation retained: root "+out.Status)
			return false, nil
		}
	}
	return true, nil
}

// deleteTemporaryOwnerGraphTx deletes ONLY the anchored temporary database
// owner graph, leaf-first, mirroring the existing unsubscribe deletion order
// (internal/anime/application/delete_subscription.go). It never touches qB
// tasks, downloaded data, library targets, or user-owned files, and it never
// deletes the marker itself.
func deleteTemporaryOwnerGraphTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) error {
	queries := persistence.New(tx)
	arg := persistence.DeleteOneShotWorkItemsParams{
		DownloadTaskID:   graph.downloadTaskID,
		ReleaseID:        graph.releaseID,
		ArtifactIds:      graph.artifactIDs,
		FileOperationIds: graph.fileOpIDs,
	}
	steps := []struct {
		name string
		call func() error
	}{
		// The run log is the leaf of the whole graph: its entries name the
		// Release, the RSS source and the subscription, so they go first.
		{"subscription_log_entries", func() error {
			return queries.DeleteOneShotSubscriptionLogEntries(ctx, graph.subscriptionID)
		}},
		{"work_items", func() error { return queries.DeleteOneShotWorkItems(ctx, arg) }},
		{"file_operations", func() error {
			return queries.DeleteOneShotFileOperations(ctx, graph.fileOpIDs)
		}},
		{"artifact_files", func() error {
			return queries.DeleteOneShotArtifactFiles(ctx, graph.artifactIDs)
		}},
		{"artifacts", func() error {
			return queries.DeleteOneShotArtifacts(ctx, graph.downloadTaskID)
		}},
		{"idempotency_records", func() error {
			return queries.DeleteOneShotIdempotencyRecords(ctx, sql.NullString{String: graph.releaseID, Valid: true})
		}},
		{"release_torrent_inputs", func() error {
			return queries.DeleteOneShotReleaseTorrentInputs(ctx, graph.releaseID)
		}},
		{"release_candidate_facts", func() error {
			return queries.DeleteOneShotReleaseCandidateFacts(ctx, graph.releaseID)
		}},
		// Admission freezes the Release media fact snapshot and inspection adds
		// one row per file; both hang off the Release and go with it, exactly
		// like the managed-anime cleanup procedure deletes them.
		{"media_fact_files", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM media_fact_files WHERE snapshot_id IN (SELECT id FROM media_fact_snapshots WHERE release_id = $1)`, graph.releaseID)
			return err
		}},
		{"media_fact_snapshots", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM media_fact_snapshots WHERE release_id = $1`, graph.releaseID)
			return err
		}},
		{"download_tasks", func() error { return queries.DeleteOneShotDownloadTasks(ctx, graph.downloadTaskID) }},
		{"releases", func() error { return queries.DeleteOneShotReleases(ctx, graph.releaseID) }},
		{"rss_source_runtime", func() error { return queries.DeleteOneShotRSSSourceRuntime(ctx, graph.rssSourceID) }},
		{"rss_sources", func() error { return queries.DeleteOneShotRSSSources(ctx, graph.rssSourceID) }},
		{"provider_group_selections", func() error {
			return queries.DeleteOneShotProviderGroupSelections(ctx, graph.subscriptionID)
		}},
		{"anime_config_overrides", func() error {
			return queries.DeleteOneShotAnimeConfigOverrides(ctx, graph.animeID)
		}},
		{"anime_business_configs", func() error {
			return queries.DeleteOneShotAnimeBusinessConfigs(ctx, graph.animeID)
		}},
		{"anime_metadata", func() error { return queries.DeleteOneShotAnimeMetadata(ctx, graph.animeID) }},
		{"subscriptions", func() error { return queries.DeleteOneShotSubscriptions(ctx, graph.subscriptionID) }},
		// The merged configuration row carries the owner versions, the three
		// policy axes and the frozen business; it references the managed anime, so
		// it must be deleted before the marker is reclaimed.
		// D-a: the owner's placement graph and its consumers. A companion is a
		// row of the Placement and goes first; coverage cascades with its
		// Placement; notification targets cascade with their admission.
		// Library files are never touched: a Placement row is only the record.
		{"placement_companions", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM placement_companions WHERE managed_anime_id = $1`, graph.animeID)
			return err
		}},
		{"placements", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM placements WHERE managed_anime_id = $1`, graph.animeID)
			return err
		}},
		{"replacement_decisions", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM replacement_decisions WHERE managed_anime_id = $1`, graph.animeID)
			return err
		}},
		{"reconciliation", func() error {
			for _, table := range []string{"reconciliation_files", "reconciliation_scans"} {
				if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE managed_anime_id = $1`, graph.animeID); err != nil {
					return err
				}
			}
			return nil
		}},
		{"notification_admissions", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM notification_admissions WHERE managed_anime_id = $1`, graph.animeID)
			return err
		}},
		{"routine_activities", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM routine_activities WHERE managed_anime_id = $1`, graph.animeID)
			return err
		}},
		{"managed_anime", func() error { return queries.DeleteOneShotManagedAnime(ctx, graph.animeID) }},
	}
	for _, step := range steps {
		if err := step.call(); err != nil {
			return fmt.Errorf("delete one-shot %s: %w", step.name, err)
		}
	}
	return nil
}

func boundedText(value string, max int) string {
	value = trimSafe(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

// trimSafe strips control characters and bounds whitespace so persisted
// reasons stay single-line, bounded, and redacted-safe.
func trimSafe(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\x00' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	start, end := 0, len(out)
	for start < end && out[start] == ' ' {
		start++
	}
	for end > start && out[end-1] == ' ' {
		end--
	}
	return string(out[start:end])
}
