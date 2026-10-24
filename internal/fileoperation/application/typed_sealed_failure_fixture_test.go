package application

import (
	"path/filepath"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
)

func assertSealedNamingFailureCannotReprepare(t *testing.T) {
	t.Helper()
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = map[string]any{"tv": map[string]any{"directory_template": "../escape", "video_filename_template": "${file.name}"}, "movie": map[string]any{"directory_template": "../escape", "video_filename_template": "${file.name}"}, "subtitle_suffix_template": ""}
	})
	s := newFixtureService(t, db, f)
	prepareFixture(t, db, s, f.artifactID)
	var reason string
	if err := db.QueryRow(`SELECT delivery_reason_code FROM acquisition_files WHERE acquisition_id=(SELECT acquisition_id FROM artifacts WHERE id=$1) AND file_ref='primary'`, f.artifactID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("invalid naming lacks failed result")
	}
	before := planGraphSnapshot(t, db)
	setFrozenSectionValue(t, db, "download-file-test", animedomain.SectionNamingRules, naming.DefaultNamingRules())
	after := planGraphSnapshot(t, db)
	if before == after {
		t.Fatal("current policy drift not arranged")
	}
	prepareFixture(t, db, s, f.artifactID)
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
	if planGraphSnapshot(t, db) != after {
		t.Fatal("sealed failure was replanned")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, f.artifactID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed declaration fabricated placeholder Operation")
	}
}

// assertAcceptedUnavailableRootDoesNotReachThePlan freezes the delivery route
// root as unavailable — the state production always writes now, because every
// naming template carries its own absolute deployment path. The plan must come
// from that template alone: no failed declaration, one operation whose target
// is absolute, and the same graph on re-entry. A frozen unavailable root is
// never repaired into a library anchor, and never substituted for the target.
func assertAcceptedUnavailableRootDoesNotReachThePlan(t *testing.T) {
	t.Helper()
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["fixture_route_values"] = map[string]any{"root": map[string]any{"state": "unavailable", "value": nil, "reason_code": "anime_root_unavailable"}}
	})
	s := newFixtureService(t, db, f)
	prepareFixture(t, db, s, f.artifactID)
	if failed := failedDeliveryResults(t, db, f.artifactID); failed != 0 {
		t.Fatalf("an unavailable route root failed %d file declarations", failed)
	}
	var count int
	var target string
	if err := db.QueryRow(`SELECT count(*), COALESCE(min(target_path), '') FROM file_operations WHERE artifact_id=$1`, f.artifactID).Scan(&count, &target); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("operations = %d, want the one the template rendered", count)
	}
	if !filepath.IsAbs(target) {
		t.Fatalf("plan target %q is not an absolute template render", target)
	}
	before := planGraphSnapshot(t, db)
	prepareFixture(t, db, s, f.artifactID)
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
	if planGraphSnapshot(t, db) != before {
		t.Fatal("re-entry replanned an unavailable route root")
	}
}
