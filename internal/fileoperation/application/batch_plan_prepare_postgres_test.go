package application

import (
	"testing"

	"bansoko/internal/workitems"
)

// A video the accepted batch plan covers elsewhere (an existing or in-flight
// placement) or dropped for a target conflict is declared excluded with the
// plan reason: no operation, no effect, no failed delivery result.
func TestPrepareExcludesVideosThePlanCoversElsewhere(t *testing.T) {
	for _, tc := range []struct{ outcome, reason string }{
		{"covered_existing", "covered_existing"},
		{"covered_existing", "replacement_partial_coverage"},
		{"covered_in_flight", "covered_in_flight"},
		{"target_collision", "target_collision_blocked"},
	} {
		outcome, reason := tc.outcome, tc.reason
		t.Run(reason, func(t *testing.T) {
			db, fixture := openFileOperationDBWithPolicy(t, func(map[string]any) {})
			defer db.Close()
			if _, err := db.Exec(`
UPDATE media_fact_snapshots
SET release_facts = jsonb_set(jsonb_set(release_facts, '{batch_plan,files,0,outcome}', to_jsonb($1::text)), '{batch_plan,files,0,reason_code}', to_jsonb($2::text))
WHERE id = 'snap-fixture-default'`, outcome, reason); err != nil {
				t.Fatal(err)
			}
			service := newFixtureService(t, db, fixture)
			prepareFixture(t, db, service, fixture.artifactID)

			if operations := loadOperations(t, db, fixture.artifactID); len(operations) != 0 {
				t.Fatalf("a covered video was planned: %+v", operations)
			}
			assertRequirement(t, db, fixture.artifactID, "primary", "excluded", reason)
			if failed := failedDeliveryResults(t, db, fixture.artifactID); failed != 0 {
				t.Fatalf("a covered video failed the acquisition: %d failed results", failed)
			}
			var effects int
			if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind=$1`, workitems.KindFileExecute).Scan(&effects); err != nil {
				t.Fatal(err)
			}
			if effects != 0 {
				t.Fatalf("a covered video scheduled %d effects", effects)
			}
		})
	}
}
