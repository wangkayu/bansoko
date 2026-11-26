package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// compactOneShotRootsTx compacts every root of the marker with trigger
// detail_expired. It reports false (and the caller rolls back) when a root is
// not compactable at the evaluation instant. It runs after the marker entered
// reclaiming in this transaction: compact accepts the temporary Release that
// still names the root's task only because deleteTemporaryOwnerGraphTx deletes
// that Release before commit (otherwise compact reports release_bound).
func (s *OneShotReclaimService) compactOneShotRootsTx(ctx context.Context, tx *sql.Tx, markerID string, graph oneShotGraph, now sql.NullTime) (bool, error) {
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
	for _, root := range roots {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT acquisition_compact($1,'detail_expired',COALESCE($2::timestamptz,CURRENT_TIMESTAMP))`, root, now).Scan(&raw); err != nil {
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
// (internal/anime/ownership/application/delete_subscription.go). It never touches qB
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
		{"config_scan_requests", func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM config_scan_requests WHERE managed_anime_id = $1`, graph.animeID)
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
