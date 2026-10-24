package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	maintenancepkg "bansoko/internal/runtime/maintenance"
	"bansoko/internal/testutil/disposabledb"
)

func seedRecoveryRun(t *testing.T, db *sql.DB, id, state string, gatePassed bool) {
	t.Helper()
	gate := map[string]any{}
	if gatePassed {
		gate["passed"] = true
	}
	gateRaw, err := json.Marshal(gate)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := map[string]any{"protocol_version": 1, "gate": json.RawMessage(gateRaw)}
	checkpointRaw, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO deployment_recovery_runs
(id, window_id, old_bundle_identity, new_bundle_identity, backup_identity, schema_identity, build_identity,
 registry_inventory, checkpoint, state, version)
VALUES ($1, 'win', 'old', 'new', 'backup', 'schema', 'build', '[]'::jsonb, $2, $3, 1)`,
		id, checkpointRaw, state); err != nil {
		t.Fatal(err)
	}
}

func gateState(gate *maintenancepkg.Gate) (state, resumeGate, writesRejected bool) {
	status := gate.Status()
	return status.State == maintenancepkg.Maintenance, status.AutomationResumeGate, status.WritesRejected
}

func TestSyncMaintenanceFromRecoveryRunningEntersMaintenance(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedRecoveryRun(t, db, "op-running", "running", false)
	gate := maintenancepkg.NewGate()
	if err := syncMaintenanceFromRecovery(context.Background(), db, gate); err != nil {
		t.Fatal(err)
	}
	state, resume, writes := gateState(gate)
	if !state || resume || !writes {
		t.Fatalf("running recovery must enter maintenance with writes rejected: maintenance=%v resume=%v writes=%v", state, resume, writes)
	}
}

func TestSyncMaintenanceFromRecoveryFailedEntersMaintenance(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedRecoveryRun(t, db, "op-failed", "failed", false)
	gate := maintenancepkg.NewGate()
	if err := syncMaintenanceFromRecovery(context.Background(), db, gate); err != nil {
		t.Fatal(err)
	}
	state, _, writes := gateState(gate)
	if !state || !writes {
		t.Fatalf("failed recovery must enter maintenance: maintenance=%v writes=%v", state, writes)
	}
}

func TestSyncMaintenanceFromRecoveryCompletedWithoutGateEntersMaintenance(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedRecoveryRun(t, db, "op-completed-no-gate", "completed", false)
	gate := maintenancepkg.NewGate()
	if err := syncMaintenanceFromRecovery(context.Background(), db, gate); err != nil {
		t.Fatal(err)
	}
	state, _, writes := gateState(gate)
	if !state || !writes {
		t.Fatalf("completed-without-passed-gate must enter maintenance: maintenance=%v writes=%v", state, writes)
	}
}

func TestSyncMaintenanceFromRecoveryPassedGateStaysServing(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedRecoveryRun(t, db, "op-passed", "completed", true)
	gate := maintenancepkg.NewGate()
	if err := syncMaintenanceFromRecovery(context.Background(), db, gate); err != nil {
		t.Fatal(err)
	}
	state, _, writes := gateState(gate)
	if state || writes {
		t.Fatalf("passed gate must stay serving: maintenance=%v writes=%v", state, writes)
	}
}

func TestSyncMaintenanceFromRecoveryNoRunStaysServing(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	gate := maintenancepkg.NewGate()
	if err := syncMaintenanceFromRecovery(context.Background(), db, gate); err != nil {
		t.Fatal(err)
	}
	state, _, writes := gateState(gate)
	if state || writes {
		t.Fatalf("no run must stay serving: maintenance=%v writes=%v", state, writes)
	}
}

func TestSyncMaintenanceFromRecoveryMissingTableStaysServing(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	// Dropping the table is owner-only DDL: the returned handle is the
	// segregated runtime role, so the setup runs on the schema-owner handle
	// while the serving path below still exercises the runtime role.
	if _, err := disposabledb.Owner(t).ExecContext(context.Background(), `DROP TABLE deployment_recovery_runs`); err != nil {
		t.Fatal(err)
	}
	gate := maintenancepkg.NewGate()
	if err := syncMaintenanceFromRecovery(context.Background(), db, gate); err != nil {
		t.Fatalf("pre-030 schema must be tolerated: %v", err)
	}
	state, _, writes := gateState(gate)
	if state || writes {
		t.Fatalf("missing table must stay serving: maintenance=%v writes=%v", state, writes)
	}
}
