package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/filemaintenance"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
)

// TestManagedOverwriteRealChain proves the overwrite setting against a managed
// file end to end: a delivered video that aligns
// onto another instance's placement replaces its file, that placement becomes
// displaced with the moved placement as its successor, its subtitle relation
// ends and the subtitle file stays on disk.
func TestManagedOverwriteRealChain(t *testing.T) {
	ctx := context.Background()
	svc, animeID, placementID, videoPath, subtitle, _ := startCompanionChain(t, ctx, generated.CollisionPolicyOverwrite)
	enableAlignmentPolicy(t, ctx, svc)
	videoBytes, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	staleVideoPath := filepath.Join(filepath.Dir(filepath.Dir(videoPath)), "Comp Real Show - legacy 01.mkv")
	staleVideo(t, ctx, svc, animeID, subtitle, videoPath, staleVideoPath)

	// Another instance's placement now holds the video's target, with a
	// subtitle of its own at a path the moving unit does not touch.
	occupant := []byte("another instance's managed episode")
	if err := os.WriteFile(canonical, occupant, 0o644); err != nil {
		t.Fatal(err)
	}
	otherSubtitle := filemaintenance.CompanionPath(canonical, "jpn", "ass")
	if err := os.WriteFile(otherSubtitle, []byte("other subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := disposabledb.Owner(t)
	if _, err := owner.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-occupant', 'Occupant Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version)
VALUES ('plc-occupant', $1, 'anime-occupant', 'reconciliation', 'obs-occupant', 'fp', 'fp', 'facts-v1')`, canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('plc-occupant', 'tv:e1', 0, 'facts-v1', 'obs-occupant')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ('cmp-occupant', 'plc-occupant', 'anime-occupant', 'src-occupant', 'adopted', 'subtitle', 'jpn', 'ass', $1, 14, repeat('c', 64))`, otherSubtitle); err != nil {
		t.Fatal(err)
	}

	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)

	var videoNow string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil {
		t.Fatal(err)
	}
	if videoNow != canonical {
		t.Fatalf("the video must replace the managed occupant: %q want %q", videoNow, canonical)
	}
	assertBytes(t, canonical, videoBytes)
	var state, cause, successor, successorAnime, companionState, companionReason string
	if err := svc.db.QueryRowContext(ctx, `
SELECT p.state, COALESCE(p.displaced_cause,''), COALESCE(p.displaced_by_placement_id,''), COALESCE(p.displaced_by_anime_id,''), c.state, COALESCE(c.ended_reason,'')
FROM placements p JOIN placement_companions c ON c.placement_id = p.id WHERE p.id = 'plc-occupant'`).Scan(&state, &cause, &successor, &successorAnime, &companionState, &companionReason); err != nil {
		t.Fatal(err)
	}
	if state != "displaced" || cause != "overwrite" || successor != placementID || successorAnime != animeID {
		t.Fatalf("occupant = %s cause=%s successor=%s/%s, want displaced by %s/%s", state, cause, successor, successorAnime, placementID, animeID)
	}
	if companionState != "ended" || companionReason != "displaced" {
		t.Fatalf("occupant subtitle relation = %s/%s, want ended displaced", companionState, companionReason)
	}
	assertBytes(t, otherSubtitle, []byte("other subtitle"))
	var covering int
	if err := svc.db.QueryRowContext(ctx, `
SELECT count(*) FROM placements WHERE managed_anime_id = 'anime-occupant' AND state = 'active'`).Scan(&covering); err != nil || covering != 0 {
		t.Fatalf("the occupant instance still has %d active placements (err=%v)", covering, err)
	}
	if got := loadCompanion(t, ctx, svc, placementID); got.CurrentPath != filemaintenance.CompanionPath(canonical, subtitle.Suffix, subtitle.OriginalExtension) {
		t.Fatalf("the moved video's subtitle must follow it: %+v", got)
	}
}

// TestVersionedCompanionRederivesRealChain proves the collision-overwrite
// leftovers: a subtitle that took a version number alone re-derives its name
// from the video's basename plus its original suffix when the video moves,
// and takes a version number only when that target collides; a unit
// versioned together moves back to the base names together once the
// collision is gone.
func TestVersionedCompanionRederivesRealChain(t *testing.T) {
	ctx := context.Background()
	occupant := []byte("an unmanaged file the user put here")
	svc, animeID, placementID, videoPath, subtitle, _ := startCompanionChain(t, ctx, generated.CollisionPolicyVersioned)
	enableAlignmentPolicy(t, ctx, svc)
	canonical, err := filepath.EvalSymlinks(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	baseSuffix := subtitle.Suffix
	aligned := subtitle.CurrentPath

	// 1. The subtitle alone takes a version number inside its suffix.
	legacy := filepath.Join(filepath.Dir(videoPath), "Comp Real Show - legacy.sc.ass")
	setCompanionPath(t, ctx, subtitle, aligned, legacy)
	if err := os.WriteFile(aligned, occupant, 0o644); err != nil {
		t.Fatal(err)
	}
	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)
	versioned := loadCompanion(t, ctx, svc, placementID)
	if versioned.Suffix != baseSuffix+" (2)" {
		t.Fatalf("the subtitle must take its own version number: %+v", versioned)
	}

	// 2. The video moves while the base subtitle target is still taken: the
	// unit takes one version number and the subtitle's name re-derives from
	// the video's new basename plus its original suffix.
	staleVideoPath := filepath.Join(filepath.Dir(filepath.Dir(videoPath)), "Comp Real Show - legacy 01.mkv")
	staleVideo(t, ctx, svc, animeID, versioned, videoPath, staleVideoPath)
	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)
	var videoNow string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil {
		t.Fatal(err)
	}
	if want := alignmentVersioned(canonical); videoNow != want {
		t.Fatalf("the unit must take one version number: video %q want %q", videoNow, want)
	}
	followed := loadCompanion(t, ctx, svc, placementID)
	if followed.Suffix != baseSuffix || followed.CurrentPath != filemaintenance.CompanionPath(videoNow, baseSuffix, subtitle.OriginalExtension) {
		t.Fatalf("the subtitle must re-derive from the video's basename and its original suffix: %+v video=%q", followed, videoNow)
	}
	assertBytes(t, aligned, occupant)

	// 3. The collision is gone: the unit moves back to the base names
	// together; the version number never shifts onto the subtitle.
	if err := os.Remove(aligned); err != nil {
		t.Fatal(err)
	}
	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil {
		t.Fatal(err)
	}
	final := loadCompanion(t, ctx, svc, placementID)
	if videoNow != canonical || final.Suffix != baseSuffix || final.CurrentPath != filemaintenance.CompanionPath(canonical, baseSuffix, subtitle.OriginalExtension) {
		t.Fatalf("the unit must return to its base names together: video=%q subtitle=%+v want video %q", videoNow, final, canonical)
	}
}

func alignmentVersioned(path string) string {
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + " (2)" + extension
}
