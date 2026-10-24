package application

import (
	"context"
	"database/sql"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
)

// Service-bound lookups over the shared anime configuration readers and the
// lifecycle fence.

func (s *Service) readOverrides(ctx context.Context, managedAnimeID string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	return animeapplication.ReadOverridesFrom(ctx, s.db, managedAnimeID)
}

func (s *Service) lockActiveLifecycleTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	return s.fences.LockActiveTx(ctx, tx, managedAnimeID)
}

func (s *Service) lockActiveLifecycleForSourceTx(ctx context.Context, tx *sql.Tx, rssSourceID string) error {
	return s.fences.LockActiveForSourceTx(ctx, tx, rssSourceID)
}
