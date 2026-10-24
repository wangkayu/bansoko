package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"fmt"
	"testing"
)

func TestWorkItemRuntimeLockFunctionsHitAndRetainLocks(t *testing.T) {
	fixtureDB := installedDB(t)
	ctx := context.Background()
	seedAcquiredChain(t, fixtureDB, "lock", true)
	for _, statement := range []string{
		`INSERT INTO file_operations(postprocessing_plan_id,file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds,id,artifact_id,artifact_version,artifact_file_id,acquisition_id,file_ref,operation,source_path,target_path,source_fingerprint,collision_policy,frozen_render_snapshot,frozen_config_snapshot,state,idempotency_key) VALUES('plan-lock',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+INTERVAL '1 day',3,60,'fo','art-lock',1,'af-lock','acq-lock','file-a','copy','/downloads/lock/ep.mkv','/library/a','{"sha256":"fp"}','block','{}','{}','pending','fo')`,
		`INSERT INTO cleanup_operations(id,idempotency_key,placement_id,managed_anime_id,coverage_keys,replacement_ref,policy_version,retention_mode,source_path,source_fingerprint,effect) VALUES('cleanup','cleanup','placement','anime-lock','[]','decision',1,'single_copy','/library/old','fp','quarantine')`,
		`INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('release','src-lock','release','{}','{}','waiting_metadata','metadata_required')`,
		`INSERT INTO work_items(id,owner_type,owner_id,kind,state,idempotency_key,max_attempts,input_snapshot,expected_owner_version) VALUES('release-wi','release','release','release_evaluate','available','release-wi',3,'{}',1)`,
		`INSERT INTO work_items(id,owner_type,owner_id,kind,state,idempotency_key,max_attempts,input_snapshot,expected_owner_version) VALUES('cleanup-wi','cleanup_operation','cleanup','cleanup_quarantine','available','cleanup-wi',3,'{}',1)`,
		`INSERT INTO work_items(id,owner_type,owner_id,acquisition_id,kind,state,lease_until,fencing_token,idempotency_key,max_attempts,input_snapshot,expected_owner_version) VALUES('fo-wi','file_operation','fo','acq-lock','file_execute','leased',CURRENT_TIMESTAMP+INTERVAL '1 hour',1,'fo',3,'{}',1)`,
		`INSERT INTO work_items(id,owner_type,owner_id,acquisition_id,kind,state,lease_until,fencing_token,idempotency_key,max_attempts,input_snapshot,expected_owner_version) VALUES('plan-wi','postprocessing_plan','plan-lock','acq-lock','postprocess_plan','leased',CURRENT_TIMESTAMP+INTERVAL '1 hour',1,'plan-wi',3,'{}',1)`,
		`INSERT INTO work_items(id,owner_type,owner_id,kind,state,idempotency_key,max_attempts,input_snapshot,expected_owner_version) VALUES('trigger-wi','file_maintenance','anime-lock','replacement_evaluate','available','trigger-wi',3,'{"candidate_release_id":"release","trigger":"placement_committed"}',1)`,
	} {
		if _, err := fixtureDB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("arrange: %v", err)
		}
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, fixtureDB, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtimeDB := openRuntimeConnection(t, password)
	defer runtimeDB.Close()
	cases := []struct {
		name, statement, id string
		blocked             bool
	}{
		{"metadata_evaluations", `SELECT * FROM public.work_item_lock_metadata_evaluations('anime-lock')`, "release-wi", false},
		{"maintenance_cleanup", `SELECT * FROM public.work_item_lock_maintenance_cleanup('cleanup','cleanup_quarantine')`, "cleanup-wi", false},
		{"placement_recovery", `SELECT * FROM public.work_item_lock_placement_recovery('fo-wi',1,'fo')`, "fo-wi", true},
		{"latest_file_execute", `SELECT * FROM public.work_item_lock_latest_file_execute('file_operation','fo','file_execute')`, "fo-wi", false},
		{"file_operation_inputs", `SELECT * FROM public.work_item_lock_file_operation_inputs('file_operation','fo')`, "fo-wi", false},
		{"prepared_plan", `SELECT * FROM public.work_item_lock_prepared_plan('plan-wi','plan-lock',1)`, "plan-wi", false},
		{"release_evaluation", `SELECT * FROM public.work_item_lock_release_evaluation('release','release','release_evaluate')`, "release-wi", false},
		{"cleanup", `SELECT * FROM public.work_item_lock_cleanup('cleanup_operation','cleanup','cleanup_quarantine')`, "cleanup-wi", false},
		{"oneshot_identity", `SELECT * FROM public.work_item_lock_oneshot_identity('fo-wi')`, "fo-wi", false},
		{"claim", `SELECT * FROM public.work_item_lock_claim('fo-wi',1)`, "fo-wi", false},
		{"owner_version", `SELECT * FROM public.work_item_lock_owner_version('fo-wi')`, "fo-wi", false},
		{"failed_retry", `SELECT * FROM public.work_item_lock_failed_retry('fo-wi','file_operation','fo')`, "fo-wi", false},
		{"live_identity", `SELECT * FROM public.work_item_lock_live_identity('fo-wi')`, "fo-wi", false},
		{"recovery", `SELECT * FROM public.work_item_lock_recovery('fo-wi',1,'file_operation','fo')`, "fo-wi", true},
		{"recovery_observation", `SELECT * FROM public.work_item_lock_recovery_observation('fo-wi',1,'file_operation','fo')`, "fo-wi", true},
		{"placement_trigger", `SELECT * FROM public.work_item_lock_placement_trigger('anime-lock','release')`, "trigger-wi", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET recovery_blocked=$1 WHERE id='fo-wi'`, tc.blocked); err != nil {
				t.Fatal(err)
			}
			tx, err := runtimeDB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			rows, err := tx.QueryContext(ctx, tc.statement)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var got int
			for rows.Next() {
				values := make([]any, len(columns))
				for i := range values {
					values[i] = new(any)
				}
				if err := rows.Scan(values...); err != nil {
					t.Fatal(err)
				}
				got++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if got != 1 {
				t.Fatalf("%s rows=%d,want1", tc.name, got)
			}
			probe, err := fixtureDB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var id string
			err = probe.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=$1 FOR UPDATE NOWAIT`, tc.id).Scan(&id)
			probe.Rollback()
			if sqlState(err) != "55P03" {
				t.Fatalf("lock not retained for %s: %v", tc.name, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := fixtureDB.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=$1 FOR UPDATE NOWAIT`, tc.id).Scan(&id); err != nil {
				t.Fatal(fmt.Errorf("lock not released: %w", err))
			}
		})
	}
}
