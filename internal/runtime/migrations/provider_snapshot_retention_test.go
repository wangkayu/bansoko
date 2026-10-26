package migrations

import (
	"strings"
	"testing"
)

// Catalog and group snapshots carry a required retention deadline that
// defaults to 30 days on the database clock, with an index for the janitor.
func TestProviderSnapshotRetentionColumns(t *testing.T) {
	db := installedDB(t)
	for _, table := range []string{"provider_catalog_snapshots", "provider_group_snapshots"} {
		var nullable string
		var defaultExpression *string
		if err := db.QueryRow(`
			SELECT is_nullable, column_default FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'retention_until'`, table).Scan(&nullable, &defaultExpression); err != nil {
			t.Fatalf("inspect %s retention_until: %v", table, err)
		}
		if nullable != "NO" {
			t.Fatalf("%s.replacement_until must be NOT NULL, got %s", table, nullable)
		}
		if defaultExpression == nil || !strings.Contains(strings.ToLower(*defaultExpression), "current_timestamp") || !strings.Contains(*defaultExpression, "30 days") {
			t.Fatalf("%s.replacement_until default must use the 30-day database clock, got %v", table, defaultExpression)
		}
		var indexed bool
		if err := db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, table+"_retention_idx").Scan(&indexed); err != nil {
			t.Fatal(err)
		}
		if !indexed {
			t.Fatalf("%s_retention_idx is missing", table)
		}
	}
}
