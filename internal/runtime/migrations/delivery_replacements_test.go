package migrations

import (
	"context"
	"strings"
	"testing"
)

// The flattened acquisition input schema 1 carries one approval per overwrite
// target. The helper reads the approvals of either delivery shape,
// the overwrite proofs match any one of them, and the version CHECKs accept 7.
func TestDeliveryReplacements(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		delivery string
		want     []string
	}{
		"v3 list":        {`{"replacements":[{"expected_object":{"location":{"canonical_path":"/l/a.mkv"}}},{"expected_object":{"location":{"canonical_path":"/l/b.mkv"}}}]}`, []string{"/l/a.mkv", "/l/b.mkv"}},
		"v3 empty":       {`{"replacements":[]}`, nil},
		"v2 known":       {`{"replacement":{"state":"known","value":{"expected_object":{"location":{"canonical_path":"/l/a.mkv"}}},"reason_code":null}}`, []string{"/l/a.mkv"}},
		"v2 absent":      {`{"replacement":{"state":"absent","value":null,"reason_code":null}}`, nil},
		"no replacement": {`{}`, nil},
	} {
		rows, err := db.QueryContext(ctx, `SELECT r->'expected_object'->'location'->>'canonical_path' FROM public.delivery_replacements($1::jsonb) r ORDER BY 1`, c.delivery)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got []string
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				t.Fatal(err)
			}
			got = append(got, path)
		}
		rows.Close()
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s: approvals = %v, want %v", name, got, c.want)
		}
	}
	var runtimeMay bool
	if err := db.QueryRowContext(ctx, `SELECT has_function_privilege('bansoko_runtime','public.delivery_replacements(jsonb)','EXECUTE')`).Scan(&runtimeMay); err != nil {
		t.Fatal(err)
	}
	if !runtimeMay {
		t.Fatal("runtime role cannot read delivery approvals")
	}
	for _, fn := range []string{
		"public.file_prepare_permit(text,text,bigint,text,text,text)",
		"public.file_assert_final_proof(text)",
		"public.postprocessing_plan_commit(text,text,bigint,text,jsonb,text,text,text,jsonb,jsonb)",
	} {
		var definition string
		if err := db.QueryRowContext(ctx, `SELECT pg_get_functiondef($1::regprocedure)`, fn).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(definition, "delivery_replacements") || strings.Contains(definition, "'delivery'->'replacement'->") {
			t.Fatalf("%s still reads the single delivery replacement", fn)
		}
	}
	var accept string
	if err := db.QueryRowContext(ctx, `SELECT pg_get_functiondef('public.accept_acquisition_queued_without_vars'::regproc)`).Scan(&accept); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(accept, "'1'::jsonb") {
		t.Fatal("acceptance does not take the flattened input schema 1")
	}
	var definition string
	if err := db.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='acquisitions_input_schema_version_check'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "= 1") {
		t.Fatalf("acquisitions_input_schema_version_check = %s, want schema 1", definition)
	}
}
