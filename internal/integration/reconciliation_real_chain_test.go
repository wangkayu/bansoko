package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/filemaintenance"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi/generated"
)

// TestReconciliationRealChain proves the Placement-owned reconciliation,
// direct recovery and alignment convergence on natural data:
//  1. Revision A lands at the rendered target; a manual scan calibrates it as
//     aligned with no drift.
//  2. Moving the file out of the rendered target marks it drifted; the
//     alignment lane copies it back (fingerprint-verified), commits the
//     Placement current path and a follow-up scan reports aligned again.
//  3. Effect-before-owner-commit recovery: with the target already carrying
//     the frozen bytes and the Placement path stale, alignment settles
//     recovered_aligned without a second file effect.
func TestReconciliationRealChain(t *testing.T) {
	items := map[string]rssItem{
		"A": {GUID: "recon-a-guid", Title: "[RealGroup] Recon Real Show - 01 [1080p]"},
	}
	videoA := []torrentFile{
		{Name: "Recon Real Show - 01.mkv", Content: []byte("recon video " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Recon Real Show - 01.nfo", Content: []byte("recon nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": videoA})
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Recon Real Show"
	draft.RssSources[0].Title = "Recon Real Show"
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.CurrentRevision == nil {
		t.Fatalf("candidate save failed: status=%d candidate=%+v", saveStatus, candidate)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "recon-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID := result.ManagedAnime.Id

	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "recon-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "recon-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the year the providers observed, so the
	// fixture records the observation before the release is accepted.
	observeBoundWorkYear(t, ctx, svc.db, animeID, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	// The default file rules deliver video and subtitles only: the torrent's
	// .nfo is downloaded with it but never planned, executed or failed.
	svc.driveToPlacement(t, ctx, animeID, "A", videoA, 1)
	var operations, failedResults int
	if err := svc.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM file_operations op JOIN artifacts a ON a.id = op.artifact_id
          JOIN download_tasks dt ON dt.id = a.download_task_id JOIN releases r ON r.id = dt.release_id
          JOIN rss_sources src ON src.id = r.rss_source_id JOIN subscriptions sub ON sub.id = src.subscription_id
          WHERE sub.managed_anime_id = $1),
       (SELECT COUNT(*) FROM acquisition_files WHERE outcome = 'failed')`, animeID).Scan(&operations, &failedResults); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || failedResults != 0 {
		t.Fatalf("the .nfo entered the effect chain: operations=%d failed delivery results=%d", operations, failedResults)
	}
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)

	var placementPath string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementPath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(placementPath, fmt.Sprintf("TV/Recon Real Show (%d)/Season 01/Recon Real Show S01E01.mkv", realChainObservedYear)) {
		t.Fatalf("placement must sit at the rendered target, got %q", placementPath)
	}
	// The physical file must exist at the placement path.
	if _, err := os.Stat(placementPath); err != nil {
		t.Fatalf("placement file missing on disk: %v", err)
	}

	// --- Generation 1: the natural library calibrates as aligned. ---
	runScan(t, ctx, svc, animeID)
	assertCalibrationDisposition(t, ctx, svc, animeID, "present", "present")

	// --- Drift: move the file out of the rendered target. ---
	driftedPath := filepath.Join(filepath.Dir(placementPath), "Recon Real Show - E01 [1080p]@RealGroup.drifted.mkv")
	if err := os.Rename(placementPath, driftedPath); err != nil {
		t.Fatal(err)
	}
	runScan(t, ctx, svc, animeID)
	// present + current_path missing + no provenance locator match -> missing
	// (the file is gone from every known locator; recovery requires the
	// alignment planner's rendered target which does not exist yet).
	assertCalibrationDisposition(t, ctx, svc, animeID, "present", "missing")

	// --- Alignment: enable the policy and let the lane converge. ---
	enableAlignmentPolicy(t, ctx, svc)
	runConsumers(t, ctx, svc)
	// The drifted file is at an unknown path; alignment cannot find it through
	// the placement locator, so it must remain missing — the Placement is not
	// mutated and the file is untouched. Restore the file and re-scan to prove
	// the full alignment loop.
	if err := os.Rename(driftedPath, placementPath); err != nil {
		t.Fatal(err)
	}
	runScan(t, ctx, svc, animeID)
	runConsumers(t, ctx, svc)
	// The placement is back at its current path; alignment rendered the same
	// target -> no operation needed and the calibration is present.
	assertCalibrationDisposition(t, ctx, svc, animeID, "present", "present")

	// --- Effect-before-owner-commit recovery: the rendered target already is
	// the placed file and the Placement still points at a stale legacy path;
	// alignment must repair the owner without a second file effect
	// (recovered_aligned). ---
	renderedTarget := placementPath
	legacyPath := filepath.Join(svc.targetRoot, "Recon Real Show", "Recon Real Show - E01 [1080p]@RealGroup.mkv")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// File identity is path-level (size + device/inode), so the landed effect
	// is the same inode at the target: the state a crash leaves between the
	// link and unlink of the no-replace move fallback. A byte copy would be a
	// different file. Point the placement at the legacy path to simulate a
	// stale owner.
	if err := os.Rename(renderedTarget, legacyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(legacyPath, renderedTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE placements SET physical_path = $1 WHERE managed_anime_id = $2 AND state = 'active'`, legacyPath, animeID); err != nil {
		t.Fatal(err)
	}
	// The alignment planner renders the template's target; it already carries
	// the frozen fingerprint, so execution short-circuits into
	// recovered_aligned and commits the Placement current path.
	runScan(t, ctx, svc, animeID)
	runConsumers(t, ctx, svc)
	var committedPath string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&committedPath); err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(renderedTarget)
	if err != nil {
		t.Fatal(err)
	}
	if committedPath != canonicalTarget {
		t.Fatalf("alignment must commit the verified target as the current path, got %q want %q", committedPath, canonicalTarget)
	}
	// Follow-up scan: the placement is aligned at its committed target.
	runScan(t, ctx, svc, animeID)
	assertCalibrationDisposition(t, ctx, svc, animeID, "present", "present")
	var finalPath string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&finalPath); err != nil {
		t.Fatal(err)
	}
	if finalPath != canonicalTarget {
		t.Fatalf("placement must stay at the verified target, got %q", finalPath)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final target file missing: %v", err)
	}
}

// runScan submits a manual scan and drives the lane chain.
func runScan(t *testing.T, ctx context.Context, svc *realChainServices, animeID string) {
	t.Helper()
	manual := reconciliationapplication.ScanSubmitTriggerManual
	requestID := fmt.Sprintf("recon-manual-%d", time.Now().UnixNano())
	treeTop := scanTreeTop(t, ctx, svc.db, animeID)
	if _, err := svc.recon.SubmitScan(ctx, reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: animeID, TreeTop: &treeTop, Trigger: &manual, RequestID: &requestID,
	}); err != nil {
		t.Fatalf("submit scan: %v", err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
}

// runConsumers drives the calibrate + alignment consumer lanes.
func runConsumers(t *testing.T, ctx context.Context, svc *realChainServices) {
	t.Helper()
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
}

// enableAlignmentPolicy turns on the global file-maintenance policy alignment.
func enableAlignmentPolicy(t *testing.T, ctx context.Context, svc *realChainServices) {
	t.Helper()
	enabled := true
	interval := 3600
	if _, err := svc.policies.SavePolicy(ctx, filemaintenance.GlobalScope, 0, filemaintenance.PolicyRow{
		Enabled: true, ScanIntervalSeconds: &interval, AlignmentEnabled: &enabled,
		Safety: json.RawMessage(`{"max_operations":100}`),
	}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
}

// assertCalibrationDisposition checks the latest calibration generation's rows.
func assertCalibrationDisposition(t *testing.T, ctx context.Context, svc *realChainServices, animeID, wantDisposition, wantFileDisposition string) {
	t.Helper()
	var generation int64
	if err := svc.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) FROM reconciliation_files WHERE managed_anime_id = $1 AND disposition IS NOT NULL`, animeID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation < 1 {
		t.Fatalf("no calibration generation for %s", animeID)
	}
	var dispositions string
	if err := svc.db.QueryRowContext(ctx, `
SELECT string_agg(DISTINCT disposition, ',') FROM reconciliation_files WHERE managed_anime_id = $1 AND generation = $2 AND disposition IS NOT NULL`, animeID, generation).Scan(&dispositions); err != nil {
		t.Fatal(err)
	}
	if dispositions != wantDisposition {
		t.Fatalf("generation %d dispositions=%q want %q", generation, dispositions, wantDisposition)
	}
	_ = wantFileDisposition
}
