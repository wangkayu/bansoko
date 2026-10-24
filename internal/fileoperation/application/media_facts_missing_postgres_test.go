package application

import (
	"testing"

	"bansoko/internal/workitems"
)

// Every acquisition admission freezes the Release media fact snapshot and
// artifact inspection writes one row per file, so a video without its snapshot
// (or its row) is an integrity failure: prepare blocks it as
// media_facts_missing and never treats it as an unknown file (skipped, or
// released into extras). A snapshot without the batch plan is incomplete
// facts too, and a row whose identity disagrees with the plan it was accepted
// with is batch_plan_drift (never a re-plan).
func TestPrepareBlocksVideoWithoutReleaseMediaFacts(t *testing.T) {
	for name, tc := range map[string]struct {
		statements []string
		reason     string
	}{
		"no snapshot": {statements: []string{
			`DELETE FROM media_fact_files WHERE snapshot_id = 'snap-fixture-default'`,
			`DELETE FROM media_fact_snapshots WHERE release_id = 'release-file-test'`,
		}, reason: "media_facts_missing"},
		"no file row": {statements: []string{
			`DELETE FROM media_fact_files WHERE snapshot_id = 'snap-fixture-default'`,
		}, reason: "media_facts_missing"},
		"no batch plan": {statements: []string{
			`UPDATE media_fact_snapshots SET release_facts = release_facts - 'batch_plan' WHERE id = 'snap-fixture-default'`,
		}, reason: "media_facts_missing"},
		"plan drift": {statements: []string{
			`UPDATE media_fact_files SET effective_facts = jsonb_set(effective_facts, '{identity,CoverageKey}', '"tv:s1:e2"') WHERE snapshot_id = 'snap-fixture-default'`,
		}, reason: "batch_plan_drift"},
	} {
		statements, wantReason := tc.statements, tc.reason
		t.Run(name, func(t *testing.T) {
			db, fixture := openFileOperationDBWithPolicy(t, func(map[string]any) {})
			defer db.Close()
			for _, statement := range statements {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			service := newFixtureService(t, db, fixture)
			prepareFixture(t, db, service, fixture.artifactID)

			var reason string
			if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, fixture.artifactID).Scan(&reason); err != nil {
				t.Fatal(err)
			}
			if reason != wantReason {
				t.Fatalf("failed delivery reason = %q, want %s", reason, wantReason)
			}
			var unknown, effects int
			if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1 AND frozen_render_snapshot->'unknown_file' = 'true'::jsonb`, fixture.artifactID).Scan(&unknown); err != nil {
				t.Fatal(err)
			}
			if unknown != 0 {
				t.Fatalf("a video without media facts was rendered as an unknown file (%d)", unknown)
			}
			if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind=$1`, workitems.KindFileExecute).Scan(&effects); err != nil {
				t.Fatal(err)
			}
			if effects != 0 {
				t.Fatalf("a video without media facts scheduled %d effects", effects)
			}
		})
	}
}
