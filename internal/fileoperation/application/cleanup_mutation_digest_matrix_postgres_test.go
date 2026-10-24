package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// cleanup.before mutation/digest propagation, hash-self-consistent. The group matrix's
// cleanup "before" case sets a foreign value without recomputing the mutation_receipt_digest,
// so its rejection could be attributed to the stale digest rather than the before==source
// binding. Here the mutation_receipt_digest is recomputed for the mutated before, so the only
// failing predicate is the reference binding (before IS DISTINCT FROM source/stage_temp/displaced)
// -> FILE_FINAL_PROOF_CLEANUP_INVALID. This proves the digest genuinely propagates the before
// (a recomputed digest for a drifted before is accepted by the digest check, then refused by the
// binding), and that the binding — not an incidental digest mismatch — is what rejects.
func verifyCleanupMutationDigestMatrix(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
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
	digest := func(domain, payload string) string {
		return fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte(domain+"\x00"), []byte(payload)...)))
	}
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
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
	for _, step := range []string{"source_cleanup", "temp_cleanup"} {
		slot := cp[step].(map[string]any)
		if slot["state"] != "confirmed" {
			continue
		}
		check := func(name string, mutate func(row map[string]any), want string) {
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
			var rej *pgconn.PgError
			if !errors.As(err, &rej) || rej.Code != "23514" || rej.Message != want {
				t.Fatalf("cleanup digest %s=%v want %s", name, err, want)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatalf("cleanup digest %s wrote graph", name)
			}
			t.Logf("sql_component_rejection Q12/cleanup/%s 23514 %s zero_write", name, want)
		}
		// before drifted (observed_at moved), mutation_receipt_digest recomputed to match, so the
		// digest check passes and only the before==source binding refuses.
		check(step+"/before_binding", func(row map[string]any) {
			rcp := row["publication_step_checkpoint"].(map[string]any)
			proof := rcp[step].(map[string]any)["proof"].(map[string]any)
			bf := proof["before"].(map[string]any)
			bf["observed_at"] = protocol.FormatTime(parse(bf["observed_at"].(string)).Add(time.Hour))
			mutation := canon(map[string]any{"before": bf, "permit_id": row["publication_permit_id"], "removed_at": rcp[step].(map[string]any)["observed_at"], "step": step})
			proof["mutation_receipt_digest"] = digest("file-mutation-receipt:v1", mutation)
			row["publication_step_proof_bytes"].(map[string]any)[step] = canon(proof)
			cpBytes := canon(rcp)
			row["publication_resolution_checkpoint_canonical"] = cpBytes
			res := row["publication_resolution_proof"].(map[string]any)
			res["checkpoint_digest"] = digest("file-checkpoint:v1", cpBytes)
			row["publication_resolution_canonical"] = canon(res)
		}, "FILE_FINAL_PROOF_CLEANUP_INVALID")
		// digest present and matching the (correct) before, but a foreign value -> digest predicate
		// refuses with the before unchanged (isolates the digest check from the binding).
		check(step+"/mutation_digest_foreign", func(row map[string]any) {
			rcp := row["publication_step_checkpoint"].(map[string]any)
			proof := rcp[step].(map[string]any)["proof"].(map[string]any)
			proof["mutation_receipt_digest"] = "sha256:" + strings.Repeat("0", 64)
			row["publication_step_proof_bytes"].(map[string]any)[step] = canon(proof)
			cpBytes := canon(rcp)
			row["publication_resolution_checkpoint_canonical"] = cpBytes
			res := row["publication_resolution_proof"].(map[string]any)
			res["checkpoint_digest"] = digest("file-checkpoint:v1", cpBytes)
			row["publication_resolution_canonical"] = canon(res)
		}, "FILE_FINAL_PROOF_CLEANUP_INVALID")
	}
}
