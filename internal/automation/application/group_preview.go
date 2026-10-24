package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	selectiondomain "bansoko/internal/selection/domain"
)

// ProviderGroupOverlay is one group's unsaved per-group mode, keyed by
// "<provider>:<group_ref>". It stands in for the values a subgroups save syncs
// onto an existing provider-group row before the recompute ranks: Manual is the
// explicit always (true) / never (false) intent, nil for an automatic group.
type ProviderGroupOverlay struct {
	Manual       *bool
	CountInLimit bool
}

// SubscriptionSelectionPreview is what RecomputeSubscriptionSelections would
// decide for one subscription. Provider is empty for an origin that has no
// provider group selection (custom), with no decisions.
type SubscriptionSelectionPreview struct {
	Provider  string
	Decisions []selectiondomain.GroupSelectionDecision
}

// PreviewSubscriptionSelections answers, without writing, what a subgroups
// save would select: the same persisted rows and latest group snapshot
// RecomputeSubscriptionSelections ranks, with overlay applied to the existing
// rows exactly as the save syncs them, ranked under policy by the same
// rankDiscoveryScope. It runs in a read-only transaction so the rows and the
// snapshot are one consistent read; nothing is locked or written.
func PreviewSubscriptionSelections(ctx context.Context, db *sql.DB, subscriptionID string, policy selectiondomain.DiscoveryPolicy, overlay map[string]ProviderGroupOverlay) (SubscriptionSelectionPreview, error) {
	if db == nil {
		return SubscriptionSelectionPreview{}, errors.New("selection preview database is nil")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SubscriptionSelectionPreview{}, fmt.Errorf("begin selection preview: %w", err)
	}
	defer tx.Rollback()
	var provider, entry string
	if err := tx.QueryRowContext(ctx, `SELECT origin,COALESCE(origin_entry_ref,'') FROM subscriptions WHERE id=$1`, subscriptionID).Scan(&provider, &entry); err != nil {
		return SubscriptionSelectionPreview{}, fmt.Errorf("read subscription for selection preview: %w", err)
	}
	if provider != "anibt" && provider != "mikan" {
		return SubscriptionSelectionPreview{}, nil
	}
	result, err := latestPersistedGroupResult(ctx, tx, provider, entry)
	if err != nil {
		return SubscriptionSelectionPreview{}, err
	}
	rows, err := readDiscoveryScopeRows(ctx, tx, subscriptionID, provider, false)
	if err != nil {
		return SubscriptionSelectionPreview{}, err
	}
	decisions, err := rankDiscoveryScope(provider, overlayDiscoveryRows(provider, rows, overlay), result, policy, false)
	if err != nil {
		return SubscriptionSelectionPreview{}, err
	}
	return SubscriptionSelectionPreview{Provider: provider, Decisions: decisions}, nil
}

// overlayDiscoveryRows applies the unsaved per-group modes to the rows that
// exist, as the save's sync updates only existing rows; a group with no row
// keeps resolving through the policy alone.
func overlayDiscoveryRows(provider string, rows map[string]discoveryObservation, overlay map[string]ProviderGroupOverlay) map[string]discoveryObservation {
	result := make(map[string]discoveryObservation, len(rows))
	for ref, row := range rows {
		if value, ok := overlay[provider+":"+ref]; ok {
			row.count = value.CountInLimit
			row.manual = nil
			if value.Manual != nil {
				manual := *value.Manual
				row.manual = &manual
			}
		}
		result[ref] = row
	}
	return result
}
