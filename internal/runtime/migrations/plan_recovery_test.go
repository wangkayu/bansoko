package migrations

import (
	"bansoko/internal/workitems"
	"context"
	"testing"
	"time"
)

func TestPlanRecoveryBudget(t *testing.T) {
	for _, scenario := range []string{"requeue", "exhausted", "cancelled", "cancel_and_exhausted", "old_token", "expired", "not_recovery", "version"} {
		t.Run(scenario, func(t *testing.T) {
			owner, db, store, c := seedTypedPlanRecovery(t)
			ctx := context.Background()
			var err error
			switch scenario {
			case "exhausted", "cancel_and_exhausted":
				_, err = owner.Exec(`UPDATE work_items SET attempt=max_attempts WHERE id=$1`, c.ID)
			case "old_token":
				c.FencingToken--
			case "expired":
				_, err = owner.Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, c.ID)
			case "not_recovery":
				_, err = owner.Exec(`UPDATE work_items SET recovery_blocked=false,recovery_reason=NULL WHERE id=$1`, c.ID)
			case "version":
				_, err = owner.Exec(`UPDATE postprocessing_plans SET version=version+1 WHERE id=$1`, c.Owner.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "cancelled" || scenario == "cancel_and_exhausted" {
				if _, err := db.Exec(`SELECT public.work_item_request_cancel($1,$2)`, c.ID, c.FencingToken); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() string {
				var raw string
				if err := owner.QueryRow(`SELECT jsonb_build_object('plan',to_jsonb(p),'work',to_jsonb(w),'root',to_jsonb(r),'results',(SELECT jsonb_agg(to_jsonb(d)) FROM acquisition_files d WHERE d.acquisition_id=r.id AND d.outcome IS NOT NULL))::text FROM postprocessing_plans p JOIN work_item_history w ON w.id=$1 JOIN acquisitions r ON r.id=p.acquisition_id WHERE p.id=$2`, c.ID, c.Owner.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			var raw []byte
			err = db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_requeue_interrupted($1,$2,$3,0::double precision)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw)
			if scenario != "requeue" && scenario != "exhausted" && scenario != "cancelled" && scenario != "cancel_and_exhausted" {
				if err == nil {
					t.Fatal("invalid recovery committed")
				}
				if after := snapshot(); after != before {
					t.Fatal("invalid recovery changed persisted graph")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var state, work, code string
			var version, expected, token int64
			var attempt int
			var blocked, cancel bool
			if err := owner.QueryRow(`SELECT p.state,p.version,w.state,w.expected_owner_version,w.fencing_token,w.attempt,w.recovery_blocked,w.cancel_requested,w.last_error_code FROM postprocessing_plans p JOIN work_item_history w ON w.id=$1 WHERE p.id=$2`, c.ID, c.Owner.ID).Scan(&state, &version, &work, &expected, &token, &attempt, &blocked, &cancel, &code); err != nil {
				t.Fatal(err)
			}
			if version != c.ExpectedOwnerVersion+1 || expected != version {
				t.Fatalf("versions plan=%d expected=%d", version, expected)
			}
			if scenario == "requeue" {
				if state != "queued" || work != "available" || token != c.FencingToken+1 || attempt != c.Attempt || blocked || code != "PLAN_INTERRUPTED" {
					t.Fatalf("requeue state=%s work=%s token=%d attempt=%d blocked=%v code=%s", state, work, token, attempt, blocked, code)
				}
				next, err := store.ClaimDue(ctx, "next-plan", []string{workitems.KindPostProcessPlan}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if next.ID != c.ID || next.Attempt != c.Attempt+1 {
					t.Fatalf("new generation=%+v", next)
				}
			} else {
				want := "PLAN_INTERRUPTED"
				if scenario != "exhausted" {
					want = "PLAN_CANCELLED"
				}
				if state != "failed" || work != "failed" || !blocked || code != want {
					t.Fatalf("failure state=%s work=%s blocked=%v code=%s", state, work, blocked, code)
				}
				if scenario != "exhausted" && !cancel {
					t.Fatal("cancel audit cleared")
				}
				var proof []byte
				if err := db.QueryRow(`SELECT public.postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&proof); err != nil {
					t.Fatal(err)
				}
				if len(proof) == 0 {
					t.Fatal("complete failed planning graph was not confirmed")
				}
			}
		})
	}
}
