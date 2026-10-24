package migrations

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/runtime/database"
)

// These tests prove the shape of the committed per-file result. PRUNE-046 S5
// folded acquisition_delivery_results into acquisition_files, so the delivery
// columns are written by UPDATE on the file row that accept, inspection and
// planning already created: the success/failure matrix, the requirement gate,
// the FK delete actions and the runtime privileges are asserted here.

type deliveryRow struct {
	file, outcome, effect, cleanup string
	mode, reason                   any
	final                          any
}

func validDelivery(file string) deliveryRow {
	return deliveryRow{file: file, outcome: "succeeded", effect: "confirmed", cleanup: "not_required",
		mode: "executed", reason: nil, final: `{"path":"Anime/Show/S01E01.mkv"}`}
}

func insertDelivery(tx *sql.Tx, root string, d deliveryRow) error {
	_, err := tx.ExecContext(context.Background(), `UPDATE acquisition_files SET
 delivery_schema_version=1, outcome=$3, completion_mode=$4, effect_class=$5, final_object=$6::jsonb,
 delivery_source_cleanup=$7, delivery_reason_code=$8, evidence_digest=$9, receipt='{}'::jsonb, delivered_at=CURRENT_TIMESTAMP
WHERE acquisition_id=$1 AND file_ref=$2`,
		root, d.file, d.outcome, d.mode, d.effect, d.final, d.cleanup, d.reason, acqDigest("e"))
	return err
}

func TestAcquisitionDeliveryResultMatrix(t *testing.T) {
	env := newRootEnv(t)
	// One committed set per case: file-video is required, file-sub is excluded.
	try := func(name, want string, mutate func(d *deliveryRow)) {
		t.Helper()
		root := env.root(t, validInput)
		if err := inTx(env.db, newDeclaredSet(root).write); err != nil {
			t.Fatalf("seed the declared set: %v", err)
		}
		d := validDelivery("file-video")
		if mutate != nil {
			mutate(&d)
		}
		expectState(t, name, inTx(env.db, func(tx *sql.Tx) error { return insertDelivery(tx, root, d) }), want)
	}

	try("a succeeded executed result", "", nil)
	try("a succeeded retain result", "", func(d *deliveryRow) { d.mode, d.effect = "retain", "no_external_effect" })
	try("a succeeded result with a completed source cleanup", "", func(d *deliveryRow) { d.cleanup = "completed" })
	try("a failed result with a reason and a partial final object", "", func(d *deliveryRow) {
		d.outcome, d.mode, d.reason, d.effect = "failed", nil, "target_exists", "unknown"
	})
	try("a failed result with an uncertain source cleanup and no final object", "", func(d *deliveryRow) {
		d.outcome, d.mode, d.reason, d.final, d.cleanup = "failed", nil, "dependency_failed", nil, "uncertain"
	})

	try("a succeeded result with no completion mode", "23514", func(d *deliveryRow) { d.mode = nil })
	try("a succeeded result with no final object", "23514", func(d *deliveryRow) { d.final = nil })
	try("a succeeded result with a failure reason", "23514", func(d *deliveryRow) { d.reason = "oops" })
	try("a succeeded result whose source cleanup failed", "23514", func(d *deliveryRow) { d.cleanup = "failed" })
	try("a succeeded result whose source cleanup is uncertain", "23514", func(d *deliveryRow) { d.cleanup = "uncertain" })
	try("a failed result with no reason", "23514", func(d *deliveryRow) { d.outcome, d.mode, d.reason = "failed", nil, nil })
	try("a failed result that still names a completion mode", "23514", func(d *deliveryRow) {
		d.outcome, d.reason = "failed", "target_exists"
	})
	try("an unknown outcome", "23514", func(d *deliveryRow) { d.outcome = "skipped" })
	try("an unknown completion mode", "23514", func(d *deliveryRow) { d.mode = "copied" })
	try("an unknown effect class", "23514", func(d *deliveryRow) { d.effect = "maybe" })
	try("an unknown source cleanup state", "23514", func(d *deliveryRow) { d.cleanup = "pending" })
	try("a final object that is not an object", "23514", func(d *deliveryRow) { d.final = `[1]` })
	try("an empty reason code", "23514", func(d *deliveryRow) { d.outcome, d.mode, d.reason = "failed", nil, "" })
	// The result's file must be a required file of the same root: the row-level
	// check refuses a result on the excluded member.
	try("a result for an excluded file", "23514", func(d *deliveryRow) { d.file = "file-sub" })
}

func TestAcquisitionFileDeliveryNoCascadeAndRuntimeCannotWrite(t *testing.T) {
	env := newRootEnv(t)
	ctx := context.Background()
	table := "acquisition_files"
	var other int
	if err := env.db.QueryRowContext(ctx, `
SELECT count(*) FROM pg_constraint
WHERE conrelid = $1::regclass AND contype = 'f' AND confdeltype <> 'a'`, table).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if other != 0 {
		t.Errorf("%s has %d foreign keys with a delete action other than no action", table, other)
	}
	rows, err := env.db.QueryContext(ctx, `
SELECT p, has_table_privilege('bansoko_runtime', $1, p)
FROM unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER']) AS p`, table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var privilege string
		var granted bool
		if err := rows.Scan(&privilege, &granted); err != nil {
			t.Fatal(err)
		}
		if want := privilege == "SELECT"; granted != want {
			t.Errorf("runtime privilege %s on %s = %t, want %t", privilege, table, granted, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()

	env.root(t, validInput)
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, env.db, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatalf("provision runtime role: %v", err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	for _, statement := range []string{
		`DELETE FROM acquisition_files`,
		`TRUNCATE acquisition_files`,
	} {
		if _, err := runtime.ExecContext(ctx, statement); err == nil {
			t.Errorf("the runtime role must be denied: %s", statement)
		} else if !isInsufficientPrivilege(err) {
			t.Errorf("runtime statement %q: error = %v, want insufficient_privilege", statement, err)
		}
	}
}
