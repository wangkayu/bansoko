package application

import (
	"bansoko/internal/apperrors"
	catalog "bansoko/internal/catalog/application"
	"bansoko/internal/persistence/txscope"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ResolveOwnerCustomSnapshotSource resolves the single saved custom RSS source
// of a pending (not started) custom subscription for its RSS test bench. It
// reads only: the caller fetches the feed and persists the snapshot under
// ValidateOwnerCustomSnapshotSourceTx.
func (s *Service) ResolveOwnerCustomSnapshotSource(ctx context.Context, managedAnimeID string) (catalog.FrozenCustomSnapshotSource, error) {
	if s == nil || s.db == nil {
		return catalog.FrozenCustomSnapshotSource{}, fmt.Errorf("owner custom source database unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return catalog.FrozenCustomSnapshotSource{}, err
	}
	defer tx.Rollback()
	source, err := resolveOwnerCustomSourceTx(ctx, tx, managedAnimeID)
	if err != nil {
		return source, err
	}
	return source, tx.Commit()
}

// ValidateOwnerCustomSnapshotSourceTx re-resolves the source inside the
// snapshot's persisting transaction (the subscription row stays share-locked
// through commit): a subscription started, retired or edited since the fetch
// began persists nothing.
func (s *Service) ValidateOwnerCustomSnapshotSourceTx(ctx context.Context, scope txscope.Tx, managedAnimeID string, expected catalog.FrozenCustomSnapshotSource) error {
	tx := scope.SQLTx()
	if tx == nil {
		return fmt.Errorf("owner custom source validation requires transaction")
	}
	source, err := resolveOwnerCustomSourceTx(ctx, tx, managedAnimeID)
	if err != nil {
		return err
	}
	if source != expected {
		return &apperrors.Business{Status: 409, Code: "CUSTOM_SOURCE_CHANGED", Message: "The subscription's custom source changed."}
	}
	return nil
}

func resolveOwnerCustomSourceTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) (catalog.FrozenCustomSnapshotSource, error) {
	var lifecycle, state, origin, subscriptionID string
	err := tx.QueryRowContext(ctx, `
SELECT a.lifecycle_state, sub.state, sub.origin, sub.id
FROM managed_anime a
JOIN subscriptions sub ON sub.managed_anime_id = a.id
WHERE a.id = $1
FOR SHARE OF sub`, managedAnimeID).Scan(&lifecycle, &state, &origin, &subscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return catalog.FrozenCustomSnapshotSource{}, fmt.Errorf("read owner custom source: %w", err)
	}
	if lifecycle != "active" {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 409, Code: "MANAGED_ANIME_RETIRED", Message: "The managed anime is retired and admits no new work."}
	}
	if origin != "custom" {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 422, Code: "CUSTOM_SOURCE_REQUIRED", Message: "A custom subscription with one RSS source is required."}
	}
	// A started subscription reads its list from its RSS runtime; the bench
	// scope exists only before start.
	if state != "pending" {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 409, Code: "CUSTOM_SNAPSHOT_SCOPE_INVALID", Message: "Only a subscription that has not been started has a test-bench feed."}
	}
	rows, err := tx.QueryContext(ctx, `SELECT source_kind, locator, group_name FROM rss_sources WHERE subscription_id = $1 ORDER BY id`, subscriptionID)
	if err != nil {
		return catalog.FrozenCustomSnapshotSource{}, fmt.Errorf("read owner custom source: %w", err)
	}
	defer rows.Close()
	type sourceRow struct{ kind, locator, group string }
	var sources []sourceRow
	for rows.Next() {
		var row sourceRow
		if err := rows.Scan(&row.kind, &row.locator, &row.group); err != nil {
			return catalog.FrozenCustomSnapshotSource{}, fmt.Errorf("scan owner custom source: %w", err)
		}
		sources = append(sources, row)
	}
	if err := rows.Err(); err != nil {
		return catalog.FrozenCustomSnapshotSource{}, fmt.Errorf("read owner custom source: %w", err)
	}
	if len(sources) != 1 || sources[0].kind != "custom_rss" || strings.TrimSpace(sources[0].group) == "" {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 422, Code: "CUSTOM_SOURCE_REQUIRED", Message: "A custom subscription with one RSS source is required."}
	}
	source := sources[0]
	entry, group := catalog.CustomSnapshotScope(catalog.OwnerCustomSnapshotIdentity(managedAnimeID), source.locator, source.group)
	return catalog.FrozenCustomSnapshotSource{Identity: entry, GroupRef: group, GroupName: source.group, Locator: source.locator}, nil
}
