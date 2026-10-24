package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
	"time"
)

// Row-value tests exercise the same private validator as the fixed persisted
// loader without updating immutable evidence or disabling its guards.
func verifyFinalProofNegatives(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	verifyFinalResolutionMatrix(t, ctx, db, permit)
	verifyFinalAuthorizationStage4(t, ctx, db, permit)
	verifyReceiptStage3(t, ctx, db, permit)
	verifyStepAuthorizationFacts(t, ctx, db)
	verifyReceiptNestedFinalProofObjectClosure(t, ctx, db, permit)
	verifyStrictFileScalars(t, ctx, db)
	verifyFinalClosureRelationMatrix(t, ctx, db, permit)
	verifyFinalContentWindowMatrix(t, ctx, db, permit)
	verifyFinalAuthorizationAllSlots(t, ctx, db, permit)
	verifyCleanupMutationDigestMatrix(t, ctx, db, permit)
	verifyParentIdentityValidatorNegatives(t, ctx)
	owner := ownerTestDB(t)
	var raw string
	if err := owner.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	for _, query := range []string{`SELECT file_assert_final_proof($1)`, `SELECT file_validate_final_proof((SELECT e FROM file_operations e WHERE publication_permit_id=$1))`} {
		_, err := db.ExecContext(ctx, query, permit)
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "42501" {
			t.Fatalf("private proof ACL=%v", err)
		}
	}
	_, err := owner.ExecContext(ctx, `SELECT file_assert_final_proof($1)`, "missing-permit")
	var missing *pgconn.PgError
	if !errors.As(err, &missing) || missing.Code != "P0002" {
		t.Fatalf("fixed loader missing=%v", err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("ACL/loader wrote graph")
	}
	cases := []struct {
		name, field string
		value       any
		code        string
	}{
		{"dispatch_open", "dispatch_state", "open", "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"effect_unknown", "effect_class", "unknown", "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"conflict", "conflict_evidence", map[string]any{"test": true}, "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"conflict_exhausted", "conflict_index_exhausted", true, "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"canonical_invalid", "resolution_canonical", " {}", "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"frozen_canonical_invalid", "frozen_objects_canonical", " {}", "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"checkpoint_canonical_invalid", "resolution_checkpoint_canonical", " {}", "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"closure_canonical_invalid", "closure_canonical", " {}", "FILE_FINAL_PROOF_UNAVAILABLE"},
		{"frozen_mismatch", "frozen_objects", map[string]any{}, "FILE_FINAL_PROOF_DIGEST_INVALID"},
		{"resolution_mismatch", "resolution_proof", map[string]any{}, "FILE_FINAL_PROOF_DIGEST_INVALID"},
		{"checkpoint_mismatch", "step_checkpoint", map[string]any{}, "FILE_FINAL_PROOF_DIGEST_INVALID"},
		{"closure_mismatch", "closed_proof", map[string]any{}, "FILE_FINAL_PROOF_DIGEST_INVALID"},
		{"step_bytes_missing", "step_proof_bytes", map[string]any{}, "FILE_FINAL_PROOF_STEP_BYTES_INVALID"},
		{"dependency_digest", "dependency_digest", "sha256:foreign", "FILE_FINAL_PROOF_PUBLICATION_INVALID"},
	}
	for _, tc := range cases {
		var row map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		row["publication_"+tc.field] = tc.value
		bytes, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(bytes))
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != tc.code {
			t.Fatalf("final proof %s=%v want %s", tc.name, err, tc.code)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatalf("final proof %s wrote graph", tc.name)
		}
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"boot_nonce", ""}, {"boot_nonce", nil}, {"boot_nonce", 1}, {"boot_nonce", " nonce"}, {"boot_nonce", "nonce "}, {"boot_nonce", "a\nb"}, {"boot_nonce", strings.Repeat("界", 43)},
		{"call_nonce", ""}, {"call_nonce", nil}, {"call_nonce", 1},
		{"joined_at", nil}, {"joined_at", 1}, {"joined_at", "2026-02-30T01:02:03Z"}, {"joined_at", "2026-10-09T01:02:03+00:00"}, {"joined_at", "2026-10-09T01:02:03.120Z"}, {"joined_at", "2026-10-09T01:02:03.1234567Z"},
		{"observer_version", ""}, {"observer_version", nil}, {"observer_version", 1}, {"observer_version", strings.Repeat("v", 129)},
	} {
		var row map[string]any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&row); err != nil {
			t.Fatal(err)
		}
		cl := row["publication_closed_proof"].(map[string]any)
		cl[tc.field] = tc.value
		if tc.field != "joined_at" {
			if tc.field == "boot_nonce" || tc.field == "call_nonce" {
				if nonce, ok := tc.value.(string); ok {
					if tc.field == "boot_nonce" {
						row["publication_executor_session"] = nonce + ":" + cl["call_nonce"].(string)
					} else {
						row["publication_executor_session"] = cl["boot_nonce"].(string) + ":" + nonce
					}
					cl["executor_session"] = row["publication_executor_session"]
				}
			}
		}
		canon := func(v any) string {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			p, err := protocol.ParseStrict(b)
			if err != nil {
				t.Fatal(err)
			}
			return string(protocol.Canonical(p))
		}
		clBytes := canon(cl)
		row["publication_closure_canonical"] = clBytes
		res := row["publication_resolution_proof"].(map[string]any)
		res["closure_digests"] = []any{fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-closure:v1\x00"), []byte(clBytes)...)))}
		row["publication_resolution_canonical"] = canon(res)
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(b))
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_FINAL_PROOF_CLOSURE_INVALID" {
			t.Fatalf("closure strict %s=%v", tc.field, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("closure strict wrote graph")
		}
	}
	for _, group := range []struct {
		step, code string
		fields     []string
	}{
		{"reserve_version", "FILE_FINAL_PROOF_OBJECT_INVALID", []string{"object.extra", "object.file_type", "object.identity", "object.identity.extra", "object.identity.inode", "object.observed_at"}},
		{"publish_target", "FILE_FINAL_PROOF_OBJECT_INVALID", []string{"displaced.value.extra", "displaced.value.file_type", "displaced.value.observed_at"}},
		{"stage_temp", "FILE_FINAL_PROOF_TEMP_INVALID", []string{"kind", "verified_source_digest", "flushed", "object.location", "parent"}},
		{"source_cleanup", "FILE_FINAL_PROOF_CLEANUP_INVALID", []string{"kind", "before", "parent", "after.method", "after.parent", "after.location", "mutation_receipt_digest"}},
		{"temp_cleanup", "FILE_FINAL_PROOF_CLEANUP_INVALID", []string{"kind", "before", "parent", "after.method", "after.parent", "after.location", "mutation_receipt_digest"}},
		{"reserve_version", "FILE_FINAL_PROOF_RESERVATION_INVALID", []string{"extra", "kind", "reservation_method", "base_path", "candidate_ordinal", "object.location.canonical_path", "object.location.root", "parent", "object.size"}},
		{"publish_target", "FILE_FINAL_PROOF_PUBLICATION_INVALID", []string{"atomic_method", "target.location", "parent", "displaced"}},
		{"publish_target", "FILE_FINAL_PROOF_PUBLICATION_INVALID", []string{"displaced.state", "displaced.value.location", "displaced.value.identity.inode", "displaced.value.identity.device", "displaced.value.size"}},
		{"publish_target", "FILE_FINAL_PROOF_OBJECT_INVALID", []string{"target.extra", "target.file_type", "target.size", "target.identity", "target.identity.extra", "target.identity.inode"}},
		{"stage_temp", "FILE_FINAL_PROOF_OBJECT_INVALID", []string{"object.extra", "object.file_type", "object.size", "object.identity", "object.identity.extra", "object.identity.inode"}},
		{"publish_target", "FILE_FINAL_PROOF_SHAPE_INVALID", []string{"extra"}},
		{"stage_temp", "FILE_FINAL_PROOF_SHAPE_INVALID", []string{"extra"}},
		{"source_cleanup", "FILE_FINAL_PROOF_SHAPE_INVALID", []string{"extra", "after.extra"}},
		{"temp_cleanup", "FILE_FINAL_PROOF_SHAPE_INVALID", []string{"extra", "after.extra"}},
		{"publish_target", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_before", "slot_after"}},
		{"stage_temp", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_before", "slot_after"}},
		{"publish_target", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_null", "slot_number", "slot_offset", "slot_calendar", "slot_precision", "slot_trailing"}},
		{"stage_temp", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_null", "slot_number", "slot_offset", "slot_calendar", "slot_precision", "slot_trailing"}},
		{"reserve_version", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_null", "slot_number", "slot_offset", "slot_calendar", "slot_precision", "slot_trailing"}},
		{"source_cleanup", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_null", "slot_number", "slot_offset", "slot_calendar", "slot_precision", "slot_trailing"}},
		{"temp_cleanup", "FILE_FINAL_PROOF_TIME_INVALID", []string{"slot_null", "slot_number", "slot_offset", "slot_calendar", "slot_precision", "slot_trailing"}},
		{"publish_target", "FILE_FINAL_PROOF_OBJECT_INVALID", []string{"object_before", "object_after", "hardlink_device", "hardlink_inode"}},
		{"stage_temp", "FILE_FINAL_PROOF_OBJECT_INVALID", []string{"object_before", "object_after"}},
	} {
		for _, field := range group.fields {
			var row map[string]any
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&row); err != nil {
				t.Fatal(err)
			}
			cp := row["publication_step_checkpoint"].(map[string]any)
			slot := cp[group.step].(map[string]any)
			if slot["state"] != "confirmed" {
				continue
			}
			proof := slot["proof"].(map[string]any)
			if strings.HasPrefix(field, "displaced.") && row["collision_policy"] != "version" {
				continue
			}
			keys := strings.Split(field, ".")
			node := proof
			for _, key := range keys[:len(keys)-1] {
				node = node[key].(map[string]any)
			}
			var bad any = "foreign"
			switch field {
			case "flushed":
				bad = false
			case "candidate_ordinal":
				bad = json.Number("1002")
			case "object.size":
				bad = json.Number("1")
			case "extra":
				bad = true
			}
			node[keys[len(keys)-1]] = bad
			if strings.HasPrefix(field, "slot_") {
				delete(proof, field)
			}
			delete(proof, "slot_before")
			delete(proof, "slot_after")
			delete(proof, "object_before")
			delete(proof, "object_after")
			delete(proof, "hardlink_device")
			delete(proof, "hardlink_inode")
			if strings.HasPrefix(field, "slot_") {
				if field == "slot_before" {
					slot["observed_at"] = "2000-01-01T00:00:00Z"
				} else {
					slot["observed_at"] = "2100-01-01T00:00:00Z"
				}
			}
			switch field {
			case "slot_null":
				slot["observed_at"] = nil
			case "slot_number":
				slot["observed_at"] = 1
			case "slot_offset":
				slot["observed_at"] = "2026-10-09T01:02:03+00:00"
			case "slot_calendar":
				slot["observed_at"] = "2026-02-30T01:02:03Z"
			case "slot_precision":
				slot["observed_at"] = "2026-10-09T01:02:03.1234567Z"
			case "slot_trailing":
				slot["observed_at"] = "2026-10-09T01:02:03.120Z"
			}
			if strings.HasPrefix(field, "object_") {
				key := "object"
				if group.step == "publish_target" {
					key = "target"
				}
				o := proof[key].(map[string]any)
				if field == "object_before" {
					o["observed_at"] = "2000-01-01T00:00:00Z"
				} else {
					o["observed_at"] = "2100-01-01T00:00:00Z"
				}
			}
			if strings.HasPrefix(field, "hardlink_") {
				if row["operation"] != "hardlink" {
					continue
				}
				o := proof["target"].(map[string]any)
				identity := o["identity"].(map[string]any)
				key := strings.TrimPrefix(field, "hardlink_")
				identity[key] = identity[key].(string) + "0"
			}
			canon := func(v any) string {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				p, err := protocol.ParseStrict(b)
				if err != nil {
					t.Fatal(err)
				}
				return string(protocol.Canonical(p))
			}
			row["publication_step_proof_bytes"].(map[string]any)[group.step] = canon(proof)
			cpBytes := canon(cp)
			row["publication_resolution_checkpoint_canonical"] = cpBytes
			res := row["publication_resolution_proof"].(map[string]any)
			res["checkpoint_digest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-checkpoint:v1\x00"), []byte(cpBytes)...)))
			row["publication_resolution_canonical"] = canon(res)
			b, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(b))
			var rejected *pgconn.PgError
			want := group.code
			if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != want {
				t.Fatalf("relationship %s/%s=%v want %s", group.step, field, err, want)
			}
			t.Logf("PROBE_RESULT|%s/%s|sql_component|%s|%s|zero_write=%v", group.step, field, rejected.Code, rejected.Message, planGraphSnapshot(t, db) == before)
			if planGraphSnapshot(t, db) != before {
				t.Logf("PROBE_RESULT|%s/%s|sql_component|%s|%s|zero_write=false", group.step, field, rejected.Code, rejected.Message)
				t.Fatalf("relationship %s/%s wrote graph", group.step, field)
			}
		}
	}
}

// Closure relation negatives at the final validator (consumer), hash-self-consistent:
// session identity, boot/call pair, coverage set, and joined_at lower bound. The closure
// strict matrix above covers nonce/Time format (keeping the relations consistent); this
// proves the validator independently rejects relation violations the recorder also guards.
func verifyFinalClosureRelationMatrix(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	owner := ownerTestDB(t)
	var raw string
	if err := owner.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	canon := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p, err := protocol.ParseStrict(b)
		if err != nil {
			t.Fatal(err)
		}
		return string(protocol.Canonical(p))
	}
	before := planGraphSnapshot(t, db)
	for _, tc := range []struct {
		name   string
		mutate func(cl, row map[string]any)
	}{
		// cl.executor_session diverges from the immutable original session; boot/call pair intact.
		{"session_identity", func(cl, row map[string]any) { cl["executor_session"] = "foreign:session" }},
		// executor_session stays equal to the original, but boot:call no longer composes it.
		{"nonce_pair", func(cl, row map[string]any) { cl["boot_nonce"] = "x"; cl["call_nonce"] = "y" }},
		{"coverage_duplicate", func(cl, row map[string]any) {
			cs, _ := cl["covered_steps"].([]any)
			if len(cs) > 0 {
				cl["covered_steps"] = []any{cs[0], cs[0]}
			} else {
				cl["covered_steps"] = []any{"stage_temp", "stage_temp"}
			}
		}},
		{"coverage_foreign", func(cl, row map[string]any) { cl["covered_steps"] = []any{"foreign_step"} }},
		// valid RFC3339 UTC microsecond form, but earlier than the original dispatch grant.
		{"joined_before_dispatch", func(cl, row map[string]any) { cl["joined_at"] = "2020-01-01T00:00:00.000000Z" }},
	} {
		var row map[string]any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&row); err != nil {
			t.Fatal(err)
		}
		cl := row["publication_closed_proof"].(map[string]any)
		tc.mutate(cl, row)
		clBytes := canon(cl)
		row["publication_closure_canonical"] = clBytes
		res := row["publication_resolution_proof"].(map[string]any)
		res["closure_digests"] = []any{fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-closure:v1\x00"), []byte(clBytes)...)))}
		row["publication_resolution_canonical"] = canon(res)
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(b))
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_FINAL_PROOF_CLOSURE_INVALID" {
			t.Fatalf("Q11 relation %s=%v want FILE_FINAL_PROOF_CLOSURE_INVALID", tc.name, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatalf("Q11 relation %s wrote graph", tc.name)
		}
		t.Logf("sql_component_rejection Q11/%s 23514 FILE_FINAL_PROOF_CLOSURE_INVALID zero_write", tc.name)
	}
}

// Object observation window at the final validator: with all
// times wire-valid, step-authorized <= object observed_at <= slot observed_at
// is enforced for every consuming object slot (stage_temp object,
// publish_target target). Two predicates own it: the path-level
// qualification (dispatch started <= observed_at <= slot observed_at) rejects
// with FILE_FINAL_PROOF_OBJECT_INVALID, and the step-authorization window
// (step started <= observed_at) rejects with FILE_FINAL_PROOF_TIME_INVALID,
// as it did when the window bounded the content read. The proof mutation rebuilds step_proof_bytes
// + checkpoint canonical + checkpoint digest so the rejection is attributable
// to the window/equality predicate, not a digest mismatch.
func verifyFinalContentWindowMatrix(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	owner := ownerTestDB(t)
	var raw string
	if err := owner.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	canon := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p, err := protocol.ParseStrict(b)
		if err != nil {
			t.Fatal(err)
		}
		return string(protocol.Canonical(p))
	}
	before := planGraphSnapshot(t, db)
	// publish_target only: the stage_temp object is referenced by the
	// temp_cleanup before equality, so any object mutation there is rejected
	// by the cleanup reference first (mutation-guard-blocked); the window
	// predicate itself is the shared file_final_object_valid, proven once
	// here and consumed by every object position.
	for _, step := range []string{"publish_target"} {
		objectKey := "object"
		if step == "publish_target" {
			objectKey = "target"
		}
		for _, mode := range []string{"observed_after_slot", "observed_before_dispatch", "observed_before_authorized"} {
			var row map[string]any
			d := json.NewDecoder(strings.NewReader(raw))
			d.UseNumber()
			if err := d.Decode(&row); err != nil {
				t.Fatal(err)
			}
			slot := row["publication_step_checkpoint"].(map[string]any)[step].(map[string]any)
			if slot["state"] != "confirmed" {
				continue
			}
			object := slot["proof"].(map[string]any)[objectKey].(map[string]any)
			want := "FILE_FINAL_PROOF_OBJECT_INVALID"
			switch mode {
			case "observed_after_slot":
				object["observed_at"] = protocol.FormatTime(parse(slot["observed_at"].(string)).Add(time.Microsecond))
			case "observed_before_dispatch":
				object["observed_at"] = protocol.FormatTime(parse(row["publication_dispatch_started_at"].(string)).Add(-time.Microsecond))
			case "observed_before_authorized":
				authorized := parse(row["publication_"+step+"_started_at"].(string))
				// Inside the dispatch window but before the step was authorized:
				// only the step-authorization window can reject it.
				if !authorized.Add(-time.Microsecond).After(parse(row["publication_dispatch_started_at"].(string))) {
					t.Logf("observation window %s/%s skipped: step authorized at dispatch start", step, mode)
					continue
				}
				object["observed_at"] = protocol.FormatTime(authorized.Add(-time.Microsecond))
				want = "FILE_FINAL_PROOF_TIME_INVALID"
			}
			proof := slot["proof"].(map[string]any)
			row["publication_step_proof_bytes"].(map[string]any)[step] = canon(proof)
			cpBytes := canon(row["publication_step_checkpoint"])
			row["publication_resolution_checkpoint_canonical"] = cpBytes
			res := row["publication_resolution_proof"].(map[string]any)
			res["checkpoint_digest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-checkpoint:v1\x00"), []byte(cpBytes)...)))
			row["publication_resolution_canonical"] = canon(res)
			b, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(b))
			var rejected *pgconn.PgError
			if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != want {
				t.Fatalf("observation window %s/%s=%v want %s", step, mode, err, want)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatalf("observation window %s/%s wrote graph", step, mode)
			}
			t.Logf("sql_component_rejection Q12-verifier-window/%s/%s 23514 %s zero_write", step, mode, want)
		}
	}
}
