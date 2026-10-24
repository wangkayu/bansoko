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

func verifyFinalAuthorizationStage4(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	owner := ownerTestDB(t)
	var raw string
	if err := owner.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	for _, mode := range []string{"missing_A", "D_after_A", "A_after_O", "J_after_E", "missing_E", "not_required_A", "O_after_J", "observed_before_A", "after_before_A", "after_after_O", "E_infinite", "A_infinite"} {
		var row map[string]any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&row); err != nil {
			t.Fatal(err)
		}
		cp := row["publication_step_checkpoint"].(map[string]any)
		step := "publish_target"
		slot := cp[step].(map[string]any)
		parse := func(s string) time.Time {
			v, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		code := "FILE_FINAL_PROOF_TIME_INVALID"
		switch mode {
		case "missing_A":
			row["publication_"+step+"_started_at"] = nil
			code = "FILE_FINAL_PROOF_AUTHORIZATION_INVALID"
		case "D_after_A":
			row["publication_dispatch_started_at"] = protocol.FormatTime(parse(row["publication_"+step+"_started_at"].(string)).Add(time.Microsecond))
		case "A_after_O":
			row["publication_"+step+"_started_at"] = protocol.FormatTime(parse(slot["observed_at"].(string)).Add(time.Microsecond))
		case "J_after_E":
			row["publication_closure_receipt_observed_at"] = "2000-01-01T00:00:00Z"
			code = "FILE_FINAL_PROOF_CLOSURE_INVALID"
		case "missing_E":
			row["publication_closure_receipt_observed_at"] = nil
			code = "FILE_FINAL_PROOF_CLOSURE_INVALID"
		case "not_required_A":
			found := false
			for k, v := range cp {
				if v.(map[string]any)["state"] == "not_required" {
					row["publication_"+k+"_started_at"] = row["publication_"+step+"_started_at"]
					found = true
					break
				}
			}
			if !found {
				continue
			}
			code = "FILE_FINAL_PROOF_AUTHORIZATION_INVALID"
		case "A_infinite":
			row["publication_"+step+"_started_at"] = "infinity"
			code = "FILE_FINAL_PROOF_AUTHORIZATION_INVALID"
		case "E_infinite":
			row["publication_closure_receipt_observed_at"] = "infinity"
			code = "FILE_FINAL_PROOF_CLOSURE_INVALID"
		case "O_after_J":
			slot["observed_at"] = protocol.FormatTime(parse(row["publication_closed_proof"].(map[string]any)["joined_at"].(string)).Add(time.Microsecond))
		case "observed_before_A":
			slot["proof"].(map[string]any)["target"].(map[string]any)["observed_at"] = protocol.FormatTime(parse(row["publication_"+step+"_started_at"].(string)).Add(-time.Microsecond))
		case "after_before_A", "after_after_O":
			step = "temp_cleanup"
			slot = cp[step].(map[string]any)
			if slot["state"] == "not_required" {
				continue
			}
			bound := parse(row["publication_"+step+"_started_at"].(string)).Add(-time.Microsecond)
			if mode == "after_after_O" {
				bound = parse(slot["observed_at"].(string)).Add(time.Microsecond)
			}
			slot["proof"].(map[string]any)["after"].(map[string]any)["observed_at"] = protocol.FormatTime(bound)
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
		row["publication_step_proof_bytes"].(map[string]any)[step] = canon(slot["proof"])
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
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != code {
			t.Fatalf("stage4 %s=%v want %s", mode, err, code)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatalf("stage4 %s wrote graph", mode)
		}
	}
}

// All-slot authorization chain + equal-microsecond boundary positives. The stage4
// loop above hardcodes publish_target/temp_cleanup; this proves the D<=A[s]<=O[s]<=J<=E chain
// for every confirmed slot (including reserve_version / stage_temp / source_cleanup positions)
// and that same-microsecond equalities are legal (validator uses <=, not <). Violations mutate
// only the A[s]/dispatch columns (no digest disturbed); equality positives that move a checkpoint
// or closure time rebuild the affected canonical bytes + digests so acceptance is genuine.
func verifyFinalAuthorizationAllSlots(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
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
	// rebuild the resolution checkpoint digest after a checkpoint mutation; step_proof_bytes
	// is rebuilt by the caller only when the proof itself changed.
	rebuildCheckpoint := func(row map[string]any) {
		cpBytes := canon(row["publication_step_checkpoint"])
		row["publication_resolution_checkpoint_canonical"] = cpBytes
		res := row["publication_resolution_proof"].(map[string]any)
		res["checkpoint_digest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-checkpoint:v1\x00"), []byte(cpBytes)...)))
		row["publication_resolution_canonical"] = canon(res)
	}
	before := planGraphSnapshot(t, db)
	var base map[string]any
	{
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&base); err != nil {
			t.Fatal(err)
		}
	}
	cp := base["publication_step_checkpoint"].(map[string]any)
	var confirmed []string
	for _, s := range []string{"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup"} {
		if cp[s].(map[string]any)["state"] == "confirmed" {
			confirmed = append(confirmed, s)
		}
	}
	check := func(name, want string, mutate func(row map[string]any)) {
		var row map[string]any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&row); err != nil {
			t.Fatal(err)
		}
		mutate(row)
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(b))
		if want == "" {
			if err != nil {
				t.Fatalf("final authorization %s unexpected rejection=%v", name, err)
			}
			t.Logf("sql_positive final-auth/%s accepted (equal-microsecond boundary legal)", name)
		} else {
			var rej *pgconn.PgError
			if !errors.As(err, &rej) || rej.Code != "23514" || rej.Message != want {
				t.Fatalf("final authorization %s=%v want %s", name, err, want)
			}
			t.Logf("sql_component_rejection final-auth/%s 23514 %s zero_write", name, want)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatalf("final authorization %s wrote graph", name)
		}
	}
	for _, s := range confirmed {
		aCol := "publication_" + s + "_started_at"
		oStr := cp[s].(map[string]any)["observed_at"].(string)
		check(s+"/missing_A", "FILE_FINAL_PROOF_AUTHORIZATION_INVALID", func(row map[string]any) { row[aCol] = nil })
		check(s+"/A_after_O", "FILE_FINAL_PROOF_TIME_INVALID", func(row map[string]any) { row[aCol] = protocol.FormatTime(parse(oStr).Add(time.Microsecond)) })
	}
	// D coincides with the earliest authorization (global min A[s]). The single dispatch column is
	// bounded by every slot's A[s], so only the earliest is a legal same-microsecond equality; a later
	// slot's A[s] would violate D<=A[earlier] and is not a legal boundary.
	var minA time.Time
	haveMin := false
	for _, s := range confirmed {
		a := parse(base["publication_"+s+"_started_at"].(string))
		if !haveMin || a.Before(minA) {
			minA = a
			haveMin = true
		}
	}
	if haveMin {
		check("D_eq_A", "", func(row map[string]any) { row["publication_dispatch_started_at"] = protocol.FormatTime(minA) })
	}
	if cp["publish_target"].(map[string]any)["state"] == "confirmed" {
		oStr := cp["publish_target"].(map[string]any)["observed_at"].(string)
		// A[s]=O[s] with the strong-read interval collapsed to the same instant. Uses publish_target:
		// its target object is not referenced by any cleanup before, so collapsing its observation
		// times does not desync a downstream cleanup binding (stage_temp IS the temp_cleanup before
		// under block collision, so mutating its object would break FILE_*_CLEANUP_INVALID).
		check("publish_target/A_eq_O_alltimes", "", func(row map[string]any) {
			slot := row["publication_step_checkpoint"].(map[string]any)["publish_target"].(map[string]any)
			obj := slot["proof"].(map[string]any)["target"].(map[string]any)
			obj["observed_at"] = oStr
			row["publication_publish_target_started_at"] = oStr
			row["publication_step_proof_bytes"].(map[string]any)["publish_target"] = canon(slot["proof"])
			rebuildCheckpoint(row)
		})
	}
	if cp["stage_temp"].(map[string]any)["state"] == "confirmed" {
		// O[s]=J (slot observation coincides with the local join). Slot-only mutation; the stage_temp
		// object/proof is untouched, so the temp_cleanup before reference stays consistent.
		jStr := base["publication_closed_proof"].(map[string]any)["joined_at"].(string)
		check("stage_temp/O_eq_J", "", func(row map[string]any) {
			row["publication_step_checkpoint"].(map[string]any)["stage_temp"].(map[string]any)["observed_at"] = jStr
			rebuildCheckpoint(row)
		})
	}
	// J=E (first closure envelope observation coincides with the join).
	eStr := base["publication_closure_receipt_observed_at"].(string)
	check("J_eq_E", "", func(row map[string]any) {
		cl := row["publication_closed_proof"].(map[string]any)
		cl["joined_at"] = protocol.FormatTime(parse(eStr)) // closure_receipt_observed_at is timestamptz (+00:00); convert to canonical Time
		clBytes := canon(cl)
		row["publication_closure_canonical"] = clBytes
		res := row["publication_resolution_proof"].(map[string]any)
		res["closure_digests"] = []any{fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte("file-closure:v1\x00"), []byte(clBytes)...)))}
		row["publication_resolution_canonical"] = canon(res)
	})
}
