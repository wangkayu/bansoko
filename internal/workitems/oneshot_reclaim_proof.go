package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	persistence "bansoko/internal/workitems/adapter/persistence/generated"

	"github.com/lib/pq"
)

// oneShotAdoptedReleaseVerdict is the RSS/one-shot Release verdict written when
// the exact selected target set already existed and no download chain was
// created. It is the only verdict allowed with a NULL marker download task.
const oneShotAdoptedReleaseVerdict = "adopted_existing"

// errOneShotChainRunning reports a download chain whose acquisition root is
// still running. The root, not the marker, owns that chain's progress (a
// running root always holds work, migration 091), so the chain is not
// terminal and the drain leaves the marker running without a write.
var errOneShotChainRunning = errors.New("the acquisition root is still running")

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
	// A configuration write of the owner (metadata patch, backfill) can leave a
	// pending config scan request; it goes with the owner.
	"config_scan_requests",
	"one_shot_acquisitions",
}

// oneShotNeverOwnerTables are tables that carry a managed_anime_id column but
// provably never hold a row of a temporary one-shot owner, so reclaim neither
// deletes them nor treats one as a blocker. The classification test requires
// every public table with that exact column to appear in exactly one of this
// list, oneShotClassifiedOwnerTables, or oneShotKnownBlockingTables.
//
// No table sits in this bucket since migration 122 dropped the retired offset
// migration tables; it stays empty so a future owner-shaped table gets an
// explicit disposition instead of silently blocking reclaims. Two
// owner-shaped tables are outside the closed set because their column is not
// named managed_anime_id:
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

// evaluateChainTx proves OSA-10 completion: the acquisition root no longer
// running, DownloadTask terminal, every created Artifact
// verified-or-known-failure, every created post-processing plan settled, every
// created FileOperation completed-or-known-failure. qB disposition knowledge is guaranteed by the
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
	// A download chain is terminal only once its acquisition root is: until
	// the root settles, a verified artifact can still have a queued plan that
	// has not committed its file operations yet.
	var rootRunning bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acquisitions r WHERE r.id IN (`+oneShotRootsQuery+`) AND r.state='running')`, graph.markerID).Scan(&rootRunning); err != nil {
		return oneShotOutcome{}, fmt.Errorf("read one-shot acquisition roots: %w", err)
	}
	if rootRunning {
		return oneShotOutcome{}, errOneShotChainRunning
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

// assertOwnersSettledTx verifies every anchored Artifact, post-processing plan
// and FileOperation is terminal; a non-terminal owner without live work is an
// anomaly that blocks reclamation instead of guessing.
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
	planRows, err := persistence.New(tx).ListAnchoredPlanStates(ctx, graph.artifactIDs)
	if err != nil {
		return fmt.Errorf("read anchored post-processing plans: %w", err)
	}
	for _, row := range planRows {
		if row.State != "succeeded" && row.State != "failed" {
			return fmt.Errorf("post-processing plan %s is still %q", row.ID, row.State)
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

type oneShotCleanupBlock struct {
	code   string
	reason string
}

// proveOneShotCleanupTx is shared by reclaim and recheck under the marker
// lock. An open chain waits for its own work to settle. A settled chain
// needs a drained ledger and no owner rows outside the deletion scope.
func (s *OneShotReclaimService) proveOneShotCleanupTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph, row oneShotMarkerRow) (live bool, blocked *oneShotCleanupBlock, err error) {
	_, outcomeErr := s.evaluateChainTx(ctx, tx, graph)
	if errors.Is(outcomeErr, errOneShotChainRunning) {
		return true, nil, nil
	}
	if live, err := s.chainLiveTx(ctx, tx, graph); err != nil || live {
		return live, nil, err
	}
	if outcomeErr != nil {
		return false, &oneShotCleanupBlock{"chain_unsettled", outcomeErr.Error()}, nil
	}
	if len(row.Leases) > 0 {
		return false, &oneShotCleanupBlock{"admission_ledger_not_drained", "the admission ledger must be empty before reclamation"}, nil
	}
	table, err := unclassifiedOwnerDependentTx(ctx, tx, graph.animeID)
	if err != nil {
		return false, nil, err
	}
	if table != "" {
		return false, &oneShotCleanupBlock{"unclassified_dependent", "the temporary owner still has rows in " + table}, nil
	}
	return false, nil, nil
}

// chainLiveTx reports live work anywhere in the marker's chain: the anchored
// owners' WorkItems, or the running root and root work that
// one_shot_chain_open reads. It is the predicate a blocked marker resumes on,
// so the drain never blocks a marker a later pass would resume.
func (s *OneShotReclaimService) chainLiveTx(ctx context.Context, tx *sql.Tx, graph oneShotGraph) (bool, error) {
	live, err := s.hasLiveWorkItemsTx(ctx, tx, graph)
	if err != nil || live {
		return live, err
	}
	open, err := persistence.New(tx).OneShotChainOpen(ctx, graph.markerID)
	if err != nil {
		return false, fmt.Errorf("check open one-shot chain: %w", err)
	}
	return open, nil
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
