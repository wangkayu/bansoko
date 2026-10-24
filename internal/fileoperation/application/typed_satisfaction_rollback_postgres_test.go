package application

import (
	"context"
	"strings"
	"testing"
)

func TestTypedObservedSatisfactionAtomicRollbackWrites(t *testing.T) {
	for _, table := range []string{"acquisition_files", "postprocessing_plans", "work_items", "acquisitions"} {
		t.Run(table, func(t *testing.T) {
			db, s, c, b, _ := preparedObservedBatch(t)
			owner := ownerTestDB(t)
			if _, err := owner.Exec(`CREATE FUNCTION test_observed_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'OBSERVED_ROLLBACK'; END $$`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := owner.Exec(`DROP TRIGGER IF EXISTS test_observed_fault ON ` + table + `; DROP FUNCTION IF EXISTS test_observed_fault()`); err != nil {
					t.Error(err)
				}
			}()
			if _, err := owner.Exec(`CREATE TRIGGER test_observed_fault BEFORE INSERT OR UPDATE ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION test_observed_fault()`); err != nil {
				t.Fatal(err)
			}
			before := planGraphSnapshot(t, db)
			_, err := s.commitTypedPlan(context.Background(), c, b)
			if err == nil || !strings.Contains(err.Error(), "OBSERVED_ROLLBACK") {
				t.Fatalf("fault not reached: %v", err)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("partial observed satisfaction commit")
			}
		})
	}
}
