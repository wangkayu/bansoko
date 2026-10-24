package migrations

import (
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestFileInspectionRecoveryRequeuesWithinExistingBudget(t *testing.T) {
	owner, db, p := progressProbe(t, "inspect-recovery", nil, nil)
	ctx := context.Background()
	_, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('inspect-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = db.QueryRowContext(ctx, `SELECT public.work_item_create('inspect-work','artifact','inspect-artifact','artifact_inspect','inspect-audit',3,'{}',1,0::double precision,7::smallint)`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: 7}
	c, err := store.ClaimDue(ctx, "inspect-audit", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BlockRecovery(ctx, c, "effect_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second',available_at=CURRENT_TIMESTAMP WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	recovery, err := store.ClaimRecovery(ctx, []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT public.artifact_inspect_requeue_interrupted($1,$2,$3,0::double precision)`, recovery.Owner.ID, recovery.ID, recovery.FencingToken).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var reply map[string]any
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	var state, code string
	var version, token, expected int64
	var attempt int
	var blocked bool
	if err := db.QueryRowContext(ctx, `SELECT a.state,a.failure_code,a.version,w.fencing_token,w.expected_owner_version,w.attempt,w.recovery_blocked FROM artifacts a JOIN work_item_history w ON w.owner_type='artifact' AND w.owner_id=a.id WHERE w.id=$1`, recovery.ID).Scan(&state, &code, &version, &token, &expected, &attempt, &blocked); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_retry" || code != "ARTIFACT_INSPECT_INTERRUPTED" || version != 2 || expected != version || token != recovery.FencingToken+1 || attempt != c.Attempt || blocked {
		t.Fatalf("state=%s code=%s version=%d token=%d expected=%d attempt=%d blocked=%t", state, code, version, token, expected, attempt, blocked)
	}
	next, err := store.ClaimDue(ctx, "inspect-next", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != c.ID || next.Attempt != c.Attempt+1 {
		t.Fatalf("normal reexecution claim=%+v", next)
	}
}
