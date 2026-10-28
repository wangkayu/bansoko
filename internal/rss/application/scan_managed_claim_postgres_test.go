package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/acquisition/target"
)

func seedClaimPlacement(t *testing.T, db *sql.DB, id, animeID, path, evidence, qualityFactsRef, state string) {
	t.Helper()
	releasedAt := "NULL"
	if state == "released" {
		releasedAt = "CURRENT_TIMESTAMP"
	}
	query := `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state,
 replacement_eligible, version, created_at, updated_at, released_at, released_by, release_request_key)
VALUES ($1, $2, $3, $4, $1, '{"size_bytes":4}', '{"size_bytes":4}', NULLIF($5, ''), 'facts-v1', 'verified', $6,
 true, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ` + releasedAt + `,
 CASE WHEN $6 = 'released' THEN 'test' END, CASE WHEN $6 = 'released' THEN 'release-' || $1 END)`
	if _, err := db.Exec(query, id, path, animeID, evidence, qualityFactsRef, state); err != nil {
		t.Fatal(err)
	}
}

func claimPlacementRef(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var ref sql.NullString
	if err := db.QueryRow(`SELECT quality_facts_ref FROM placements WHERE id = $1`, id).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	return ref.String
}

// A release that adopts its targets claims exactly the anime's unclaimed,
// active scan-managed files at those paths; the claim makes the release the
// file's quality facts. Other files at or off the targets are untouched.
func TestClaimScanManagedTargetsAttachesOnlyUnclaimedScanFiles(t *testing.T) {
	db, _, _ := newRetryFixture(t, false, true, "release-claim")
	defer db.Close()
	ctx := context.Background()
	owner := ownerTestDB(t)
	if _, err := owner.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-claim-other', 'Other')`); err != nil {
		t.Fatal(err)
	}
	seedClaimPlacement(t, owner, "plc-scan", "anime-rss-test", "/library/Show/a.mkv", "reconciliation", "", "active")
	seedClaimPlacement(t, owner, "plc-scan-off", "anime-rss-test", "/library/Show/c.mkv", "reconciliation", "", "active")
	seedClaimPlacement(t, owner, "plc-downloaded", "anime-rss-test", "/library/Show/b.mkv", "file_operation", "", "active")
	seedClaimPlacement(t, owner, "plc-foreign", "anime-claim-other", "/library/Show/a.mkv", "reconciliation", "", "active")

	claimable, err := scanManagedUnclaimedAt(ctx, db, "anime-rss-test", "/library/Show/a.mkv")
	if err != nil || !claimable {
		t.Fatalf("unclaimed scan-managed file must be claimable: %v %v", claimable, err)
	}
	if claimable, err := scanManagedUnclaimedAt(ctx, db, "anime-rss-test", "/library/Show/b.mkv"); err != nil || claimable {
		t.Fatalf("a downloaded file is never claimable: %v %v", claimable, err)
	}

	set := target.Set{ReleaseID: "release-claim", Targets: []target.Spec{
		{TargetPath: "/library/Show/a.mkv"}, {TargetPath: "/library/Show/b.mkv"},
	}}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := claimScanManagedTargetsTx(ctx, tx, "anime-rss-test", "release-claim", set)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if claimed != 1 {
		t.Fatalf("claimed = %d, want 1", claimed)
	}
	if ref := claimPlacementRef(t, owner, "plc-scan"); ref != "release-claim" {
		t.Fatalf("claimed file facts = %q", ref)
	}
	for _, id := range []string{"plc-scan-off", "plc-downloaded", "plc-foreign"} {
		if ref := claimPlacementRef(t, owner, id); ref != "" {
			t.Fatalf("%s must stay unclaimed: %q", id, ref)
		}
	}
	if claimable, err := scanManagedUnclaimedAt(ctx, db, "anime-rss-test", "/library/Show/a.mkv"); err != nil || claimable {
		t.Fatalf("a claimed file is no longer claimable: %v %v", claimable, err)
	}
	// A second claim by another release changes nothing.
	tx, err = owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := claimScanManagedTargetsTx(ctx, tx, "anime-rss-test", "release-other", set)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if again != 0 || claimPlacementRef(t, owner, "plc-scan") != "release-claim" {
		t.Fatalf("a claimed file keeps its release: again=%d", again)
	}
}
