package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"testing"
)

// Component proof only: original publication and recovery are real runtime
// chains; owner tests the still-private projection under complete prelocks.
func verifyD9Suppression(t *testing.T, ctx context.Context, db *sql.DB, c workitems.Claim, p preparedFileResult) {
	t.Helper()
	before := planGraphSnapshot(t, db)
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const siblingSQL = `SELECT jsonb_build_object('operation',to_jsonb(o),'work',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM public.work_item_history w WHERE w.owner_type='file_operation' AND w.owner_id=o.id),'result',(SELECT to_jsonb(r) FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=o.file_ref))::text FROM public.file_operations o WHERE o.acquisition_id=$1 AND o.file_ref='independent'`
	// The fold keeps the permit on the operation row, so the "clone the source
	// permit onto the subtitle child" probe is an owner-row slot write: a fresh
	// prepared permit that the D9 suppression must treat as an unresolved binding.
	const cloneSQL = `UPDATE public.file_operations c SET
	 publication_permit_id='d9-risk-permit',publication_work_item_id='d9-risk-work',publication_effect_key='d9-risk',
	 publication_attempt=GREATEST(parent.publication_attempt,1),publication_evidence_version=1,
	 publication_spec_digest=parent.publication_spec_digest,publication_plan_digest=parent.publication_plan_digest,
	 publication_dependency_digest=parent.publication_dependency_digest,publication_executor_session=parent.publication_executor_session,
	 publication_fencing_token=GREATEST(parent.publication_fencing_token,1),publication_owner_version=c.version,
	 publication_prepared_at=parent.publication_prepared_at,publication_frozen_objects=parent.publication_frozen_objects,
	 publication_step_checkpoint=parent.publication_step_checkpoint,publication_step_proof_bytes='{}'::jsonb,
	 publication_reconcile_wall_seconds=parent.publication_reconcile_wall_seconds,publication_max_lookups=parent.publication_max_lookups,
	 publication_dispatch_state='prepared',publication_effect_class='unknown',publication_dispatch_started_at=NULL,
	 publication_closed_proof=NULL,publication_resolution_proof=NULL,publication_retry_safe=false,publication_lookup_count=0,
	 publication_lookup_ticket=NULL,publication_unknown_since=NULL,publication_reconcile_until=NULL
	FROM public.file_operations parent WHERE c.id=$1 AND parent.publication_permit_id=$2`
	var siblingBefore string
	if err := tx.QueryRowContext(ctx, siblingSQL, p.root).Scan(&siblingBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.file_finalization_cancelled_gate($1,$2,$3,$4)`, c.Owner.ID, c.ID, c.FencingToken, p.permit); err != nil {
		t.Fatal(err)
	}
	if err := verifyPreparedFileResultTx(ctx, tx, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.file_success_projection($1,$2,$3)`, p.operation, p.permit, p.fileFactCanonical); err != nil {
		t.Fatal(err)
	}
	var child string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM public.file_operations WHERE acquisition_id=$1 AND file_ref='subtitle'`, p.root).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT risk`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.file_operations c SET file_attempts_started=parent.file_attempts_started,file_attempt_index=parent.file_attempt_index,file_attempt_ticket=parent.file_attempt_ticket FROM public.file_operations parent WHERE c.id=$1 AND parent.id=$2`, child, p.operation); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT public.file_cancelled_dependency_suppression($1,$2)`, p.operation, c.ID).Scan(&count); err == nil {
		t.Fatal("issued ticket accepted")
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT risk`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT permit_risk`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, cloneSQL, child, p.permit); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT public.file_cancelled_dependency_suppression($1,$2)`, p.operation, c.ID).Scan(&count); err == nil {
		t.Fatal("existing permit accepted")
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT permit_risk`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.work_item_create('d9-risk-work','file_operation',id,'file_execute',idempotency_key,file_max_attempts,jsonb_build_object('operation_id',id,'artifact_id',artifact_id,'artifact_file_id',artifact_file_id),version,0::double precision,7::smallint) FROM public.file_operations WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, cloneSQL, child, p.permit); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT public.file_cancelled_dependency_suppression($1,$2)`, p.operation, c.ID).Scan(&count); err == nil {
		t.Fatal("existing bound permit accepted")
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT permit_risk`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT late_conflict`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.file_operations child SET file_attempts_started=parent.file_attempts_started,file_attempt_index=parent.file_attempt_index,file_attempt_ticket=parent.file_attempt_ticket FROM public.file_operations parent WHERE parent.id=$1 AND child.id=(SELECT id FROM public.file_operations WHERE acquisition_id=$2 AND file_ref IN ('subtitle','subtitle2') ORDER BY id COLLATE "C" DESC LIMIT 1)`, p.operation, p.root); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT public.file_cancelled_dependency_suppression($1,$2)`, p.operation, c.ID).Scan(&count); err == nil {
		t.Fatal("late-set issued ticket accepted")
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT late_conflict`); err != nil {
		t.Fatal(err)
	}
	var premature int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM public.acquisition_files WHERE acquisition_id=$1 AND file_ref IN ('subtitle','subtitle2') AND outcome IS NOT NULL`, p.root).Scan(&premature); err != nil || premature != 0 {
		t.Fatalf("partial suppression=%d err=%v", premature, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT public.file_cancelled_dependency_suppression($1,$2)`, p.operation, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("suppressed=%d err=%v", count, err)
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.acquisition_files WHERE acquisition_id=$1 AND file_ref='subtitle' AND outcome='failed' AND effect_class='no_external_effect' AND delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED') AND NOT EXISTS(SELECT 1 FROM public.work_item_history WHERE owner_type='file_operation' AND owner_id=$2)`, p.root, child).Scan(&valid); err != nil || !valid {
		t.Fatalf("D9 result=%v err=%v", valid, err)
	}
	var failures int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM public.acquisition_files WHERE acquisition_id=$1 AND file_ref IN ('subtitle','subtitle2') AND delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED'`, p.root).Scan(&failures); err != nil || failures != 2 {
		t.Fatalf("fanout failures=%d err=%v", failures, err)
	}
	var siblingAfter string
	if err := tx.QueryRowContext(ctx, siblingSQL, p.root).Scan(&siblingAfter); err != nil || siblingAfter != siblingBefore {
		t.Fatalf("independent component changed: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if planGraphSnapshot(t, db) != before {
		t.Fatal("projection rollback changed graph")
	}
}
