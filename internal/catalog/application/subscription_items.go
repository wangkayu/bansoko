package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/catalog/domain"
)

// SubscriptionItemReader is the only ProviderItemReader: it projects the
// lossless association from external provider catalog entries to existing
// formal owners. A subscription is associated when its origin and
// origin_entry_ref match the external entry. Duplicate subscriptions for the
// same entry are legal; the provider ref is not a deduplication key or owner
// identity, so every owner association is returned. Ordering is active-first
// then creation order; the caller decides how to present them.
type SubscriptionItemReader struct {
	db *sql.DB
}

func NewSubscriptionItemReader(db *sql.DB) SubscriptionItemReader {
	return SubscriptionItemReader{db: db}
}

func (r SubscriptionItemReader) ReadProviderCatalogItems(ctx context.Context, provider string, refs []string) (map[string][]domain.InstanceLinkage, []domain.Item, error) {
	overlays := map[string][]domain.InstanceLinkage{}
	if r.db == nil {
		return overlays, nil, errors.New("catalog database is nil")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if len(refs) == 0 {
		return overlays, nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT s.id, s.managed_anime_id, s.state, s.version, s.origin_entry_ref, lifecycle.lifecycle_state, lifecycle.fence_epoch
FROM subscriptions s
JOIN managed_anime lifecycle ON lifecycle.id = s.managed_anime_id
WHERE s.origin = $1 AND s.origin_entry_ref = ANY($2)
ORDER BY lifecycle.lifecycle_state ASC, s.state ASC, s.created_at ASC`, provider, refs)
	if err != nil {
		return nil, nil, fmt.Errorf("read subscription catalog overlays: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var linkage domain.InstanceLinkage
		var ref string
		if err := rows.Scan(&linkage.SubscriptionRef, &linkage.ManagedAnimeRef, &linkage.SubscriptionState, &linkage.SubscriptionVersion, &ref, &linkage.LifecycleState, &linkage.LifecycleEpoch); err != nil {
			return nil, nil, fmt.Errorf("scan subscription catalog overlay: %w", err)
		}
		overlays[ref] = append(overlays[ref], linkage)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate subscription catalog overlays: %w", err)
	}
	return overlays, nil, nil
}
