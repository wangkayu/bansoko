package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
)

// Probe the existing unadmitted subtitle, without inventing a work or removing
// its predecessor's committed delivery result.
func probeUnadmittedPrepare(t *testing.T, ctx context.Context, db *sql.DB, root, session string) {
	t.Helper()
	var child string
	var results int
	if err := db.QueryRowContext(ctx, `SELECT id FROM file_operations WHERE acquisition_id=$1 AND file_ref='subtitle'`, root).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM file_operations o JOIN file_operations pred ON pred.id=o.bound_video_operation_id JOIN acquisition_files r ON r.acquisition_id=pred.acquisition_id AND r.file_ref=pred.file_ref WHERE o.id=$1 AND r.outcome='succeeded'`, child).Scan(&results); err != nil {
		t.Fatal(err)
	}
	if results != 0 {
		t.Fatal("dependency probe must precede predecessor success")
	}
	before := planGraphSnapshot(t, db)
	_, err := db.ExecContext(ctx, `SELECT file_prepare_permit($1,'probe-no-admitted-work',1,$2,'probe-missing-dependency','{}')`, child, session)
	var pg *pgconn.PgError
	errors.As(err, &pg)
	var landed bool
	if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM file_operations WHERE publication_permit_id='probe-missing-dependency')`).Scan(&landed); e != nil {
		t.Fatal(e)
	}
	state, code := "", ""
	if pg != nil {
		state, code = pg.Code, pg.Message
	}
	t.Logf("PROBE_RESULT|missing_success_dependency|Prepare execution qualification (no admitted subtitle work)|%s|%s|zero_write=%v|landed=%v", state, code, planGraphSnapshot(t, db) == before, landed)
	if err == nil || landed || planGraphSnapshot(t, db) != before {
		t.Fatal("unadmitted Prepare unexpectedly wrote")
	}
}

// Wrap the real callback at the first receipt boundary, retaining its actual
// StartStep grant and actual effect. Never mutate the persistent evidence.
func probeFirstReceiptCallbacks(t *testing.T, s *Service, db *sql.DB, permit string, op domain.FileOperation, callbacks domain.TypedPublicationCallbacks) domain.TypedPublicationCallbacks {
	t.Helper()
	normal := callbacks.RecordStep
	callbacks.RecordStep = func(ctx context.Context, session domain.PublicationSession, r domain.PublicationStepReceipt) error {
		if r.Err != nil {
			return normal(ctx, session, r)
		}
		base, objects, dependency, err := s.publicationReceiptBase(ctx, permit)
		if err != nil {
			return err
		}
		proof, err := publicationStepProof(base, objects, dependency, op, r)
		if err != nil {
			return err
		}
		// Materialize json.RawMessage nested values before test-only mutation.
		// Decode with UseNumber: a float64 round-trip could re-render large int64
		// sizes, and the rebuilt receipt would then be refused by the parent
		// binding before the injected key is judged.
		proofRaw, err := json.Marshal(proof)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(proofRaw))
		decoder.UseNumber()
		if err := decoder.Decode(&proof); err != nil {
			return err
		}
		// A nested unexpected key, not a top-level receipt identity change.
		var target map[string]any
		switch r.Step {
		case "stage_temp", "reserve_version":
			target = proof["object"].(map[string]any)
		case "publish_target":
			target = proof["target"].(map[string]any)
		default:
			target = proof["before"].(map[string]any)
		}
		target["probe_extra"] = true
		at := observationTime(r.ObservedAt)
		base["observed_at"] = at
		base["payload"].(map[string]any)["checkpoint"].(map[string]any)[r.Step] = map[string]any{"state": "confirmed", "proof": proof, "observed_at": at, "reason_code": nil}
		raw, err := json.Marshal(base)
		if err != nil {
			return err
		}
		v, err := protocol.ParseStrict(raw)
		if err != nil {
			return err
		}
		before := planGraphSnapshot(t, db)
		_, err = db.ExecContext(ctx, `SELECT file_record_receipt($1,$2)`, permit, string(protocol.Canonical(v)))
		var pg *pgconn.PgError
		errors.As(err, &pg)
		state, code := "", "accepted"
		if pg != nil {
			state, code = pg.Code, pg.Message
		}
		zero := planGraphSnapshot(t, db) == before
		t.Logf("PROBE_RESULT|first_nested_receipt/%s|producer_receipt (SQL error context %v)|%s|%s|zero_write=%v|landed=%v", r.Step, pg, state, code, zero, err == nil)
		if err == nil || !zero {
			return fmt.Errorf("malformed first receipt accepted or wrote: %s", r.Step)
		}
		// The injected key is an extra member of the returned object — or, for the
		// cleanup steps, of the `before` object that file_final_object_valid closes
		// at record time — so the object-closure check must be what refuses it.
		// This assertion is the regression guard for the deep copy above: with the
		// old float64 round-trip every case reported the relation binding instead
		// (TEMP_/RESERVATION_/PUBLICATION_BINDING), which quietly degraded this
		// probe into relation-boundary evidence for the same injection.
		if state != "23514" || code != "FILE_RECEIPT_OBJECT_INVALID" {
			return fmt.Errorf("first nested receipt %s rejection=%s %s want FILE_RECEIPT_OBJECT_INVALID", r.Step, state, code)
		}
		// The cleanup sample is component evidence for the cleanup branch's
		// file_final_object_valid guard on `before` (the member carrying the
		// injected key), not for the cleanup relation binding.
		if r.Step == "source_cleanup" || r.Step == "temp_cleanup" {
			t.Logf("PROBE_RESULT|cleanup.before/%s|producer receipt object guard (file_final_object_valid on before)|%s|%s|zero_write=%v|landed=false", r.Step, state, code, zero)
		}
		return normal(ctx, session, r)
	}
	return callbacks
}

// nestedClosureContainers lists every nested object layer the producer emits
// inside the first receipt of a step. file_record_receipt must refuse an
// unknown key injected into any of them instead of binding that layer field by
// field and persisting the extra key.
func nestedClosureContainers(step string) [][]string {
	switch step {
	case "stage_temp", "reserve_version":
		return [][]string{{"object"}, {"object", "location"}, {"object", "identity"}, {"parent"}}
	case "publish_target":
		return [][]string{
			{"target"}, {"target", "location"}, {"target", "identity"}, {"parent"},
			{"displaced"},
			{"displaced", "value"}, {"displaced", "value", "location"}, {"displaced", "value", "identity"},
		}
	default:
		return nil
	}
}

// probeNestedClosureReceipt re-submits the first receipt of one step with
// probe_extra injected into one nested layer. The recorder must reject it and
// write nothing; the base is re-read from the database on every call, so a
// rejected probe never leaks into the next one. Numbers are decoded with
// UseNumber so the rebuilt proof stays byte-exact (a float64 round-trip would
// re-render large sizes and trip the parent binding before the nested
// closure check is reached).
func probeNestedClosureReceipt(ctx context.Context, t *testing.T, s *Service, db *sql.DB, permit string, op domain.FileOperation, r domain.PublicationStepReceipt, container []string) error {
	t.Helper()
	layer := strings.Join(container, ".")
	base, objects, dependency, err := s.publicationReceiptBase(ctx, permit)
	if err != nil {
		return err
	}
	proof, err := publicationStepProof(base, objects, dependency, op, r)
	if err != nil {
		return err
	}
	// Materialize json.RawMessage nested values before test-only mutation.
	proofRaw, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(proofRaw))
	decoder.UseNumber()
	if err := decoder.Decode(&proof); err != nil {
		return err
	}
	node := map[string]any(proof)
	for _, key := range container {
		child, ok := node[key].(map[string]any)
		if !ok {
			// displaced.value is JSON null for block/no_replace: only the
			// wrapper is a real object under that policy.
			t.Logf("PROBE_RESULT|nested_receipt/%s/%s|skipped: layer is not an object|n/a|n/a|zero_write=true|landed=false", r.Step, layer)
			return nil
		}
		node = child
	}
	node["probe_extra"] = true
	at := observationTime(r.ObservedAt)
	base["observed_at"] = at
	base["payload"].(map[string]any)["checkpoint"].(map[string]any)[r.Step] = map[string]any{"state": "confirmed", "proof": proof, "observed_at": at, "reason_code": nil}
	raw, err := json.Marshal(base)
	if err != nil {
		return err
	}
	v, err := protocol.ParseStrict(raw)
	if err != nil {
		return err
	}
	before := planGraphSnapshot(t, db)
	_, err = db.ExecContext(ctx, `SELECT file_record_receipt($1,$2)`, permit, string(protocol.Canonical(v)))
	var pg *pgconn.PgError
	errors.As(err, &pg)
	state, code := "", "accepted"
	if pg != nil {
		state, code = pg.Code, pg.Message
	}
	zero := planGraphSnapshot(t, db) == before
	t.Logf("PROBE_RESULT|nested_receipt/%s/%s|file_record_receipt nested object closure|%s|%s|zero_write=%v|landed=%v", r.Step, layer, state, code, zero, err == nil)
	if err == nil || !zero {
		return fmt.Errorf("nested receipt %s/%s accepted or wrote", r.Step, layer)
	}
	// Every nested layer, including the displaced wrapper, is refused by the
	// object-closure check, which runs before any binding comparison.
	want := "FILE_RECEIPT_OBJECT_INVALID"
	if state != "23514" || code != want {
		return fmt.Errorf("nested receipt %s/%s rejection=%s %s want %s", r.Step, layer, state, code, want)
	}
	return nil
}

// probeNestedClosureCallbacks probes every nested layer of the first receipt
// as a refused re-submission, then records the real receipt through the
// wrapped callbacks unchanged (the existing top-level probe stays in place).
func probeNestedClosureCallbacks(t *testing.T, s *Service, db *sql.DB, permit string, op domain.FileOperation, callbacks domain.TypedPublicationCallbacks) domain.TypedPublicationCallbacks {
	t.Helper()
	normal := callbacks.RecordStep
	callbacks.RecordStep = func(ctx context.Context, session domain.PublicationSession, r domain.PublicationStepReceipt) error {
		if r.Err != nil {
			return normal(ctx, session, r)
		}
		for _, container := range nestedClosureContainers(r.Step) {
			if err := probeNestedClosureReceipt(ctx, t, s, db, permit, op, r, container); err != nil {
				return err
			}
		}
		return normal(ctx, session, r)
	}
	return callbacks
}

// verifyReceiptNestedFinalProofObjectClosure probes the reserve_version branch
// of file_validate_final_proof_before181: an unknown key inside proof.object
// must be refused with FILE_FINAL_PROOF_OBJECT_INVALID and no write, while the
// untouched closed row stays acceptable. A row without a closed, confirmed
// reserve_version slot (block/no_replace lanes) is reported as skipped.
func verifyReceiptNestedFinalProofObjectClosure(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	owner := ownerTestDB(t)
	var raw string
	if err := owner.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&row); err != nil {
		t.Fatal(err)
	}
	checkpoint := row["publication_step_checkpoint"].(map[string]any)
	slot, ok := checkpoint["reserve_version"].(map[string]any)
	if !ok || slot["state"] != "confirmed" || row["publication_dispatch_state"] != "closed" || row["publication_effect_class"] != "confirmed" {
		t.Logf("PROBE_RESULT|final_proof_before181/reserve_version.object|skipped: permit %s has no closed confirmed reserve_version slot", permit)
		return
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
	proof := slot["proof"].(map[string]any)
	proof["object"].(map[string]any)["probe_extra"] = true
	row["publication_step_proof_bytes"].(map[string]any)["reserve_version"] = canon(proof)
	checkpointBytes := canon(checkpoint)
	row["publication_resolution_checkpoint_canonical"] = checkpointBytes
	resolution := row["publication_resolution_proof"].(map[string]any)
	resolution["checkpoint_digest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-checkpoint:v1\x00"), []byte(checkpointBytes)...)))
	row["publication_resolution_canonical"] = canon(resolution)
	payload, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof_before181(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(payload))
	var rejected *pgconn.PgError
	state, code := "", "accepted"
	if errors.As(err, &rejected) {
		state, code = rejected.Code, rejected.Message
	}
	zero := planGraphSnapshot(t, db) == before
	t.Logf("PROBE_RESULT|final_proof_before181/reserve_version.object|sql_component|%s|%s|zero_write=%v", state, code, zero)
	if err == nil || !zero {
		t.Fatalf("before181 accepted an unknown reserve_version object key (landed=%v)", err == nil)
	}
	if state != "23514" || code != "FILE_FINAL_PROOF_OBJECT_INVALID" {
		t.Fatalf("before181 reserve_version object rejection=%s %s", state, code)
	}
	if _, err := owner.ExecContext(ctx, `SELECT file_validate_final_proof_before181(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, raw); err != nil {
		t.Fatalf("before181 refused the untouched closed row: %v", err)
	}
	t.Logf("PROBE_RESULT|final_proof_before181/reserve_version.object|positive_control|23514|untouched closed row accepted|zero_write=true")
}
