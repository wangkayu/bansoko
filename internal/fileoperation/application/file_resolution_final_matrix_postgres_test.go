package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
)

// Mutate both persistent-value representation and canonical bytes in memory.
// This proves final SQL schema/digest predicates, not a malformed persisted row.
func verifyFinalResolutionMatrix(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	owner := ownerTestDB(t)
	var raw string
	if err := owner.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	for _, tc := range []struct {
		name, key string
		value     any
		remove    bool
	}{
		{"extra", "extra", true, false}, {"missing_version", "schema_version", nil, true}, {"null_version", "schema_version", nil, false}, {"string_version", "schema_version", "1", false}, {"wrong_version", "schema_version", 2, false},
		{"missing_effect", "effect_class", nil, true}, {"null_effect", "effect_class", nil, false}, {"wrong_effect", "effect_class", "unknown", false},
		{"missing_checkpoint", "checkpoint_digest", nil, true}, {"null_checkpoint", "checkpoint_digest", nil, false}, {"wrong_checkpoint", "checkpoint_digest", "sha256:" + strings.Repeat("0", 64), false},
		{"missing_closures", "closure_digests", nil, true}, {"null_closures", "closure_digests", nil, false}, {"empty_closures", "closure_digests", []any{}, false}, {"wrong_closures_type", "closure_digests", "foreign", false},
	} {
		var row map[string]any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&row); err != nil {
			t.Fatal(err)
		}
		res := row["publication_resolution_proof"].(map[string]any)
		if tc.remove {
			delete(res, tc.key)
		} else {
			res[tc.key] = tc.value
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		v, err := protocol.ParseStrict(b)
		if err != nil {
			t.Fatal(err)
		}
		row["publication_resolution_canonical"] = string(protocol.Canonical(v))
		b, err = json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.ExecContext(ctx, `SELECT file_validate_final_proof(jsonb_populate_record(NULL::file_operations,$1::jsonb))`, string(b))
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_FINAL_PROOF_DIGEST_INVALID" {
			t.Fatalf("Q03/%s=%v", tc.name, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatalf("Q03/%s wrote graph", tc.name)
		}
		t.Logf("sql_component_rejection Q03/%s resolution.%s 23514 FILE_FINAL_PROOF_DIGEST_INVALID zero_write", tc.name, tc.key)
	}
}
