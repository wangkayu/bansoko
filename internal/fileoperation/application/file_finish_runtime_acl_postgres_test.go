package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/testutil/disposabledb"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestPGFileFinishRuntimeACLBidirectional is the ACL registration check:
// the fixed finish commands and their gates stay PRIVATE against the
// runtime role (the runtime reaches completion only through the trusted fixed
// caller inside Service.Handle), and the runtime-facing chain commands keep
// exactly their registered EXECUTE grants — no grant growth, no grant loss,
// on both directions of the comparison.
func TestPGFileFinishRuntimeACLBidirectional(t *testing.T) {
	ctx := context.Background()
	runtimeDB := disposabledb.ResetDisposableSchema(t)
	ownerDB := ownerTestDB(t)

	// Direction 1 (actual denial): the runtime role cannot call any private
	// finish command or gate — 42501, zero graph change.
	// Direction 2 (registration): the runtime-facing chain commands keep their
	// exact EXECUTE grants (no extra grant exists, none is missing).
	private := []string{
		"public.file_finish_success(text,text,bigint,text,text,text)",
		"public.file_finish_cancelled_success(text,text,bigint,text,text,text)",
		"public.file_success_atomic_body(text,text,text,text,text)",
		"public.file_finalization_normal_gate(text,text,bigint,text)",
		"public.file_finalization_cancelled_gate(text,text,bigint,text)",
		"public.file_finalization_prelock(text,text)",
		"public.file_assert_final_proof(text)",
		"public.file_success_projection(text,text,text)",
		"public.file_success_handoff(text,text)",
		"public.file_cancelled_dependency_suppression(text,text)",
		"public.file_success_notification(text,text,jsonb)",
		// B5 (184): the typed-failure atomic body and its effect classifier.
		"public.file_failure_atomic_body(text,text,text,text)",
		"public.file_failure_effect_class(text)",
		// The typed-failure notification projection.
		"public.file_failure_notification(text,text,text)",
	}
	for _, fn := range private {
		var denied bool
		if err := runtimeDB.QueryRowContext(ctx, `SELECT has_function_privilege('bansoko_runtime', $1, 'EXECUTE')`, fn).Scan(&denied); err != nil {
			t.Fatal(err)
		}
		if denied {
			t.Fatalf("runtime must NOT hold EXECUTE on %s", fn)
		}
	}
	runtimeGranted := []string{
		"public.file_start_attempt(text,text,bigint,text)",
		"public.file_prepare_permit(text,text,bigint,text,text,text)",
		"public.file_start_dispatch(text,text,bigint,text,text)",
		"public.file_start_step(text,text,bigint,text,text,text)",
		"public.file_record_receipt(text,text)",
		"public.file_receipt_capture(text)",
		// The fixed runtime entry is the ONLY runtime-callable finish surface —
		// a thin SECURITY DEFINER wrapper that adds no write and delegates to the
		// private commands; the components themselves stay REVOKEed above.
		"public.file_finish_runtime_entry(text,text,bigint,text,text,text,boolean)",
		// B5 (184): the fixed typed-failure command (prelock-gated, no I/O).
		"public.file_finish_failed(text,text,bigint,text,text)",
	}
	for _, fn := range runtimeGranted {
		var granted bool
		if err := runtimeDB.QueryRowContext(ctx, `SELECT has_function_privilege('bansoko_runtime', $1, 'EXECUTE')`, fn).Scan(&granted); err != nil {
			t.Fatal(err)
		}
		if !granted {
			t.Fatalf("runtime EXECUTE grant missing on %s", fn)
		}
	}
	// The registry must be exactly the registered set over the typed-chain
	// domain (file_% functions): no runtime EXECUTE on any unregistered
	// file_% function. (Other domains' grants — acquisition, plan, receipt
	// capture etc. — belong to their own slices' registrations; builtins like
	// pgcrypto's armor() inherit owner privileges and are out of scope.)
	rows, err := runtimeDB.QueryContext(ctx, `
SELECT p.oid::regprocedure::text FROM pg_proc p
JOIN pg_namespace n ON n.oid=p.pronamespace
WHERE n.nspname='public' AND p.proname LIKE 'v2\_file\_%' AND has_function_privilege('bansoko_runtime', p.oid, 'EXECUTE')
ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	// regprocedure::text omits the schema when the function is visible in
	// search_path (the disposable harness keeps public in the path), so the
	// registry comparison uses the unqualified signature.
	unqualify := func(fn string) string {
		return strings.TrimPrefix(fn, "public.")
	}
	allowed := map[string]bool{}
	for _, fn := range append(append([]string{}, private...), runtimeGranted...) {
		allowed[unqualify(fn)] = true
	}
	for rows.Next() {
		var fn string
		if err := rows.Scan(&fn); err != nil {
			t.Fatal(err)
		}
		if !allowed[fn] {
			t.Fatalf("unregistered runtime EXECUTE grant: %s", fn)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Direction 1 runtime: a direct call is refused 42501 at the driver (the
	// SQL surface never appears; the error must not be a data rejection).
	err = runtimeDB.QueryRowContext(ctx, "SELECT public.file_finish_success('op','work',1,'permit','{}','{}')").Scan(new([]byte))
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("runtime direct finish call=%v (want 42501)", err)
	}
	var count int
	if err := ownerDB.QueryRowContext(ctx, `SELECT count(*) FROM notification_admissions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied call persisted rows=%d err=%v", count, err)
	}
}
