package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	maintenancepkg "bansoko/internal/runtime/maintenance"
)

// syncMaintenanceFromRecovery initializes the in-process maintenance gate
// from the durable deployment recovery checkpoint:
//
//   - no run or no checkpoint table (pre-030 backup) → stay serving;
//   - latest run completed with gate passed → stay serving;
//   - any other state (running / failed / completed without passed gate) →
//     enter maintenance and keep the automation resume gate closed until a
//     bansoko-recovery run settles it.
//
// The server reads the checkpoint only at startup: a deployment rollback
// always restarts the server, and the recovery CLI is the only
// writer of the gate state.
func syncMaintenanceFromRecovery(ctx context.Context, db *sql.DB, gate *maintenancepkg.Gate) error {
	var (
		state   string
		gateRaw []byte
	)
	err := db.QueryRowContext(ctx, `
SELECT state, COALESCE(checkpoint->'gate', '{}'::jsonb)
FROM deployment_recovery_runs
ORDER BY updated_at DESC, id DESC
LIMIT 1`).Scan(&state, &gateRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // no recovery run: normal serving
		}
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "42P01" {
			return nil // pre-030 schema: no checkpoint table, no run
		}
		return fmt.Errorf("read deployment recovery run: %w", err)
	}
	if state == "completed" {
		var gateStatus struct {
			Passed bool `json:"passed"`
		}
		if err := json.Unmarshal(gateRaw, &gateStatus); err == nil && gateStatus.Passed {
			return nil // recovery settled with the automation resume gate passed
		}
	}
	gate.EnterMaintenance()
	gate.SetAutomationResumeGate(false)
	return nil
}
