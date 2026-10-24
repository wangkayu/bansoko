package application

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	selectionapplication "bansoko/internal/selection/application"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// A group that newly appears and scores higher under the live site policy
// (here through a Global name keyword) takes the only max_rss slot from the
// lower-scored group on the next discovery; the Global site policy is read
// live, not from the instance section.
func TestDiscoveryHigherScoredNewGroupReplacesLowerWithinMaxRSS(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":1,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":10,"lastUpdatedAt":1}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":2,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":10,"lastUpdatedAt":1},{"slug":"zeta","name":"Zeta 简繁","status":"active","totalReleases":2,"lastUpdatedAt":2}]}}`))
	}))
	defer server.Close()
	connections := dispatcherTestConnections{{
		ID: "anibt-test", Kind: configurationdomain.ConnectionAniBT, Endpoint: server.URL,
		State: configurationdomain.ConnectionConfigured,
	}}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB: db, WorkItems: store,
		GroupReaders: map[string]GroupReader{
			"anibt": dispatcherTestGroupReader{Endpoint: server.URL, Client: server.Client()},
		},
		Connections: connections, PolicyReader: selectionapplication.NewService(db),
	}
	insertProviderSubscription(t, db)
	insertSubgroupsAnimeOverride(t, db, generated.SubgroupsSection{MaxRss: 1, Entries: []generated.SubgroupScoreEntry{}})
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value)
VALUES ('subgroups', 'subgroups-v1', '{"entries":[],"max_rss":3,"name_keywords":{"anibt":[{"keyword":"简繁","score":20}]}}')`); err != nil {
		t.Fatal(err)
	}

	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	assertSiteSelection(t, db, map[string]siteSelectionWant{"alpha": {selected: true, score: 50}})

	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	assertSiteSelection(t, db, map[string]siteSelectionWant{
		"alpha": {selected: false, score: 50},
		"zeta":  {selected: true, score: 70},
	})
}

type siteSelectionWant struct {
	selected bool
	score    int
}

func assertSiteSelection(t *testing.T, db *sql.DB, want map[string]siteSelectionWant) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT group_ref, effective_selected, effective_score FROM provider_group_selections WHERE subscription_id = 'sub-1' ORDER BY group_ref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]siteSelectionWant{}
	for rows.Next() {
		var ref string
		var value siteSelectionWant
		if err := rows.Scan(&ref, &value.selected, &value.score); err != nil {
			t.Fatal(err)
		}
		got[ref] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("selections=%+v, want %+v", got, want)
	}
	for ref, value := range want {
		if got[ref] != value {
			t.Fatalf("selection %s=%+v, want %+v (all %+v)", ref, got[ref], value, got)
		}
	}
}
