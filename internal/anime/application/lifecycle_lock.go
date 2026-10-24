package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/apperrors"
)

// lockManagedAnimeLifecycleTx locks one ManagedAnime row FOR UPDATE and
// returns its lifecycle state and fence epoch. It returns sql.ErrNoRows when
// the owner does not exist; callers map that to not-found.
func lockManagedAnimeLifecycleTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) (string, int64, error) {
	var state string
	var epoch int64
	err := tx.QueryRowContext(ctx, `
SELECT lifecycle_state, fence_epoch
FROM managed_anime
WHERE id = $1
FOR UPDATE`, managedAnimeID).Scan(&state, &epoch)
	return state, epoch, err
}

// lockActiveManagedAnimeLifecycleTx is the tx-scoped lifecycle-first lock used
// by callbacks that only have a *sql.Tx (for example the workitems manual-retry
// preflight). It fails closed with the typed retirement boundary when the
// owner is retired and with not-found when the owner does not exist. A blank
// id is a no-op for synthetic owners.
func lockActiveManagedAnimeLifecycleTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	if managedAnimeID == "" {
		return nil
	}
	var state string
	err := tx.QueryRowContext(ctx, `
SELECT lifecycle_state FROM managed_anime WHERE id = $1 FOR UPDATE`, managedAnimeID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return fmt.Errorf("lock managed anime lifecycle: %w", err)
	}
	if state != "active" {
		return managedAnimeRetiredError()
	}
	return nil
}

// lockActiveLifecycleTx locks one ManagedAnime's lifecycle row FOR UPDATE
// before any other owner row. Every visible ManagedAnime mutation must take
// this lock first so it shares retirement's lock order (lifecycle, then
// owner/WorkItem): the two then serialize on one row and the result is either
// a successful mutation or the typed retirement boundary, never a 40P01
// deadlock. A retired marker returns the typed retirement error; a real owner
// whose marker is missing fails closed.
func (s *Service) lockActiveLifecycleTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	return s.fences.LockActiveTx(ctx, tx, managedAnimeID)
}

// lockActiveLifecycleForSourceTx resolves the source's ManagedAnime and locks
// that lifecycle row first, for refresh paths that only carry a source id.
func (s *Service) lockActiveLifecycleForSourceTx(ctx context.Context, tx *sql.Tx, rssSourceID string) error {
	return s.fences.LockActiveForSourceTx(ctx, tx, rssSourceID)
}

// lockActiveLifecycleForReleaseTx resolves the release's ManagedAnime and locks
// that lifecycle row first, for paths that only carry a release id.
func (s *Service) lockActiveLifecycleForReleaseTx(ctx context.Context, tx *sql.Tx, releaseID string) error {
	return s.fences.LockActiveForReleaseTx(ctx, tx, releaseID)
}

// Fences owns the lifecycle-first transaction locks shared by anime owners.
type Fences struct {
	db *sql.DB
}

// NewFences returns the shared lifecycle-fence locker for one database.
func NewFences(db *sql.DB) *Fences {
	return &Fences{db: db}
}

func (f *Fences) LockActiveTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	if f == nil || f.db == nil {
		return errors.New("anime lifecycle fence database is nil")
	}
	state, _, err := lockManagedAnimeLifecycleTx(ctx, tx, managedAnimeID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return fmt.Errorf("lock managed anime lifecycle: %w", err)
	}
	if state != "active" {
		return managedAnimeRetiredError()
	}
	return nil
}

func (f *Fences) LockActiveForSourceTx(ctx context.Context, tx *sql.Tx, rssSourceID string) error {
	var state string
	err := tx.QueryRowContext(ctx, `
SELECT anime.lifecycle_state
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime anime ON anime.id = sub.managed_anime_id
WHERE src.id = $1
FOR UPDATE OF anime`, rssSourceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "rss_source", ID: rssSourceID}
	}
	if err != nil {
		return fmt.Errorf("lock lifecycle for rss source: %w", err)
	}
	if state != "active" {
		return managedAnimeRetiredError()
	}
	return nil
}

func (f *Fences) LockActiveForReleaseTx(ctx context.Context, tx *sql.Tx, releaseID string) error {
	var state string
	err := tx.QueryRowContext(ctx, `
SELECT anime.lifecycle_state
FROM releases release
JOIN rss_sources src ON src.id = release.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime anime ON anime.id = sub.managed_anime_id
WHERE release.id = $1
FOR UPDATE OF anime`, releaseID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "release", ID: releaseID}
	}
	if err != nil {
		return fmt.Errorf("lock lifecycle for release: %w", err)
	}
	if state != "active" {
		return managedAnimeRetiredError()
	}
	return nil
}
