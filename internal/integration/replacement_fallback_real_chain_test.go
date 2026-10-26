package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/batchplan"
	"bansoko/internal/transport/httpapi/generated"
)

// fallbackScenario subscribes one single_copy instance to a revision feed of
// two same-episode releases, A first. A's web seed is held, so its qB task is
// still downloading when B is evaluated: B is skipped as covered_in_flight.
type fallbackScenario struct {
	svc      *realChainServices
	fixture  *revisionFixture
	animeID  string
	sourceID string
	files    map[string][]torrentFile
}

func startFallbackScenario(t *testing.T, ctx context.Context, title, key string, items map[string]rssItem, files map[string][]torrentFile) *fallbackScenario {
	t.Helper()
	fixture := startRevisionFixture(t, items, files)
	t.Cleanup(fixture.Close)
	fixture.Hold("A")
	svc := newRealChainServices(t, fixture.client)
	t.Cleanup(func() { svc.db.Close() })
	seedRetentionSingleCopy(t, svc.db)
	animeID, sourceID := subscribeRevisionScenario(t, ctx, svc, fixture.FeedURL(), title, flatNamingRules(svc.targetRoot), generated.CollisionPolicyOverwrite, key)
	s := &fallbackScenario{svc: svc, fixture: fixture, animeID: animeID, sourceID: sourceID, files: files}
	if state := s.releaseState(t, ctx, items["A"].GUID); state.verdict != "submitted" && state.verdict != "accepted_pending_download" {
		t.Fatalf("A must be accepted first: %+v", state)
	}
	fixture.SetCurrent("B")
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	b := s.releaseState(t, ctx, items["B"].GUID)
	if b.verdict != "filtered" || b.task.Valid {
		t.Fatalf("B must be skipped while A downloads: %+v", b)
	}
	s.requireVideoOutcome(t, ctx, b.id, batchplan.OutcomeCoveredInFlight)
	return s
}

type releaseRow struct {
	id      string
	verdict string
	reason  string
	task    sql.NullString
}

func (s *fallbackScenario) releaseState(t *testing.T, ctx context.Context, guid string) releaseRow {
	t.Helper()
	var row releaseRow
	if err := s.svc.db.QueryRowContext(ctx, `SELECT id, verdict, reason_code, download_task_id FROM releases WHERE stable_item_key = $1`, guid).
		Scan(&row.id, &row.verdict, &row.reason, &row.task); err != nil {
		t.Fatalf("read release %s: %v", guid, err)
	}
	return row
}

func (s *fallbackScenario) requireVideoOutcome(t *testing.T, ctx context.Context, releaseID string, want batchplan.Outcome) {
	t.Helper()
	var raw []byte
	if err := s.svc.db.QueryRowContext(ctx, `SELECT release_facts->'batch_plan' FROM media_fact_snapshots WHERE release_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, releaseID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan batchplan.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		if file.Kind == "video" && file.Outcome != want {
			t.Fatalf("release %s video %s outcome = %s, want %s", releaseID, file.SourcePath, file.Outcome, want)
		}
	}
}

// settle runs one qB poll round, the worker and the acquisition settlement
// producer, then the worker again (the settle enqueues the fallback query).
func (s *fallbackScenario) settle(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := s.svc.download.DispatchQBSync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, s.svc.runtime, s.svc.db, 64); err != nil {
		t.Fatal(err)
	}
	settlement, err := acquisitionsettlement.NewSettlementService(s.svc.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.RunPass(ctx); err != nil {
		t.Fatalf("settlement pass: %v", err)
	}
	if err := runWorkerUntilIdle(ctx, s.svc.runtime, s.svc.db, 64); err != nil {
		t.Fatal(err)
	}
}

func (s *fallbackScenario) acquisitionState(t *testing.T, ctx context.Context, releaseID string) string {
	t.Helper()
	var state string
	if err := s.svc.db.QueryRowContext(ctx, `SELECT state FROM acquisitions WHERE source_release_ref = $1 ORDER BY accepted_at DESC LIMIT 1`, releaseID).Scan(&state); err != nil {
		t.Fatalf("read acquisition of %s: %v", releaseID, err)
	}
	return state
}

func (s *fallbackScenario) taskCount(t *testing.T, ctx context.Context, releaseID string) int {
	t.Helper()
	var count int
	if err := s.svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// qbDeleteByTag deletes every qB torrent carrying the tag, with its files.
func qbDeleteByTag(t *testing.T, tag string) {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	cookie := qbLogin(t, endpoint, os.Getenv("BANSOKO_QB_USERNAME"), os.Getenv("BANSOKO_QB_PASSWORD"))
	call := func(method, path string, body io.Reader, contentType string) []byte {
		t.Helper()
		req, err := http.NewRequest(method, endpoint+"/api/v2/torrents/"+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Referer", endpoint)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("qB %s HTTP %d", path, resp.StatusCode)
		}
		return raw
	}
	var rows []struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(call(http.MethodGet, "info?tag="+url.QueryEscape(tag), nil, ""), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("qB tag %s lists %d torrents, want 1", tag, len(rows))
	}
	call(http.MethodPost, "delete", strings.NewReader(url.Values{"hashes": {rows[0].Hash}, "deleteFiles": {"true"}}.Encode()), "application/x-www-form-urlencoded")
}

// TestReplacementFallbackOnDownloaderRemovalRealQB: A downloads first and B is
// skipped as covered_in_flight. A is deleted in qBittorrent: two confirmed
// absent polls cancel A (deleted in the downloader), the settle triggers the
// fallback query and B is submitted and placed. The cancelled A is never
// evaluated again (it keeps its failed task until a retry).
func TestReplacementFallbackOnDownloaderRemovalRealQB(t *testing.T) {
	ctx := context.Background()
	items := map[string]rssItem{
		"A": {GUID: "fbrm-a-guid", Title: "[RealGroup] Fallback Removal Show - 01 [1080p]"},
		"B": {GUID: "fbrm-b-guid", Title: "[RealGroup] Fallback Removal Show - 01 [720p]"},
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	files := map[string][]torrentFile{
		"A": {
			{Name: "Fallback Removal Show - 01.mkv", Content: bytes.Repeat([]byte("A"), 40000)},
			{Name: "Fallback Removal Show - 01 A.nfo", Content: []byte("revision A nfo " + stamp)},
		},
		"B": {
			{Name: "Fallback Removal Show - 01.mkv", Content: []byte("revision B 720p video " + stamp)},
			{Name: "Fallback Removal Show - 01 B.nfo", Content: []byte("revision B nfo " + stamp)},
		},
	}
	s := startFallbackScenario(t, ctx, "Fallback Removal Show", "fbrm", items, files)
	a := s.releaseState(t, ctx, items["A"].GUID)
	b := s.releaseState(t, ctx, items["B"].GUID)
	if state := s.acquisitionState(t, ctx, a.id); state != "running" {
		t.Fatalf("A must still be running: %s", state)
	}
	var tag string
	if err := s.svc.db.QueryRowContext(ctx, `SELECT provider_correlation->>'stable_tag' FROM download_tasks WHERE id = $1`, a.task.String).Scan(&tag); err != nil || tag == "" {
		t.Fatalf("read A's qB tag: %q %v", tag, err)
	}
	qbDeleteByTag(t, tag)

	deadline := time.Now().Add(3 * time.Minute)
	for s.acquisitionState(t, ctx, a.id) == "running" {
		if time.Now().After(deadline) {
			t.Fatal("A was never cancelled after its deletion in qB")
		}
		s.settle(t, ctx)
		time.Sleep(time.Second)
	}
	if state := s.acquisitionState(t, ctx, a.id); state != "cancelled" {
		t.Fatalf("A acquisition = %s, want cancelled", state)
	}
	var failure string
	if err := s.svc.db.QueryRowContext(ctx, `SELECT COALESCE(failure_code, '') FROM download_tasks WHERE id = $1`, a.task.String).Scan(&failure); err != nil || failure != "DOWNLOADER_TASK_REMOVED" {
		t.Fatalf("A task failure = %q %v", failure, err)
	}
	// The settle enqueued the fallback; B is promoted and submitted.
	deadline = time.Now().Add(2 * time.Minute)
	for !s.releaseState(t, ctx, items["B"].GUID).task.Valid {
		if time.Now().After(deadline) {
			t.Fatalf("B was never promoted: %+v", s.releaseState(t, ctx, items["B"].GUID))
		}
		s.settle(t, ctx)
		time.Sleep(time.Second)
	}
	var fallbackRuns int
	if err := s.svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'file_maintenance' AND owner_id = $1 AND kind = 'replacement_fallback' AND state = 'succeeded'`, s.animeID).Scan(&fallbackRuns); err != nil || fallbackRuns < 1 {
		t.Fatalf("the fallback query must have run: %d %v", fallbackRuns, err)
	}
	s.svc.driveToPlacement(t, ctx, s.animeID, "B", files["B"], 1)
	waitForPlacementCount(t, ctx, s.svc.db, s.animeID, 1)
	var placed string
	if err := s.svc.db.QueryRowContext(ctx, `
SELECT p.physical_path FROM placements p
WHERE p.managed_anime_id = $1 AND p.state = 'active' AND p.verification_state = 'verified'`, s.animeID).Scan(&placed); err != nil {
		t.Fatalf("read the active placement: %v", err)
	}
	if content, err := os.ReadFile(placed); err != nil || !bytes.Contains(content, []byte("revision B 720p")) {
		t.Fatalf("the library file must carry B: %v", err)
	}
	if b.task.Valid {
		t.Fatalf("B had no task before the fallback: %+v", b)
	}
	// The cancelled A stays out: it keeps its failed task and was never
	// evaluated again.
	after := s.releaseState(t, ctx, items["A"].GUID)
	if !after.task.Valid || after.task.String != a.task.String || s.taskCount(t, ctx, a.id) != 1 {
		t.Fatalf("the cancelled A must keep its one failed task: %+v tasks=%d", after, s.taskCount(t, ctx, a.id))
	}
}

// TestReplacementFallbackAfterSuccessWashesRealQB: A (720p) downloads first and
// B (1080p) is skipped as covered_in_flight. A completes and is placed; its
// settle triggers the fallback query, B is evaluated against the placed A,
// wins, downloads and replaces it.
func TestReplacementFallbackAfterSuccessWashesRealQB(t *testing.T) {
	ctx := context.Background()
	items := map[string]rssItem{
		"A": {GUID: "fbws-a-guid", Title: "[RealGroup] Fallback Wash Show - 01 [720p]"},
		"B": {GUID: "fbws-b-guid", Title: "[RealGroup] Fallback Wash Show - 01 [1080p]"},
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	files := map[string][]torrentFile{
		"A": {
			{Name: "Fallback Wash Show - 01.mkv", Content: []byte("revision A 720p video " + stamp)},
			{Name: "Fallback Wash Show - 01 A.nfo", Content: []byte("revision A nfo " + stamp)},
		},
		"B": {
			{Name: "Fallback Wash Show - 01.mkv", Content: []byte("revision B 1080p video larger payload " + stamp)},
			{Name: "Fallback Wash Show - 01 B.nfo", Content: []byte("revision B nfo " + stamp)},
		},
	}
	s := startFallbackScenario(t, ctx, "Fallback Wash Show", "fbws", items, files)
	a := s.releaseState(t, ctx, items["A"].GUID)
	b := s.releaseState(t, ctx, items["B"].GUID)

	s.fixture.Release("A")
	s.svc.driveToPlacement(t, ctx, s.animeID, "A", files["A"], 1)
	waitForPlacementCount(t, ctx, s.svc.db, s.animeID, 1)
	var placementA, pathA string
	if err := s.svc.db.QueryRowContext(ctx, `SELECT id, physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, s.animeID).Scan(&placementA, &pathA); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for !s.releaseState(t, ctx, items["B"].GUID).task.Valid {
		if time.Now().After(deadline) {
			t.Fatalf("B was never promoted after A succeeded: A acquisition=%s B=%+v", s.acquisitionState(t, ctx, a.id), s.releaseState(t, ctx, items["B"].GUID))
		}
		s.settle(t, ctx)
		time.Sleep(time.Second)
	}
	if state := s.acquisitionState(t, ctx, a.id); state != "succeeded" {
		t.Fatalf("A acquisition = %s, want succeeded", state)
	}
	var verdict string
	if err := s.svc.db.QueryRowContext(ctx, `SELECT verdict FROM replacement_decisions WHERE candidate_release_id = $1 AND state <> 'stale' ORDER BY created_at DESC LIMIT 1`, b.id).Scan(&verdict); err != nil || verdict != "replace" {
		t.Fatalf("B must be admitted to replace A: %q %v", verdict, err)
	}
	s.svc.driveToPlacement(t, ctx, s.animeID, "B", files["B"], 2)
	if err := runWorkerUntilIdle(ctx, s.svc.runtime, s.svc.db, 64); err != nil {
		t.Fatal(err)
	}
	waitForPlacementCount(t, ctx, s.svc.db, s.animeID, 1)
	var activePath string
	if err := s.svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, s.animeID).Scan(&activePath); err != nil {
		t.Fatal(err)
	}
	if state := placementStateOf(t, ctx, s.svc.db, placementA); state != "superseded" {
		t.Fatalf("A placement = %s, want superseded", state)
	}
	content, err := os.ReadFile(activePath)
	if err != nil || !bytes.Contains(content, []byte("revision B 1080p")) {
		t.Fatalf("the library file must carry B: %v", err)
	}
	if filepath.Clean(activePath) != filepath.Clean(pathA) {
		if _, err := os.Stat(pathA); !os.IsNotExist(err) {
			t.Fatalf("A's file must be deleted: %v", err)
		}
	}
}

func placementStateOf(t *testing.T, ctx context.Context, db *sql.DB, id string) string {
	t.Helper()
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM placements WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
