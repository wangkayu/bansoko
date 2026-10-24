package application

import (
	"context"
	"database/sql"
	"errors"
)

type discoveryFence struct{ subscription, owner, generation int64 }

func readDiscoveryFence(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, subscription, provider string) (discoveryFence, error) {
	var f discoveryFence
	err := q.QueryRowContext(ctx, `SELECT s.version, COALESCE(o.subgroups_version,0),COALESCE(g.generation,0) FROM subscriptions s LEFT JOIN anime_business_configs o ON o.managed_anime_id=s.managed_anime_id LEFT JOIN provider_group_scope_generations g ON g.subscription_id=s.id AND g.provider=$2 WHERE s.id=$1`, subscription, provider).Scan(&f.subscription, &f.owner, &f.generation)
	return f, err
}
func verifyDiscoveryFence(ctx context.Context, tx *sql.Tx, subscription, provider string, expected discoveryFence) error {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE id=$1 FOR UPDATE`, subscription).Scan(&version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT o.subgroups_version FROM anime_business_configs o JOIN subscriptions s ON s.managed_anime_id=o.managed_anime_id WHERE s.id=$1 FOR UPDATE OF o`, subscription); err != nil {
		return err
	}
	actual, err := readDiscoveryFence(ctx, tx, subscription, provider)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("group discovery input changed during provider fetch")
	}
	return nil
}
