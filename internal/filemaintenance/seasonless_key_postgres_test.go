package filemaintenance

import (
	"context"
	"testing"
)

// An acquisition sealed with v1 keys before the upgrade lands after it: its
// placement is written with the seasonless key, and a replay keeps the
// coverage the placement committed with.
func TestAdmitFromFileOperationLandsAV1CoverageAsTheSeasonlessKey(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-v1", ManagedAnimeID: "anime-1", ArtifactVerified: true, TargetPath: "/library/Show/EP02.mkv",
		SourceFingerprint: "fp-src", TargetFingerprint: "fp-src",
	}}
	service := NewPlacementService(db, reader, nil)
	ctx := context.Background()
	first, err := service.AdmitFromFileOperation(ctx, "op-v1", []string{"tv:s1:e2"}, "facts-v1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AdmitFromFileOperation(ctx, "op-v1", []string{"tv:s1:e2"}, "facts-v1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("replay must return the same placement id")
	}
	rows, err := db.QueryContext(ctx, `SELECT observation_key FROM placement_coverage WHERE placement_id = $1`, first)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "tv:e2" {
		t.Fatalf("coverage = %v, want [tv:e2]", keys)
	}
}

// After the rewrite a placement of the reference season carries tv:e1 and
// pools with a new tv:e1 file, which replaces it. A placement the rewrite
// excluded (another season, v1 key kept, not replacement-eligible) is never
// pooled, never superseded and never handed to cleanup.
func TestSeasonlessRewriteMigratedPlacementPoolsAndExcludedPlacementStaysOut(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	excluded := seedFilePlacement(t, db, "anime-1", "sub-1", "release-s1", []string{"tv:s1:e1"}, Facts{Resolution: "480p", Group: "GroupA"}, "2026-06-01T00:00:00Z")
	if _, err := db.Exec(`
UPDATE placements SET replacement_eligible = false, version = version + 1, physical_path = '/library/Show/S01/ep.mkv'
WHERE id = $1`, excluded); err != nil {
		t.Fatal(err)
	}
	migrated := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"}, Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionsByKey(t, views)["tv:e1"]
	if len(views) != 1 || decision.Verdict != "replace" {
		t.Fatalf("decisions = %+v", views)
	}
	if losers := viewLosers(decision); len(losers) != 1 || losers[0] != migrated {
		t.Fatalf("losers = %v, want only the migrated placement %s", losers, migrated)
	}
	var pooled int
	if err := db.QueryRow(`SELECT count(*) FROM replacement_decisions WHERE strpos(pools::text, $1) > 0`, excluded).Scan(&pooled); err != nil {
		t.Fatal(err)
	}
	if pooled != 0 {
		t.Fatalf("the excluded placement entered %d decision pools", pooled)
	}

	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if placementState(t, db, migrated) != "superseded" {
		t.Fatal("the migrated placement must be replaced by the new tv:e1 file")
	}
	var state string
	var eligible bool
	if err := db.QueryRow(`SELECT state, replacement_eligible FROM placements WHERE id = $1`, excluded).Scan(&state, &eligible); err != nil {
		t.Fatal(err)
	}
	if state != "active" || eligible {
		t.Fatalf("excluded placement = %s eligible %v, want active and still excluded", state, eligible)
	}
	var handedOff int
	if err := db.QueryRow(`SELECT count(*) FROM replacement_deletions WHERE strpos(row_to_json(replacement_deletions)::text, $1) > 0`, excluded).Scan(&handedOff); err != nil {
		t.Fatal(err)
	}
	if handedOff != 0 {
		t.Fatalf("the excluded placement was handed to cleanup %d times", handedOff)
	}
}
