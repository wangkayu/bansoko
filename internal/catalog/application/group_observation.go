package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	catalogdomain "bansoko/internal/catalog/domain"
	providerdomain "bansoko/internal/provider/domain"
	"github.com/google/uuid"
)

// PersistGroupObservationTx is the catalog-owned whole-observation write used
// by both manual discovery and automation. The caller owns the enclosing tx;
// allocation, canonical freezing and the typed SQL write never span provider I/O.
func PersistGroupObservationTx(ctx context.Context, tx *sql.Tx, result providerdomain.GroupReadResult) (providerdomain.GroupSnapshot, error) {
	if tx == nil {
		return providerdomain.GroupSnapshot{}, errors.New("group observation transaction is nil")
	}
	key := "bansoko:provider-groups:" + string(result.Provider) + ":" + result.CatalogEntryRef
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key); err != nil {
		return providerdomain.GroupSnapshot{}, err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM provider_group_snapshots WHERE provider=$1 AND catalog_entry_ref=$2`, result.Provider, result.CatalogEntryRef).Scan(&version); err != nil {
		return providerdomain.GroupSnapshot{}, err
	}
	snapshot := providerdomain.GroupSnapshot{ID: uuid.NewString(), Provider: result.Provider, CatalogEntryRef: result.CatalogEntryRef,
		ObservedAt: result.ObservedAt, SourceVersion: result.SourceVersion, SchemaVersion: result.SchemaVersion, Version: version,
		Groups: append([]providerdomain.GroupSnapshotEntry(nil), result.Groups...)}
	if err := snapshot.Validate(); err != nil {
		return snapshot, err
	}
	observation, err := catalogdomain.FreezeGroupObservation(snapshot, result)
	if err != nil {
		return snapshot, err
	}
	canonical, digest, err := observation.Canonical()
	if err != nil {
		return snapshot, err
	}
	type locatorProjection struct {
		GroupRef    string                     `json:"group_ref"`
		FeedLocator providerdomain.FeedLocator `json:"feed_locator"`
	}
	entries := make([]locatorProjection, 0, len(snapshot.Groups))
	for _, g := range snapshot.Groups {
		entries = append(entries, locatorProjection{GroupRef: g.Ref.ExternalID, FeedLocator: g.FeedLocator})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return snapshot, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT publish_group_observation($1,$2,$3::jsonb)`, string(canonical), digest, raw); err != nil {
		return snapshot, fmt.Errorf("publish group observation: %w", err)
	}
	return snapshot, nil
}

// ReadGroupObservationTx reads one immutable canonical value, never latest.
// An old NULL capsule is unverifiable, not an empty/complete observation.
func ReadGroupObservationTx(ctx context.Context, tx *sql.Tx, id string) (catalogdomain.GroupObservation, error) {
	if tx == nil {
		return catalogdomain.GroupObservation{}, errors.New("group observation transaction is nil")
	}
	var canonical, digest sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT canonical_observation,observation_digest FROM provider_group_snapshots WHERE id=$1`, id).Scan(&canonical, &digest); err != nil {
		return catalogdomain.GroupObservation{}, err
	}
	if !canonical.Valid || !digest.Valid {
		return catalogdomain.GroupObservation{}, catalogdomain.ErrGroupObservationUnverifiable
	}
	return catalogdomain.DecodeGroupObservation([]byte(canonical.String), digest.String)
}

// ReuseUnchangedGroupObservationTx is the scheduled-discovery shortcut: when
// the latest sealed observation of the same (provider, entry) already says
// exactly what result says, apart from observation times, no new snapshot is
// written. Sealed observations are immutable, so the freshness of the reused
// one moves through the refresh-health row (last_attempt_at/last_success_at)
// and its retention is extended. It returns the reused snapshot header and
// true, or false when a new observation must be persisted.
func ReuseUnchangedGroupObservationTx(ctx context.Context, tx *sql.Tx, result providerdomain.GroupReadResult) (providerdomain.GroupSnapshot, bool, error) {
	if tx == nil {
		return providerdomain.GroupSnapshot{}, false, errors.New("group observation transaction is nil")
	}
	key := "bansoko:provider-groups:" + string(result.Provider) + ":" + result.CatalogEntryRef
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key); err != nil {
		return providerdomain.GroupSnapshot{}, false, err
	}
	var id string
	var version int64
	var canonical, digest sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT id, version, canonical_observation, observation_digest FROM provider_group_snapshots
WHERE provider = $1 AND catalog_entry_ref = $2 ORDER BY version DESC LIMIT 1`, result.Provider, result.CatalogEntryRef).Scan(&id, &version, &canonical, &digest)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!canonical.Valid || !digest.Valid)) {
		return providerdomain.GroupSnapshot{}, false, nil
	}
	if err != nil {
		return providerdomain.GroupSnapshot{}, false, fmt.Errorf("read latest group observation: %w", err)
	}
	previous, err := catalogdomain.DecodeGroupObservation([]byte(canonical.String), digest.String)
	if err != nil {
		return providerdomain.GroupSnapshot{}, false, nil
	}
	snapshot := providerdomain.GroupSnapshot{ID: id, Provider: result.Provider, CatalogEntryRef: result.CatalogEntryRef,
		ObservedAt: result.ObservedAt, SourceVersion: result.SourceVersion, SchemaVersion: result.SchemaVersion, Version: version,
		Groups: append([]providerdomain.GroupSnapshotEntry(nil), result.Groups...)}
	if err := snapshot.Validate(); err != nil {
		return snapshot, false, err
	}
	current, err := catalogdomain.FreezeGroupObservation(snapshot, result)
	if err != nil {
		return snapshot, false, err
	}
	if !sameGroupObservationContent(previous, current) {
		return snapshot, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_group_snapshots SET retention_until = CURRENT_TIMESTAMP + INTERVAL '30 days' WHERE id = $1`, id); err != nil {
		return snapshot, false, fmt.Errorf("extend unchanged group observation retention: %w", err)
	}
	state := "fresh"
	if len(result.Groups) == 0 {
		state = "empty"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_group_refresh_health(provider, catalog_entry_ref, state, last_attempt_at, last_success_at, version)
VALUES ($1, $2, $3, $4, $4, 1)
ON CONFLICT (provider, catalog_entry_ref) DO UPDATE SET state = EXCLUDED.state,
    last_attempt_at = EXCLUDED.last_attempt_at, last_success_at = EXCLUDED.last_success_at,
    error_code = NULL, error_message = NULL, version = provider_group_refresh_health.version + 1,
    updated_at = CURRENT_TIMESTAMP`, result.Provider, result.CatalogEntryRef, state, result.ObservedAt); err != nil {
		return snapshot, false, fmt.Errorf("refresh unchanged group observation health: %w", err)
	}
	return snapshot, true, nil
}

// sameGroupObservationContent compares two observations ignoring the
// snapshot identity and the observation times.
func sameGroupObservationContent(a, b catalogdomain.GroupObservation) bool {
	normalize := func(o catalogdomain.GroupObservation) catalogdomain.GroupObservation {
		o.SnapshotRef, o.SnapshotVersion, o.ObservedAt = "", 0, ""
		groups := make([]catalogdomain.ObservedGroup, len(o.Groups))
		copy(groups, o.Groups)
		for i := range groups {
			groups[i].ObservedAt = ""
		}
		o.Groups = groups
		return o
	}
	left, err := json.Marshal(normalize(a))
	if err != nil {
		return false
	}
	right, err := json.Marshal(normalize(b))
	return err == nil && string(left) == string(right)
}
