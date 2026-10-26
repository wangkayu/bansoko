package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// seedDisplaceAnime creates managed anime rows for the displacement tests.
func seedDisplaceAnime(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ($1, $1)`, id); err != nil {
			t.Fatal(err)
		}
	}
}

// seedDisplacePlacement admits one reconciliation placement with coverage and
// one companion beside it.
func seedDisplacePlacement(t *testing.T, service *PlacementService, db *sql.DB, animeID, ref, path, key string) string {
	t.Helper()
	id, err := service.admit(context.Background(), placementInput{
		SourceEvidenceType: "reconciliation", SourceOwnerRef: ref,
		PhysicalPath: path, SourceFingerprint: "fp-" + ref, TargetFingerprint: "fp-" + ref,
		FactsVersion: "facts-v1", Coverage: []string{key}, ManagedAnimeID: animeID, SnapshotGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ($1, $2, $3, $4, 'adopted', 'subtitle', 'chs', 'ass', $5, 1, repeat('c', 64))`,
		"cmp-"+ref, id, animeID, "src-"+ref, CompanionPath(path, "chs", "ass")); err != nil {
		t.Fatal(err)
	}
	return id
}

func admitFileOperation(t *testing.T, db *sql.DB, opID, animeID, path, key string) string {
	t.Helper()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: opID, ManagedAnimeID: animeID, ArtifactVerified: true, TargetPath: path,
		SourceFingerprint: "fp-" + opID, TargetFingerprint: "fp-" + opID,
	}}
	id, err := NewPlacementService(db, reader, nil).AdmitFromFileOperation(context.Background(), opID, []string{key}, "facts-v1")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type displacedRow struct {
	state, cause, successor, successorAnime, ref, companionState, companionReason string
}

func readDisplaced(t *testing.T, db *sql.DB, id string) displacedRow {
	t.Helper()
	var row displacedRow
	if err := db.QueryRow(`
SELECT p.state, COALESCE(p.displaced_cause,''), COALESCE(p.displaced_by_placement_id,''), COALESCE(p.displaced_by_anime_id,''),
       COALESCE(p.displaced_by_ref,''), c.state, COALESCE(c.ended_reason,'')
FROM placements p JOIN placement_companions c ON c.placement_id = p.id WHERE p.id = $1`, id).Scan(
		&row.state, &row.cause, &row.successor, &row.successorAnime, &row.ref, &row.companionState, &row.companionReason); err != nil {
		t.Fatal(err)
	}
	return row
}

// TestFileOperationOverwriteDisplacesManagedPlacement: a file operation's
// placement that activates at a path another placement holds (another
// instance's, or the same instance's other episode) displaces it in the same
// commit: successor and cause recorded, companions end with their files left
// alone, the episode is no longer covered, and a replay changes nothing.
func TestFileOperationOverwriteDisplacesManagedPlacement(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedDisplaceAnime(t, db, "anime-a", "anime-b")
	service := NewPlacementService(db, nil, nil)
	foreign := seedDisplacePlacement(t, service, db, "anime-b", "obs-b", "/library/Shared/EP03.mkv", "tv:s1:e3")
	sibling := seedDisplacePlacement(t, service, db, "anime-a", "obs-a", "/library/A/EP02.mkv", "tv:s1:e2")
	bystander := seedDisplacePlacement(t, service, db, "anime-a", "obs-a2", "/library/A/EP04.mkv", "tv:s1:e4")

	successor := admitFileOperation(t, db, "op-cross", "anime-a", "/library/Shared/EP03.mkv", "tv:s1:e1")
	got := readDisplaced(t, db, foreign)
	if got != (displacedRow{"displaced", "overwrite", successor, "anime-a", "file_operation:op-cross", "ended", "displaced"}) {
		t.Fatalf("another instance's placement = %+v, want displaced by %s", got, successor)
	}
	sameAnime := admitFileOperation(t, db, "op-same", "anime-a", "/library/A/EP02.mkv", "tv:s1:e5")
	if got := readDisplaced(t, db, sibling); got.state != "displaced" || got.successor != sameAnime || got.cause != "overwrite" {
		t.Fatalf("same instance's other episode = %+v, want displaced by %s", got, sameAnime)
	}
	if got := readDisplaced(t, db, bystander); got.state != "active" || got.companionState != "active" {
		t.Fatalf("a placement at another path must stay: %+v", got)
	}
	if again := admitFileOperation(t, db, "op-cross", "anime-a", "/library/Shared/EP03.mkv", "tv:s1:e1"); again != successor {
		t.Fatalf("replay = %s, want %s", again, successor)
	}
	views, err := service.ListPlacements(ctx, "anime-b", 10, 0)
	if err != nil || len(views) != 0 {
		t.Fatalf("anime-b active placements = %+v err=%v, the displaced episode must lose coverage", views, err)
	}
	var covering int
	if err := db.QueryRow(`
SELECT count(*) FROM placement_coverage c JOIN placements p ON p.id = c.placement_id
WHERE p.managed_anime_id = 'anime-a' AND p.state = 'active' AND c.observation_key = 'tv:s1:e2'`).Scan(&covering); err != nil || covering != 0 {
		t.Fatalf("episode 2 still covered by %d active placements (err=%v)", covering, err)
	}
}

// TestDisplacedIsTerminal: a displaced row never changes again, and it is
// never admitted for deletion.
func TestDisplacedIsTerminal(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	seedDisplaceAnime(t, db, "anime-a")
	service := NewPlacementService(db, nil, nil)
	old := seedDisplacePlacement(t, service, db, "anime-a", "obs-old", "/library/A/EP01.mkv", "tv:s1:e1")
	admitFileOperation(t, db, "op-new", "anime-a", "/library/A/EP01.mkv", "tv:s1:e1")
	for _, stmt := range []string{
		`UPDATE placements SET state = 'active', displaced_at = NULL, displaced_cause = NULL, displaced_by_placement_id = NULL, displaced_by_anime_id = NULL, displaced_by_ref = NULL WHERE id = $1`,
		`UPDATE placements SET physical_path = '/library/A/moved.mkv' WHERE id = $1`,
	} {
		if _, err := db.Exec(stmt, old); err == nil {
			t.Fatalf("displaced row accepted %q", stmt)
		}
	}
	// Deletion admission accepts only superseded losers, so a displaced row
	// is never deleted.
	var state string
	if err := db.QueryRow(`SELECT state FROM placements WHERE id = $1`, old).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "displaced" {
		t.Fatalf("placement state = %s, want displaced", state)
	}
}

// TestRedownloadRequestDisplacesWhereverTheNewFileLands: a placement with a
// pending redownload request is displaced (cause redownload) by the next
// placement of its instance covering one of its episodes, at any path; a
// placement of another instance never displaces it that way.
func TestRedownloadRequestDisplacesWhereverTheNewFileLands(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	seedDisplaceAnime(t, db, "anime-a", "anime-b")
	service := NewPlacementService(db, nil, nil)
	old := seedDisplacePlacement(t, service, db, "anime-a", "obs-old", "/library/A/EP07 [old].mkv", "tv:s1:e7")
	if _, err := db.Exec(`UPDATE placements SET redownload_requested_at = now(), redownload_request_key = 'redownload-key-1' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	admitFileOperation(t, db, "op-other-anime", "anime-b", "/library/B/EP07.mkv", "tv:s1:e7")
	if got := readDisplaced(t, db, old); got.state != "active" {
		t.Fatalf("another instance's file must not take the redownload: %+v", got)
	}
	successor := admitFileOperation(t, db, "op-redownload", "anime-a", "/library/A/EP07 [new].mkv", "tv:s1:e7")
	if got := readDisplaced(t, db, old); got.state != "displaced" || got.cause != "redownload" || got.successor != successor || got.companionReason != "displaced" {
		t.Fatalf("redownloaded placement = %+v, want displaced (redownload) by %s", got, successor)
	}
}

// TestPlacementExitRelease: only an active placement whose latest calibrated
// scan found it missing or mismatched is listed and may be released; a
// release ends its companions, keeps the episode uncovered, and replays.
func TestPlacementExitRelease(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedDisplaceAnime(t, db, "anime-a")
	service := NewPlacementService(db, nil, nil)
	missing := seedDisplacePlacement(t, service, db, "anime-a", "obs-missing", "/library/A/EP01.mkv", "tv:s1:e1")
	present := seedDisplacePlacement(t, service, db, "anime-a", "obs-present", "/library/A/EP02.mkv", "tv:s1:e2")
	observe := func(generation int, id, path, disposition string) {
		if _, err := db.Exec(`
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, facts, observed_at, disposition)
VALUES ('anime-a', $1, $2, 'fp', 1, jsonb_build_object('placement_id', $3::text), now(), $4)`, generation, path, id, disposition); err != nil {
			t.Fatal(err)
		}
	}
	observe(1, missing, "/library/A/EP01.mkv", "missing")
	observe(1, present, "/library/A/EP02.mkv", "present")
	issues, err := service.ListPlacementIssues(ctx, "anime-a")
	if err != nil || len(issues) != 1 || issues[0].PlacementID != missing || issues[0].ReasonCode != "missing" {
		t.Fatalf("issues = %+v err=%v, want the missing placement only", issues, err)
	}
	var exit *PlacementExitError
	if _, err := service.ReleasePlacement(ctx, present, "release-key-present"); !errors.As(err, &exit) || exit.Reason != "file_not_missing" {
		t.Fatalf("releasing a present placement = %v, want file_not_missing", err)
	}
	if _, err := service.ReleasePlacement(ctx, missing, "short"); err == nil {
		t.Fatal("a short idempotency key must be rejected")
	}
	result, err := service.ReleasePlacement(ctx, missing, "release-key-1")
	if err != nil || result.Released != 1 || result.ManagedAnimeID != "anime-a" {
		t.Fatalf("release = %+v err=%v", result, err)
	}
	replay, err := service.ReleasePlacement(ctx, missing, "release-key-1")
	if err != nil || replay != result {
		t.Fatalf("replay = %+v err=%v, want %+v", replay, err, result)
	}
	if _, err := service.ReleasePlacement(ctx, missing, "release-key-2"); !errors.As(err, &exit) || exit.Reason != "placement_not_active" {
		t.Fatalf("a second release with another key = %v, want placement_not_active", err)
	}
	var state, companionState, companionReason string
	if err := db.QueryRow(`SELECT p.state, c.state, COALESCE(c.ended_reason,'') FROM placements p JOIN placement_companions c ON c.placement_id = p.id WHERE p.id = $1`, missing).Scan(&state, &companionState, &companionReason); err != nil {
		t.Fatal(err)
	}
	if state != "released" || companionState != "ended" || companionReason != "released" {
		t.Fatalf("released placement=%s companion=%s/%s", state, companionState, companionReason)
	}
	// The file came back at the next scan: the issue is gone.
	observe(2, present, "/library/A/EP02.mkv", "present")
	if issues, err := service.ListPlacementIssues(ctx, "anime-a"); err != nil || len(issues) != 0 {
		t.Fatalf("issues after release = %+v err=%v", issues, err)
	}
}

// TestPlacementIssueClearsWhenTheFileReturns: a later calibrated scan that
// finds the file again removes the issue without any state change.
func TestPlacementIssueClearsWhenTheFileReturns(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedDisplaceAnime(t, db, "anime-a")
	service := NewPlacementService(db, nil, nil)
	id := seedDisplacePlacement(t, service, db, "anime-a", "obs-1", "/library/A/EP01.mkv", "tv:s1:e1")
	for generation, disposition := range map[int]string{1: "fingerprint_conflict", 2: "present"} {
		if _, err := db.Exec(`
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, facts, observed_at, disposition)
VALUES ('anime-a', $1, '/library/A/EP01.mkv', 'fp', 1, jsonb_build_object('placement_id', $2::text), now(), $3)`, generation, id, disposition); err != nil {
			t.Fatal(err)
		}
	}
	if issues, err := service.ListPlacementIssues(ctx, "anime-a"); err != nil || len(issues) != 0 {
		t.Fatalf("issues = %+v err=%v, the latest scan found the file", issues, err)
	}
}

// TestRedownloadOverFingerprintConflictPerPolicy: a redownload of a placement
// whose path holds other content ends by the global collision setting.
// overwrite replaces the unknown file at the same path and version places the
// new file beside it: either way the old placement is displaced (cause
// redownload) and the episode is covered by the new one. skip places nothing:
// the old placement stays active with its request pending and can still be
// released.
func TestRedownloadOverFingerprintConflictPerPolicy(t *testing.T) {
	for _, c := range []struct{ policy, newPath string }{
		{"overwrite", "/library/A/EP01.mkv"},
		{"version", "/library/A/EP01 (2).mkv"},
		{"skip", ""},
	} {
		t.Run(c.policy, func(t *testing.T) {
			db := openPlacementTestDB(t)
			defer db.Close()
			ctx := context.Background()
			seedDisplaceAnime(t, db, "anime-a")
			service := NewPlacementService(db, nil, nil)
			old := seedDisplacePlacement(t, service, db, "anime-a", "obs-old", "/library/A/EP01.mkv", "tv:s1:e1")
			if _, err := db.Exec(`
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, facts, observed_at, disposition)
VALUES ('anime-a', 1, '/library/A/EP01.mkv', 'other', 1, jsonb_build_object('placement_id', $1::text), now(), 'fingerprint_conflict')`, old); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE placements SET redownload_requested_at = now(), redownload_request_key = 'redownload-key-1' WHERE id = $1`, old); err != nil {
				t.Fatal(err)
			}
			if c.newPath == "" {
				issues, err := service.ListPlacementIssues(ctx, "anime-a")
				if err != nil || len(issues) != 1 || !issues[0].RedownloadRequested || issues[0].ReasonCode != "fingerprint_conflict" {
					t.Fatalf("issues = %+v err=%v, want the pending conflict", issues, err)
				}
				if got := readDisplaced(t, db, old); got.state != "active" {
					t.Fatalf("skip must leave the placement active: %+v", got)
				}
				if _, err := service.ReleasePlacement(ctx, old, "release-key-1"); err != nil {
					t.Fatalf("a pending redownload must not block release: %v", err)
				}
				return
			}
			successor := admitFileOperation(t, db, "op-redownload", "anime-a", c.newPath, "tv:s1:e1")
			if got := readDisplaced(t, db, old); got.state != "displaced" || got.cause != "redownload" || got.successor != successor {
				t.Fatalf("old placement = %+v, want displaced (redownload) by %s", got, successor)
			}
			var covering int
			if err := db.QueryRow(`
SELECT count(*) FROM placement_coverage c JOIN placements p ON p.id = c.placement_id
WHERE p.managed_anime_id = 'anime-a' AND p.state = 'active' AND c.observation_key = 'tv:s1:e1'`).Scan(&covering); err != nil || covering != 1 {
				t.Fatalf("episode 1 covered by %d active placements (err=%v), want the new one", covering, err)
			}
		})
	}
}
