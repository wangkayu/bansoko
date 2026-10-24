package application

import (
	"context"
	"database/sql"
)

// Persisted selections are observation/ranking truth, not reconstructed policy
// entries. Availability and generation must come from the actual owner rows.
func (s *Service) readPersistedSelections(ctx context.Context, subscriptionID string) ([]ProviderGroupSelection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,provider,group_ref,display_name,selection_mode,manual_selected,effective_selected,effective_score,count_in_limit,version,available,availability_generation FROM provider_group_selections WHERE subscription_id=$1 ORDER BY (effective_selected AND available) DESC, effective_score DESC, display_name COLLATE "C", provider COLLATE "C", group_ref COLLATE "C"`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProviderGroupSelection{}
	for rows.Next() {
		var value ProviderGroupSelection
		var manual sql.NullBool
		value.SubscriptionID = subscriptionID
		if err := rows.Scan(&value.ID, &value.Provider, &value.GroupRef, &value.DisplayName, &value.SelectionMode, &manual, &value.EffectiveSelected, &value.EffectiveScore, &value.CountInLimit, &value.Version, &value.Available, &value.AvailabilityGeneration); err != nil {
			return nil, err
		}
		if manual.Valid {
			v := manual.Bool
			value.ManualSelected = &v
		}
		value.EffectiveSelected = value.EffectiveSelected && value.Available
		result = append(result, value)
	}
	return result, rows.Err()
}
