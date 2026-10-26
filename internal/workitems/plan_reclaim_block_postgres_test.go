package workitems

import (
	"bansoko/internal/testutil/disposabledb"
	"context"
	"testing"
	"time"
)

// Replacement may update its marker diagnostic, never delete Plan evidence.
func TestPlanRetentionPreservesAnchoredGraph(t *testing.T) {
	db := openTestDB(t)
	const marker = "plan-retention-block"
	createOneShotChain(t, db, marker, []string{"retained.mkv"})
	owner := disposabledb.Owner(t)
	if _, err := owner.Exec(`INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,state,outcome_code,outcome_reason,input_digest) VALUES('retained-plan',$1,$2,1,'failed','plan_failed','fixture failure','sha256:'||repeat('c',64))`, "art-"+marker, "acq-"+marker); err != nil {
		t.Fatal(err)
	}
	insertOneShotMarker(t, db, marker, OneShotStateReclaimPending, oneShotReclaimAt(time.Now().Add(-time.Hour)))
	var before string
	const graph = `SELECT jsonb_build_object('plan',(SELECT to_jsonb(p) FROM postprocessing_plans p WHERE id='retained-plan'),'artifact',(SELECT to_jsonb(a) FROM artifacts a WHERE id='art-plan-retention-block'),'root',(SELECT to_jsonb(r) FROM acquisitions r WHERE id='acq-plan-retention-block'))::text`
	if err := db.QueryRow(graph).Scan(&before); err != nil {
		t.Fatal(err)
	}
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := service.reclaimOne(context.Background(), marker)
	if err != nil || reclaimed {
		t.Fatalf("reclaimed=%v err=%v", reclaimed, err)
	}
	var state, code, after string
	if err := db.QueryRow(`SELECT state,terminal_reason_code FROM one_shot_acquisitions WHERE id=$1`, marker).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	// Independent running-root retention waits without changing marker outcome.
	if state != OneShotStateReclaimPending || code != "one_shot_completed" {
		t.Fatalf("%s %s", state, code)
	}
	if err := db.QueryRow(graph).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("retention changed Plan graph")
	}
}
