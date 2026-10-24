package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"database/sql"
	"testing"
)

// Resolution digests are derived by the recorder, not accepted from a receipt.
// Cross-check persisted bytes independently in Go and SQL without editing them.
func verifyResolutionDigests(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	var checkpoint, closure, resolution string
	if err := db.QueryRowContext(ctx, `SELECT publication_resolution_checkpoint_canonical,publication_closure_canonical,publication_resolution_canonical FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&checkpoint, &closure, &resolution); err != nil {
		t.Fatal(err)
	}
	value, err := protocol.ParseStrict([]byte(resolution))
	if err != nil {
		t.Fatal(err)
	}
	if value.Object["checkpoint_digest"].String != protocol.Digest("file-checkpoint:v1", []byte(checkpoint)) || len(value.Object["closure_digests"].Array) != 1 || value.Object["closure_digests"].Array[0].String != protocol.Digest("file-closure:v1", []byte(closure)) {
		t.Fatal("Go resolution digest mismatch")
	}
	owner := ownerTestDB(t)
	var valid bool
	if err := owner.QueryRowContext(ctx, `SELECT publication_resolution_proof->>'checkpoint_digest'=public.file_proof_digest('file-checkpoint:v1',publication_resolution_checkpoint_canonical) AND publication_resolution_proof->'closure_digests'=jsonb_build_array(public.file_proof_digest('file-closure:v1',publication_closure_canonical)) FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&valid); err != nil || !valid {
		t.Fatalf("SQL resolution digest=%v err=%v", valid, err)
	}
	for _, domain := range []string{"file-checkpoint:v1", "file-closure:v1"} {
		original := checkpoint
		if domain == "file-closure:v1" {
			original = closure
		}
		if protocol.Digest(domain, []byte(original)) == protocol.Digest(domain, []byte(original+" ")) {
			t.Fatal("changed digest bytes accepted")
		}
		if err := owner.QueryRowContext(ctx, `SELECT public.file_proof_digest($1,$2)<>public.file_proof_digest($1,$3)`, domain, original, original+" ").Scan(&valid); err != nil || !valid {
			t.Fatalf("SQL altered digest bytes=%v err=%v", valid, err)
		}
	}
}
