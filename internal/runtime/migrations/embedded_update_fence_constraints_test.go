package migrations

import (
	"context"
	"testing"
)

// The embedded updater is the only fence owner: the open lock pins admitted_by
// to the single value 'embedded', and the recovery columns the folded fence
// audit writes live on that same row.
func TestEmbeddedUpdateFenceOwnerIsEmbeddedOnly(t *testing.T) {
	db := installedDB(t)
	const constraint = "embedded_update_locks_admitted_by_check"
	var definition string
	if err := db.QueryRowContext(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1`, constraint).Scan(&definition); err != nil {
		t.Fatalf("%s: %v", constraint, err)
	}
	if want := "CHECK ((admitted_by = 'embedded'::text))"; definition != want {
		t.Fatalf("%s = %q, want %q", constraint, definition, want)
	}
	var nullable int
	if err := db.QueryRowContext(context.Background(), `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'public' AND column_name = 'admitted_by' AND is_nullable = 'YES'
  AND table_name = 'embedded_update_locks'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != 0 {
		t.Fatal("admitted_by is nullable on the fence lock table")
	}
}
