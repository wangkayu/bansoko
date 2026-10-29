package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/library/domain"
)

// recordingSubmitter records every request identity it is given and can run a
// hook inside the submission (a write made while the scan runs).
type recordingSubmitter struct {
	mu   sync.Mutex
	ids  []string
	err  error
	hook func()
}

func (r *recordingSubmitter) SubmitScan(_ context.Context, _, _, requestID string) error {
	r.mu.Lock()
	r.ids = append(r.ids, requestID)
	hook, err := r.hook, r.err
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func requestConfigScan(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO config_scan_requests(managed_anime_id) VALUES ($1)
ON CONFLICT (managed_anime_id) DO UPDATE SET revision = config_scan_requests.revision + 1, requested_at = CURRENT_TIMESTAMP`, animeID); err != nil {
		t.Fatal(err)
	}
}

func pendingConfigScans(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM config_scan_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The tick submits a pending request with a request identity that is new for
// every request, clears it, and a later request is a different identity (a
// new generation instead of a replay of the completed one).
func TestPGConfigScanRequestDispatchUsesFreshIdentityPerRequest(t *testing.T) {
	f := newOffsetFixture(t, ModePerGroup, domain.CollisionBlock)
	f.seedFile(t, "anime-1", "sub-1", "rel-a", "Show/ep1.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "A"}, "2026-07-01T00:00:00Z")
	submitter := &recordingSubmitter{}
	f.scheduler.submitter = submitter
	ctx := context.Background()

	requestConfigScan(t, f.db, "anime-1")
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if len(submitter.ids) != 1 || !strings.HasPrefix(submitter.ids[0], "config:anime-1:") {
		t.Fatalf("one config scan submitted: %v", submitter.ids)
	}
	if pendingConfigScans(t, f.db) != 0 {
		t.Fatal("a submitted request is cleared")
	}
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if len(submitter.ids) != 1 {
		t.Fatalf("nothing pending, nothing submitted: %v", submitter.ids)
	}

	requestConfigScan(t, f.db, "anime-1")
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if len(submitter.ids) != 2 || submitter.ids[0] == submitter.ids[1] {
		t.Fatalf("every request carries its own identity: %v", submitter.ids)
	}
}

// A refused submission (a scan is running) keeps the request for the next tick;
// a write made during the submission stays pending after the dispatch clears
// the revision it read.
func TestPGConfigScanRequestSurvivesRunningScanAndLaterWrite(t *testing.T) {
	f := newOffsetFixture(t, ModePerGroup, domain.CollisionBlock)
	f.seedFile(t, "anime-1", "sub-1", "rel-a", "Show/ep1.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "A"}, "2026-07-01T00:00:00Z")
	submitter := &recordingSubmitter{err: errors.New("scan in progress")}
	f.scheduler.submitter = submitter
	ctx := context.Background()

	requestConfigScan(t, f.db, "anime-1")
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if pendingConfigScans(t, f.db) != 1 {
		t.Fatal("a refused submission keeps the request")
	}

	submitter.mu.Lock()
	submitter.err = nil
	submitter.hook = func() { requestConfigScan(t, f.db, "anime-1") }
	submitter.mu.Unlock()
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := f.db.QueryRow(`SELECT revision FROM config_scan_requests WHERE managed_anime_id = 'anime-1'`).Scan(&revision); err != nil {
		t.Fatalf("the write made during the scan stays pending: %v", err)
	}
	if revision != 2 {
		t.Fatalf("pending revision=%d, want 2", revision)
	}

	submitter.mu.Lock()
	submitter.hook = nil
	submitter.mu.Unlock()
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if pendingConfigScans(t, f.db) != 0 {
		t.Fatal("the next tick dispatches the remaining request")
	}
	if n := len(submitter.ids); n != 3 || submitter.ids[1] == submitter.ids[2] {
		t.Fatalf("the later request is a new identity: %v", submitter.ids)
	}
}

// A stopped subscription is not scanned and keeps its request; an anime that
// placed nothing has no tree top and its request is dropped.
func TestPGConfigScanRequestSkipsStoppedAndDropsPlacementless(t *testing.T) {
	f := newOffsetFixture(t, ModePerGroup, domain.CollisionBlock)
	seedAnimeSubscription(t, f.db, "anime-stopped", "sub-stopped", "stopped")
	f.seedFile(t, "anime-stopped", "sub-stopped", "rel-stopped", "Stopped/ep1.mkv", "tv:e1", "", Facts{Resolution: "1080p", Group: "B"}, "2026-07-01T00:00:00Z")
	seedAnimeSubscription(t, f.db, "anime-empty", "sub-empty", "active")
	submitter := &recordingSubmitter{}
	f.scheduler.submitter = submitter
	ctx := context.Background()

	requestConfigScan(t, f.db, "anime-stopped")
	requestConfigScan(t, f.db, "anime-empty")
	if err := f.scheduler.dispatchConfigScanRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if len(submitter.ids) != 0 {
		t.Fatalf("no scan for a stopped or placementless anime: %v", submitter.ids)
	}
	var remaining []string
	rows, err := f.db.Query(`SELECT managed_anime_id FROM config_scan_requests`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, id)
	}
	if len(remaining) != 1 || remaining[0] != "anime-stopped" {
		t.Fatalf("the stopped request waits for the resume, the empty one is dropped: %v", remaining)
	}
}
