package persistence

import (
	"context"
	"slices"
	"strings"
	"time"

	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
)

type RefreshHealth struct {
	Provider      string
	Kind          string
	Outcomes      provider.Outcomes
	LastAttemptAt time.Time
	LastSuccessAt time.Time
}

type Diagnostics struct {
	Quotas    []outbound.Health
	Refreshes []RefreshHealth
}

type HealthReader struct {
	Store
	Live func() []outbound.Health
}

func (r HealthReader) Read(ctx context.Context) (Diagnostics, error) {
	persisted, err := r.Load(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	quotas := make(map[outbound.BucketKey]outbound.Health, len(persisted))
	for _, row := range persisted {
		quotas[row.Bucket] = row
	}
	if r.Live != nil {
		for _, row := range r.Live() {
			previous := quotas[row.Bucket]
			if row.CooldownUntil.After(previous.CooldownUntil) ||
				(row.CooldownUntil.Equal(previous.CooldownUntil) && row.LastLimitedAt.After(previous.LastLimitedAt)) {
				quotas[row.Bucket] = row
			}
		}
	}
	result := Diagnostics{Quotas: make([]outbound.Health, 0, len(quotas))}
	for _, row := range quotas {
		result.Quotas = append(result.Quotas, row)
	}
	slices.SortFunc(result.Quotas, func(a, b outbound.Health) int { return strings.Compare(string(a.Bucket), string(b.Bucket)) })

	groups, err := r.queries.ListGroupRefreshHealth(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	catalogs, err := r.queries.ListCatalogRefreshHealth(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	collect := func(kind, name, state string, n int64, attempt, success time.Time) {
		// Both queries order by provider so each series is contiguous.
		if rows := result.Refreshes; len(rows) == 0 || rows[len(rows)-1].Provider != name || rows[len(rows)-1].Kind != kind {
			result.Refreshes = append(rows, RefreshHealth{Provider: name, Kind: kind})
		}
		row := &result.Refreshes[len(result.Refreshes)-1]
		row.Outcomes.Count(provider.State(state), int(n))
		if attempt.After(row.LastAttemptAt) {
			row.LastAttemptAt = attempt
		}
		if success.After(row.LastSuccessAt) {
			row.LastSuccessAt = success
		}
	}
	for _, row := range groups {
		collect("groups", row.Provider, row.State.String, row.Count, row.LastAttemptAt, row.LastSuccessAt)
	}
	for _, row := range catalogs {
		collect("catalog", row.Provider, row.State, row.Count, row.LastAttemptAt, row.LastSuccessAt)
	}
	return result, nil
}
