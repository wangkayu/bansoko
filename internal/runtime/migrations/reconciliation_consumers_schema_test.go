package migrations

import (
	"context"
	"testing"
)

// TestReconciliationConsumersSchemaShape proves the installed schema carries the
// calibration provenance and consumer checkpoint owners plus the three
// FileMaintenance work item kinds, with zero batch effects: a fresh install
// contains no scan, WorkItem, placement or calibration row.
func TestReconciliationConsumersSchemaShape(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Verify(ctx); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// The merged observation/calibration owner exists with its typed columns.
	for table, columns := range map[string][]string{
		"reconciliation_files": {"managed_anime_id", "generation", "file_path", "derived_missing", "batch_index", "fingerprint", "size_bytes", "facts", "observed_at", "disposition", "coverage", "resolution", "revision", "source", "language", "group", "parser_version", "facts_fingerprint", "version", "created_at", "updated_at"},
	} {
		actual, err := tableColumns(ctx, db, table)
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		for _, column := range columns {
			if _, ok := actual[column]; !ok {
				t.Fatalf("required column %s.%s is missing", table, column)
			}
		}
	}

	// The work item kind allowlist carries the three new kinds.
	var kindList string
	if err := db.QueryRowContext(ctx, `
SELECT pg_get_constraintdef(oid) FROM pg_constraint
WHERE conname = 'work_items_owner_kind_check' AND conrelid = 'work_items'::regclass`).Scan(&kindList); err != nil {
		t.Fatalf("read kind check: %v", err)
	}
	for _, kind := range []string{"reconciliation_calibrate", "reconciliation_consume_alignment", "reconciliation_consume_replacement"} {
		if !containsFragment(kindList, kind) {
			t.Fatalf("kind allowlist missing %s: %s", kind, kindList)
		}
	}

	// Zero batch effects: the install created no scans, work items,
	// placements or calibration rows, and no filesystem effect happened.
	for table, where := range map[string]string{
		"reconciliation_scans":  "",
		"reconciliation_files":  "",
		"work_items":            "",
		"work_item_outcomes":    "",
		"placements":            "",
		"replacement_decisions": "",
		"cleanup_operations":    "",
		"alignment_operations":  "",
	} {
		query := "SELECT COUNT(*) FROM " + table
		if where != "" {
			query += " WHERE " + where
		}
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("install must not batch-create rows in %s: %d", table, count)
		}
	}
}

func containsFragment(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
