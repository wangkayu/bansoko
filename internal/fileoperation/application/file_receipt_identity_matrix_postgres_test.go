package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
	"time"
)

// Recorder rejection evidence; does not mutate immutable evidence to fabricate
// a corrupted proof reachable by the finish.
func verifyReceiptIdentityMatrix(t *testing.T, ctx context.Context, s *Service, db *sql.DB, permit string) {
	t.Helper()
	base, _, _, err := s.publicationReceiptBase(ctx, permit)
	if err != nil {
		t.Fatal(err)
	}
	base["observed_at"] = observationTime(time.Now().UTC())
	for _, field := range []string{"permit_id", "root_id", "original_work_id", "fencing_token", "executor_session", "spec_digest", "schema_version"} {
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var candidate map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&candidate); err != nil {
			t.Fatal(err)
		}
		identity := candidate["identity"].(map[string]any)
		if field == "fencing_token" || field == "schema_version" {
			identity[field] = json.Number("2")
		} else if field == "spec_digest" {
			identity[field] = "sha256:" + strings.Repeat("0", 64)
		} else {
			identity[field] = "foreign"
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := protocol.ParseStrict(encoded)
		if err != nil {
			t.Fatal(err)
		}
		before := planGraphSnapshot(t, db)
		err = s.recordFileReceipt(ctx, permit, receipt)
		var domainError *protocol.Error
		if !errors.As(err, &domainError) || domainError.Code != protocol.CodeConstraintViolated {
			t.Fatalf("application identity %s rejection=%v", field, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("application identity rejection wrote graph")
		}
		_, err = db.ExecContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, string(protocol.Canonical(receipt)))
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "FILE_RECEIPT_IDENTITY_INVALID" {
			t.Fatalf("SQL identity %s rejection=%v", field, err)
		}
		t.Logf("producer_rejection Q13 Receipt identity.%s SQLSTATE=%s code=%s zero_write_checked", field, pg.Code, pg.Message)
		if planGraphSnapshot(t, db) != before {
			t.Fatal("identity rejection wrote graph")
		}
	}
}

func verifyReceiptCanonicalMatrix(t *testing.T, ctx context.Context, s *Service, db *sql.DB, permit string) {
	t.Helper()
	base, _, _, err := s.publicationReceiptBase(ctx, permit)
	if err != nil {
		t.Fatal(err)
	}
	base["observed_at"] = observationTime(time.Now().UTC())
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	value, err := protocol.ParseStrict(append([]byte(" \n"), raw...))
	if err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	if err := s.recordFileReceipt(ctx, permit, value); err != nil {
		t.Fatalf("Go canonical normalization=%v", err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("duplicate normalized receipt wrote graph")
	}
	for _, bytes := range []string{" " + string(protocol.Canonical(value)), string(protocol.Canonical(value)) + "\n"} {
		_, err = db.ExecContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, bytes)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "FILE_RECEIPT_CANONICAL_INVALID" {
			t.Fatalf("SQL canonical refusal=%v", err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("noncanonical refusal wrote graph")
		}
	}
}

func verifyRequiredStepMatrix(t *testing.T, ctx context.Context, s *Service, db *sql.DB, permit string) {
	base, _, _, err := s.publicationReceiptBase(ctx, permit)
	if err != nil {
		t.Fatal(err)
	}
	base["observed_at"] = observationTime(time.Now().UTC())
	checkpoint := base["payload"].(map[string]any)["checkpoint"].(map[string]any)
	for _, step := range []string{"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup"} {
		if checkpoint[step].(map[string]any)["state"] != "confirmed" {
			continue
		}
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var candidate map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&candidate); err != nil {
			t.Fatal(err)
		}
		candidate["payload"].(map[string]any)["checkpoint"].(map[string]any)[step] = map[string]any{"state": "not_required", "proof": nil, "observed_at": nil, "reason_code": nil}
		raw, err = json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := protocol.ParseStrict(raw)
		if err != nil {
			t.Fatal(err)
		}
		before := planGraphSnapshot(t, db)
		err = s.recordFileReceipt(ctx, permit, receipt)
		var domainError *protocol.Error
		if !errors.As(err, &domainError) || domainError.Code != protocol.CodeConstraintViolated {
			t.Fatalf("application required step %s=%v", step, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("application required step refusal wrote graph")
		}
		_, err = db.ExecContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, string(protocol.Canonical(receipt)))
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "FILE_RECEIPT_STEP_NOT_GRANTED" {
			t.Fatalf("SQL required step %s=%v", step, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("SQL required step refusal wrote graph")
		}
	}
}

func verifyReceiptClosureMatrix(t *testing.T, ctx context.Context, s *Service, db *sql.DB, permit string) {
	t.Helper()
	base, _, _, err := s.publicationReceiptBase(ctx, permit)
	if err != nil {
		t.Fatal(err)
	}
	base["observed_at"] = observationTime(time.Now().UTC())
	var closureRaw []byte
	if err := db.QueryRowContext(ctx, `SELECT publication_closure_canonical FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&closureRaw); err != nil {
		t.Fatal(err)
	}
	// Recorder-side minimal set: null family (six fields — jsonb
	// null is the recorder call-path three-valued-logic risk), type confusion
	// (number nonce, object joined_at), deep-format representatives (control
	// char, UTF-8 oversize, bad calendar/offset/precision), and the envelope
	// shapes (illegal FV state, unavailable with malformed reason_code). The
	// relation cases (executor_session value, covered_steps set, joined_at
	// window, duplicate/foreign coverage) stay at the full R256 set — no
	// reduction. Each candidate is canonical-form (protocol.Canonical) and
	// carries no resolution digests (recorder lane), so a rejection is
	// attributable to the format/shape predicate, not an unrelated digest.
	for _, field := range []string{"executor_session", "covered_steps", "joined_at", "boot_nonce_empty", "call_nonce_empty", "observer_empty", "observer_oversize", "observer_null", "coverage_duplicate", "coverage_foreign",
		"boot_nonce_null", "call_nonce_null", "joined_at_null", "executor_session_null", "covered_steps_null",
		"boot_nonce_number", "joined_at_object",
		"boot_nonce_control", "boot_nonce_utf8_oversize", "joined_at_bad_calendar", "joined_at_bad_offset", "joined_at_bad_precision",
		"closure_state_illegal", "closure_unavailable_bad_reason"} {
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var candidate map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&candidate); err != nil {
			t.Fatal(err)
		}
		var closure map[string]any
		if err := json.Unmarshal(closureRaw, &closure); err != nil {
			t.Fatal(err)
		}
		if field == "executor_session" {
			closure[field] = "foreign:session"
		} else if field == "joined_at" {
			closure[field] = observationTime(time.Now().UTC().Add(time.Hour))
		} else if field == "boot_nonce_empty" {
			closure["boot_nonce"] = ""
		} else if field == "call_nonce_empty" {
			closure["call_nonce"] = ""
		} else if field == "observer_empty" {
			closure["observer_version"] = ""
		} else if field == "observer_null" {
			closure["observer_version"] = nil
		} else if field == "observer_oversize" {
			closure["observer_version"] = strings.Repeat("v", 129)
		} else if field == "coverage_duplicate" {
			closure["covered_steps"] = []string{"publish_target", "publish_target"}
		} else if field == "coverage_foreign" {
			closure["covered_steps"] = []string{"delete_everything"}
		} else if field == "boot_nonce_null" {
			closure["boot_nonce"] = nil
		} else if field == "call_nonce_null" {
			closure["call_nonce"] = nil
		} else if field == "joined_at_null" {
			closure["joined_at"] = nil
		} else if field == "executor_session_null" {
			closure["executor_session"] = nil
		} else if field == "covered_steps_null" {
			closure["covered_steps"] = nil
		} else if field == "boot_nonce_number" {
			closure["boot_nonce"] = json.Number("1")
		} else if field == "joined_at_object" {
			closure["joined_at"] = map[string]any{"drifted": true}
		} else if field == "boot_nonce_control" {
			closure["boot_nonce"] = "\u0001nonce"
		} else if field == "boot_nonce_utf8_oversize" {
			closure["boot_nonce"] = strings.Repeat("界", 43)
		} else if field == "joined_at_bad_calendar" {
			closure["joined_at"] = "2026-02-30T01:02:03Z"
		} else if field == "joined_at_bad_offset" {
			closure["joined_at"] = "2026-10-09T01:02:03+00:00"
		} else if field == "joined_at_bad_precision" {
			closure["joined_at"] = "2026-10-09T01:02:03.120Z"
		} else {
			closure[field] = []string{}
		}
		closureEnvelope := map[string]any{"state": "known", "value": closure, "reason_code": nil}
		if field == "closure_state_illegal" {
			closureEnvelope["state"] = "rescheduled"
		}
		if field == "closure_unavailable_bad_reason" {
			closureEnvelope = map[string]any{"state": "unavailable", "value": nil, "reason_code": "bad_reason_code"}
		}
		candidate["payload"].(map[string]any)["closure"] = closureEnvelope
		encoded, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := protocol.ParseStrict(encoded)
		if err != nil {
			t.Fatal(err)
		}
		before := planGraphSnapshot(t, db)
		err = s.recordFileReceipt(ctx, permit, receipt)
		var domainError *protocol.Error
		expected := protocol.CodeConstraintViolated
		if strings.HasSuffix(field, "_empty") || strings.HasSuffix(field, "_null") || strings.HasSuffix(field, "_number") || strings.HasSuffix(field, "_object") || strings.HasSuffix(field, "_control") || strings.HasSuffix(field, "_utf8_oversize") || strings.HasSuffix(field, "_bad_calendar") || strings.HasSuffix(field, "_bad_offset") || strings.HasSuffix(field, "_bad_precision") || field == "observer_oversize" || field == "closure_unavailable_bad_reason" {
			expected = protocol.CodeTypeInvalid
		}
		if field == "coverage_foreign" || field == "closure_state_illegal" {
			expected = protocol.CodeUnionBranchInvalid
		}
		if !errors.As(err, &domainError) || domainError.Code != expected {
			t.Fatalf("application closure %s=%v", field, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("application closure refusal wrote graph")
		}
		_, err = db.ExecContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, string(protocol.Canonical(receipt)))
		var pg *pgconn.PgError
		sqlCode := "FILE_RECEIPT_CLOSURE_INVALID"
		if field == "closure_state_illegal" || field == "closure_unavailable_bad_reason" {
			sqlCode = "FILE_RECEIPT_CLOSURE_SHAPE"
		}
		if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != sqlCode {
			t.Fatalf("SQL closure %s=%v want %s", field, err, sqlCode)
		}
		t.Logf("PROBE_RESULT|Receipt/closure/%s|producer_receipt|%s|%s|zero_write=%v", field, pg.Code, pg.Message, planGraphSnapshot(t, db) == before)
		if planGraphSnapshot(t, db) != before {
			t.Fatal("SQL closure refusal wrote graph")
		}
	}
}
