// Package groupsource owns the one upsert of a provider group's rss_sources row
// and its runtime row. Discovery (the dispatcher) and the owner's pre-start
// offset entry both create the same row through it, so a link the owner made
// before discovery ran is the very row discovery later refreshes.
package groupsource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
)

// PendingSnapshotVersion marks a row created before discovery has observed its
// group. It is not a "v<N>" version, so the first real discovery overwrites the
// locator and provenance while the owner's episode_offset is kept.
const PendingSnapshotVersion = "pending"

// ConnectionSource provides the current user-configured connections.
type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

// ReaderEndpoint resolves the connection endpoint for a provider kind: the one
// enabled connection, else the kind's default. An unresolvable endpoint is "".
func ReaderEndpoint(ctx context.Context, source ConnectionSource, providerKind providerdomain.Kind) string {
	if source == nil {
		return ""
	}
	connections, err := source.ListConnections(ctx)
	if err != nil {
		return ""
	}
	selected, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionKind(providerKind))
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return ""
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(configurationdomain.ConnectionKind(providerKind))
		if !ok {
			return ""
		}
		selected = fallback
	}
	return selected.Endpoint
}

// Source is one provider group source to upsert.
type Source struct {
	SubscriptionID  string
	Provider        string
	CatalogEntryRef string
	GroupExternalID string
	DisplayName     string
	AnimeTitle      string
	RSSURL          string
	SnapshotVersion string
}

// UpsertTx upserts the provider_group source and ensures its runtime row. A
// stale discovery snapshot leaves an existing row untouched. inserted reports a
// row this call created. episode_offset is never written by the conflict path.
func UpsertTx(ctx context.Context, tx *sql.Tx, src Source) (sourceID string, inserted bool, err error) {
	providerKind := providerdomain.Kind(src.Provider)
	groupRef, err := providerdomain.NewGroupRef(providerKind, src.GroupExternalID)
	if err != nil {
		return "", false, fmt.Errorf("validate provider group ref: %w", err)
	}
	if err := (providerdomain.FeedLocator{
		Provider:        providerKind,
		CatalogEntryRef: src.CatalogEntryRef,
		Group:           groupRef,
	}).Validate(); err != nil {
		return "", false, fmt.Errorf("validate provider feed locator: %w", err)
	}

	sourceID = uuid.NewString()
	upsertErr := tx.QueryRowContext(ctx, `
INSERT INTO rss_sources(
    id, subscription_id, source_kind, locator, locator_display, group_name, title,
    provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version
)
VALUES ($1, $2, 'provider_group', $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (subscription_id, provider, provider_entry_ref, provider_group_ref)
    WHERE source_kind = 'provider_group' DO UPDATE SET
    locator = EXCLUDED.locator,
    locator_display = EXCLUDED.locator_display,
    group_name = EXCLUDED.group_name,
    title = EXCLUDED.title,
    provider = EXCLUDED.provider,
    provider_entry_ref = EXCLUDED.provider_entry_ref,
    provider_group_ref = EXCLUDED.provider_group_ref,
    discovery_snapshot_version = EXCLUDED.discovery_snapshot_version,
    provenance = EXCLUDED.provenance,
    version = rss_sources.version + 1,
    updated_at = NOW()
WHERE
    (
        substring(rss_sources.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NULL
        AND substring(EXCLUDED.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NOT NULL
    )
    OR (
        substring(rss_sources.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NOT NULL
        AND substring(EXCLUDED.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NOT NULL
        AND CAST(substring(EXCLUDED.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') AS NUMERIC)
            >= CAST(substring(rss_sources.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') AS NUMERIC)
    )
RETURNING id, (xmax = 0)`,
		sourceID, src.SubscriptionID, src.RSSURL, src.RSSURL, src.DisplayName, src.AnimeTitle,
		src.Provider, src.CatalogEntryRef, groupRef.ExternalID, src.SnapshotVersion).Scan(&sourceID, &inserted)
	if upsertErr != nil && !errors.Is(upsertErr, sql.ErrNoRows) {
		return "", false, fmt.Errorf("upsert rss source: %w", upsertErr)
	}
	if errors.Is(upsertErr, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `
SELECT id
FROM rss_sources
WHERE subscription_id = $1 AND provider = $2 AND provider_entry_ref = $3
  AND provider_group_ref = $4 AND source_kind = 'provider_group'`,
			src.SubscriptionID, src.Provider, src.CatalogEntryRef, groupRef.ExternalID).Scan(&sourceID); err != nil {
			return "", false, fmt.Errorf("read existing rss source after stale snapshot: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)
ON CONFLICT (rss_source_id) DO NOTHING`, sourceID); err != nil {
		return "", false, fmt.Errorf("ensure rss source runtime: %w", err)
	}
	return sourceID, inserted, nil
}
