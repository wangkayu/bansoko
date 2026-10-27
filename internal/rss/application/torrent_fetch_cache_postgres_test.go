package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	acquisitionapplication "bansoko/internal/acquisition/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// cacheFixtureSourceFetcher is the HTTP fixture fetcher that can also fetch a
// single .torrent the way the explicit Release retry does.
type cacheFixtureSourceFetcher struct{ httpFixtureSourceFetcher }

func (cacheFixtureSourceFetcher) FetchSourceTorrent(ctx context.Context, _ SourceLocator, torrentURL string) (acquisitionapplication.TorrentPayload, error) {
	return (acquisitionapplication.HTTPTorrentMetadataReader{AllowPrivateHosts: []string{rssFixtureHost}, Resolver: rssFixtureResolver}).FetchPayload(ctx, torrentURL)
}

// torrentFixtureServer serves a NexusPHP-style feed whose items download from
// download.php?id=N&passkey=K and counts torrent requests per id. Synthetic
// keys only.
type torrentFixtureServer struct {
	mu       sync.Mutex
	items    string
	requests map[string]int
	server   *httptest.Server
}

func newTorrentFixtureServer(t *testing.T) *torrentFixtureServer {
	t.Helper()
	fixture := &torrentFixtureServer{requests: map[string]int{}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		switch r.URL.Path {
		case "/rss.xml":
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title>` + fixture.items + `</channel></rss>`))
		case "/download.php":
			id := r.URL.Query().Get("id")
			fixture.requests[id]++
			// Every id of one episode serves the same torrent.
			episode, _ := strconv.Atoi(id)
			if episode == 9 {
				episode = 2
			}
			name := fmt.Sprintf("show-%02d.mkv", episode)
			_, _ = w.Write([]byte(fmt.Sprintf("d4:infod4:name%d:%s6:lengthi1eee", len(name), name)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *torrentFixtureServer) host() string {
	return rssFixtureHost + ":" + f.server.URL[strings.LastIndex(f.server.URL, ":")+1:]
}

func (f *torrentFixtureServer) fetcher() cacheFixtureSourceFetcher {
	return cacheFixtureSourceFetcher{httpFixtureSourceFetcher{feedURL: "http://" + f.host() + "/rss.xml"}}
}

// item renders one feed item; a zero published time omits pubDate.
func (f *torrentFixtureServer) item(guid string, episode int, id, passkey string, published time.Time) string {
	pubDate := ""
	if !published.IsZero() {
		pubDate = "<pubDate>" + published.UTC().Format(time.RFC1123Z) + "</pubDate>"
	}
	return fmt.Sprintf(`<item><guid>%s</guid><title>[Group] Test Show - %02d 1080p</title>%s<enclosure url="http://%s/download.php?id=%s&amp;passkey=%s" type="application/x-bittorrent"/></item>`,
		guid, episode, pubDate, f.host(), id, passkey)
}

func (f *torrentFixtureServer) setItems(items ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = strings.Join(items, "")
}

func (f *torrentFixtureServer) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[id]
}

// With new-releases-only, a refresh never fetches the .torrent of an item the gate
// covers; the Release is still stored and takes start_baseline. An explicit
// retry fetches it once and evaluates it through every rule.
func TestStartBaselineItemsSkipTorrentFetchUntilRetry(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	baseline := enableNewReleasesOnly(t, 24*time.Hour)
	fixture := newTorrentFixtureServer(t)
	fixture.setItems(
		fixture.item("guid-cache-old", 1, "1", "synthetic-key-a", baseline.Add(-30*24*time.Hour)),
		fixture.item("guid-cache-new", 2, "2", "synthetic-key-a", time.Now().UTC().Add(-time.Hour)),
		fixture.item("guid-cache-undated", 3, "3", "synthetic-key-a", time.Time{}),
	)
	service, err := newRSSService(t, db, store, fixture.fetcher())
	if err != nil {
		t.Fatal(err)
	}

	runDiscoveryRefresh(t, db, store, service, sourceID, "cache-first")
	if old, fresh, undated := fixture.count("1"), fixture.count("2"), fixture.count("3"); old != 0 || fresh != 1 || undated != 0 {
		t.Fatalf("torrent requests old=%d new=%d undated=%d, want 0/1/0", old, fresh, undated)
	}
	evaluateDueReleases(t, store, service)
	oldID, oldVerdict, _, _ := releaseByKey(t, db, sourceID, "guid-cache-old")
	_, undatedVerdict, _, _ := releaseByKey(t, db, sourceID, "guid-cache-undated")
	_, newVerdict, _, _ := releaseByKey(t, db, sourceID, "guid-cache-new")
	if oldVerdict != ReleaseVerdictStartBaseline || undatedVerdict != ReleaseVerdictStartBaseline || newVerdict == ReleaseVerdictStartBaseline {
		t.Fatalf("verdicts old=%s undated=%s new=%s", oldVerdict, undatedVerdict, newVerdict)
	}

	result, err := service.RetryRelease(ctx, oldID, "retry-key-cache-old")
	if err != nil {
		t.Fatalf("retry of a baseline release without torrent: %v", err)
	}
	if fixture.count("1") != 1 {
		t.Fatalf("retry torrent requests = %d, want 1", fixture.count("1"))
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" {
		t.Fatalf("retry result = %+v, want accepted through the rules", result)
	}
	var inputs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_torrent_inputs WHERE release_id = $1`, oldID).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if inputs != 1 {
		t.Fatalf("retry stored %d torrent inputs, want 1", inputs)
	}
	if _, verdict, reason, task := releaseByKey(t, db, sourceID, "guid-cache-old"); verdict == ReleaseVerdictStartBaseline || !task || strings.HasPrefix(reason, "torrent_") {
		t.Fatalf("retried release verdict=%s reason=%s task=%v", verdict, reason, task)
	}

	// A later refresh does not fetch the admitted release again.
	runDiscoveryRefresh(t, db, store, service, sourceID, "cache-second")
	if fixture.count("1") != 1 || fixture.count("2") != 1 || fixture.count("3") != 0 {
		t.Fatalf("second refresh torrent requests old=%d new=%d undated=%d", fixture.count("1"), fixture.count("2"), fixture.count("3"))
	}
}

// A refresh never fetches the .torrent of an item the feed-layer filter
// rejects: the filter reads the feed's own facts, so the Release is filtered
// with the same reason without it, and a later refresh does not fetch it
// either. Once a rules change lets the item through, its evaluation fetches the
// torrent once and judges it with its file list, exactly as a fetched item.
func TestFeedFilteredItemsSkipTorrentFetchUntilTheyPass(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	setReleaseNotIn := func(notIn string) {
		t.Helper()
		filter, err := json.Marshal(map[string]any{"must_contain": "", "not_in": notIn})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
UPDATE anime_config_overrides SET schema_version = 'release-rules-v1', value = $1
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`, filter); err != nil {
			t.Fatal(err)
		}
	}
	setReleaseNotIn("Show - 01")
	fixture := newTorrentFixtureServer(t)
	published := time.Now().UTC().Add(-time.Hour)
	fixture.setItems(
		fixture.item("guid-prefilter-1", 1, "1", "synthetic-key-a", published),
		fixture.item("guid-prefilter-2", 2, "2", "synthetic-key-a", published),
	)
	service, err := newRSSService(t, db, store, fixture.fetcher())
	if err != nil {
		t.Fatal(err)
	}

	runDiscoveryRefresh(t, db, store, service, sourceID, "prefilter-first")
	if filtered, passed := fixture.count("1"), fixture.count("2"); filtered != 0 || passed != 1 {
		t.Fatalf("torrent requests filtered=%d passed=%d, want 0/1", filtered, passed)
	}
	evaluateDueReleases(t, store, service)
	filteredID, verdict, reason, task := releaseByKey(t, db, sourceID, "guid-prefilter-1")
	if verdict != "filtered" || reason != "filtered_by_rules" || task {
		t.Fatalf("feed-filtered release verdict=%s reason=%s task=%v, want filtered_by_rules", verdict, reason, task)
	}
	if _, verdict, _, task := releaseByKey(t, db, sourceID, "guid-prefilter-2"); verdict == "filtered" || !task {
		t.Fatalf("passing release verdict=%s task=%v, want accepted", verdict, task)
	}

	runDiscoveryRefresh(t, db, store, service, sourceID, "prefilter-second")
	if fixture.count("1") != 0 {
		t.Fatalf("a later refresh fetched the filtered torrent %d times", fixture.count("1"))
	}

	// The rules change: the automatic evaluation fetches the torrent now.
	setReleaseNotIn("")
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM releases WHERE id = $1`, filteredID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateOrMergeReleaseEvaluation(ctx, txscope.Wrap(tx), store, filteredID, version, "test"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	evaluateDueReleases(t, store, service)
	if fixture.count("1") != 1 {
		t.Fatalf("evaluation torrent requests = %d, want 1", fixture.count("1"))
	}
	if _, verdict, reason, task := releaseByKey(t, db, sourceID, "guid-prefilter-1"); verdict == "filtered" || !task || strings.HasPrefix(reason, "torrent_") {
		t.Fatalf("re-evaluated release verdict=%s reason=%s task=%v, want accepted with its file list", verdict, reason, task)
	}
	var inputs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_torrent_inputs WHERE release_id = $1`, filteredID).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if inputs != 1 {
		t.Fatalf("evaluation stored %d torrent inputs, want 1", inputs)
	}
}

// A rotated passkey changes no torrent: a known, not yet evaluated Release
// reuses its stored metadata without a request. A different torrent id is a
// different source and is fetched.
func TestTorrentReuseSurvivesCredentialRotation(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newTorrentFixtureServer(t)
	published := time.Now().UTC().Add(-time.Hour)
	fixture.setItems(fixture.item("guid-rotate", 2, "2", "synthetic-key-a", published))
	service, err := newRSSService(t, db, store, fixture.fetcher())
	if err != nil {
		t.Fatal(err)
	}

	runDiscoveryRefresh(t, db, store, service, sourceID, "rotate-first")
	if fixture.count("2") != 1 {
		t.Fatalf("first refresh torrent requests = %d, want 1", fixture.count("2"))
	}

	fixture.setItems(fixture.item("guid-rotate", 2, "2", "synthetic-key-b", published))
	runDiscoveryRefresh(t, db, store, service, sourceID, "rotate-second")
	if fixture.count("2") != 1 {
		t.Fatalf("rotated passkey refetched: torrent requests = %d, want 1", fixture.count("2"))
	}
	if _, verdict, reason, _ := releaseByKey(t, db, sourceID, "guid-rotate"); strings.HasPrefix(reason, "torrent_") {
		t.Fatalf("rotated passkey release verdict=%s reason=%s", verdict, reason)
	}

	fixture.setItems(fixture.item("guid-rotate", 2, "9", "synthetic-key-b", published))
	runDiscoveryRefresh(t, db, store, service, sourceID, "rotate-third")
	if fixture.count("9") != 1 {
		t.Fatalf("different torrent id requests = %d, want 1", fixture.count("9"))
	}
}
