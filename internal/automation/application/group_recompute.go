package application

import (
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	"context"
	"database/sql"
	"errors"
)

// RecomputeSubscriptionSelections uses the identical persisted observation union
// as discovery, without fetching or retiring availability. Caller owns commit.
func (d *Dispatcher) RecomputeSubscriptionSelections(ctx context.Context, scope txscope.Tx, subscriptionID, managedAnimeID string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("group recompute transaction missing")
	}
	reader, ok := d.PolicyReader.(DiscoveryPolicyTxReader)
	if !ok {
		return errors.New("transactional group policy reader missing")
	}
	var provider, entry string
	if err := tx.QueryRowContext(ctx, `SELECT origin,COALESCE(origin_entry_ref,'') FROM subscriptions WHERE id=$1 FOR UPDATE`, subscriptionID).Scan(&provider, &entry); err != nil {
		return err
	}
	if provider != "anibt" && provider != "mikan" {
		return nil
	}
	policy, err := reader.ReadDiscoveryPolicyInTx(ctx, scope, managedAnimeID)
	if err != nil {
		return err
	}
	result, err := latestPersistedGroupResult(ctx, tx, provider, entry)
	if err != nil {
		return err
	}
	decisions, err := rankDiscoveryScopeTx(ctx, tx, subscriptionID, provider, result, policy, false)
	if err != nil {
		return err
	}
	return d.applyDiscoveryScopeTx(ctx, tx, subscriptionID, provider, result, decisions, false)
}

// latestPersistedGroupResult is the latest persisted group snapshot of one
// catalog entry as a non-authoritative read result. No snapshot is an empty
// result: the persisted rows alone then make up the union.
func latestPersistedGroupResult(ctx context.Context, tx *sql.Tx, provider, entry string) (providerdomain.GroupReadResult, error) {
	result := providerdomain.GroupReadResult{Provider: providerdomain.Kind(provider), CatalogEntryRef: entry}
	var snapshotID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM provider_group_snapshots WHERE provider=$1 AND catalog_entry_ref=$2 ORDER BY version DESC LIMIT 1`, provider, entry).Scan(&snapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	groups, err := readPersistedSnapshotGroups(ctx, tx, snapshotID, result.Provider)
	if err != nil {
		return result, err
	}
	for _, group := range groups {
		result.Groups = append(result.Groups, providerdomain.GroupSnapshotEntry{Ref: group.ref, DisplayName: group.displayName, Active: group.active})
	}
	return result, nil
}
