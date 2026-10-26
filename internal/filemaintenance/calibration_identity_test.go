package filemaintenance

import (
	"database/sql"
	"strings"
	"testing"

	"bansoko/internal/workitems"
)

// seedPlacementWithFingerprints inserts a verified active placement with the
// given frozen fingerprints and one coverage key, plus its Placement-owned
// observation carrying a metadata fingerprint.
func seedPlacementWithFingerprints(t *testing.T, db *sql.DB, animeID string, generation int64, placementID, filePath, sourceFingerprint, targetFingerprint, coverageKey, observed string, observedSize int64) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, snapshot_generation, version)
VALUES ($1, $2, $3, 'reconciliation', $4, $5, $6, 'facts-v1', $7, 1)`,
		placementID, filePath, animeID, placementID+"-seed", sourceFingerprint, targetFingerprint, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, 0, 'facts-v1', $1)`, placementID, coverageKey); err != nil {
		t.Fatal(err)
	}
	seedObservationFacts(t, db, animeID, generation, 0, filePath, observed, observedSize,
		`{"placement_id":"`+placementID+`","disposition":"present","located_via":"current_path","located_path":"`+filePath+`"}`)
}

// The scan is path-level: calibration decides "changed" from the observed
// size (and inode) against the size the placement recorded, never a digest.
// A placement without a recorded size stays present, and an observed
// placement is never admitted again.
func TestCalibrationComparesMetadataObservationWithRecordedSize(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	const anime = "anime-cal-meta"
	seedAnimeSubscription(t, fixture.db, anime, "sub-1", "stopped")
	seedInstanceReplacement(t, fixture.db, anime, ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, anime, 1, map[string]string{})
	digest := strings.Repeat("ab", 32)
	typedSource := `{"algorithm":"sha256","value":"` + digest + `"}`
	typedTarget := `{"algorithm":"sha256","value":"` + digest + `","size_bytes":100}`
	seedPlacementWithFingerprints(t, fixture.db, anime, 1, "plc-meta-same", "/library/Show/EP 01.mkv",
		typedSource, typedTarget, "tv:s1:e1", "meta:size=100;dev=7;ino=11", 100)
	seedPlacementWithFingerprints(t, fixture.db, anime, 1, "plc-meta-grown", "/library/Show/EP 02.mkv",
		typedSource, typedTarget, "tv:s1:e2", "meta:size=101;dev=7;ino=12", 101)
	seedPlacementWithFingerprints(t, fixture.db, anime, 1, "plc-meta-inode", "/library/Show/EP 03.mkv",
		"meta:size=100;dev=7;ino=13", "meta:size=100;dev=7;ino=13", "tv:s1:e3", "meta:size=100;dev=9;ino=14", 100)
	seedPlacementWithFingerprints(t, fixture.db, anime, 1, "plc-meta-remount", "/library/Show/EP 04.mkv",
		"meta:size=100;dev=7;ino=15", "meta:size=100;dev=7;ino=15", "tv:s1:e4", "meta:size=100;dev=9;ino=15", 100)
	seedPlacementWithFingerprints(t, fixture.db, anime, 1, "plc-meta-unrecorded", "/library/Show/EP 05.mkv",
		"legacy-digest", "legacy-digest", "tv:s1:e5", "meta:size=55;dev=7;ino=16", 55)

	calibrate := fixture.calibration.CalibrateHandler()
	outcome := fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	if outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("calibration must succeed: %+v", outcome)
	}
	for path, want := range map[string]string{
		"/library/Show/EP 01.mkv": DispositionPresent,             // recorded size matches
		"/library/Show/EP 02.mkv": DispositionFingerprintConflict, // size changed
		"/library/Show/EP 03.mkv": DispositionFingerprintConflict, // same size, other inode
		"/library/Show/EP 04.mkv": DispositionPresent,             // remount: device only
		"/library/Show/EP 05.mkv": DispositionPresent,             // no recorded size
	} {
		var got string
		if err := fixture.db.QueryRow(`
SELECT disposition FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = 1 AND file_path = $2`, anime, path).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s disposition = %q, want %q", path, got, want)
		}
	}
	var minted int
	if err := fixture.db.QueryRow(`
SELECT count(*) FROM placements
WHERE managed_anime_id = $1 AND source_owner_ref LIKE $1 || ':1:%'`, anime).Scan(&minted); err != nil {
		t.Fatal(err)
	}
	if minted != 0 {
		t.Fatalf("calibration must observe placed files, not mint %d new placements", minted)
	}
}
