package application

import (
	"bansoko/internal/testutil/storedsections"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	selectionapplication "bansoko/internal/selection/application"
	"bansoko/internal/workitems"
)

// TestScheduledGroupDiscoveryDedupesCadenceAndUnchanged proves scheduled group
// discovery reads each provider entry once per run even when several
// subscriptions share it, seals no new observation when only the observation
// time changed, rediscovers an airing entry on every tick but a non-airing one
// only after 24 hours, and still seals a new observation when groups change.
func TestScheduledGroupDiscoveryDedupesCadenceAndUnchanged(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	var mu sync.Mutex
	hits := map[string]int{}
	total123 := "1"
	groups123 := `[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":10,"lastUpdatedAt":1}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := r.URL.Query().Get("bgmId")
		mu.Lock()
		hits[entry]++
		body := `{"ok":true,"data":{"bgmId":` + entry + `,"total":1,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":10,"lastUpdatedAt":1}]}}`
		if entry == "123" {
			body = `{"ok":true,"data":{"bgmId":123,"total":` + total123 + `,"groups":` + groups123 + `}}`
		}
		mu.Unlock()
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	hitCount := func(entry string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[entry]
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	dispatcher := &Dispatcher{
		DB: db, WorkItems: store,
		GroupReaders: map[string]GroupReader{"anibt": dispatcherTestGroupReader{Endpoint: server.URL, Client: server.Client()}},
		Connections:  dispatcherTestConnections{{ID: "anibt-test", Kind: configurationdomain.ConnectionAniBT, Endpoint: server.URL, State: configurationdomain.ConnectionConfigured}},
		PolicyReader: selectionapplication.NewService(db),
		now:          func() time.Time { return now },
	}
	insertProviderSubscription(t, db)
	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-2', 'Second Owner'), ('anime-3', 'Old Show')`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-2'), ('anime-3') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state) VALUES ('sub-2', 'anime-2', 'anibt', '123', 'second', 'active'), ('sub-3', 'anime-3', 'anibt', '456', 'old', 'active')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
		storedsections.Seed(t, db, "anime-2")
		storedsections.Seed(t, db, "anime-3")
	}
	snapshots := func(entry string) int {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_snapshots WHERE provider = 'anibt' AND catalog_entry_ref = $1`, entry).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	lastSuccess := func(entry string) time.Time {
		var at time.Time
		if err := db.QueryRowContext(ctx, `SELECT last_success_at FROM provider_group_refresh_health WHERE provider = 'anibt' AND catalog_entry_ref = $1`, entry).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	first, err := dispatcher.dispatchGroupDiscoveryRun(ctx, true)
	if err != nil || first.errors != 0 {
		t.Fatalf("first scheduled discovery counts=%+v err=%v", first, err)
	}
	if hitCount("123") != 1 || hitCount("456") != 1 || snapshots("123") != 1 || snapshots("456") != 1 {
		t.Fatalf("shared entry must be read once: hits=%v snapshots=%d/%d", hits, snapshots("123"), snapshots("456"))
	}

	// Entry 123 is in the current season's catalog: it is airing.
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
VALUES ('season-now', 'anibt', $1, 'fresh', CURRENT_TIMESTAMP, 'test', CURRENT_TIMESTAMP, 1)`, catalogdomain.SeasonAt(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, bgm_id, title, source_hint, state, observed_at)
VALUES ('entry-now', 'season-now', 'anibt', '123', '123', 'Airing', '{}', 'present', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	before := lastSuccess("123")
	second, err := dispatcher.dispatchGroupDiscoveryRun(ctx, true)
	if err != nil || second.errors != 0 || second.skipped == 0 {
		t.Fatalf("second scheduled discovery counts=%+v err=%v", second, err)
	}
	if hitCount("123") != 2 || hitCount("456") != 1 {
		t.Fatalf("airing entry must be rediscovered and the old one skipped: hits=%v", hits)
	}
	if snapshots("123") != 1 || !lastSuccess("123").After(before) {
		t.Fatalf("unchanged groups must only re-stamp freshness: snapshots=%d last_success %s -> %s", snapshots("123"), before, lastSuccess("123"))
	}

	mu.Lock()
	total123 = "2"
	groups123 = `[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":10,"lastUpdatedAt":1},{"slug":"beta","name":"Beta","status":"active","totalReleases":3,"lastUpdatedAt":1}]`
	mu.Unlock()
	if _, err := dispatcher.dispatchGroupDiscoveryRun(ctx, true); err != nil {
		t.Fatal(err)
	}
	if snapshots("123") != 2 {
		t.Fatalf("changed groups must seal a new observation: snapshots=%d", snapshots("123"))
	}
}
