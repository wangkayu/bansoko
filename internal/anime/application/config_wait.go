package application

import (
	"context"
	"database/sql"
	"fmt"
)

// configWakeBatch bounds the waiting plans one wake or sweep pass makes due;
// the rest are reached by the next pass.
const configWakeBatch = 200

// WakeConfigWaitingPlansTx makes the anime's imports that wait for
// configuration due now, inside the caller's configuration write, which
// commits the wake with itself. A woken plan checks the configuration again
// on its next claim and waits again when it is still incomplete. Only
// per-anime writes wake: a Global section is the default a new draft copies,
// never an input of an existing anime's render.
func WakeConfigWaitingPlansTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	if _, err := tx.ExecContext(ctx, `SELECT public.postprocessing_plan_wake_config($1, $2)`, managedAnimeID, configWakeBatch); err != nil {
		return fmt.Errorf("wake imports waiting for configuration: %w", err)
	}
	return nil
}

// SweepConfigWaitingPlans is the periodic backstop of the wake: it makes one
// batch of waiting plans of any anime due, the longest parked first, and
// reports how many it woke.
func SweepConfigWaitingPlans(ctx context.Context, db *sql.DB) (int, error) {
	var woken int
	if err := db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_wake_config(NULL, $1)`, configWakeBatch).Scan(&woken); err != nil {
		return 0, fmt.Errorf("sweep imports waiting for configuration: %w", err)
	}
	return woken, nil
}
