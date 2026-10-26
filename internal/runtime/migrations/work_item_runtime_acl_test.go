package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"testing"
)

func TestWorkItemRuntimeOperationsACL(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	for _, statement := range []string{
		`INSERT INTO public.work_items(id) VALUES ('forged-work')`,
		`UPDATE public.work_items SET cancel_requested=true WHERE false`,
		`DELETE FROM public.work_items WHERE false`,
		`TRUNCATE public.work_items`,
		`SELECT id FROM public.work_items FOR UPDATE`,
		`SELECT id FROM public.work_items FOR SHARE`,
		`SELECT id FROM public.work_items FOR NO KEY UPDATE`,
		`SELECT id FROM public.work_items FOR KEY SHARE`,
		`INSERT INTO public.work_item_outcomes(id) VALUES ('forged-outcome')`,
		`UPDATE public.work_item_outcomes SET attempt=0 WHERE false`,
		`DELETE FROM public.work_item_outcomes WHERE false`,
		`TRUNCATE public.work_item_outcomes`,
		`SELECT id FROM public.work_item_outcomes FOR UPDATE`,
		`SELECT public.work_item_close('missing','failed')`,
	} {
		if _, err := runtime.ExecContext(ctx, statement); !isInsufficientPrivilege(err) {
			t.Fatalf("direct DML %q = %v, want 42501", statement, err)
		}
	}
	var count int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname LIKE 'work_item_%' AND p.prosecdef AND p.proconfig @> ARRAY['search_path=pg_catalog, public'] AND has_function_privilege('bansoko_runtime',p.oid,'EXECUTE') AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee=0 AND a.privilege_type='EXECUTE')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	// The set of owner-held work_item_* functions is fixed: every one is
	// SECURITY DEFINER with a pinned search_path, granted to the runtime role
	// and never to PUBLIC. A function that silently loses any of those
	// properties drops out of this count.
	if count != 48 {
		t.Fatalf("fixed owner-held functions = %d, want 48", count)
	}
	// Every fixed lock statement must be executable by runtime, including empty results.
	for _, statement := range []string{
		`SELECT * FROM public.work_item_lock_metadata_evaluations('missing')`,
		`SELECT * FROM public.work_item_lock_maintenance_cleanup('missing','cleanup_execute')`,
		`SELECT * FROM public.work_item_lock_placement_recovery('missing',1,'missing')`,
		`SELECT * FROM public.work_item_lock_latest_file_execute('file_operation','missing','file_execute')`,
		`SELECT * FROM public.work_item_lock_file_operation_inputs('file_operation','missing')`,
		`SELECT * FROM public.work_item_lock_prepared_plan('missing','missing',1)`,
		`SELECT * FROM public.work_item_lock_release_evaluation('release','missing','release_evaluate')`,
		`SELECT * FROM public.work_item_lock_cleanup('replacement_deletion','missing','cleanup_execute')`,
		`SELECT * FROM public.work_item_lock_oneshot_identity('missing')`,
		`SELECT * FROM public.work_item_lock_claim('missing',1)`,
		`SELECT * FROM public.work_item_lock_owner_version('missing')`,
		`SELECT * FROM public.work_item_lock_failed_retry('missing','release','missing')`,
		`SELECT * FROM public.work_item_lock_live_identity('missing')`,
		`SELECT * FROM public.work_item_lock_recovery('missing',1,'release','missing')`,
		`SELECT * FROM public.work_item_lock_recovery_observation('missing',1,'release','missing')`,
		`SELECT * FROM public.work_item_lock_placement_trigger('missing','missing')`,
	} {
		rows, err := runtime.QueryContext(ctx, statement)
		if err != nil {
			t.Fatalf("runtime lock call %s: %v", statement, err)
		}
		if rows.Next() {
			t.Fatalf("missing fixture unexpectedly matched %s", statement)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var affected int64
	if err := runtime.QueryRowContext(ctx, `SELECT public.work_item_request_cancel('missing',0)`).Scan(&affected); err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("missing claim affected=%d, want 0", affected)
	}
	if err := runtime.QueryRowContext(ctx, `SELECT public.work_item_renew_lease(CURRENT_TIMESTAMP,'missing',0)`).Scan(&affected); err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("missing lease affected=%d, want 0", affected)
	}
}
