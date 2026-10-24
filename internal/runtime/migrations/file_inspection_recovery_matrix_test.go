package migrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

func TestFileInspectionRecoveryTerminalAndStaleMatrix(t *testing.T) {
	for _, scenario := range []string{"exhausted", "cancelled", "old_token", "expired_lease", "owner_version", "not_recovery", "verified", "failed", "retired"} {
		t.Run(scenario, func(t *testing.T) {
			owner, db, p := progressProbe(t, "recovery-matrix", nil, nil)
			ctx := context.Background()
			if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('matrix-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('matrix-work','artifact','matrix-artifact','artifact_inspect','matrix',3,'{}',1,0::double precision,7::smallint)`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			store := &workitems.Store{DB: db, RetentionDays: 7}
			claim, err := store.ClaimDue(ctx, "matrix", []string{workitems.KindArtifactInspect}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "not_recovery" {
				if err := store.BlockRecovery(ctx, claim, "effect_unknown"); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET available_at=CURRENT_TIMESTAMP,lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
					t.Fatal(err)
				}
				claim, err = store.ClaimRecovery(ctx, []string{workitems.KindArtifactInspect}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "exhausted":
				_, err = owner.ExecContext(ctx, `UPDATE work_items SET attempt=max_attempts WHERE id=$1`, claim.ID)
			case "cancelled":
				_, err = db.ExecContext(ctx, `SELECT public.work_item_request_cancel('matrix-work',$1)`, claim.FencingToken)
			case "old_token":
				claim.FencingToken--
			case "expired_lease":
				_, err = owner.ExecContext(ctx, `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID)
			case "owner_version":
				_, err = owner.ExecContext(ctx, `UPDATE artifacts SET version=version+1 WHERE id='matrix-artifact'`)
			case "verified", "failed":
				_, err = owner.ExecContext(ctx, `UPDATE artifacts SET state=$1,verified_at=CASE WHEN $1='verified' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE id='matrix-artifact'`, scenario)
			case "retired":
				_, err = owner.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired',fence_epoch=fence_epoch+1,lifecycle_version=lifecycle_version+1,retired_at=CURRENT_TIMESTAMP,retired_subscription_version=1 WHERE id=$1`, claim.ManagedAnimeID)
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var raw string
				if err := owner.QueryRowContext(ctx, `SELECT jsonb_build_object('artifact',to_jsonb(a),'work',to_jsonb(w),'root',to_jsonb(r),'plans',(SELECT jsonb_agg(to_jsonb(p)) FROM postprocessing_plans p WHERE p.acquisition_id=r.id))::text FROM artifacts a JOIN work_item_history w ON w.owner_type='artifact' AND w.owner_id=a.id JOIN acquisitions r ON r.id=a.acquisition_id WHERE w.id='matrix-work'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			var raw []byte
			err = db.QueryRowContext(ctx, `SELECT public.artifact_inspect_requeue_interrupted($1,$2,$3,0::double precision)`, claim.Owner.ID, claim.ID, claim.FencingToken).Scan(&raw)
			if scenario != "exhausted" && scenario != "cancelled" {
				if err == nil || !strings.Contains(err.Error(), "23514") {
					t.Fatalf("%s refusal=%v", scenario, err)
				}
				if after := snapshot(); after != before {
					t.Fatalf("%s refusal wrote graph", scenario)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var reply map[string]any
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			var artifactState, workState, code, requirement string
			var blocked, cancelled bool
			var version, expected int64
			if err := db.QueryRowContext(ctx, `SELECT a.state,w.state,a.failure_code,a.inspection_requirements_status,w.recovery_blocked,w.cancel_requested,a.version,w.expected_owner_version FROM artifacts a JOIN work_item_history w ON w.owner_type='artifact' AND w.owner_id=a.id WHERE w.id=$1`, claim.ID).Scan(&artifactState, &workState, &code, &requirement, &blocked, &cancelled, &version, &expected); err != nil {
				t.Fatal(err)
			}
			wanted := "ARTIFACT_INSPECT_INTERRUPTED"
			if scenario == "cancelled" {
				wanted = "ARTIFACT_INSPECT_CANCELLED"
			}
			if artifactState != "failed" || workState != "failed" || code != wanted || requirement != "not_reached" || !blocked || version != 2 || expected != version || cancelled != (scenario == "cancelled") {
				t.Fatalf("terminal state=%s/%s code=%s requirements=%s blocked=%t cancel=%t versions=%d/%d", artifactState, workState, code, requirement, blocked, cancelled, version, expected)
			}
		})
		if t.Failed() {
			return
		}
	}
}
