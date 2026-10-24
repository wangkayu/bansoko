package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// seedRedownloadPlacement records one active placement of the RSS test anime
// produced by releaseID (empty: no RSS Release) and, when disposition is not
// empty, the latest calibrated scan's observation of it.
func seedRedownloadPlacement(t *testing.T, id, releaseID, disposition string) {
	t.Helper()
	owner := ownerTestDB(t)
	if _, err := owner.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, quality_facts_ref, facts_version)
VALUES ($1, '/library/Test Show/'||$1||'.mkv', 'anime-rss-test', 'reconciliation', $1, 'fp', 'fp', NULLIF($2, ''), 'facts-v1')`, id, releaseID); err != nil {
		t.Fatal(err)
	}
	if disposition == "" {
		return
	}
	if _, err := owner.Exec(`
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, facts, observed_at, disposition)
VALUES ('anime-rss-test', 1, '/library/Test Show/'||$1||'.mkv', 'fp', 1, jsonb_build_object('placement_id', $1::text), now(), $2)`, id, disposition); err != nil {
		t.Fatal(err)
	}
}

func redownloadMarker(t *testing.T, db *sql.DB, id string) (requested bool, key string) {
	t.Helper()
	var stored sql.NullString
	if err := db.QueryRow(`SELECT redownload_requested_at IS NOT NULL, redownload_request_key FROM placements WHERE id = $1`, id).Scan(&requested, &stored); err != nil {
		t.Fatal(err)
	}
	return requested, stored.String
}

func assertPlacementExit(t *testing.T, err error, reason string) {
	t.Helper()
	var exit *PlacementExitError
	if !errors.As(err, &exit) || exit.Reason != reason {
		t.Fatalf("redownload error = %v, want placement exit %s", err, reason)
	}
}

// TestRedownloadPlacementRunsItsReleaseAgain: a missing placement's Release
// is accepted again as a new attempt and the request stays on the placement;
// a request that does not start a download is withdrawn; a present placement,
// one without an RSS Release and an unknown one are refused.
func TestRedownloadPlacementRunsItsReleaseAgain(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-redownload")
	ctx := context.Background()
	evaluateAutomatically(t, db, store, service, "release-redownload")
	seedRedownloadPlacement(t, "plc-missing", "release-redownload", "missing")
	seedRedownloadPlacement(t, "plc-present", "release-redownload", "present")
	seedRedownloadPlacement(t, "plc-adopted", "", "fingerprint_conflict")

	_, err := service.RedownloadPlacement(ctx, "plc-present", "redownload-present")
	assertPlacementExit(t, err, PlacementExitFileNotMissing)
	_, err = service.RedownloadPlacement(ctx, "plc-adopted", "redownload-adopted")
	assertPlacementExit(t, err, PlacementExitNoSource)
	if _, err := service.RedownloadPlacement(ctx, "plc-unknown", "redownload-unknown"); err == nil {
		t.Fatal("an unknown placement must be refused")
	}

	// The first download is still running: nothing starts and the request
	// is withdrawn.
	_, err = service.RedownloadPlacement(ctx, "plc-missing", "redownload-busy")
	assertPlacementExit(t, err, RetryReasonDownloadInProgress)
	if requested, _ := redownloadMarker(t, db, "plc-missing"); requested {
		t.Fatal("a refused redownload must withdraw its request")
	}

	failCurrentDownload(t, "release-redownload")
	result, err := service.RedownloadPlacement(ctx, "plc-missing", "redownload-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" {
		t.Fatalf("redownload result = %+v, want a new accepted download", result)
	}
	if requested, key := redownloadMarker(t, db, "plc-missing"); !requested || key != "redownload-key-1" {
		t.Fatalf("accepted redownload marker = %v/%s", requested, key)
	}
	if count := releaseTaskCount(t, db, "release-redownload"); count != 2 {
		t.Fatalf("download tasks = %d, want the first and the redownload", count)
	}
	replay, err := service.RedownloadPlacement(ctx, "plc-missing", "redownload-key-1")
	if err != nil || replay != result {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, result)
	}
	if count := releaseTaskCount(t, db, "release-redownload"); count != 2 {
		t.Fatalf("replay created a task: %d", count)
	}
}
