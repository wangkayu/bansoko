package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/deployment"
	"bansoko/internal/filemaintenance"
	executorpkg "bansoko/internal/filemaintenance/executor"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/postprocessing/naming"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// startCompanionChain delivers "Comp Real Show - 01" with a bound .sc.ass
// subtitle through the real chain and returns the registered companion.
func startCompanionChain(t *testing.T, ctx context.Context, collision ...generated.CollisionPolicy) (*realChainServices, string, string, string, filemaintenance.Companion, *filemaintenance.PathDiffService) {
	t.Helper()
	items := map[string]rssItem{
		"A": {GUID: "comp-a-guid", Title: "[RealGroup] Comp Real Show - 01 [1080p][CHS]"},
	}
	files := []torrentFile{
		{Name: "Comp Real Show - 01.mkv", Content: []byte("companion video " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Comp Real Show - 01.sc.ass", Content: []byte("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,companion subtitle")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": files})
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	t.Cleanup(func() { _ = svc.db.Close() })
	seedReplacementSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Comp Real Show"
	draft.RssSources[0].Title = "Comp Real Show"
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	if len(collision) == 1 {
		var fileEffect generated.FileEffectRulesOverride
		if err := fileEffect.FromFileEffectRulesReplace(generated.FileEffectRulesReplace{
			Mode:  generated.FileEffectRulesReplaceModeReplace,
			Value: generated.FileEffectRules{Operation: generated.FileEffectRulesOperationCopy, CollisionPolicy: collision[0]},
		}); err != nil {
			t.Fatal(err)
		}
		draft.Rules.Sections.FileEffect = &fileEffect
	}
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.CurrentRevision == nil {
		t.Fatalf("candidate save failed: status=%d candidate=%+v", saveStatus, candidate)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "comp-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID := result.ManagedAnime.Id
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "comp-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "comp-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, svc.db, animeID, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	svc.driveToPlacement(t, ctx, animeID, "A", files, 2)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)

	var placementID, videoPath string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementID, &videoPath); err != nil {
		t.Fatal(err)
	}
	companions, err := svc.placements.ListCompanions(ctx, placementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(companions) != 1 {
		t.Fatalf("the bound subtitle must be the video's one companion, got %+v", companions)
	}
	subtitle := companions[0]
	if subtitle.OriginalExtension != "ass" || subtitle.CurrentPath != filemaintenance.CompanionPath(videoPath, subtitle.Suffix, "ass") {
		t.Fatalf("companion must sit beside the video under its basename: %+v video=%q", subtitle, videoPath)
	}
	if _, err := os.Stat(subtitle.CurrentPath); err != nil {
		t.Fatalf("companion file missing: %v", err)
	}
	pathDiff := filemaintenance.NewPathDiffService(svc.db, svc.namingRenderer, nil)
	return svc, animeID, placementID, videoPath, subtitle, pathDiff
}

// setCompanionPath moves a companion's file and records the new current path
// the way a pre-companion backfill registers a subtitle where it lies.
func setCompanionPath(t *testing.T, ctx context.Context, companion filemaintenance.Companion, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `UPDATE placement_companions SET current_path = $1 WHERE id = $2`, to, companion.ID); err != nil {
		t.Fatal(err)
	}
}

func companionDiff(t *testing.T, ctx context.Context, pathDiff *filemaintenance.PathDiffService, animeID string) (filemaintenance.PathDiffView, filemaintenance.CompanionPathDiffView) {
	t.Helper()
	views, err := pathDiff.ListPathDiffs(ctx, animeID, 0, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Companions) != 1 {
		t.Fatalf("expected one video row with one companion, got %+v", views)
	}
	return views[0], views[0].Companions[0]
}

func companionIssueCodes(t *testing.T, ctx context.Context, pathDiff *filemaintenance.PathDiffService, animeID string) []string {
	t.Helper()
	issues, err := pathDiff.CompanionIssues(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.ReasonCode)
	}
	return codes
}

// TestCompanionScanRealChain proves companion scan semantics: a scan observes the
// video's companion at its registered path; a companion away from the path
// derived from the video's expected path is reported not aligned while the
// video itself stays aligned; a missing companion is reported and never
// changes the video's disposition.
func TestCompanionScanRealChain(t *testing.T) {
	ctx := context.Background()
	svc, animeID, _, videoPath, subtitle, pathDiff := startCompanionChain(t, ctx)
	alignedSubtitle := subtitle.CurrentPath

	runScan(t, ctx, svc, animeID)
	video, companion := companionDiff(t, ctx, pathDiff, animeID)
	if video.Differs || companion.Differs || companion.State != "present" || companion.ExpectedPath == nil || *companion.ExpectedPath != alignedSubtitle {
		t.Fatalf("a delivered video and subtitle must both be aligned: video=%+v companion=%+v", video, companion)
	}
	if codes := companionIssueCodes(t, ctx, pathDiff, animeID); len(codes) != 0 {
		t.Fatalf("an aligned companion has no issue, got %v", codes)
	}

	// A subtitle registered under a legacy name: the video stays aligned and
	// the subtitle is reported not aligned toward the video's basename.
	legacy := filepath.Join(filepath.Dir(videoPath), "Comp Real Show - 01.sc.ass")
	setCompanionPath(t, ctx, subtitle, alignedSubtitle, legacy)
	runScan(t, ctx, svc, animeID)
	video, companion = companionDiff(t, ctx, pathDiff, animeID)
	if video.Differs || !companion.Differs || companion.FilePath != legacy || *companion.ExpectedPath != alignedSubtitle {
		t.Fatalf("a legacy-named subtitle must be not aligned while the video is aligned: video=%+v companion=%+v", video, companion)
	}
	if codes := companionIssueCodes(t, ctx, pathDiff, animeID); len(codes) != 1 || codes[0] != "companion_not_aligned" {
		t.Fatalf("expected companion_not_aligned, got %v", codes)
	}

	// A missing companion is reported; the video's disposition is unchanged.
	parked := legacy + ".parked"
	if err := os.Rename(legacy, parked); err != nil {
		t.Fatal(err)
	}
	runScan(t, ctx, svc, animeID)
	assertCalibrationDisposition(t, ctx, svc, animeID, "present", "present")
	if _, companion = companionDiff(t, ctx, pathDiff, animeID); companion.State != "missing" {
		t.Fatalf("a removed subtitle must be observed missing, got %+v", companion)
	}
	if codes := companionIssueCodes(t, ctx, pathDiff, animeID); len(codes) != 1 || codes[0] != "companion_missing" {
		t.Fatalf("expected companion_missing, got %v", codes)
	}
	if err := os.Rename(parked, legacy); err != nil {
		t.Fatal(err)
	}
}

// runAlignmentsToRest drives the worker until no alignment operation of the
// anime is still pending (a companion may wait for its video).
func runAlignmentsToRest(t *testing.T, ctx context.Context, svc *realChainServices, animeID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		runConsumers(t, ctx, svc)
		var live int
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alignment_operations WHERE managed_anime_id = $1 AND state IN ('planned','ready','running')`, animeID).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d alignment operations still pending", live)
		}
		time.Sleep(time.Second)
	}
}

func loadCompanion(t *testing.T, ctx context.Context, svc *realChainServices, placementID string) filemaintenance.Companion {
	t.Helper()
	companions, err := svc.placements.ListCompanions(ctx, placementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(companions) != 1 {
		t.Fatalf("expected one companion, got %+v", companions)
	}
	return companions[0]
}

// staleVideo moves the video and its companion to a flat legacy location and
// points the owners at it, as an older naming template would have left them.
func staleVideo(t *testing.T, ctx context.Context, svc *realChainServices, animeID string, companion filemaintenance.Companion, videoPath, staleVideoPath string) string {
	t.Helper()
	if err := os.Rename(videoPath, staleVideoPath); err != nil {
		t.Fatal(err)
	}
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `UPDATE placements SET physical_path = $1 WHERE managed_anime_id = $2 AND state = 'active'`, staleVideoPath, animeID); err != nil {
		t.Fatal(err)
	}
	staleCompanion := filemaintenance.CompanionPath(staleVideoPath, companion.Suffix, companion.OriginalExtension)
	setCompanionPath(t, ctx, companion, companion.CurrentPath, staleCompanion)
	return staleCompanion
}

// TestCompanionAlignmentRealChain proves companion alignment: a companion away from
// its derived path moves alone when the video is already aligned; when the
// video moves its companion follows under the new basename; a companion whose
// target is occupied stays where it is with a typed reason while the video's
// move stands, and the occupying file is untouched.
func TestCompanionAlignmentRealChain(t *testing.T) {
	ctx := context.Background()
	svc, animeID, placementID, videoPath, subtitle, pathDiff := startCompanionChain(t, ctx)
	alignedSubtitle := subtitle.CurrentPath
	subtitleBytes, err := os.ReadFile(alignedSubtitle)
	if err != nil {
		t.Fatal(err)
	}
	enableAlignmentPolicy(t, ctx, svc)

	// 1. Video in place, subtitle under a legacy name: only the subtitle moves.
	legacy := filepath.Join(filepath.Dir(videoPath), "Comp Real Show - 01.sc.ass")
	setCompanionPath(t, ctx, subtitle, alignedSubtitle, legacy)
	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)
	if got := loadCompanion(t, ctx, svc, placementID); got.CurrentPath != alignedSubtitle || got.AlignmentFailureCode != "" {
		t.Fatalf("the subtitle must move to the video's basename: %+v", got)
	}
	if moved, err := os.ReadFile(alignedSubtitle); err != nil || string(moved) != string(subtitleBytes) {
		t.Fatalf("aligned subtitle bytes differ: err=%v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("the legacy subtitle must be moved, not copied: %v", err)
	}
	var videoNow string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil || videoNow != videoPath {
		t.Fatalf("the aligned video must stay: %q err=%v", videoNow, err)
	}

	// 2. The video moves; its subtitle follows under the new basename.
	staleVideoPath := filepath.Join(filepath.Dir(filepath.Dir(videoPath)), "Comp Real Show - legacy 01.mkv")
	staleSubtitle := staleVideo(t, ctx, svc, animeID, loadCompanion(t, ctx, svc, placementID), videoPath, staleVideoPath)
	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil {
		t.Fatal(err)
	}
	canonicalVideo, err := filepath.EvalSymlinks(videoPath)
	if err != nil || videoNow != canonicalVideo {
		t.Fatalf("the video must return to its target: %q want %q err=%v", videoNow, canonicalVideo, err)
	}
	followed := loadCompanion(t, ctx, svc, placementID)
	if followed.CurrentPath != filemaintenance.CompanionPath(videoNow, followed.Suffix, followed.OriginalExtension) || followed.AlignmentFailureCode != "" {
		t.Fatalf("the subtitle must follow the video: %+v video=%q", followed, videoNow)
	}
	if _, err := os.Stat(staleSubtitle); !os.IsNotExist(err) {
		t.Fatalf("the stale subtitle must be moved: %v", err)
	}

	// 3. An occupied subtitle target: the video and its subtitle are one
	// unit, so neither moves; the video's operation names the occupied path
	// and the occupant is never touched.
	staleSubtitle = staleVideo(t, ctx, svc, animeID, followed, videoNow, staleVideoPath)
	occupant := []byte("an unmanaged file the user put here")
	if err := os.WriteFile(followed.CurrentPath, occupant, 0o644); err != nil {
		t.Fatal(err)
	}
	runScan(t, ctx, svc, animeID)
	runAlignmentsToRest(t, ctx, svc, animeID)
	assertUnitBlocked(t, ctx, svc, animeID, placementID, staleVideoPath, staleSubtitle, followed.CurrentPath, occupant)
	runScan(t, ctx, svc, animeID)
	if codes := companionIssueCodes(t, ctx, pathDiff, animeID); len(codes) != 1 || codes[0] != "companion_not_aligned" {
		t.Fatalf("the blocked subtitle must surface as not aligned, got %v", codes)
	}
}

// assertUnitBlocked: the video and its subtitle stayed at their stale paths,
// the latest video alignment is blocked with companion_target_occupied naming
// the occupied subtitle target, and the occupant is untouched.
func assertUnitBlocked(t *testing.T, ctx context.Context, svc *realChainServices, animeID, placementID, staleVideoPath, staleSubtitle, occupiedPath string, occupant []byte) {
	t.Helper()
	var videoNow string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil || videoNow != staleVideoPath {
		t.Fatalf("an occupied unit must keep the video in place: %q err=%v", videoNow, err)
	}
	var state, code, reason string
	if err := svc.db.QueryRowContext(ctx, `
SELECT state, COALESCE(failure_code,''), COALESCE(reason,'') FROM alignment_operations
WHERE managed_anime_id = $1 AND companion_id IS NULL ORDER BY created_at DESC LIMIT 1`, animeID).Scan(&state, &code, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || code != "companion_target_occupied" || !strings.Contains(reason, filepath.Base(occupiedPath)) {
		t.Fatalf("the video alignment must block on the occupied subtitle target: state=%s code=%s reason=%q", state, code, reason)
	}
	blocked := loadCompanion(t, ctx, svc, placementID)
	if blocked.CurrentPath != staleSubtitle {
		t.Fatalf("the subtitle must stay with its video: %+v", blocked)
	}
	if _, err := os.Stat(staleSubtitle); err != nil {
		t.Fatalf("the blocked subtitle must stay on disk: %v", err)
	}
	if _, err := os.Stat(staleVideoPath); err != nil {
		t.Fatalf("the blocked video must stay on disk: %v", err)
	}
	if kept, err := os.ReadFile(occupiedPath); err != nil || string(kept) != string(occupant) {
		t.Fatalf("the occupying file must be untouched: err=%v", err)
	}
}

// TestCompanionAlignmentUnitRealChain proves the alignment unit rules: a video and its subtitles follow the effective
// collision setting as one unit. fail: an occupied subtitle target fails a
// companion-only move with target_occupied. version: one version number keeps
// the unit's targets free, with no churn on the next scan; a companion-only
// collision gives the subtitle its own version number inside its suffix, and
// the next scan finds it bound and aligned. overwrite: unmanaged occupants of
// the video's and the subtitle's targets are replaced; an exchange stopped
// mid-way is finished after a restart from the persisted record.
func TestCompanionAlignmentUnitRealChain(t *testing.T) {
	ctx := context.Background()
	occupant := []byte("an unmanaged file the user put here")
	t.Run("fail_companion_only", func(t *testing.T) {
		svc, animeID, placementID, videoPath, subtitle, _ := startCompanionChain(t, ctx, generated.CollisionPolicyFail)
		enableAlignmentPolicy(t, ctx, svc)
		aligned := subtitle.CurrentPath
		legacy := filepath.Join(filepath.Dir(videoPath), "Comp Real Show - legacy.sc.ass")
		setCompanionPath(t, ctx, subtitle, aligned, legacy)
		if err := os.WriteFile(aligned, occupant, 0o644); err != nil {
			t.Fatal(err)
		}
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		if got := loadCompanion(t, ctx, svc, placementID); got.CurrentPath != legacy || got.AlignmentFailureCode != "target_occupied" {
			t.Fatalf("a companion-only collision under fail must fail with target_occupied: %+v", got)
		}
		if kept, err := os.ReadFile(aligned); err != nil || string(kept) != string(occupant) {
			t.Fatalf("the occupying file must be untouched: err=%v", err)
		}
	})
	t.Run("version_unit", func(t *testing.T) {
		svc, animeID, placementID, videoPath, subtitle, _ := startCompanionChain(t, ctx, generated.CollisionPolicyVersioned)
		enableAlignmentPolicy(t, ctx, svc)
		staleVideoPath := filepath.Join(filepath.Dir(filepath.Dir(videoPath)), "Comp Real Show - legacy 01.mkv")
		staleVideo(t, ctx, svc, animeID, subtitle, videoPath, staleVideoPath)
		if err := os.WriteFile(subtitle.CurrentPath, occupant, 0o644); err != nil {
			t.Fatal(err)
		}
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		var videoNow string
		if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil {
			t.Fatal(err)
		}
		dir, err := filepath.EvalSymlinks(filepath.Dir(videoPath))
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(videoPath)
		want := filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base))+" (2)"+filepath.Ext(base))
		if videoNow != want {
			t.Fatalf("the video must take one version number: %q want %q", videoNow, want)
		}
		followed := loadCompanion(t, ctx, svc, placementID)
		if followed.CurrentPath != filemaintenance.CompanionPath(videoNow, followed.Suffix, followed.OriginalExtension) || followed.AlignmentFailureCode != "" {
			t.Fatalf("the subtitle must follow the versioned video: %+v video=%q", followed, videoNow)
		}
		if kept, err := os.ReadFile(subtitle.CurrentPath); err != nil || string(kept) != string(occupant) {
			t.Fatalf("the occupying file must be untouched: err=%v", err)
		}
		// The next scan finds the unit at its versioned target: nothing moves.
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		var again string
		if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&again); err != nil || again != videoNow {
			t.Fatalf("a versioned unit must stay put: %q want %q err=%v", again, videoNow, err)
		}
		if got := loadCompanion(t, ctx, svc, placementID); got.CurrentPath != followed.CurrentPath {
			t.Fatalf("a versioned subtitle must stay put: %+v", got)
		}
	})
	t.Run("version_companion_only", func(t *testing.T) {
		svc, animeID, placementID, videoPath, subtitle, pathDiff := startCompanionChain(t, ctx, generated.CollisionPolicyVersioned)
		enableAlignmentPolicy(t, ctx, svc)
		aligned := subtitle.CurrentPath
		legacy := filepath.Join(filepath.Dir(videoPath), "Comp Real Show - legacy.sc.ass")
		setCompanionPath(t, ctx, subtitle, aligned, legacy)
		if err := os.WriteFile(aligned, occupant, 0o644); err != nil {
			t.Fatal(err)
		}
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		got := loadCompanion(t, ctx, svc, placementID)
		wantSuffix := subtitle.Suffix + " (2)"
		if got.Suffix != wantSuffix || got.CurrentPath != filemaintenance.CompanionPath(videoPath, wantSuffix, subtitle.OriginalExtension) || got.AlignmentFailureCode != "" {
			t.Fatalf("the subtitle must take its own version number inside its suffix: %+v", got)
		}
		if !strings.HasPrefix(filepath.Base(got.CurrentPath), strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))+".") {
			t.Fatalf("the versioned subtitle must keep the video's basename prefix: %q", got.CurrentPath)
		}
		if kept, err := os.ReadFile(aligned); err != nil || string(kept) != string(occupant) {
			t.Fatalf("the occupying file must be untouched: err=%v", err)
		}
		// The next scan finds the versioned subtitle bound and aligned.
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		if codes := companionIssueCodes(t, ctx, pathDiff, animeID); len(codes) != 0 {
			t.Fatalf("a versioned subtitle must be aligned, got %v", codes)
		}
		if again := loadCompanion(t, ctx, svc, placementID); again.CurrentPath != got.CurrentPath {
			t.Fatalf("a versioned subtitle must stay put: %+v", again)
		}
	})
	t.Run("overwrite", func(t *testing.T) {
		svc, animeID, placementID, videoPath, subtitle, _ := startCompanionChain(t, ctx, generated.CollisionPolicyOverwrite)
		enableAlignmentPolicy(t, ctx, svc)
		videoBytes, err := os.ReadFile(videoPath)
		if err != nil {
			t.Fatal(err)
		}
		subtitleBytes, err := os.ReadFile(subtitle.CurrentPath)
		if err != nil {
			t.Fatal(err)
		}
		aligned := subtitle.CurrentPath
		staleVideoPath := filepath.Join(filepath.Dir(filepath.Dir(videoPath)), "Comp Real Show - legacy 01.mkv")
		staleVideo(t, ctx, svc, animeID, subtitle, videoPath, staleVideoPath)
		for _, path := range []string{videoPath, aligned} {
			if err := os.WriteFile(path, occupant, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		var videoNow string
		if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&videoNow); err != nil {
			t.Fatal(err)
		}
		canonical, err := filepath.EvalSymlinks(videoPath)
		if err != nil || videoNow != canonical {
			t.Fatalf("the video must replace the unmanaged occupant: %q want %q err=%v", videoNow, canonical, err)
		}
		assertBytes(t, videoPath, videoBytes)
		if got := loadCompanion(t, ctx, svc, placementID); got.CurrentPath != aligned || got.AlignmentFailureCode != "" {
			t.Fatalf("the subtitle must replace the unmanaged occupant: %+v", got)
		}
		assertBytes(t, aligned, subtitleBytes)

		// Companion-only: the video is in place, an unmanaged occupant of
		// the subtitle's target is replaced.
		legacy := filepath.Join(filepath.Dir(videoPath), "Comp Real Show - legacy.sc.ass")
		setCompanionPath(t, ctx, loadCompanion(t, ctx, svc, placementID), aligned, legacy)
		if err := os.WriteFile(aligned, occupant, 0o644); err != nil {
			t.Fatal(err)
		}
		runScan(t, ctx, svc, animeID)
		runAlignmentsToRest(t, ctx, svc, animeID)
		if got := loadCompanion(t, ctx, svc, placementID); got.CurrentPath != aligned || got.AlignmentFailureCode != "" {
			t.Fatalf("a companion-only overwrite must replace the occupant: %+v", got)
		}
		assertBytes(t, aligned, subtitleBytes)
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(videoPath), ".bansoko-overwrite-*")); len(leftovers) != 0 {
			t.Fatalf("overwrite staging left behind: %v", leftovers)
		}
	})
	t.Run("overwrite_restart", func(t *testing.T) {
		svc, animeID, _, videoPath, subtitle, _ := startCompanionChain(t, ctx, generated.CollisionPolicyOverwrite)
		enableAlignmentPolicy(t, ctx, svc)
		videoBytes, err := os.ReadFile(videoPath)
		if err != nil {
			t.Fatal(err)
		}
		staleVideoPath := filepath.Join(filepath.Dir(filepath.Dir(videoPath)), "Comp Real Show - legacy 01.mkv")
		staleVideo(t, ctx, svc, animeID, subtitle, videoPath, staleVideoPath)
		if err := os.WriteFile(videoPath, occupant, 0o644); err != nil {
			t.Fatal(err)
		}
		crashed := false
		svc.alignment.SetFilePorts(stoppingOverwriteExecutor{at: domain.OverwriteStageExchanged, crashed: &crashed}, filesystem.OSFileObserver{})
		submitScanTolerating(t, ctx, svc, animeID)
		if !crashed {
			t.Fatal("the overwrite exchange did not run")
		}
		var state string
		var recorded bool
		if err := svc.db.QueryRowContext(ctx, `SELECT state, overwrite_recovery IS NOT NULL FROM alignment_operations WHERE managed_anime_id = $1 AND companion_id IS NULL ORDER BY created_at DESC LIMIT 1`, animeID).Scan(&state, &recorded); err != nil {
			t.Fatal(err)
		}
		if state != "running" || !recorded {
			t.Fatalf("the stopped exchange must stay running with its record: state=%s recorded=%v", state, recorded)
		}
		// Restart: a fresh executor, then the deployment reconcile.
		svc.alignment.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
		if _, err := svc.alignment.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "align-restart"}); err != nil {
			t.Fatal(err)
		}
		assertBytes(t, videoPath, videoBytes)
		if _, err := os.Stat(staleVideoPath); !os.IsNotExist(err) {
			t.Fatalf("the stale video must be moved: %v", err)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(videoPath), ".bansoko-overwrite-*")); len(leftovers) != 0 {
			t.Fatalf("overwrite staging left behind: %v", leftovers)
		}
	})
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
		t.Fatalf("%s holds %q (err=%v), want %q", filepath.Base(path), got, err, want)
	}
}

// stoppingOverwriteExecutor stops the process right after the given exchange
// stage was persisted, once.
type stoppingOverwriteExecutor struct {
	filesystem.OSFileExecutor
	at      domain.OverwriteStage
	crashed *bool
}

func (e stoppingOverwriteExecutor) ExecuteMaintenanceOverwrite(ctx context.Context, op domain.FileOperation, i executorpkg.MaintenanceInvocation, checkpoint domain.FileOperationCheckpoint) (domain.FileOperation, error) {
	return e.OSFileExecutor.ExecuteMaintenanceOverwrite(ctx, op, i, func(ctx context.Context, current domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		updated, err := checkpoint(ctx, current, recovery)
		if err == nil && !*e.crashed && recovery.Stage == e.at {
			*e.crashed = true
			return updated, errors.New("process stopped")
		}
		return updated, err
	})
}

// submitScanTolerating submits a scan and drives the worker until idle,
// tolerating the one unresolved alignment a stopped process leaves leased.
func submitScanTolerating(t *testing.T, ctx context.Context, svc *realChainServices, animeID string) {
	t.Helper()
	manual := reconciliationapplication.ScanSubmitTriggerManual
	requestID := fmt.Sprintf("recon-manual-%d", time.Now().UnixNano())
	treeTop := scanTreeTop(t, ctx, svc.db, animeID)
	if _, err := svc.recon.SubmitScan(ctx, reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: animeID, TreeTop: &treeTop, Trigger: &manual, RequestID: &requestID,
	}); err != nil {
		t.Fatalf("submit scan: %v", err)
	}
	for index := 0; index < 64; index++ {
		handled, err := svc.runtime.RunOnce(ctx)
		if err != nil && !errors.Is(err, workitems.ErrPublicationUnresolved) {
			t.Fatal(err)
		}
		if !handled {
			return
		}
	}
}

// TestCompanionReplacementCleanupRealChain proves companion replacement and cleanup:
// when a better revision supersedes the old video, the old subtitle enters
// cleanup with it (one cleanup record per companion) and is deleted with the
// video, the winner's video and subtitle are untouched, and a loser
// companion whose path is owned by the winner is never taken (the "never
// delete media" guard): its relation ends and the winner's file stays.
func TestCompanionReplacementCleanupRealChain(t *testing.T) {
	ctx := context.Background()
	items := map[string]rssItem{
		"A": {GUID: "comp-repl-a-guid", Title: "[RealGroup] Comp Repl Show - 01 [720p][CHS]"},
		"B": {GUID: "comp-repl-b-guid", Title: "[RealGroup] Comp Repl Show - 01 [1080p][CHS]"},
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	filesA := []torrentFile{
		{Name: "Comp Repl Show - 01.mkv", Content: []byte("revision A 720p video " + stamp)},
		{Name: "Comp Repl Show - 01.sc.ass", Content: []byte("Dialogue: revision A subtitle")},
	}
	filesB := []torrentFile{
		{Name: "Comp Repl Show - 01.mkv", Content: []byte("revision B 1080p video with a larger payload " + stamp)},
		{Name: "Comp Repl Show - 01.sc.ass", Content: []byte("Dialogue: revision B subtitle")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": filesA, "B": filesB})
	t.Cleanup(fixture.Close)
	svc := newRealChainServices(t, fixture.client)
	t.Cleanup(func() { _ = svc.db.Close() })
	seedReplacementSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Comp Repl Show"
	draft.RssSources[0].Title = "Comp Repl Show"
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	// Revisions A and B are the same episode of the same show, and the shipped
	// basic template names only kind/title/year/season plus the episode: both
	// revisions would render one identical target path, so no second path could
	// hold the superseded revision this scenario cleans up. The scenario states
	// its own instance rules — the shipped shape with the release's resolution
	// kept in the video name — rather than reading a discriminator into the
	// default. (The same-path replacement delivery is covered by
	// TestPrepareAdoptsExistingSidecarUnderReplacement.)
	var namingOverride generated.NamingRulesOverride
	if err := namingOverride.FromNamingRulesReplace(generated.NamingRulesReplace{
		Mode:  generated.NamingRulesReplaceModeReplace,
		Value: resolutionNamingRules(svc.targetRoot),
	}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.Naming = &namingOverride
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.CurrentRevision == nil {
		t.Fatalf("candidate save failed: status=%d", saveStatus)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "comp-repl-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID, sourceID := result.ManagedAnime.Id, result.RssSources[0].Id
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "comp-repl-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "comp-repl-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The rules name ${anime.season_folder}: the owner season must be set.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, svc.db, animeID, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	svc.driveToPlacement(t, ctx, animeID, "A", filesA, 2)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementA, videoA string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementA, &videoA); err != nil {
		t.Fatal(err)
	}
	subtitleA := loadCompanion(t, ctx, svc, placementA)

	// The guard: a second companion of A claims the path B's video will own,
	// with B's exact bytes. Cleanup must never take it. B's path is A's with the
	// revision's resolution: the instance rules keep it in the video name.
	videoB := strings.Replace(videoA, "720p", "1080p", 1)
	if videoB == videoA {
		t.Fatalf("revision paths must differ by resolution: %q", videoA)
	}
	digestB := sha256.Sum256(filesB[0].Content)
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO placement_companions (id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ('cmp-guard', $1, $2, 'guard-source', 'intake', 'other', '', 'mkv', $3, $4, $5)`,
		placementA, animeID, videoB, len(filesB[0].Content), hex.EncodeToString(digestB[:])); err != nil {
		t.Fatal(err)
	}

	// B's subtitle target already holds B's exact subtitle: it is adopted
	// without a write and becomes the winner's companion. (B publishes at its
	// own path, so this is an ordinary delivery; the same-path replacement
	// delivery case of ruling (b) is TestPrepareAdoptsExistingSidecarUnderReplacement.)
	preseeded := filemaintenance.CompanionPath(videoB, subtitleA.Suffix, "ass")
	if err := os.WriteFile(preseeded, filesB[1].Content, 0o644); err != nil {
		t.Fatal(err)
	}
	preseededInfo, statErr := os.Stat(preseeded)
	if statErr != nil {
		t.Fatal(statErr)
	}

	fixture.SetCurrent("B")
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	svc.driveToPlacement(t, ctx, animeID, "B", filesB, 3) // the adopted subtitle needs no FileOperation
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementB, activeVideo string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementB, &activeVideo); err != nil {
		t.Fatal(err)
	}
	if placementB == placementA || activeVideo != videoB {
		t.Fatalf("revision B must win at its own path: %q want %q", activeVideo, videoB)
	}
	subtitleB := loadCompanion(t, ctx, svc, placementB)
	if subtitleB.CurrentPath != preseeded {
		t.Fatalf("the existing subtitle must become the winner's companion: %+v want %q", subtitleB, preseeded)
	}
	if info, err := os.Stat(preseeded); err != nil || !os.SameFile(info, preseededInfo) {
		t.Fatalf("the existing subtitle must be adopted without a write: err=%v", err)
	}

	// One cleanup record for A's video and one for A's subtitle; none for
	// the guarded path.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
			t.Fatal(err)
		}
		var open int
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = $1 AND state = 'admitted'`, animeID).Scan(&open); err != nil {
			t.Fatal(err)
		}
		if open == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d cleanup operations still admitted", open)
		}
		time.Sleep(time.Second)
	}
	rows, err := svc.db.QueryContext(ctx, `SELECT COALESCE(companion_id, ''), source_path, state, COALESCE(failure_code, '') FROM replacement_deletions WHERE managed_anime_id = $1 ORDER BY source_path`, animeID)
	if err != nil {
		t.Fatal(err)
	}
	type cleanupRow struct{ companion, path, state, code string }
	var deletions []cleanupRow
	for rows.Next() {
		var row cleanupRow
		if err := rows.Scan(&row.companion, &row.path, &row.state, &row.code); err != nil {
			t.Fatal(err)
		}
		deletions = append(deletions, row)
	}
	rows.Close()
	if len(deletions) != 2 {
		t.Fatalf("expected the old video and its subtitle in cleanup, got %+v", deletions)
	}
	for _, row := range deletions {
		switch row.path {
		case subtitleA.CurrentPath:
			if row.companion != subtitleA.ID {
				t.Fatalf("the subtitle cleanup must name its companion: %+v", row)
			}
		case videoA:
			if row.companion != "" {
				t.Fatalf("the video cleanup carries no companion: %+v", row)
			}
		default:
			t.Fatalf("cleanup took a path it does not own: %+v", row)
		}
		if row.state != string(filemaintenance.CleanupDeleted) {
			t.Fatalf("cleanup did not delete: %+v", row)
		}
	}
	if _, err := os.Stat(subtitleA.CurrentPath); !os.IsNotExist(err) {
		t.Fatalf("the old subtitle must leave the library with its video: %v", err)
	}
	if _, err := os.Stat(videoA); !os.IsNotExist(err) {
		t.Fatalf("the old video must be deleted: %v", err)
	}
	if kept, err := os.ReadFile(videoB); err != nil || string(kept) != string(filesB[0].Content) {
		t.Fatalf("the winner's video must be untouched: err=%v", err)
	}
	if kept, err := os.ReadFile(subtitleB.CurrentPath); err != nil || string(kept) != string(filesB[1].Content) {
		t.Fatalf("the winner's subtitle must be untouched: err=%v", err)
	}
	var guardState, guardReason string
	if err := svc.db.QueryRowContext(ctx, `SELECT state, COALESCE(ended_reason, '') FROM placement_companions WHERE id = 'cmp-guard'`).Scan(&guardState, &guardReason); err != nil {
		t.Fatal(err)
	}
	if guardState != filemaintenance.CompanionStateEnded || guardReason != "path_owned_by_winner" {
		t.Fatalf("a loser companion on the winner's path must end, not be cleaned: %s/%s", guardState, guardReason)
	}
}

// resolutionNamingRules is the shipped default template shape — kind, title and
// the providers' year, the season directory and the SxxExx episode — with the
// release's resolution kept in the video name. A scenario that needs a
// superseded revision to reach a path of its own states that requirement here:
// the shipped basic template does not name the resolution, so two revisions of
// one episode are one target and are replaced in place.
func resolutionNamingRules(root string) generated.NamingRules {
	prefix := filepath.ToSlash(root)
	return generated.NamingRules{
		Language: naming.Language,
		Tv: generated.NamingVariant{
			DirectoryTemplate:     prefix + "/${anime.kind}/${anime.title} ${anime.year}/${anime.season_folder}",
			VideoFilenameTemplate: "${anime.title} ${s00e00} ${media.resolution}.${file.ext}",
		},
		Movie: generated.NamingVariant{
			DirectoryTemplate:     prefix + "/${anime.kind}/${anime.title} ${anime.year}",
			VideoFilenameTemplate: "${file.stem}.${file.ext}",
		},
		Unknown: generated.NamingVariant{
			DirectoryTemplate:     prefix + "/${anime.kind}/${anime.title} ${anime.year}/Extras",
			VideoFilenameTemplate: "${file.stem}.${file.ext}",
		},
		SubtitleSuffixTemplate: "${rt.sub_lang}",
	}
}
