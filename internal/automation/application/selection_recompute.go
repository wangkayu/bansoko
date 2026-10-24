package application

import (
	"context"
	"database/sql"
	"fmt"

	providerdomain "bansoko/internal/provider/domain"
)

type persistedSnapshotGroup struct {
	ref         providerdomain.GroupRef
	displayName string
	active      *bool
}

func readPersistedSnapshotGroups(ctx context.Context, tx rowQuerier, snapshotID string, provider providerdomain.Kind) ([]persistedSnapshotGroup, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT e->>'group_ref' AS provider_group_ref, e->>'display_name' AS display_name,
       CASE WHEN g.value->'available'->>'state'='known' THEN (g.value->'available'->>'value')::boolean ELSE NULL END
FROM provider_group_snapshots s
CROSS JOIN LATERAL jsonb_array_elements(s.entries) e
LEFT JOIN LATERAL jsonb_array_elements(s.observation->'groups') g(value) ON g.value->>'group_ref'=e->>'group_ref'
WHERE s.id = $1 AND s.provider = $2
ORDER BY e->>'group_ref'`, snapshotID, provider)
	if err != nil {
		return nil, fmt.Errorf("read persisted provider groups: %w", err)
	}
	defer rows.Close()

	groups := make([]persistedSnapshotGroup, 0)
	for rows.Next() {
		var externalID, displayName string
		var available sql.NullBool
		if err := rows.Scan(&externalID, &displayName, &available); err != nil {
			return nil, fmt.Errorf("scan persisted provider group: %w", err)
		}
		ref, err := providerdomain.NewGroupRef(provider, externalID)
		if err != nil {
			return nil, fmt.Errorf("validate persisted provider group %q: %w", externalID, err)
		}
		var active *bool
		if available.Valid {
			value := available.Bool
			active = &value
		}
		groups = append(groups, persistedSnapshotGroup{ref: ref, displayName: displayName, active: active})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate persisted provider groups: %w", err)
	}
	return groups, nil
}
