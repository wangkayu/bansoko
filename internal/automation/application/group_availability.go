package application

import (
	"context"
	"database/sql"
	"errors"

	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

// applyDiscoveryScope commits the complete scope as one owner transaction.
// Only an explicit authoritative action can withdraw observed availability.
func (d *Dispatcher) applyDiscoveryScope(ctx context.Context, subscriptionID, provider string, result providerdomain.GroupReadResult, decisions []selectiondomain.GroupSelectionDecision, authoritative bool) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := d.applyDiscoveryScopeTx(ctx, tx, subscriptionID, provider, result, decisions, authoritative); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Dispatcher) applyDiscoveryScopeTx(ctx context.Context, tx *sql.Tx, subscriptionID, provider string, result providerdomain.GroupReadResult, decisions []selectiondomain.GroupSelectionDecision, authoritative bool) error {
	if authoritative && (!result.Complete || result.CompletenessEvidence == "") {
		return errors.New("authoritative discovery requires a proven complete result")
	}
	statuses := map[string]bool{}
	for _, group := range result.Groups {
		if authoritative && group.Active == nil {
			return errors.New("authoritative discovery requires explicit group status")
		}
		statuses[group.Ref.ExternalID] = group.Active == nil || *group.Active
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id=$1 FOR UPDATE`, subscriptionID).Scan(&state); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_group_scope_generations(subscription_id,provider) VALUES($1,$2) ON CONFLICT DO NOTHING`, subscriptionID, provider); err != nil {
		return err
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `UPDATE provider_group_scope_generations SET generation=generation+1 WHERE subscription_id=$1 AND provider=$2 RETURNING generation`, subscriptionID, provider).Scan(&generation); err != nil {
		return err
	}
	if authoritative {
		if _, err := tx.ExecContext(ctx, `UPDATE provider_group_selections SET available=FALSE,availability_generation=$3,version=version+1,updated_at=NOW() WHERE subscription_id=$1 AND provider=$2`, subscriptionID, provider, generation); err != nil {
			return err
		}
	}
	for _, decision := range decisions {
		ref := decision.Group.Ref.ExternalID
		display := ref
		existed := false
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_group_selections WHERE subscription_id=$1 AND provider=$2 AND group_ref=$3)`, subscriptionID, provider, ref).Scan(&existed); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT display_name FROM provider_group_selections WHERE subscription_id=$1 AND provider=$2 AND group_ref=$3`, subscriptionID, provider, ref).Scan(&display); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, g := range result.Groups {
			if g.Ref.ExternalID == ref {
				display = g.DisplayName
				break
			}
		}
		if _, err := upsertProviderGroupSelection(ctx, tx, subscriptionID, provider, ref, display, decision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE provider_group_selections SET effective_selected=$4, count_in_limit=$5 WHERE subscription_id=$1 AND provider=$2 AND group_ref=$3`, subscriptionID, provider, ref, decision.Outcome == selectiondomain.GroupSelected, decision.Group.CountInLimit); err != nil {
			return err
		}
		if authoritative {
			if _, err := tx.ExecContext(ctx, `UPDATE provider_group_selections SET available=$4,availability_generation=$3 WHERE subscription_id=$1 AND provider=$2 AND group_ref=$5`, subscriptionID, provider, generation, statuses[ref], ref); err != nil {
				return err
			}
		} else {
			if !existed {
				if active, observed := statuses[ref]; observed && !active {
					if _, err := tx.ExecContext(ctx, `UPDATE provider_group_selections SET available=FALSE WHERE subscription_id=$1 AND provider=$2 AND group_ref=$3`, subscriptionID, provider, ref); err != nil {
						return err
					}
				}
			}
			// Automatic observations never retire a group or resurrect a manually
			// withdrawn group. New rows begin available; inactive observations stay facts.
			if _, err := tx.ExecContext(ctx, `UPDATE provider_group_selections SET availability_generation=$3 WHERE subscription_id=$1 AND provider=$2 AND group_ref=$4`, subscriptionID, provider, generation, ref); err != nil {
				return err
			}
		}
	}
	return nil
}
