package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"testing"
)

func seedOffsetRecoveryReadyFixture(t *testing.T, db *sql.DB, store *workitems.Store) string {
	t.Helper()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, created, err := NewOffsetMigrationAdmissionService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("admission %s %v %v", id, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	planner.SetWorkItems(store)
	generation, err := planner.PlanGeneration(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if generation.State != OffsetMigrationGenerationReady {
		t.Fatalf("legal recovery fixture must plan ready: %+v", generation)
	}
	return id
}
