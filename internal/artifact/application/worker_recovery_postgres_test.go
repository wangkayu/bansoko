package application

import (
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPGVerifiedArtifactRecoveryRepairsHandoffWithoutInspection(t *testing.T) {
	db := openArtifactTestDB(t)
	seedArtifactOwner(t, db, "artifact-verified", "task-verified", "completed", "verified", `{"external_id":"tag:verified","artifact_path":"/downloads/already.mkv"}`, `{}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-verified", []byte(`{"download_task_id":"task-verified","artifact_id":"artifact-verified"}`))
	inspector := &fakeInspector{err: errors.New("must not inspect")}
	service, err := NewService(db, inspector)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BlockRecovery(context.Background(), claim, "attempt_exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimRecovery(context.Background(), []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observer, ok := service.InspectHandler().(worker.RecoveryObserver)
	if !ok {
		t.Fatal("registry lacks observer")
	}
	o, err := observer.ObserveRecovery(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if o.Disposition != workitems.DispositionAmbiguous || o.ReasonCode != "ARTIFACT_VERIFIED_WORK_LIVE" || o.WriteOwner != nil || o.PostTransition != nil {
		t.Fatalf("verified recovery must remain blocked: %+v", o)
	}
	var plans int
	var workState string
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM postprocessing_plans WHERE artifact_id='artifact-verified'),state FROM work_item_history WHERE id=$1`, c.ID).Scan(&plans, &workState); err != nil {
		t.Fatal(err)
	}
	if plans != 0 || workState != "leased" {
		t.Fatalf("recovery wrote graph plans=%d work=%s", plans, workState)
	}
	if len(inspector.paths) != 0 {
		t.Fatalf("recovery reran inspector: paths=%v", inspector.paths)
	}
}
