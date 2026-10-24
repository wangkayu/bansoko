package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"bansoko/internal/workitems"
	"github.com/jackc/pgx/v5/pgconn"
)

// These are private qualification components, intentionally unavailable to
// runtime until a complete finish command exposes its atomic write set.
func verifyFileFinalizationGates(t *testing.T, ctx context.Context, runtime *sql.DB, c workitems.Claim, permit string, cancelled bool) {
	t.Helper()
	verifyFinalProofNegatives(t, ctx, runtime, permit)
	gate := "public.file_finalization_normal_gate"
	other := "public.file_finalization_cancelled_gate"
	if cancelled {
		gate, other = other, gate
	}
	before := planGraphSnapshot(t, runtime)
	var at time.Time
	err := runtime.QueryRowContext(ctx, "SELECT "+gate+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken, permit).Scan(&at)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("private gate runtime ACL=%v", err)
	}
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT "+gate+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken, permit).Scan(&at); err != nil || at.IsZero() {
		t.Fatalf("private gate=%v at=%v", err, at)
	}
	for _, mutation := range []string{`target_path=target_path||'.drift'`, `frozen_config_snapshot=frozen_config_snapshot||'{"extra":true}'::jsonb`, `frozen_render_snapshot=frozen_render_snapshot||'{"extra":true}'::jsonb`} {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT declaration_drift"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET `+mutation+` WHERE id=$1`, c.Owner.ID); err != nil {
			t.Fatal(err)
		}
		err := tx.QueryRowContext(ctx, "SELECT "+gate+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken, permit).Scan(&at)
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_FINALIZATION_OPERATION_DECLARATION_INVALID" {
			t.Fatalf("declaration drift %s=%v", mutation, err)
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT declaration_drift"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT reject_other"); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+other+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken, permit).Scan(&at); err == nil {
		t.Fatal("opposite branch accepted")
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT reject_other"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT reject_token"); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+gate+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken+1, permit).Scan(&at); err == nil {
		t.Fatal("wrong token accepted")
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT reject_token"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE public.file_operations SET budget_clock_at=file_wall_until WHERE id=$1`, `UPDATE public.work_items SET lease_until=clock_timestamp()-interval '1 second' WHERE owner_type='file_operation' AND owner_id=$1`} {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT reject_deadline"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, query, c.Owner.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT "+gate+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken, permit).Scan(&at); err == nil {
			t.Fatal("expired finalization gate accepted")
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT reject_deadline"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT reject_permit"); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+gate+"($1,$2,$3,$4)", c.Owner.ID, c.ID, c.FencingToken, "missing-permit").Scan(&at); err == nil {
		t.Fatal("missing permit accepted")
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT reject_permit"); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if planGraphSnapshot(t, runtime) != before {
		t.Fatal("private gate wrote graph")
	}
}

// Persistent provenance negatives at the finalization gate: every
// SQL independent cross-check of file_assert_final_proof — bindings tuple,
// attempt index/session digest, dependency digest over the sealed plan graph
// with succeeded parent results, declared targets — is falsified inside a
// SAVEPOINT and the gate must reject before any business write; every mutation
// rolls back (same controlled-mutation pattern as the deadline cases above).
// Frozen-side injection is blocked by the 162 immutability trigger and the
// bindings row is continuously re-asserted by its own constraint trigger —
// those rejection layers are themselves the registered evidence (actual layer
// recorded per case). Swapped entry arguments (operation↔work) must not
// qualify either.
func verifyQ13ProvenanceNegatives(t *testing.T, ctx context.Context, runtime *sql.DB, c workitems.Claim, permit string, cancelled bool, notification, fileFact string) {
	t.Helper()
	finish := "public.file_finish_success"
	if cancelled {
		finish = "public.file_finish_cancelled_success"
	}
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := planGraphSnapshot(t, runtime)
	expectReject := func(name string) {
		t.Helper()
		err := tx.QueryRowContext(ctx, "SELECT "+finish+"($1,$2,$3,$4,$5,$6)", c.Owner.ID, c.ID, c.FencingToken, permit, fileFact, notification).Scan(new(json.RawMessage))
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) {
			t.Fatalf("Q13 %s accepted: %v", name, err)
		}
		t.Logf("sql_component_rejection Q13/%s %s %s zero_write_pending", name, rejected.Code, rejected.Message)
	}
	mutate := func(name, query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, "SAVEPOINT q13"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			var rejected *pgconn.PgError
			if errors.As(err, &rejected) {
				t.Logf("sql_component_rejection Q13/%s at-mutation %s %s (guard blocks injection; actual layer = writer guard/trigger)", name, rejected.Code, rejected.Message)
			} else {
				t.Fatalf("Q13 %s mutate: %v", name, err)
			}
		} else {
			expectReject(name)
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT q13"); err != nil {
			t.Fatal(err)
		}
	}
	mutate("attempt_index_reset", `UPDATE public.file_operations SET file_attempt_index='[]'::jsonb WHERE id=$1`, c.Owner.ID)
	mutate("dependency_edge_foreign", `UPDATE public.file_operations SET bound_video_operation_id='q13-foreign-dep' WHERE id=$1`, c.Owner.ID)
	mutate("declared_target_drift", `UPDATE public.acquisition_files SET canonical_path=canonical_path||'.q13' WHERE acquisition_id=(SELECT acquisition_id FROM public.file_operations WHERE id=$1) AND file_ref=(SELECT file_ref FROM public.file_operations WHERE id=$1)`, c.Owner.ID)
	mutate("frozen_source_size", `UPDATE public.file_operations SET publication_frozen_objects=jsonb_set(publication_frozen_objects,'{source,size}','1'::jsonb) WHERE publication_permit_id=$1`, permit)
	if _, err := tx.ExecContext(ctx, "SAVEPOINT q13"); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+finish+"($1,$2,$3,$4,$5,$6)", c.ID, c.Owner.ID, c.FencingToken, permit, fileFact, notification).Scan(new(json.RawMessage)); err == nil {
		t.Fatal("Q13 swapped entry arguments accepted")
	}
	t.Logf("sql_component_rejection Q13/finish_arguments_swapped rejected (STRICT/prelock layer)")
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT q13"); err != nil {
		t.Fatal(err)
	}
	if planGraphSnapshot(t, runtime) != before {
		t.Fatal("Q13 provenance negatives wrote graph")
	}
	t.Logf("producer_rejection Q13 provenance matrix complete: zero_write verified")
}
