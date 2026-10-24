package migrations

import "testing"

// File maintenance policies keep only their own settings; the retention,
// cleanup and profile settings live with the retention policies.
func TestFilemaintenancePoliciesHaveNoRetentionColumns(t *testing.T) {
	db := installedDB(t)
	for _, column := range []string{"retention", "cleanup", "profile"} {
		if columnPresent(t, db, "filemaintenance_policies", column) {
			t.Fatalf("filemaintenance_policies.%s must not exist", column)
		}
	}
}
