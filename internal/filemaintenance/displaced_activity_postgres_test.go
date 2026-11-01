package filemaintenance

import (
	"context"
	"database/sql"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
)

// seedDisplacedRelease inserts the Release identity a displaced entry names:
// its source (the subgroup) and its torrent title.
func seedDisplacedRelease(t *testing.T, db *sql.DB, releaseID, subID, group, title string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', $3, $3, $4, 'Show')`,
		"src-"+releaseID, subID, "https://feeds.example/rss/"+releaseID, group); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $3, jsonb_build_object('title', $4::text), '{}', 'discovered', 'new')`,
		releaseID, "src-"+releaseID, "key-"+releaseID, title); err != nil {
		t.Fatal(err)
	}
}

// seedDisplacedPlacement records the managed file that will lose its place.
func seedDisplacedPlacement(t *testing.T, db *sql.DB, id, animeID, path, releaseID string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ($1, $2, $3, 'reconciliation', $4, $5, $5, $6, 'facts-v1', 'verified', 'active', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		id, path, animeID, "obs-"+id, "fp-"+id, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, 'tv:e1', 0, 'facts-v1', $2)`, id, "obs-"+id); err != nil {
		t.Fatal(err)
	}
}

type displacedLine struct {
	managedAnimeID, subject, message, reason, cause, fileName, byFile, byGroup, byTitle, dedupe string
}

func displacedLines(t *testing.T, db *sql.DB) []displacedLine {
	t.Helper()
	rows, err := db.Query(`
SELECT managed_anime_id, COALESCE(subject_id,''), message_code, COALESCE(reason_code,''),
       COALESCE(details->>'cause',''), COALESCE(details->>'file_name',''),
       COALESCE(details->>'replaced_by_file',''), COALESCE(details->>'replaced_by_group',''),
       COALESCE(details->>'replaced_by_title',''), dedupe_key
FROM subscription_log_entries WHERE message_code = 'placement_displaced' ORDER BY occurred_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []displacedLine
	for rows.Next() {
		var line displacedLine
		if err := rows.Scan(&line.managedAnimeID, &line.subject, &line.message, &line.reason, &line.cause,
			&line.fileName, &line.byFile, &line.byGroup, &line.byTitle, &line.dedupe); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOverwriteRecordsTheDisplacedFile: a managed file another instance's file
// takes over gets one entry on its own instance's stream, naming the file that
// took the place and where it came from. A replay writes nothing new.
func TestOverwriteRecordsTheDisplacedFile(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedAnimeSubscription(t, db, "anime-b", "sub-b", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-b", "GroupOld", "[GroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupNew", "[GroupNew] Show - 01 [1080p]")
	seedDisplacedPlacement(t, db, "plc-old", "anime-b", "/library/Shared/EP01.mkv", "release-old")

	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(writer)
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	got := displacedLines(t, db)
	if len(got) != 1 {
		t.Fatalf("displaced lines = %+v, want one", got)
	}
	want := displacedLine{
		managedAnimeID: "anime-b", subject: "plc-old", message: "placement_displaced", reason: "placement_displaced",
		cause: "overwrite", fileName: "EP01.mkv", byFile: "EP01.mkv", byGroup: "GroupNew",
		byTitle: "[GroupNew] Show - 01 [1080p]", dedupe: "placement_displaced:plc-old",
	}
	if got[0] != want {
		t.Fatalf("displaced line = %+v, want %+v", got[0], want)
	}

	// The successor's own instance is not the one that lost a file.
	var onSuccessor int
	if err := db.QueryRow(`SELECT count(*) FROM subscription_log_entries WHERE message_code = 'placement_displaced' AND managed_anime_id = 'anime-a'`).Scan(&onSuccessor); err != nil {
		t.Fatal(err)
	}
	if onSuccessor != 0 {
		t.Fatalf("the displacing instance recorded %d displaced lines", onSuccessor)
	}

	// A replay is the same admission: nothing new is displaced, nothing new is written.
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}
	if again := displacedLines(t, db); len(again) != 1 {
		t.Fatalf("a replay wrote more lines: %+v", again)
	}
}

// TestRedownloadRecordsTheDisplacedFile: a requested redownload replaces the
// old version wherever the new file lands, and the entry says so.
func TestRedownloadRecordsTheDisplacedFile(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-a", "GroupA", "[GroupA] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupA", "[GroupA] Show - 01 [1080p]")
	seedDisplacedPlacement(t, db, "plc-old", "anime-a", "/library/A/EP01 [old].mkv", "release-old")
	if _, err := db.Exec(`UPDATE placements SET redownload_requested_at = CURRENT_TIMESTAMP, redownload_request_key = 'redownload-key-1' WHERE id = 'plc-old'`); err != nil {
		t.Fatal(err)
	}

	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/A/EP01 [new].mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(writer)
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	got := displacedLines(t, db)
	if len(got) != 1 || got[0].cause != "redownload" || got[0].subject != "plc-old" {
		t.Fatalf("redownload displaced lines = %+v, want one redownload line for plc-old", got)
	}
	if got[0].byFile != "EP01 [new].mkv" {
		t.Fatalf("displaced line names %q as the taker, want the new file", got[0].byFile)
	}
}

// TestReplacementLineNamesTheReplacedVersion: a settled replacement names the
// version it replaced with the subgroup and title it came from, next to the
// file name older entries carry.
func TestReplacementLineNamesTheReplacedVersion(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, _ := lifecycleService(t, db)
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict != "replace" {
		t.Fatalf("evaluate = %+v, %v; want one admitted replace decision", views, err)
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}

	var group, title, fileName string
	if err := db.QueryRow(`
SELECT COALESCE(details->'replaced_targets'->0->>'group_name',''),
       COALESCE(details->'replaced_targets'->0->>'release_title',''),
       COALESCE(details->'replaced_targets'->0->>'file_name','')
FROM subscription_log_entries WHERE message_code = 'replacement_replaced'`).Scan(&group, &title, &fileName); err != nil {
		t.Fatal(err)
	}
	if group != "GroupA" || title != "[GroupA] Show - 01 [720p]" || fileName != "ep.mkv" {
		t.Fatalf("replaced target = %q/%q/%q, want GroupA and the old version's title and file", group, title, fileName)
	}
}
