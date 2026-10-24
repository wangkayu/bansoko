package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// discoveryNotices records the release_discovered notices the release owner
// write admits.
type discoveryNotices struct {
	mu       sync.Mutex
	requests []plugintargetport.BusinessDeliveryRequest
}

func (r *discoveryNotices) EnqueueBusinessDelivery(_ context.Context, scope txscope.Tx, request plugintargetport.BusinessDeliveryRequest) error {
	if scope.SQLTx() == nil {
		return errors.New("notice admitted outside the owner transaction")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return nil
}

func (r *discoveryNotices) list() []plugintargetport.BusinessDeliveryRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]plugintargetport.BusinessDeliveryRequest(nil), r.requests...)
}

// discoveryFeedItem is one custom RSS item with a single-file torrent for
// episode n.
func discoveryFeedItem(t *testing.T, n int) acquisitiondomain.FeedItem {
	t.Helper()
	name := fmt.Sprintf("show-%02d.mkv", n)
	payload := []byte(fmt.Sprintf("d4:infod4:name%d:%s6:lengthi1eee", len(name), name))
	meta, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	size := meta.Files[0].SizeBytes
	return acquisitiondomain.FeedItem{
		GUID: fmt.Sprintf("guid-discovery-%d", n), Title: fmt.Sprintf("[Group] Test Show - %02d 1080p", n),
		Enclosure:       fmt.Sprintf("https://downloads.example.test/show-%02d.torrent", n),
		TorrentFilename: meta.TorrentName, TorrentInfoHash: meta.InfoHashV1, TorrentInfoHashV1: meta.InfoHashV1,
		TorrentVersion: meta.TorrentVersion, TorrentSizeBytes: meta.TotalSize, TorrentPayload: payload,
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{
			SourcePath: meta.Files[0].Path, Filename: meta.Files[0].Name,
			SizeBytes: &size, MediaKind: meta.Files[0].MediaKind, SourceHash: meta.InfoHashV1,
		}},
	}
}

// runDiscoveryRefresh runs one refresh of the source through the worker
// boundary.
func runDiscoveryRefresh(t *testing.T, db *sql.DB, store *workitems.Store, service *Service, sourceID, key string) {
	t.Helper()
	ctx := context.Background()
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	claim := createKeyedRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, key, version)
	outcome, err := service.Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("refresh %s outcome=%+v err=%v", key, outcome, err)
	}
	if err := store.FinishWithResult(ctx, claim, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
}

func createKeyedRSSClaim(t *testing.T, db *sql.DB, store *workitems.Store, owner workitems.OwnerRef, kind, key string, version int64) workitems.Claim {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: owner, Kind: kind, IdempotencyKey: kind + ":" + key, InputSnapshot: json.RawMessage(`{"trigger":"test"}`), MaxAttempts: 3, ExpectedOwnerVersion: version,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rss-discovery-worker", []string{kind}, time.Minute)
	if err != nil || claim.ID != id {
		t.Fatalf("claim: id=%s claim=%+v err=%v%s", id, claim, err, describeWorkItem(db, id))
	}
	return claim
}

// evaluateDueReleases runs every due release evaluation.
func evaluateDueReleases(t *testing.T, store *workitems.Store, service *Service) int {
	t.Helper()
	ran := 0
	for {
		claim, err := store.ClaimDue(context.Background(), "rss-discovery-eval", []string{workitems.KindReleaseEvaluate}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			return ran
		}
		if err != nil {
			t.Fatal(err)
		}
		finishReleaseClaim(t, store, service, claim)
		ran++
	}
}

func discoveryDue(t *testing.T, db *sql.DB, releaseID string) bool {
	t.Helper()
	var due bool
	if err := db.QueryRowContext(context.Background(), `SELECT discovery_notice_due FROM releases WHERE id = $1`, releaseID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	return due
}

// The first successful scan of a source is its baseline: what the feed
// already holds is history and admits no notice. A release first seen by a
// later scan admits exactly one release_discovered notice once it passes
// filtering; evaluating it again admits nothing more.
func TestReleaseDiscoveredSkipsBaselineAndNotifiesOnce(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{discoveryFeedItem(t, 1)}}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	notices := &discoveryNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)

	runDiscoveryRefresh(t, db, store, service, sourceID, "baseline")
	var baselineDue int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FILTER (WHERE discovery_notice_due) FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&baselineDue); err != nil {
		t.Fatal(err)
	}
	if baselineDue != 0 {
		t.Fatalf("the baseline scan marked %d releases as discoveries", baselineDue)
	}
	evaluateDueReleases(t, store, service)
	if got := notices.list(); len(got) != 0 {
		t.Fatalf("the baseline admitted notices: %+v", got)
	}

	fetcher.items = []acquisitiondomain.FeedItem{discoveryFeedItem(t, 1), discoveryFeedItem(t, 2)}
	runDiscoveryRefresh(t, db, store, service, sourceID, "second")
	var newReleaseID string
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM releases WHERE rss_source_id = $1 AND discovery_notice_due`, sourceID).Scan(&newReleaseID); err != nil {
		t.Fatalf("the second scan must mark exactly the new release: %v", err)
	}
	evaluateDueReleases(t, store, service)
	got := notices.list()
	if len(got) != 1 {
		t.Fatalf("notices=%+v, want one", got)
	}
	request := got[0]
	if request.TriggerKind != plugintargetport.TriggerReleaseDiscovered || request.SourceOwnerType != plugintargetport.OwnerRelease ||
		request.SourceOwnerID != newReleaseID || request.ManagedAnimeID != "anime-rss-test" || request.ResultKey != "anime:anime-rss-test" {
		t.Fatalf("notice=%+v", request)
	}
	var facts map[string]any
	if err := json.Unmarshal(request.Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["anime.title"] != "Test Show" || facts["release.src"] != "custom_rss" || facts["release.id"] != newReleaseID ||
		!strings.Contains(fmt.Sprint(facts["release.title"]), "Test Show - 02") {
		t.Fatalf("facts=%v", facts)
	}
	if discoveryDue(t, db, newReleaseID) {
		t.Fatal("the notice must clear the discovery mark")
	}

	// Evaluating the release again is not a new discovery.
	claim := createKeyedRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: newReleaseID}, workitems.KindReleaseEvaluate, "again", currentReleaseVersion(t, db, newReleaseID))
	finishReleaseClaim(t, store, service, claim)
	if again := notices.list(); len(again) != 1 {
		t.Fatalf("a second evaluation admitted another notice: %+v", again)
	}
}

// A discovered release that filtering rejects admits no notice, and the mark
// is dropped so a later evaluation cannot notify either.
func TestReleaseDiscoveredFilteredReleaseDoesNotNotify(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	filter, err := json.Marshal(map[string]any{"must_contain": "", "not_in": "SP"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
UPDATE anime_config_overrides
SET schema_version = 'release-rules-v1', value = $1
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`, filter); err != nil {
		t.Fatal(err)
	}
	insertReleaseForEvaluation(t, db, sourceID, "release-discovered-filtered", "[Group] Test Show - 01 SP 1080p", "https://downloads.example.test/show-01.torrent")
	if _, err := ownerTestDB(t).ExecContext(context.Background(), `UPDATE releases SET discovery_notice_due = TRUE WHERE id = 'release-discovered-filtered'`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	notices := &discoveryNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-discovered-filtered"}, workitems.KindReleaseEvaluate, 1)
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-discovered-filtered", "filtered", "filtered_by_rules", false)
	if got := notices.list(); len(got) != 0 {
		t.Fatalf("a filtered release admitted notices: %+v", got)
	}
	if discoveryDue(t, db, "release-discovered-filtered") {
		t.Fatal("filtering must drop the discovery mark")
	}
}

// A release that cannot be evaluated yet keeps its mark: a later evaluation
// decides.
func TestReleaseDiscoveredWaitsForAConclusiveEvaluation(t *testing.T) {
	for _, tc := range []struct {
		verdict      string
		notify, drop bool
	}{
		{"accepted_pending_download", true, false},
		{"filtered", false, true},
		{"subscription_stopped", false, true},
		{"parse_failed", false, false},
		{"source_unavailable", false, false},
	} {
		notify, drop := discoveryVerdictSettles(tc.verdict)
		if notify != tc.notify || drop != tc.drop {
			t.Fatalf("%s: notify=%v drop=%v, want %v %v", tc.verdict, notify, drop, tc.notify, tc.drop)
		}
	}
}
