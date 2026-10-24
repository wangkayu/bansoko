package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	catalog "bansoko/internal/catalog/domain"
)

// Run under the A1 owner's genuine fixture and isolated runtime, for all three
// config sources. Every negative enters 132's locked recheck first; simply
// testing a digest mismatch before querying PostgreSQL would not prove A4.
func checkWitnessRecheckPostgres(t *testing.T, ctx context.Context, db *sql.DB, captured admissionCapture) {
	t.Helper()
	input := catalog.DigestBytes([]byte("fixture-only frozen input"))
	witness, err := constructWitness(captured, input, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("valid", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		at, err := recheckWitnessTx(ctx, tx, captured, input, witness, testWitnessLimits())
		if err != nil || at.IsZero() {
			t.Fatalf("locked valid witness: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong request", func(v map[string]any) { v["request_hash"] = input }},
		{"wrong input", func(v map[string]any) { v["input_digest"] = captured.requestHash }},
		{"wrong config", func(v map[string]any) { v["config_digest"] = input }},
		{"wrong observation", func(v map[string]any) { v["observation_digest"] = input }},
		{"wrong observation body", func(v map[string]any) { v["observation"].(map[string]any)["snapshot_version"] = int64(999) }},
		{"wrong policy", func(v map[string]any) { v["policy"].(map[string]any)["max_rss"] = int64(0) }},
		{"wrong policy digest", func(v map[string]any) { v["policy_digest"] = input }},
		{"wrong rank", func(v map[string]any) { v["decisions"].([]any)[0].(map[string]any)["rank"] = int64(99) }},
		{"wrong reason", func(v map[string]any) { v["decisions"].([]any)[0].(map[string]any)["reason_code"] = "policy_always" }},
		{"wrong origin", func(v map[string]any) {
			v["decisions"].([]any)[0].(map[string]any)["policy_origin"].(map[string]any)["config_version"] = int64(999)
		}},
		{"omitted union", func(v map[string]any) { v["decisions"] = []any{} }},
		{"wrong selected", func(v map[string]any) { v["selected_refs"] = []any{} }},
		{"translation forbidden", func(v map[string]any) { v["translation"] = map[string]any{} }},
		{"unknown key", func(v map[string]any) { v["skip"] = true }},
		{"evaluator unregistered", func(v map[string]any) { v["evaluator_version"] = "future" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal([]byte(witness.canonical), &value); err != nil {
				t.Fatal(err)
			}
			tc.change(value)
			// Re-sign the tampered whole value so this tests real semantics, not the
			// simple digest guard already present in 128.
			canonical := mustCanonical(value)
			bad := preparationWitness{canonical: string(canonical), digest: protocol.Digest(witnessDigestDomain, canonical)}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			at, err := recheckWitnessTx(ctx, tx, captured, input, bad, testWitnessLimits())
			if !errors.Is(err, errWitnessMismatch) || !at.IsZero() {
				t.Fatalf("tampered witness accepted: %v", err)
			}
			var held bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation='provider_group_snapshots'::regclass AND granted)`).Scan(&held); err != nil || !held {
				t.Fatalf("132 lock path not executed: %v", err)
			}
			assertWitnessZeroGraph(t, ctx, tx)
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertWitnessZeroGraph(t *testing.T, ctx context.Context, tx *sql.Tx) {
	t.Helper()
	var roots, manifests, tasks, work int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM acquisitions),
 (SELECT count(*) FROM acquisition_files),
 (SELECT count(*) FROM download_tasks),
 (SELECT count(*) FROM work_item_history WHERE kind IN ('qb_submit','acquisition_settle'))`).Scan(&roots, &manifests, &tasks, &work); err != nil {
		t.Fatal(err)
	}
	if roots+manifests+tasks+work != 0 {
		t.Fatalf("rejection wrote graph: %d/%d/%d/%d", roots, manifests, tasks, work)
	}
}
