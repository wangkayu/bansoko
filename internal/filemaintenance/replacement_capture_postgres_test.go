package filemaintenance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	target "bansoko/internal/acquisition/target"
	terminal "bansoko/internal/acquisition/terminal/application"
	"bansoko/internal/testutil/disposabledb"
)

func TestReplacementCaptureRequiresNewApprovedComparison(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	placement := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"}, Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")
	path := filepath.Join(t.TempDir(), "incumbent")
	data := []byte("approved incumbent")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// The placement records its FileOperation's fingerprint document; its
	// size_bytes is what path-level capture compares against.
	document := fmt.Sprintf(`{"algorithm":"sha256","value":"%x","size_bytes":%d}`, sha256.Sum256(data), len(data))
	if _, err := disposabledb.Owner(t).Exec(`UPDATE placements SET physical_path=$2,target_fingerprint=$3 WHERE id=$1`, placement, path, document); err != nil {
		t.Fatal(err)
	}
	intent, err := releaseIntent(NewReplacementDecisionService(db).CandidateIntents(ctx, "anime-1", "release-new", resolvedTVEvidence(t, "[G] Show - 01 [1080p]", "tv:s1:e1", "1")))
	if err != nil || intent != target.IntentReplace {
		t.Fatalf("decision %s %v", intent, err)
	}
	captured, err := terminal.CaptureReplacement(ctx, db, "release-new", path)
	if err != nil || captured == nil {
		t.Fatalf("production capture %v", err)
	}
	// Same path + same size is accepted weak identity: same-size content is
	// deliberately not proved.
	if err := os.WriteFile(path, []byte("Approved incumbent"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.CaptureReplacement(ctx, db, "release-new", path); err != nil {
		t.Fatalf("same-size incumbent rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("resized incumbent!!"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.CaptureReplacement(ctx, db, "release-new", path); err == nil {
		t.Fatal("incumbent with a different size accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.CaptureReplacement(ctx, db, "release-new", path); err == nil {
		t.Fatal("absent incumbent accepted")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := disposabledb.Owner(t).Exec(`UPDATE replacement_decisions SET pools=pools||pools WHERE candidate_release_id='release-new'`); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.CaptureReplacement(ctx, db, "release-new", path); err != nil {
		t.Fatalf("same placement repeated across coverage rejected: %v", err)
	}
	if _, err := disposabledb.Owner(t).Exec(`UPDATE replacement_decisions SET pools=(SELECT jsonb_agg(value-'comparisons') FROM jsonb_array_elements(pools)) WHERE candidate_release_id='release-new'`); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.CaptureReplacement(ctx, db, "release-new", path); err == nil {
		t.Fatal("legacy decision backfilled as approval")
	}
}
