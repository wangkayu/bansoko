package application

import (
	"context"
	"database/sql"

	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

// rowQuerier is the read surface shared by a locking transaction and a
// read-only preview: both *sql.Tx and *sql.DB satisfy it.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// discoveryObservation is one persisted provider-group row as ranking reads it.
type discoveryObservation struct {
	name      string
	available bool
	manual    *bool
}

// rankDiscoveryScopeTx ranks the entire available observation union, including
// manual exceptions. Display names are scoring evidence, never identity keys.
func rankDiscoveryScopeTx(ctx context.Context, tx *sql.Tx, subscriptionID, provider string, result providerdomain.GroupReadResult, policy discoveryPolicy, authoritative bool) ([]selectiondomain.GroupSelectionDecision, error) {
	groups, err := readDiscoveryScopeRows(ctx, tx, subscriptionID, provider, true)
	if err != nil {
		return nil, err
	}
	return rankDiscoveryScope(provider, groups, result, policy, authoritative)
}

// readDiscoveryScopeRows reads one subscription's persisted provider-group rows
// for one provider. lock takes them FOR UPDATE, as a recompute that writes the
// decisions back must; a preview reads them without a lock.
func readDiscoveryScopeRows(ctx context.Context, q rowQuerier, subscriptionID, provider string, lock bool) (map[string]discoveryObservation, error) {
	query := `SELECT group_ref,display_name,available,selection_mode,manual_selected FROM provider_group_selections WHERE subscription_id=$1 AND provider=$2 ORDER BY group_ref`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := q.QueryContext(ctx, query, subscriptionID, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]discoveryObservation{}
	for rows.Next() {
		var ref, name, mode string
		var available bool
		var manual sql.NullBool
		if err := rows.Scan(&ref, &name, &available, &mode, &manual); err != nil {
			return nil, err
		}
		o := discoveryObservation{name: name, available: available}
		if mode == "manual" && manual.Valid {
			v := manual.Bool
			o.manual = &v
		}
		groups[ref] = o
	}
	return groups, rows.Err()
}

// rankDiscoveryScope is the pure ranking step of rankDiscoveryScopeTx: the
// persisted rows merged with the read result's groups, ranked under policy.
func rankDiscoveryScope(provider string, persisted map[string]discoveryObservation, result providerdomain.GroupReadResult, policy discoveryPolicy, authoritative bool) ([]selectiondomain.GroupSelectionDecision, error) {
	groups := make(map[string]discoveryObservation, len(persisted)+len(result.Groups))
	for ref, o := range persisted {
		if authoritative {
			o.available = false
		}
		groups[ref] = o
	}
	for _, g := range result.Groups {
		o, exists := groups[g.Ref.ExternalID]
		o.name = g.DisplayName
		if authoritative {
			o.available = g.Active != nil && *g.Active
		} else if !exists {
			o.available = g.Active == nil || *g.Active
		}
		groups[g.Ref.ExternalID] = o
	}
	observed := make([]selectiondomain.ObservedGroup, 0, len(groups))
	for id, o := range groups {
		observed = append(observed, selectiondomain.ObservedGroup{
			ExternalID: id, Name: o.name, Available: o.available, Manual: o.manual,
		})
	}
	return selectiondomain.RankObservedGroups(providerdomain.Kind(provider), observed, policy)
}
