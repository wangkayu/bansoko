package application

import (
	"context"
	"database/sql"
)

// sourceScopeSelected reads the provider-namespaced identity carried by the
// source, never its display group name. Custom sources have no provider gate.
func sourceScopeSelected(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID string, lock bool) (bool, error) {
	query := `SELECT src.source_kind, (selection.effective_selected AND selection.available)
 FROM rss_sources src
 LEFT JOIN provider_group_selections selection ON selection.subscription_id=src.subscription_id AND selection.provider=src.provider AND selection.group_ref=src.provider_group_ref
 WHERE src.id=$1`
	if lock {
		// LEFT JOIN rows cannot be locked. Lock the exact selection separately so
		// concurrent policy updates cannot invalidate admission after this check.
		var id string
		err := q.QueryRowContext(ctx, `SELECT selection.id FROM provider_group_selections selection JOIN rss_sources src ON selection.subscription_id=src.subscription_id AND selection.provider=src.provider AND selection.group_ref=src.provider_group_ref WHERE src.id=$1 FOR UPDATE OF selection`, sourceID).Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
	}
	var kind string
	var selected sql.NullBool
	err := q.QueryRowContext(ctx, query, sourceID).Scan(&kind, &selected)
	if err != nil {
		return false, err
	}
	return groupScopeAllowed(kind, selected.Valid, selected.Bool), nil
}
func groupScopeAllowed(kind string, exists, selected bool) bool {
	return kind != "provider_group" || (exists && selected)
}
