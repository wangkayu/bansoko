package application

import (
	"bansoko/internal/testutil/storedsections"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/events"
	providerdomain "bansoko/internal/provider/domain"
	selectionapplication "bansoko/internal/selection/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

type dispatcherTestConnections []configurationdomain.Connection

type dispatcherTestGroupReader struct {
	Endpoint string
	Client   *http.Client
}

type staticDispatcherGroupReader struct {
	result providerdomain.GroupReadResult
}

func (r staticDispatcherGroupReader) ReadGroups(context.Context, string) (providerdomain.GroupReadResult, error) {
	return r.result, nil
}

func (r dispatcherTestGroupReader) ReadGroups(ctx context.Context, catalogEntryRef string) (providerdomain.GroupReadResult, error) {
	endpoint, err := url.Parse(r.Endpoint + "/api/anime/groups")
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	query := endpoint.Query()
	query.Set("bgmId", catalogEntryRef)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	response, err := r.Client.Do(request)
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerdomain.GroupReadResult{}, fmt.Errorf("test group reader returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data struct {
			BGMID  int64 `json:"bgmId"`
			Groups []struct {
				Slug string `json:"slug"`
				Name string `json:"name"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	if payload.Data.BGMID == 0 {
		return providerdomain.GroupReadResult{}, errors.New("test group reader response has no BGM ID")
	}
	result := providerdomain.GroupReadResult{
		Provider: providerdomain.ProviderAniBT, CatalogEntryRef: catalogEntryRef,
		ObservedAt: time.Now().UTC(), SchemaVersion: "anibt-anime-groups-v1", Groups: make([]providerdomain.GroupSnapshotEntry, 0, len(payload.Data.Groups)),
	}
	for _, item := range payload.Data.Groups {
		ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, item.Slug)
		if err != nil {
			return providerdomain.GroupReadResult{}, err
		}
		result.Groups = append(result.Groups, providerdomain.GroupSnapshotEntry{
			Ref: ref, DisplayName: item.Name, ObservedAt: result.ObservedAt,
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: catalogEntryRef, Group: ref},
		})
	}
	return result, nil
}

func (s dispatcherTestConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return append([]configurationdomain.Connection(nil), s...), nil
}

func openDispatcherTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

// ownerTestDB returns the schema-owner handle for a test-only DDL path.
func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.Owner(t)
}

func TestDispatchGroupDiscoveryAppliesPoliciesAndRemainsIdempotent(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/anime/groups" || r.URL.Query().Get("bgmId") != "123" {
			t.Fatalf("unexpected group request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":3,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":10,"lastUpdatedAt":1},{"slug":"beta","name":"Beta","status":"active","totalReleases":8,"lastUpdatedAt":1},{"slug":"gamma","name":"Gamma","status":"active","totalReleases":6,"lastUpdatedAt":1}]}}`))
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
		Connections:  connections,
		PolicyReader: selectionapplication.NewService(db),
	}

	insertProviderSubscription(t, db)
	insertSubgroupsAnimeOverride(t, db, generated.SubgroupsSection{
		MaxRss: 1,
		Entries: []generated.SubgroupScoreEntry{
			{Provider: generated.ProviderKindAnibt, GroupRef: "alpha", DisplayName: "Alpha", SelectionMode: generated.SelectionModeAuto, Score: 90, CountInLimit: true},
			{Provider: generated.ProviderKindAnibt, GroupRef: "beta", DisplayName: "Beta", SelectionMode: generated.SelectionModeAuto, Score: 80, CountInLimit: true},
			{Provider: generated.ProviderKindAnibt, GroupRef: "gamma", DisplayName: "Gamma", SelectionMode: generated.SelectionModeManual, ManualSelected: boolPointer(true), CountInLimit: false},
		},
	})

	counts, err := dispatcher.dispatchGroupDiscovery(ctx)
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	if counts.targetCount != 1 || counts.errors != 0 {
		t.Fatalf("unexpected first discovery counts: %+v", counts)
	}
	assertDiscoveryRows(t, db, 3, 3, 2)

	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("repeat discovery: %v", err)
	}
	assertDiscoveryRows(t, db, 3, 3, 2)
	assertProviderSourceProvenance(t, db, "v2")

	insertSubgroupsAnimeOverride(t, db, generated.SubgroupsSection{
		MaxRss: 1,
		Entries: []generated.SubgroupScoreEntry{
			{Provider: generated.ProviderKindAnibt, GroupRef: "alpha", DisplayName: "Alpha", SelectionMode: generated.SelectionModeManual, ManualSelected: boolPointer(false), CountInLimit: false},
			{Provider: generated.ProviderKindAnibt, GroupRef: "beta", DisplayName: "Beta", SelectionMode: generated.SelectionModeManual, ManualSelected: boolPointer(true), CountInLimit: false},
			{Provider: generated.ProviderKindAnibt, GroupRef: "gamma", DisplayName: "Gamma", SelectionMode: generated.SelectionModeManual, ManualSelected: boolPointer(false), CountInLimit: false},
		},
	})
	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("override discovery: %v", err)
	}
	assertDiscoveryRows(t, db, 3, 3, 3)

	refreshCounts, err := dispatcher.dispatchRSSRefresh(ctx)
	if err != nil {
		t.Fatalf("refresh after override: %v", err)
	}
	// Global manual-shaped policy is derived preference, not persisted user
	// intent. The anime override excludes gamma; only beta remains in scope.
	if refreshCounts.targetCount != 1 || refreshCounts.enqueued != 0 || refreshCounts.merged != 1 || refreshCounts.errors != 0 {
		t.Fatalf("refresh must target only beta and merge its live work item: %+v", refreshCounts)
	}

	var alphaSelected, betaSelected, gammaSelected bool
	rows, err := db.QueryContext(ctx, `SELECT group_ref, effective_selected FROM provider_group_selections ORDER BY group_ref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var selected bool
		if err := rows.Scan(&ref, &selected); err != nil {
			t.Fatal(err)
		}
		switch ref {
		case "alpha":
			alphaSelected = selected
		case "beta":
			betaSelected = selected
		case "gamma":
			gammaSelected = selected
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if alphaSelected || !betaSelected || gammaSelected {
		t.Fatalf("anime override scope must contain only beta; derived global include is not manual intent: alpha=%t beta=%t gamma=%t", alphaSelected, betaSelected, gammaSelected)
	}
}

func TestDispatchGroupDiscoveryPreservesManualSelectionUntilRestoreAuto(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	displayName := "Alpha Renamed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":1,"groups":[{"slug":"alpha","name":"` + displayName + `","status":"active","totalReleases":10,"lastUpdatedAt":1}]}}`))
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
	insertSubgroupsAnimeOverride(t, db, generated.SubgroupsSection{MaxRss: 1, Entries: []generated.SubgroupScoreEntry{
		{Provider: generated.ProviderKindAnibt, GroupRef: "alpha", DisplayName: "Policy Alpha", SelectionMode: generated.SelectionModeAuto, Score: 90, CountInLimit: true},
	}})
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_group_selections(
    id, subscription_id, provider, group_ref, display_name, selection_mode,
    manual_selected, effective_selected, effective_score, count_in_limit
) VALUES ('manual-alpha', 'sub-1', 'anibt', 'alpha', 'Old Alpha', 'manual', FALSE, FALSE, 1, FALSE)`); err != nil {
		t.Fatal(err)
	}

	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("manual-preserving discovery: %v", err)
	}
	var mode, storedDisplay string
	var manual sql.NullBool
	var effective, countInLimit bool
	var score int
	if err := db.QueryRowContext(ctx, `
SELECT selection_mode, manual_selected, effective_selected, effective_score, count_in_limit, display_name
FROM provider_group_selections WHERE id = 'manual-alpha'`).Scan(
		&mode, &manual, &effective, &score, &countInLimit, &storedDisplay); err != nil {
		t.Fatal(err)
	}
	if mode != "manual" || !manual.Valid || manual.Bool || effective || countInLimit {
		t.Fatalf("manual-owned fields were overwritten: mode=%s manual=%v effective=%t count=%t", mode, manual, effective, countInLimit)
	}
	// An automatic score is the live site default (code default 50); the
	// stored entry score 90 is a projection, not an input.
	if score != 50 || storedDisplay != displayName {
		t.Fatalf("automatic facts were not refreshed: score=%d display=%q", score, storedDisplay)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE provider_group_selections
SET selection_mode = 'auto', manual_selected = NULL, effective_selected = FALSE, count_in_limit = TRUE
WHERE id = 'manual-alpha'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("restore-auto discovery: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT selection_mode, manual_selected, effective_selected, effective_score, count_in_limit
FROM provider_group_selections WHERE id = 'manual-alpha'`).Scan(
		&mode, &manual, &effective, &score, &countInLimit); err != nil {
		t.Fatal(err)
	}
	if mode != "auto" || manual.Valid || !effective || score != 50 || !countInLimit {
		t.Fatalf("restored auto selection did not resume reconciliation: mode=%s manual=%v effective=%t score=%d count=%t", mode, manual, effective, score, countInLimit)
	}
}

func TestGroupDiscoveryRetainsLastGoodStateAfterProviderFailure(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":2,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":1,"lastUpdatedAt":1},{"slug":"beta","name":"Beta","status":"active","totalReleases":1,"lastUpdatedAt":1}]}}`))
	}))
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
		Connections:  connections,
		PolicyReader: selectionapplication.NewService(db),
	}
	insertProviderSubscription(t, db)
	insertSubgroupsAnimeOverride(t, db, generated.SubgroupsSection{MaxRss: 1, Entries: []generated.SubgroupScoreEntry{}})
	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("initial discovery: %v", err)
	}
	server.Close()

	counts, err := dispatcher.dispatchGroupDiscovery(ctx)
	if err != nil {
		t.Fatalf("failed discovery should be recorded per target: %v", err)
	}
	if counts.errors != 1 {
		t.Fatalf("failed discovery must report one target error: %+v", counts)
	}
	assertDiscoveryRows(t, db, 2, 2, 1)
	var snapshotCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 1 {
		t.Fatalf("provider failure must retain the last-good snapshot, got %d snapshots", snapshotCount)
	}
	var state, errorCode string
	if err := db.QueryRowContext(ctx, `SELECT state, error_code FROM provider_group_refresh_health WHERE provider = 'anibt' AND catalog_entry_ref = '123'`).Scan(&state, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != "error" || errorCode != "PROVIDER_GROUP_READ_FAILED" {
		t.Fatalf("provider failure health was not recorded: state=%q code=%q", state, errorCode)
	}
}

func TestStoppedSubscriptionDoesNotReceiveDiscoveryWork(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":123,"total":1,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":1,"lastUpdatedAt":1}]}}`))
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
		Connections:  connections,
		PolicyReader: selectionapplication.NewService(db),
	}
	insertProviderSubscription(t, db)
	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatalf("initial discovery: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'stopped' WHERE id = 'sub-1'`); err != nil {
		t.Fatal(err)
	}
	counts, err := dispatcher.dispatchGroupDiscovery(ctx)
	if err != nil {
		t.Fatalf("stopped discovery: %v", err)
	}
	if counts.targetCount != 0 || counts.enqueued != 0 || counts.errors != 0 {
		t.Fatalf("stopped subscription must not be a discovery target: %+v", counts)
	}
	assertDiscoveryRows(t, db, 1, 1, 1)
}

func TestDispatchRSSRefreshRequiresCurrentEffectiveSelection(t *testing.T) {
	tests := []struct {
		name       string
		sourceKind string
		provider   string
		groupRef   string
		selected   *bool
		stopped    bool
		wantTarget int
	}{
		{name: "active custom", sourceKind: "custom_rss", wantTarget: 1},
		{name: "provider selection missing", sourceKind: "provider_group", provider: "anibt", groupRef: "alpha"},
		{name: "provider selection false", sourceKind: "provider_group", provider: "anibt", groupRef: "alpha", selected: boolPointer(false)},
		{name: "provider selection mismatched", sourceKind: "provider_group", provider: "anibt", groupRef: "alpha", selected: boolPointer(true)},
		{name: "provider selection true", sourceKind: "provider_group", provider: "anibt", groupRef: "alpha", selected: boolPointer(true), wantTarget: 1},
		{name: "stopped custom", sourceKind: "custom_rss", stopped: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openDispatcherTestDB(t)
			ctx := context.Background()
			state := "active"
			if test.stopped {
				state = "stopped"
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh', 'Refresh Test')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-refresh', 'anime-refresh', 'custom', 'Refresh', $1)`, state); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `
			INSERT INTO rss_sources(
			    id, subscription_id, source_kind, locator, locator_display, group_name, title,
			    provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version
			) VALUES ('source-refresh', 'sub-refresh', $1, 'https://example.test/feed', 'https://example.test/feed', 'Group', 'Refresh Test',
			          NULLIF($2, ''), CASE WHEN $2 = '' THEN NULL ELSE '123' END, NULLIF($3, ''), CASE WHEN $2 = '' THEN NULL ELSE 'v1' END)`,
				test.sourceKind, test.provider, test.groupRef); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-refresh')`); err != nil {
				t.Fatal(err)
			}
			if test.selected != nil {
				selectionGroup := test.groupRef
				if test.name == "provider selection mismatched" {
					selectionGroup = "beta"
				}
				if _, err := db.ExecContext(ctx, `
				INSERT INTO provider_group_selections(
				    id, subscription_id, provider, group_ref, display_name, selection_mode,
				    manual_selected, effective_selected, effective_score, count_in_limit
				) VALUES ('selection-refresh', 'sub-refresh', 'anibt', $1, 'Group', 'auto', NULL, $2, 50, TRUE)`, selectionGroup, *test.selected); err != nil {
					t.Fatal(err)
				}
			}
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			counts, err := (&Dispatcher{DB: db, WorkItems: store}).dispatchRSSRefresh(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if counts.targetCount != test.wantTarget || counts.enqueued != test.wantTarget || counts.errors != 0 {
				t.Fatalf("dispatch counts=%+v, want target/enqueued=%d", counts, test.wantTarget)
			}
			var workCount int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'source-refresh' AND kind = 'rss_refresh'`).Scan(&workCount); err != nil {
				t.Fatal(err)
			}
			if workCount != test.wantTarget {
				t.Fatalf("refresh work count=%d, want %d", workCount, test.wantTarget)
			}
		})
	}
}

func TestDiscoverGroupsReportsPartialProviderError(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	observedAt := time.Now().UTC()
	groups := make([]providerdomain.GroupSnapshotEntry, 0, 2)
	for _, externalID := range []string{"alpha", "beta"} {
		ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, externalID)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, providerdomain.GroupSnapshotEntry{
			Ref: ref, DisplayName: externalID, ObservedAt: observedAt,
			Active:      boolPointer(true),
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Group: ref},
		})
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `
CREATE FUNCTION test_fail_beta_provider_source() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.provider_group_ref = 'beta' THEN
        RAISE EXCEPTION 'test beta provider source failure';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER test_fail_beta_provider_source_trigger
BEFORE INSERT OR UPDATE ON rss_sources
FOR EACH ROW EXECUTE FUNCTION test_fail_beta_provider_source()`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB: db, WorkItems: store,
		GroupReaders: map[string]GroupReader{"anibt": staticDispatcherGroupReader{result: providerdomain.GroupReadResult{
			Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", ObservedAt: observedAt,
			SchemaVersion: "anibt-groups-v1", Groups: groups,
			Complete: true, CompletenessEvidence: "test-full-entry",
		}}},
		Connections: dispatcherTestConnections{{Kind: configurationdomain.ConnectionAniBT, Endpoint: "https://anibt.example", State: configurationdomain.ConnectionConfigured}},
	}

	result, err := dispatcher.DiscoverGroups(ctx, "anime-1")
	if err != nil {
		t.Fatalf("partial discovery returned error: %v", err)
	}
	if result.Outcome != "provider_error" || result.DiscoveredCount != 0 {
		t.Fatalf("failed discovery=%+v, want zero committed groups", result)
	}
	var selections, sources, workItems int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM provider_group_selections`: &selections,
		`SELECT COUNT(*) FROM rss_sources`:               &sources,
		`SELECT COUNT(*) FROM work_item_history`:         &workItems,
	} {
		if err := db.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if selections != 0 || sources != 0 || workItems != 0 {
		t.Fatalf("failed discovery committed partial rows: %d/%d/%d", selections, sources, workItems)
	}
}

func TestCreateProviderGroupSourceRechecksSelection(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
	INSERT INTO provider_group_selections(
	    id, subscription_id, provider, group_ref, display_name, selection_mode,
	    manual_selected, effective_selected, effective_score, count_in_limit
	) VALUES ('selection-disabled', 'sub-1', 'anibt', 'alpha', 'Alpha', 'auto', NULL, FALSE, 50, TRUE)`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := (&Dispatcher{DB: db, WorkItems: store}).createProviderGroupSource(
		ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha", "Test Anime",
		"https://anibt.example/rss/123/alpha", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("disabled selection must not create refresh work from a stale decision")
	}
	assertDiscoveryRows(t, db, 1, 1, 0)
}

func TestCreateProviderGroupSourcePublishesRSSInvalidationAfterCommit(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO provider_group_selections(
		    id, subscription_id, provider, group_ref, display_name, selection_mode,
		    manual_selected, effective_selected, effective_score, count_in_limit
		) VALUES ('selection-rss-event', 'sub-1', 'anibt', 'alpha', 'Alpha', 'auto', NULL, TRUE, 50, TRUE)`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	eventsCh, cancel := hub.Subscribe()
	defer cancel()
	dispatcher := &Dispatcher{DB: db, WorkItems: store}
	dispatcher.SetEventHub(hub)
	if _, err := dispatcher.createProviderGroupSource(
		ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha", "Test Anime",
		"https://anibt.example/rss/123/alpha", "v1"); err != nil {
		t.Fatalf("create provider source: %v", err)
	}

	select {
	case event := <-eventsCh:
		if event.Target != events.TargetRSS {
			t.Fatalf("invalidation target=%q, want %q", event.Target, events.TargetRSS)
		}
	case <-time.After(time.Second):
		t.Fatal("provider source commit did not publish RSS invalidation")
	}
	var sourceCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources WHERE source_kind = 'provider_group'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("source count=%d after invalidation, want committed source", sourceCount)
	}
}

func TestCreateProviderGroupSourceUsesStableProviderIdentity(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO provider_group_selections(
		    id, subscription_id, provider, group_ref, display_name, selection_mode,
		    manual_selected, effective_selected, effective_score, count_in_limit
		) VALUES ('selection-alpha', 'sub-1', 'anibt', 'alpha', 'Alpha', 'auto', NULL, TRUE, 50, TRUE)`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{DB: db, WorkItems: store}

	if _, err := dispatcher.createProviderGroupSource(ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha legacy", "Test Anime legacy", "https://feed.example/legacy", "legacy-current"); err != nil {
		t.Fatalf("first source upsert with malformed current snapshot: %v", err)
	}
	var sourceID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM rss_sources WHERE source_kind = 'provider_group'`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
		VALUES ('release-stable-source', $1, 'stable-key', '{}', '{}', 'discovered', 'new')`, sourceID); err != nil {
		t.Fatalf("seed source release: %v", err)
	}
	if _, err := dispatcher.createProviderGroupSource(ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha v2", "Test Anime v2", "https://feed.example/a", "v2"); err != nil {
		t.Fatalf("same locator snapshot update: %v", err)
	}
	if _, err := dispatcher.createProviderGroupSource(ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha v3", "Test Anime v3", "https://feed.example/b", "v3"); err != nil {
		t.Fatalf("locator-change source upsert: %v", err)
	}
	if _, err := dispatcher.createProviderGroupSource(ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha malformed", "Test Anime malformed", "https://feed.example/malformed", "malformed-incoming"); err != nil {
		t.Fatalf("malformed incoming snapshot upsert: %v", err)
	}
	if _, err := dispatcher.createProviderGroupSource(ctx, "sub-1", "anibt", "123", "alpha", "alpha", "Alpha stale", "Test Anime stale", "https://feed.example/stale", "v2"); err != nil {
		t.Fatalf("stale snapshot source upsert: %v", err)
	}

	var gotID, locator, groupName, title, snapshot string
	var provenanceProvider, provenanceEntry, provenanceGroup, provenanceSnapshot string
	if err := db.QueryRowContext(ctx, `
		SELECT id, locator, group_name, title, discovery_snapshot_version,
		       provenance ->> 'provider', provenance ->> 'provider_entry_ref',
		       provenance ->> 'provider_group_ref', provenance ->> 'discovery_snapshot_version'
		FROM rss_sources WHERE source_kind = 'provider_group'`).Scan(
		&gotID, &locator, &groupName, &title, &snapshot,
		&provenanceProvider, &provenanceEntry, &provenanceGroup, &provenanceSnapshot); err != nil {
		t.Fatal(err)
	}
	if gotID != sourceID || locator != "https://feed.example/b" || groupName != "Alpha v3" || title != "Test Anime v3" || snapshot != "v3" {
		t.Fatalf("source identity/update mismatch: id=%q locator=%q group=%q title=%q snapshot=%q", gotID, locator, groupName, title, snapshot)
	}
	if provenanceProvider != "anibt" || provenanceEntry != "123" || provenanceGroup != "alpha" || provenanceSnapshot != "v3" {
		t.Fatalf("provenance mismatch: %q/%q/%q/%q", provenanceProvider, provenanceEntry, provenanceGroup, provenanceSnapshot)
	}
	assertDiscoveryRows(t, db, 1, 1, 1)
	var releaseSourceID, workOwnerID, runtimeID string
	if err := db.QueryRowContext(ctx, `SELECT rss_source_id FROM releases WHERE id = 'release-stable-source'`).Scan(&releaseSourceID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT rss_source_id FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT owner_id
		FROM work_items
		WHERE owner_type = 'rss_source_runtime' AND kind = 'rss_refresh' AND state IN ('available', 'leased')
		LIMIT 1`).Scan(&workOwnerID); err != nil {
		t.Fatal(err)
	}
	if releaseSourceID != sourceID || runtimeID != sourceID || workOwnerID != sourceID {
		t.Fatalf("owner chain changed after locator update: source=%q release=%q runtime=%q work=%q", sourceID, releaseSourceID, runtimeID, workOwnerID)
	}
	for _, tc := range []struct {
		name, provider, entry, group string
	}{
		{"missing group", "anibt", "123", ""},
		{"invalid group", "anibt", "123", "bad slug"},
		{"non-normalized catalog", "anibt", "00123", "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := dispatcher.createProviderGroupSource(ctx, "sub-1", tc.provider, tc.entry, tc.group, tc.group, "Alpha", "Test Anime", "https://feed.example/c", "v4"); err == nil {
				t.Fatal("invalid provider identity accepted")
			}
		})
	}
}

func TestValidEmptyDiscoveryPersistsSnapshotAndRetainsLastGoodSources(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	response := `{"ok":true,"data":{"bgmId":123,"total":1,"groups":[{"slug":"alpha","name":"Alpha","status":"active","totalReleases":1,"lastUpdatedAt":1}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
	defer server.Close()
	connections := dispatcherTestConnections{{ID: "anibt-test", Kind: configurationdomain.ConnectionAniBT, Endpoint: server.URL, State: configurationdomain.ConnectionConfigured}}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB: db, WorkItems: store,
		GroupReaders: map[string]GroupReader{"anibt": dispatcherTestGroupReader{Endpoint: server.URL, Client: server.Client()}},
		Connections:  connections, PolicyReader: selectionapplication.NewService(db),
	}
	insertProviderSubscription(t, db)
	insertSubgroupsAnimeOverride(t, db, generated.SubgroupsSection{MaxRss: 1, Entries: []generated.SubgroupScoreEntry{}})
	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatal(err)
	}
	response = `{"ok":true,"data":{"bgmId":123,"total":0,"groups":[]}}`
	if _, err := dispatcher.dispatchGroupDiscovery(ctx); err != nil {
		t.Fatal(err)
	}
	var snapshots, emptySnapshots, selections, sources int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM provider_group_snapshots`:                       &snapshots,
		`SELECT COUNT(*) FROM provider_group_snapshots WHERE group_count = 0`: &emptySnapshots,
		`SELECT COUNT(*) FROM provider_group_selections`:                      &selections,
		`SELECT COUNT(*) FROM rss_sources`:                                    &sources,
	} {
		if err := db.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if snapshots != 2 || emptySnapshots != 1 || selections != 1 || sources != 1 {
		t.Fatalf("valid empty retention snapshots=%d empty=%d selections=%d sources=%d", snapshots, emptySnapshots, selections, sources)
	}
	var health string
	var errorCode sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, error_code FROM provider_group_refresh_health WHERE provider = 'anibt' AND catalog_entry_ref = '123'`).Scan(&health, &errorCode); err != nil {
		t.Fatal(err)
	}
	if health != "empty" || errorCode.Valid {
		t.Fatalf("valid empty health=%q error=%v", health, errorCode)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `SELECT public.work_item_close(id, 'succeeded') FROM work_items`); err != nil {
		t.Fatal(err)
	}
	counts, err := dispatcher.dispatchRSSRefresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.targetCount != 1 || counts.enqueued != 1 {
		t.Fatalf("retained selected source must remain dispatchable: %+v", counts)
	}
}

func insertProviderSubscription(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Test Anime')`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-1') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state) VALUES ('sub-1', 'anime-1', 'anibt', '123', 'active')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("insert subscription fixture: %v", err)
		}
	}
	storedsections.Seed(t, db, "anime-1")
}

// insertSubgroupsAnimeOverride stores the subscription's own subgroups section
// (every per-anime section is one-time input; Global is never read).
func insertSubgroupsAnimeOverride(t *testing.T, db *sql.DB, section generated.SubgroupsSection) {
	t.Helper()
	insertSubgroupsSection(t, db, `INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value) VALUES ('anime-1', 'subgroups', 'subgroups', 'subgroups-v1', $1)
ON CONFLICT (managed_anime_id, section_name) DO UPDATE SET schema_version = EXCLUDED.schema_version, value = EXCLUDED.value, updated_at = NOW()`, section)
}

func insertSubgroupsSection(t *testing.T, db *sql.DB, statement string, section generated.SubgroupsSection) {
	t.Helper()
	raw, err := json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), statement, raw); err != nil {
		t.Fatalf("insert subgroup section: %v", err)
	}
}

func assertAnimeSubgroupsEntries(t *testing.T, db *sql.DB, wantRefs []string) {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT value FROM anime_config_overrides WHERE managed_anime_id = 'anime-1' AND section_name = 'subgroups'`).Scan(&raw); err != nil {
		t.Fatalf("read anime subgroups override: %v", err)
	}
	var section generated.SubgroupsSection
	if err := json.Unmarshal(raw, &section); err != nil {
		t.Fatalf("decode anime subgroups override: %v", err)
	}
	gotRefs := make([]string, 0, len(section.Entries))
	for _, e := range section.Entries {
		gotRefs = append(gotRefs, e.GroupRef)
	}
	if len(gotRefs) != len(wantRefs) {
		t.Fatalf("anime subgroups entries=%v, want %v", gotRefs, wantRefs)
	}
	for i := range wantRefs {
		if gotRefs[i] != wantRefs[i] {
			t.Fatalf("anime subgroups entries=%v, want %v", gotRefs, wantRefs)
		}
	}
}
func assertProviderSourceProvenance(t *testing.T, db *sql.DB, wantSnapshot string) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
SELECT provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version,
       provenance ->> 'provider', provenance ->> 'provider_entry_ref',
       provenance ->> 'provider_group_ref', provenance ->> 'discovery_snapshot_version'
FROM rss_sources
WHERE source_kind = 'provider_group'
ORDER BY provider_group_ref`)
	if err != nil {
		t.Fatalf("read provider source provenance: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var provider, entryRef, groupRef, snapshot string
		var provenanceProvider, provenanceEntryRef, provenanceGroupRef, provenanceSnapshot string
		if err := rows.Scan(&provider, &entryRef, &groupRef, &snapshot,
			&provenanceProvider, &provenanceEntryRef, &provenanceGroupRef, &provenanceSnapshot); err != nil {
			t.Fatalf("scan provider source provenance: %v", err)
		}
		count++
		if provider != "anibt" || provider != provenanceProvider ||
			entryRef != "123" || entryRef != provenanceEntryRef ||
			groupRef == "" || groupRef != provenanceGroupRef ||
			snapshot != wantSnapshot || snapshot != provenanceSnapshot {
			t.Fatalf("unexpected provider source provenance: provider=%q/%q entry=%q/%q group=%q/%q snapshot=%q/%q", provider, provenanceProvider, entryRef, provenanceEntryRef, groupRef, provenanceGroupRef, snapshot, provenanceSnapshot)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate provider source provenance: %v", err)
	}
	if count != 3 {
		t.Fatalf("provider source provenance count=%d, want 3", count)
	}
}

func assertDiscoveryRows(t *testing.T, db *sql.DB, selections, sources, workItems int) {
	t.Helper()
	ctx := context.Background()
	for table, want := range map[string]int{
		"provider_group_selections": selections,
		"rss_sources":               sources,
		"work_items":                workItems,
	} {
		var got int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count=%d, want %d", table, got, want)
		}
	}
	var runtimeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_source_runtime`).Scan(&runtimeCount); err != nil {
		t.Fatalf("count rss source runtimes: %v", err)
	}
	if runtimeCount != sources {
		t.Fatalf("rss source runtime count=%d, want %d", runtimeCount, sources)
	}
	var liveWorkItems, maxLivePerOwner int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM work_items
WHERE owner_type = 'rss_source_runtime'
  AND kind = 'rss_refresh'
  AND state IN ('available', 'leased')`).Scan(&liveWorkItems); err != nil {
		t.Fatalf("count live rss refresh work items: %v", err)
	}
	if liveWorkItems > sources {
		t.Fatalf("live rss refresh work items=%d, cannot exceed %d sources", liveWorkItems, sources)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(live_count), 0)
FROM (
    SELECT owner_id, COUNT(*) AS live_count
    FROM work_items
    WHERE owner_type = 'rss_source_runtime'
      AND kind = 'rss_refresh'
      AND state IN ('available', 'leased')
    GROUP BY owner_id
) live`).Scan(&maxLivePerOwner); err != nil {
		t.Fatalf("check live rss refresh uniqueness: %v", err)
	}
	if maxLivePerOwner > 1 {
		t.Fatalf("max live rss refresh work items per runtime=%d, want at most 1", maxLivePerOwner)
	}
}

func TestDispatchDueUsesDatabaseDueBoundary(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = (kind = 'rss_refresh'),
    interval_value = CASE WHEN kind = 'rss_refresh' THEN 5 ELSE interval_value END,
    interval_unit = CASE WHEN kind = 'rss_refresh' THEN 'minute' ELSE interval_unit END,
    next_run_at = CASE WHEN kind = 'rss_refresh' THEN CURRENT_TIMESTAMP + INTERVAL '1 hour' ELSE NULL END`); err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{DB: db}
	count, err := dispatcher.DispatchDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("database-future schedule dispatched: %d", count)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET next_run_at = CURRENT_TIMESTAMP - INTERVAL '5 seconds'
WHERE kind = 'rss_refresh'`); err != nil {
		t.Fatal(err)
	}
	count, err = dispatcher.DispatchDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("database-due schedule count=%d, want 1", count)
	}
	var runs int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM automation_dispatch_runs
WHERE kind = 'rss_refresh' AND trigger = 'scheduled' AND state = 'completed'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("completed scheduled runs=%d, want 1", runs)
	}
}

func TestDispatchOneRechecksDueAfterScheduleLock(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = TRUE, next_run_at = CURRENT_TIMESTAMP
WHERE kind = 'rss_refresh'`); err != nil {
		t.Fatal(err)
	}
	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback()
	if _, err := lockTx.ExecContext(ctx, `
SELECT kind FROM automation_schedules WHERE kind = 'rss_refresh' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- (&Dispatcher{DB: db}).dispatchOne(ctx, "rss_refresh", 5, "minute", time.Now().UTC())
	}()
	<-started
	if _, err := lockTx.ExecContext(ctx, `
UPDATE automation_schedules
SET next_run_at = CURRENT_TIMESTAMP + INTERVAL '1 hour'
WHERE kind = 'rss_refresh'`); err != nil {
		t.Fatal(err)
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch second check did not complete")
	}
	var runs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_dispatch_runs WHERE kind = 'rss_refresh'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("locked second check created %d runs for a no-longer-due schedule", runs)
	}
}

func TestScheduledRunAdvancesScheduleByExactDatabaseInterval(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = TRUE, interval_value = 1, interval_unit = 'hour', next_run_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'
WHERE kind = 'metadata_match'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at)
VALUES ('scheduled-completion-run', 'metadata_match', 'scheduled', 'running', CURRENT_TIMESTAMP - INTERVAL '1 minute')`); err != nil {
		t.Fatal(err)
	}

	dispatcher := &Dispatcher{DB: db}
	dispatcher.finishRun(ctx, "scheduled-completion-run", "metadata_match", dispatchCounts{}, nil, time.Now().UTC())

	var state string
	var exact bool
	if err := db.QueryRowContext(ctx, `
SELECT r.state,
       s.next_run_at = r.finished_at + INTERVAL '1 hour'
       AND s.last_dispatch_at = r.finished_at
       AND s.updated_at = r.finished_at
FROM automation_dispatch_runs r
JOIN automation_schedules s ON s.kind = r.kind
WHERE r.id = 'scheduled-completion-run'`).Scan(&state, &exact); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || !exact {
		t.Fatalf("scheduled finish state=%s exact_database_interval=%v", state, exact)
	}
}

func TestReapStaleRunUsesOneDatabaseTimestamp(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = TRUE, next_run_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'
WHERE kind = 'metadata_match'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at)
VALUES ('stale-metadata-run', 'metadata_match', 'scheduled', 'running', CURRENT_TIMESTAMP - INTERVAL '20 minutes')`); err != nil {
		t.Fatal(err)
	}

	dispatcher := &Dispatcher{DB: db}
	if err := dispatcher.ReapStaleRuns(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	var state string
	var exact bool
	if err := db.QueryRowContext(ctx, `
SELECT r.state,
       r.finished_at = s.next_run_at
       AND r.finished_at = s.last_dispatch_at
       AND r.finished_at = s.updated_at
FROM automation_dispatch_runs r
JOIN automation_schedules s ON s.kind = r.kind
WHERE r.id = 'stale-metadata-run'`).Scan(&state, &exact); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !exact {
		t.Fatalf("stale reaper state=%s one_database_timestamp=%v", state, exact)
	}
}

func TestReapStaleManualRunDoesNotAdvanceSchedule(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = TRUE, next_run_at = CURRENT_TIMESTAMP + INTERVAL '1 hour', last_dispatch_at = NULL
WHERE kind = 'metadata_match'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at)
VALUES ('stale-manual-metadata-run', 'metadata_match', 'manual', 'running', CURRENT_TIMESTAMP - INTERVAL '20 minutes')`); err != nil {
		t.Fatal(err)
	}

	dispatcher := &Dispatcher{DB: db}
	if err := dispatcher.ReapStaleRuns(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	var state string
	var unchanged bool
	if err := db.QueryRowContext(ctx, `
SELECT r.state,
       s.next_run_at > CURRENT_TIMESTAMP + INTERVAL '50 minutes'
       AND s.last_dispatch_at IS NULL
FROM automation_dispatch_runs r
JOIN automation_schedules s ON s.kind = r.kind
WHERE r.id = 'stale-manual-metadata-run'`).Scan(&state, &unchanged); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !unchanged {
		t.Fatalf("stale manual state=%s schedule_unchanged=%v", state, unchanged)
	}
}

func TestDispatcherGroupSnapshotRetentionUsesDatabaseTime(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	future := time.Now().UTC().Add(365 * 24 * time.Hour)
	inactive := false
	groupRef := providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "alpha"}
	dispatcher := &Dispatcher{DB: db}
	snapshot, err := dispatcher.persistGroupSnapshot(ctx, providerdomain.GroupReadResult{
		Provider:        providerdomain.ProviderAniBT,
		CatalogEntryRef: "123",
		ObservedAt:      future,
		SourceVersion:   "future-source",
		SchemaVersion:   "anibt-anime-groups-v1",
		Complete:        true, CompletenessEvidence: "anibt-entry-total-v1",
		Groups: []providerdomain.GroupSnapshotEntry{{
			Ref:         groupRef,
			DisplayName: "Alpha",
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Group: groupRef},
			ObservedAt:  future,
			Active:      &inactive,
		}},
	})
	if err != nil {
		t.Fatalf("persist future group snapshot: %v", err)
	}
	var deadlineWithinDatabaseWindow, observedInFuture bool
	if err := db.QueryRowContext(ctx, `
	SELECT retention_until <= CURRENT_TIMESTAMP + INTERVAL '30 days'
	       AND retention_until > CURRENT_TIMESTAMP + INTERVAL '29 days',
	       observed_at > CURRENT_TIMESTAMP + INTERVAL '300 days'
	FROM provider_group_snapshots WHERE id = $1`, snapshot.ID).Scan(&deadlineWithinDatabaseWindow, &observedInFuture); err != nil {
		t.Fatal(err)
	}
	if !deadlineWithinDatabaseWindow || !observedInFuture {
		t.Fatalf("dispatcher group retention must use database time: deadline_in_window=%t observed_in_future=%t", deadlineWithinDatabaseWindow, observedInFuture)
	}
	var entryCount, healthCount int
	if err := db.QueryRowContext(ctx, `SELECT jsonb_array_length(entries) FROM provider_group_snapshots WHERE id = $1`, snapshot.ID).Scan(&entryCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_refresh_health WHERE provider = 'anibt' AND catalog_entry_ref = '123'`).Scan(&healthCount); err != nil {
		t.Fatal(err)
	}
	if entryCount != 1 || healthCount != 1 {
		t.Fatalf("dispatcher group snapshot side rows: entries=%d health=%d, want 1/1", entryCount, healthCount)
	}
	var preserved bool
	if err := db.QueryRowContext(ctx, `SELECT observation->'completeness'->'value'->'complete'='true'::jsonb
        AND observation->'groups'->0->'available'->'value'='false'::jsonb
        AND observation_digest IS NOT NULL
        FROM provider_group_snapshots WHERE id=$1`, snapshot.ID).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("automation lost evidence: %t %v", preserved, err)
	}

}

func TestDispatchManualClaimsQueuedRunAndCompletes(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, request_idempotency_key)
VALUES ('manual-claim-run', 'rss_refresh', 'manual', 'queued', 'manual-claim-idem')`); err != nil {
		t.Fatal(err)
	}

	dispatcher := &Dispatcher{DB: db}
	claimed, err := dispatcher.DispatchManual(ctx)
	if err != nil {
		t.Fatalf("dispatch manual: %v", err)
	}
	if claimed != 1 {
		t.Fatalf("expected 1 claimed run, got %d", claimed)
	}

	var state string
	var retentionExact bool
	if err := db.QueryRowContext(ctx, `
SELECT state,
       started_at IS NOT NULL AND finished_at IS NOT NULL
       AND retention_until = finished_at + INTERVAL '30 days'
FROM automation_dispatch_runs
WHERE id = 'manual-claim-run'`).Scan(&state, &retentionExact); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || !retentionExact {
		t.Fatalf("manual run state=%s retention_anchored_to_finish=%v", state, retentionExact)
	}
}
