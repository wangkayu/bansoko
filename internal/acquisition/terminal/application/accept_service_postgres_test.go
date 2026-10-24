package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/persistence/txscope"
)

func assertAcceptanceClass(t *testing.T, err error, status int, code string) {
	t.Helper()
	var got *AcceptanceError
	if !errors.As(err, &got) || got.Status != status || got.Code != code {
		t.Fatalf("want %d %s, got %v; cause=%v", status, code, err, errors.Unwrap(err))
	}
}
func assertNoAcceptGraph(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	// One fixed query proves rollback across all required accepting leaves.
	err := db.QueryRowContext(context.Background(), `SELECT (SELECT count(*) FROM acquisitions)+(SELECT count(*) FROM download_tasks)+(SELECT count(*) FROM work_item_history WHERE owner_type='download_task' OR owner_type='acquisition')`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("accept graph not zero: %d %v", count, err)
	}
}
func checkAcceptServicePostgres(t *testing.T, ctx context.Context, owner, runtime *sql.DB, revisions *draft.Service, req qualificationRequest, b anime.BusinessSnapshot) {
	req.entryMode = "subscription_direct"
	t.Helper()
	service, err := NewService(runtime, revisions, 200000, 50)
	if err != nil {
		t.Fatal(err)
	}
	access := acceptAccess{scope: "deployment_owner_v1"}
	request := requestIdentity{namespace: "manual_acquire_v1", key: "b1-service-key", canonical: `{"command":"acquire","release_ref":"rel-1"}`}
	request.hash = protocol.Digest("acquisition-request:v1", []byte(request.canonical))
	req.requestHash = request.hash
	capture, err := captureQualification(ctx, runtime, revisions, req)
	if err != nil {
		t.Fatal(err)
	}
	intent := serviceIntentFixture(t, capture)
	intent.capture = req
	t.Run("automatic_metadata_hold", func(t *testing.T) {
		automatic := intent
		automatic.entryMode = "automatic_source"
		result, err := service.AcceptNative(ctx, access, request, automatic)
		assertAcceptanceClass(t, err, 422, "ADMISSION_REJECTED")
		var e *AcceptanceError
		errors.As(err, &e)
		if e.Reason != "metadata_hold" || len(result.Receipt()) != 0 {
			t.Fatal("metadata hold bypassed")
		}
		assertNoAcceptGraph(t, owner)
	})
	// The manual retry entry respects the metadata hold exactly like the
	// automatic entry; only the one-shot entries bypass it.
	t.Run("subscription_direct_metadata_hold", func(t *testing.T) {
		result, err := service.AcceptNative(ctx, access, request, intent)
		assertAcceptanceClass(t, err, 422, "ADMISSION_REJECTED")
		var e *AcceptanceError
		errors.As(err, &e)
		if e.Reason != "metadata_hold" || len(result.Receipt()) != 0 {
			t.Fatal("metadata hold bypassed by subscription_direct")
		}
		assertNoAcceptGraph(t, owner)
	})
	// Bind the metadata so the accepting part runs on a capture whose hold
	// is satisfied, then capture again from that current state.
	if _, err := owner.ExecContext(ctx, `UPDATE anime_metadata SET bgm_work_id='424242',tmdb_work_id='99',tmdb_media_kind='tv',tmdb_season=1,version=version+1 WHERE managed_anime_id='anime-1'`); err != nil {
		t.Fatal(err)
	}
	capture, err = captureQualification(ctx, runtime, revisions, req)
	if err != nil {
		t.Fatal(err)
	}
	intent = serviceIntentFixture(t, capture)
	intent.capture = req
	for _, claim := range []bool{false, true} {
		bad := intent
		if claim {
			v, err := valueOf(map[string]any{"kind": "existing", "placement_ref": "placement-1", "expected_revision": 1, "file_fact_digest": capture.payloadDigest})
			if err != nil {
				t.Fatal(err)
			}
			bad.existingClaim = &v
		} else {
			bad.requiresExisting = true
		}
		result, err := service.AcceptNative(ctx, access, request, bad)
		assertAcceptanceClass(t, err, 422, "INPUT_SCHEMA_UNSUPPORTED")
		var e *AcceptanceError
		errors.As(err, &e)
		if e.Reason != "placement_existing_unsupported" || len(result.Receipt()) != 0 {
			t.Fatal("existing fallback or receipt")
		}
		assertNoAcceptGraph(t, owner)
	}
	evaluation, err := evaluateAdmission(ctx, capture, intent)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareAdmission(capture, evaluation, intent, access, request, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("SQL_defense_is_internal", func(t *testing.T) {
		bad := prepared
		var q map[string]any
		if err := json.Unmarshal(capture.qualification.Canonical(), &q); err != nil {
			t.Fatal(err)
		}
		q["placement"] = map[string]any{"kind": "existing", "placement_ref": "placement-1", "expected_revision": 1, "file_fact_digest": capture.payloadDigest}
		v, err := valueOf(q)
		if err != nil {
			t.Fatal(err)
		}
		bad.args.Qualification = sqlJSON(string(protocol.Canonical(v)))
		result, err := service.commitPrepared(ctx, bad)
		assertAcceptanceClass(t, err, 500, "INTERNAL_INVARIANT")
		if len(result.Receipt()) != 0 {
			t.Fatal("uncommitted receipt")
		}
		assertNoAcceptGraph(t, owner)
	})
	t.Run("metadata_changed_after_capture", func(t *testing.T) {
		if _, err := owner.ExecContext(ctx, `UPDATE anime_metadata SET bgm_work_id='123',version=version+1 WHERE managed_anime_id='anime-1'`); err != nil {
			t.Fatal(err)
		}
		result, err := service.commitPrepared(ctx, prepared)
		assertAcceptanceClass(t, err, 409, "INPUT_STALE")
		if len(result.Receipt()) != 0 {
			t.Fatal("metadata stale receipt")
		}
		assertNoAcceptGraph(t, owner)
	})
	t.Run("qualification_changed", func(t *testing.T) {
		if _, err := owner.ExecContext(ctx, `UPDATE subscriptions SET version=version+1 WHERE id='sub-1'`); err != nil {
			t.Fatal(err)
		}
		result, err := service.commitPrepared(ctx, prepared)
		assertAcceptanceClass(t, err, 409, "INPUT_STALE")
		if len(result.Receipt()) != 0 {
			t.Fatal("stale receipt")
		}
		assertNoAcceptGraph(t, owner)
	})
	// Re-capture the actual current version, never patch the old Qualification.
	capture, err = captureQualification(ctx, runtime, revisions, req)
	if err != nil {
		t.Fatal(err)
	}
	intent = serviceIntentFixture(t, capture)
	intent.capture = req
	evaluation, err = evaluateAdmission(ctx, capture, intent)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = prepareAdmission(capture, evaluation, intent, access, request, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("vars_record_missing_rejected_atomically", func(t *testing.T) {
		bad := prepared
		bad.varsRecord = ""
		result, err := service.commitPrepared(ctx, bad)
		assertAcceptanceClass(t, err, 500, "INTERNAL_INVARIANT")
		if len(result.Receipt()) != 0 {
			t.Fatal("missing evidence exposed acceptance")
		}
		assertNoAcceptGraph(t, owner)
	})
	for _, field := range []string{"acquisition_id", "admission_digest", "program_digest", "program_version", "program_runtime_version", "runtime_version", "input_digest", "output_digest", "evaluated_at", "output"} {
		t.Run("vars_binding_"+field, func(t *testing.T) {
			bad := prepared
			v, err := protocol.ParseStrict([]byte(bad.varsRecord))
			if err != nil {
				t.Fatal(err)
			}
			if field == "output" {
				v.Object[field], err = valueOf(map[string]any{"actual": map[string]string{"title": "wrong"}})
			} else {
				v.Object[field], err = valueOf("wrong")
			}
			if err != nil {
				t.Fatal(err)
			}
			bad.varsRecord = string(protocol.Canonical(v))
			result, err := service.commitPrepared(ctx, bad)
			assertAcceptanceClass(t, err, 500, "INTERNAL_INVARIANT")
			if len(result.Receipt()) != 0 {
				t.Fatal("conflicting record exposed receipt")
			}
			assertNoAcceptGraph(t, owner)
		})
	}
	for _, mode := range []string{"extra_key", "missing_nonce", "invalid_nonce", "schema_version", "duplicate_key", "noncanonical"} {
		t.Run("vars_closed_record_"+mode, func(t *testing.T) {
			bad := prepared
			v, err := protocol.ParseStrict([]byte(bad.varsRecord))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "extra_key":
				v.Object["unexpected"], _ = valueOf("extra")
			case "missing_nonce":
				delete(v.Object, "evaluation_id")
			case "invalid_nonce":
				v.Object["evaluation_id"], _ = valueOf("")
			case "schema_version":
				v.Object["schema_version"], _ = valueOf(0)
			}
			bad.varsRecord = string(protocol.Canonical(v))
			if mode == "duplicate_key" {
				bad.varsRecord = `{"schema_version":1,` + bad.varsRecord[1:]
			}
			if mode == "noncanonical" {
				bad.varsRecord = " " + bad.varsRecord
			}
			result, err := service.commitPrepared(ctx, bad)
			assertAcceptanceClass(t, err, 500, "INTERNAL_INVARIANT")
			if len(result.Receipt()) != 0 {
				t.Fatal("invalid closed record exposed receipt")
			}
			assertNoAcceptGraph(t, owner)
		})
	}
	for _, at := range []string{"0000-01-01T00:00:00Z", "2026-02-30T00:00:00Z", "2026-10-08T01:02:03.1234567Z", "2026-10-08T01:02:03+00:00", "2026-10-08T01:02:03.120000Z"} {
		t.Run("vars_invalid_time_"+at, func(t *testing.T) {
			bad := prepared
			v, err := protocol.ParseStrict([]byte(bad.varsRecord))
			if err != nil {
				t.Fatal(err)
			}
			v.Object["evaluated_at"], err = valueOf(at)
			if err != nil {
				t.Fatal(err)
			}
			bad.varsRecord = string(protocol.Canonical(v))
			result, err := service.commitPrepared(ctx, bad)
			assertAcceptanceClass(t, err, 500, "INTERNAL_INVARIANT")
			if len(result.Receipt()) != 0 {
				t.Fatal("invalid time exposed receipt")
			}
			assertNoAcceptGraph(t, owner)
		})
	}
	t.Run("commit_failure_rolls_back_whole_graph", func(t *testing.T) {
		// A real deferred database rejection fires at Commit after the SQL returned
		// its pending result. No injectable production callbacks or fake commits.
		if _, err := owner.ExecContext(ctx, `CREATE FUNCTION test_reject_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'FIXTURE_COMMIT_FAILURE' USING ERRCODE='23514'; END $$; CREATE CONSTRAINT TRIGGER test_reject_commit AFTER INSERT ON acquisitions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION test_reject_commit()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := owner.ExecContext(ctx, `DROP TRIGGER IF EXISTS test_reject_commit ON acquisitions; DROP FUNCTION IF EXISTS test_reject_commit()`); err != nil {
				t.Error(err)
			}
		})
		result, err := service.commitPrepared(ctx, prepared)
		assertAcceptanceClass(t, err, 503, "ACCEPTANCE_UNKNOWN")
		if len(result.Receipt()) != 0 {
			t.Fatal("failed commit exposed receipt")
		}
		assertNoAcceptGraph(t, owner)
		if _, err := owner.ExecContext(ctx, `DROP TRIGGER test_reject_commit ON acquisitions; DROP FUNCTION test_reject_commit()`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("single_txscope_pending_then_rollback", func(t *testing.T) {
		tx, err := runtime.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id='rel-1'`); err != nil {
			t.Fatal(err)
		}
		pending, err := acceptNativeTx(ctx, txscope.Wrap(tx), prepared)
		if err != nil || pending.receipt == "" {
			t.Fatalf("pending: %v", err)
		}
		assertNoAcceptGraph(t, owner)
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		assertNoAcceptGraph(t, owner)
	})
	t.Run("B2a_prepare_accept_caller_rollback", func(t *testing.T) {
		tx, err := runtime.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		p, err := service.PrepareNativeTx(ctx, txscope.Wrap(tx), access, request, intent)
		if err != nil || p.found {
			t.Fatalf("prepare: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id='rel-1'`); err != nil {
			t.Fatal(err)
		}
		pending, err := service.AcceptNativeTx(ctx, txscope.Wrap(tx), p.prepared)
		if err != nil || pending.receipt == "" {
			t.Fatalf("accept: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET reason_code='b2a_outer' WHERE id='rel-1'`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		assertNoAcceptGraph(t, owner)
		var reason string
		if err := owner.QueryRowContext(ctx, `SELECT reason_code FROM releases WHERE id='rel-1'`).Scan(&reason); err != nil || reason == "b2a_outer" {
			t.Fatalf("outer write escaped: %v", err)
		}
	})
	t.Run("queued_commit_and_replay", func(t *testing.T) {
		result, err := service.AcceptNative(ctx, access, request, intent)
		if err != nil || len(result.Receipt()) == 0 || result.Replayed() {
			t.Fatalf("accepted: %v", err)
		}
		var root, canonical, digest string
		var version int
		if err := owner.QueryRowContext(ctx, `SELECT a.id,a.vars_evidence_version,a.vars_evaluation_canonical_utf8,a.vars_evidence_digest FROM acquisitions a WHERE a.request_key=$1`, request.key).Scan(&root, &version, &canonical, &digest); err != nil {
			t.Fatal(err)
		}
		record, err := protocol.ParseStrict([]byte(canonical))
		if err != nil || version != 1 || protocol.Digest("acquisition-vars-evaluation:v1", []byte(canonical)) != digest {
			t.Fatalf("record binding: %v", err)
		}
		if err := protocol.ValidateVarsEvaluation(record, evaluation.input.Object["config"].Object["vars"], root, evaluation.inputDigest); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ExecContext(ctx, `UPDATE subscriptions SET state='stopped',version=version+1 WHERE id='sub-1'`); err != nil {
			t.Fatal(err)
		}
		replay, err := service.AcceptNative(ctx, access, request, acquisitionIntent{})
		if err != nil || !replay.Replayed() || !bytes.Equal(result.Receipt(), replay.Receipt()) {
			t.Fatalf("replay recaptured or changed receipt: %v", err)
		}
		for _, statement := range []string{
			`UPDATE acquisitions SET vars_evaluation_canonical_utf8=vars_evaluation_canonical_utf8 WHERE id=$1`,
			`SELECT id FROM acquisitions WHERE id=$1 FOR UPDATE NOWAIT`,
			`DELETE FROM acquisitions WHERE id=$1`,
		} {
			if _, err := runtime.ExecContext(ctx, statement, root); err == nil {
				t.Fatalf("runtime naked Vars DML accepted: %s", statement)
			} else {
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.Code != "42501" {
					t.Fatalf("Vars ACL: %v", err)
				}
			}
		}
		for _, statement := range []string{
			`UPDATE acquisitions SET vars_evaluation_canonical_utf8=vars_evaluation_canonical_utf8||' ' WHERE id=$1`,
			`UPDATE acquisitions SET vars_evidence_digest=repeat('f',64) WHERE id=$1`,
		} {
			tx, err := owner.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.ExecContext(ctx, statement, root)
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			// The VARS guards became column CHECKs (PRUNE-046 S4), so a mutated
			// digest or record is refused by the constraint, not by a trigger.
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" || pg.ConstraintName != "acquisitions_vars_evaluation_check" {
				t.Fatalf("Vars immutable/live guard: %s: %v", statement, err)
			}
		}
		var mapped bool
		if err := owner.QueryRowContext(ctx, `SELECT vars_filefact_valid($1,jsonb_build_object('render_inputs',jsonb_build_object('vars',jsonb_build_object('state','known','reason_code',NULL,'value',jsonb_build_object('program_digest',vars_evaluation->'program_digest','input_digest',vars_evaluation->'input_digest','output',vars_evaluation->'output','runtime_version',vars_evaluation->'runtime_version','evaluated_at',vars_evaluation->'evaluated_at'))))) FROM acquisitions WHERE id=$1`, root).Scan(&mapped); err != nil || !mapped {
			t.Fatalf("real producer FileFact mapping: %v", err)
		}
		if err := owner.QueryRowContext(ctx, `SELECT vars_filefact_valid($1,'{"render_inputs":{"vars":{"state":"unavailable","value":null,"reason_code":"VARS_EVALUATION_RECORD_UNAVAILABLE"}}}'::jsonb)`, root).Scan(&mapped); err != nil || mapped {
			t.Fatalf("new record degraded to historical: %v", err)
		}
		for _, field := range []string{"program_digest", "input_digest", "output", "runtime_version", "evaluated_at"} {
			if err := owner.QueryRowContext(ctx, `SELECT vars_filefact_valid($1,jsonb_build_object('render_inputs',jsonb_build_object('vars',jsonb_build_object('state','known','reason_code',NULL,'value',jsonb_set(jsonb_build_object('program_digest',vars_evaluation->'program_digest','input_digest',vars_evaluation->'input_digest','output',vars_evaluation->'output','runtime_version',vars_evaluation->'runtime_version','evaluated_at',vars_evaluation->'evaluated_at'),ARRAY[$2]::text[],'"wrong"'::jsonb))))) FROM acquisitions WHERE id=$1`, root, field).Scan(&mapped); err != nil || mapped {
				t.Fatalf("changed FileFact %s accepted: %v", field, err)
			}
		}
		var untouched string
		if err := owner.QueryRowContext(ctx, `SELECT vars_evaluation_canonical_utf8 FROM acquisitions WHERE id=$1`, root).Scan(&untouched); err != nil || untouched != canonical {
			t.Fatalf("guard changed record: %v", err)
		}
		var after string
		if err := owner.QueryRowContext(ctx, `SELECT vars_evaluation_canonical_utf8 FROM acquisitions WHERE id=$1`, root).Scan(&after); err != nil || after != canonical {
			t.Fatalf("replay changed evaluation: %v", err)
		}
		bad := request
		bad.hash = protocol.Digest("acquisition-request:v1", []byte("different"))
		_, err = service.AcceptNative(ctx, access, bad, intent)
		assertAcceptanceClass(t, err, 409, "IDEMPOTENCY_KEY_REUSED")
		checkAcceptedVarsPrepare(t, ctx, owner, runtime, root, canonical)
	})
	_ = b
}
