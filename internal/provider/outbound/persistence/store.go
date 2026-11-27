package persistence

import (
	"context"
	"database/sql"

	"bansoko/internal/provider/outbound"
	"bansoko/internal/provider/outbound/persistence/generated"
)

type Store struct{ queries *generated.Queries }

func New(db *sql.DB) Store { return Store{queries: generated.New(db)} }

func (s Store) Load(ctx context.Context) ([]outbound.Health, error) {
	rows, err := s.queries.ListOutboundHealth(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]outbound.Health, 0, len(rows))
	for _, row := range rows {
		states = append(states, outbound.Health{
			Bucket: outbound.BucketKey(row.BucketKey), CooldownUntil: row.CooldownUntil, LastLimitedAt: row.LastLimitedAt,
			ReasonCode: row.ReasonCode, Summary: row.Summary, Consecutive: int(row.Consecutive), Version: row.Version, UpdatedAt: row.UpdatedAt,
		})
	}
	return states, nil
}

func (s Store) Save(ctx context.Context, state outbound.Health) error {
	return s.queries.SaveOutboundHealth(ctx, generated.SaveOutboundHealthParams{
		BucketKey: string(state.Bucket), CooldownUntil: state.CooldownUntil, LastLimitedAt: state.LastLimitedAt,
		ReasonCode: state.ReasonCode, Summary: state.Summary, Consecutive: int32(state.Consecutive), Version: state.Version, UpdatedAt: state.UpdatedAt,
	})
}

var _ outbound.Store = Store{}
