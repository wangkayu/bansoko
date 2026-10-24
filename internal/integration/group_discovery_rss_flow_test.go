package integration_test

import (
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/presetdraft"
	"bansoko/internal/testutil/storedsections"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	automationapplication "bansoko/internal/automation/application"
	configurationdomain "bansoko/internal/configuration/domain"
	observabilityapplication "bansoko/internal/observability/application"
	provideradapter "bansoko/internal/provider/adapter"
	providerdomain "bansoko/internal/provider/domain"
	selectionapplication "bansoko/internal/selection/application"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"

	"github.com/stretchr/testify/require"
)

// TestGroupDiscoveryCreatesRSSSources validates the complete flow:
// 1. Subscribe to an AniBT anime (with empty rss_sources)
// 2. Group discovery discovers available groups
// 3. RSS sources are created with correct URLs
func TestGroupDiscoveryCreatesRSSSources(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()

	// Setup mock AniBT server that returns group list
	mockAniBT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/anime/groups" {
			bgmId := r.URL.Query().Get("bgmId")
			// Return mock groups
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":` + bgmId + `,"total":2,"groups":[{"slug":"group-alpha","name":"Alpha Sub","status":"active","totalReleases":10,"lastUpdatedAt":1},{"slug":"group-beta","name":"Beta Sub","status":"active","totalReleases":5,"lastUpdatedAt":1}]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockAniBT.Close()

	// Create test data using helper functions from dispatcher_postgres_test.go
	insertProviderSubscriptionWithBGM(t, db, "12345")
	insertSubgroupsGlobal(t, db, generated.SubgroupsSection{
		MaxRss: 2,
		Entries: []generated.SubgroupScoreEntry{
			{Provider: generated.ProviderKindAnibt, GroupRef: "group-alpha", DisplayName: "Alpha Sub", SelectionMode: generated.SelectionModeAuto, Score: 50, CountInLimit: true},
			{Provider: generated.ProviderKindAnibt, GroupRef: "group-beta", DisplayName: "Beta Sub", SelectionMode: generated.SelectionModeAuto, Score: 50, CountInLimit: true},
		},
	})

	// Verify no RSS sources yet
	var rssCount int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources`).Scan(&rssCount)
	require.NoError(t, err)
	require.Equal(t, 0, rssCount, "should have no RSS sources before group discovery")

	// Configure AniBT connection using the correct type
	connections := testConnections{{
		ID: "anibt-test", Kind: configurationdomain.ConnectionAniBT, Endpoint: mockAniBT.URL,
		State: configurationdomain.ConnectionConfigured,
	}}

	store, err := workitems.NewStore(db, 0, nil)
	require.NoError(t, err, "create work items store")
	selectionService := selectionapplication.NewService(db)

	// Create dispatcher with correct field structure
	dispatcher := &automationapplication.Dispatcher{
		DB:           db,
		WorkItems:    store,
		PolicyReader: selectionService,
		GroupReaders: map[string]automationapplication.GroupReader{
			"anibt": provideradapter.ConfiguredGroupReader{Provider: providerdomain.ProviderAniBT, Connections: connections},
		},
		Connections: connections,
	}

	// Make group_discovery the only due schedule. The seeded rss_refresh comes
	// due 15 minutes after migration, which a long gate run can pass.
	_, err = db.ExecContext(ctx, `
		UPDATE automation_schedules
		SET next_run_at = CASE WHEN kind = 'group_discovery'
			THEN NOW() - INTERVAL '1 hour' ELSE NOW() + INTERVAL '1 day' END
	`)
	require.NoError(t, err, "update schedule to be due")

	// Run group discovery (via DispatchDue which triggers all due schedules)
	count, err := dispatcher.DispatchDue(ctx)
	require.NoError(t, err, "DispatchDue should succeed")
	require.Equal(t, 1, count, "should have processed 1 due schedule (group_discovery)")
	t.Logf("DispatchDue processed %d runs", count)

	// Verify RSS sources were created
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources`).Scan(&rssCount)
	require.NoError(t, err)
	require.Greater(t, rssCount, 0, "should have RSS sources after group discovery")

	// Verify RSS URL format
	rows, err := db.QueryContext(ctx, `
		SELECT id, locator, group_name, source_kind
		FROM rss_sources
	`)
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id, locator, groupName, kind string
		err := rows.Scan(&id, &locator, &groupName, &kind)
		require.NoError(t, err)
		t.Logf("RSS source: id=%s locator=%s group=%s kind=%s", id, locator, groupName, kind)

		// Verify URL format
		require.Contains(t, locator, "bgmId=", "RSS URL should contain bgmId")
		require.Contains(t, locator, "/rss/anime.xml", "RSS URL should have correct path")
		require.NotEmpty(t, groupName, "group name should be set")
	}
}

func TestCandidateActivationMaterializesProviderSourcesThroughDispatcher(t *testing.T) {
	for _, initialState := range []subscriptiondomain.SubscriptionState{
		subscriptiondomain.SubscriptionActive,
		subscriptiondomain.SubscriptionStopped,
	} {
		t.Run(string(initialState), func(t *testing.T) {
			// The load-business-preset action inside this flow reads the
			// active naming-vars program, so the schema must carry the
			// published test registries, not a bare migration-only schema.
			db := animetest.OpenTestDB(t)
			ctx := context.Background()
			mockAniBT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/anime/groups" || r.URL.Query().Get("bgmId") != "12345" {
					t.Fatalf("unexpected group request: %s", r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":12345,"total":2,"groups":[{"slug":"group-alpha","name":"Alpha Sub","status":"active","totalReleases":10,"lastUpdatedAt":1},{"slug":"group-beta","name":"Beta Sub","status":"active","totalReleases":5,"lastUpdatedAt":1}]}}`))
			}))
			defer mockAniBT.Close()

			insertSubgroupsGlobal(t, db, generated.SubgroupsSection{
				MaxRss: 2,
				Entries: []generated.SubgroupScoreEntry{
					{Provider: generated.ProviderKindAnibt, GroupRef: "group-alpha", DisplayName: "Alpha Sub", SelectionMode: generated.SelectionModeAuto, Score: 50, CountInLimit: true},
					{Provider: generated.ProviderKindAnibt, GroupRef: "group-beta", DisplayName: "Beta Sub", SelectionMode: generated.SelectionModeAuto, Score: 50, CountInLimit: true},
				},
			})

			store, err := workitems.NewStore(db, 0, nil)
			require.NoError(t, err, "create work items store")
			connections := testConnections{{
				ID: "anibt-test", Kind: configurationdomain.ConnectionAniBT, Endpoint: mockAniBT.URL,
				State: configurationdomain.ConnectionConfigured,
			}}
			selectionService := selectionapplication.NewService(db)
			dispatcher := &automationapplication.Dispatcher{
				DB: db, WorkItems: store, PolicyReader: selectionService,
				GroupReaders: map[string]automationapplication.GroupReader{
					"anibt": provideradapter.ConfiguredGroupReader{Provider: providerdomain.ProviderAniBT, Connections: connections},
				},
				Connections: connections,
			}
			service, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
			require.NoError(t, err, "create draft service")
			activityWriter, err := observabilityapplication.NewService(db)
			require.NoError(t, err, "create activity writer")
			service.SetActivityWriter(activityWriter)
			// Wired like the server composition root.
			service.SetFrozenParserPolicy(parserpolicy.NewService(db))
			service.SetGroupDiscoveryProvider(dispatcher)

			entryRef := "12345"
			// The draft copies the current Global business (the load-business-preset action).
			candidate, err := service.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: presetdraft.LoadDomain(t, db, animedomain.AnimeDraft{
				Origin:         animedomain.SubscriptionOriginAnibt,
				OriginEntryRef: &entryRef,
				Title:          "Candidate Materialization",
				Metadata: animedomain.AnimeMetadataDraft{
					Bgm:  &animedomain.BGMBinding{WorkId: "12345"},
					Tmdb: &animedomain.TMDBBinding{WorkId: "tmdb-12345", MediaKind: animedomain.TMDBMediaKindTv, Season: 1},
				},
				Subgroups:  animedomain.SubgroupsSection{Entries: []animedomain.SubgroupScoreEntry{}, MaxRss: 2},
				RssSources: []animedomain.RSSSourceDraft{},
			})})
			require.NoError(t, err, "save provider candidate")
			require.NotNil(t, candidate.CurrentRevision, "saved candidate must expose its immutable revision")

			// Activation identity is the saved immutable revision (the same
			// contract the real-chain lanes use); the mutable CAS version is
			// only the concurrency target.
			input := draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision}
			if initialState == subscriptiondomain.SubscriptionStopped {
				input.InitialState = &initialState
			} else {
				input.Start = true
			}
			result, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-dispatcher-materialization", input)
			require.NoError(t, err, "activate provider candidate")

			var sourceCount, selectionCount, runtimeCount, refreshCount int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources WHERE subscription_id = $1`, result.Subscription.ID).Scan(&sourceCount))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_selections WHERE subscription_id = $1`, result.Subscription.ID).Scan(&selectionCount))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_source_runtime rt JOIN rss_sources src ON src.id = rt.rss_source_id WHERE src.subscription_id = $1`, result.Subscription.ID).Scan(&runtimeCount))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history wi WHERE wi.kind = 'rss_refresh' AND wi.owner_type = 'rss_source_runtime' AND EXISTS (SELECT 1 FROM rss_sources src JOIN rss_source_runtime rt ON rt.rss_source_id = src.id WHERE rt.rss_source_id = wi.owner_id AND src.subscription_id = $1)`, result.Subscription.ID).Scan(&refreshCount))
			require.Equal(t, 2, sourceCount, "dispatcher discovery must materialize one source per group")
			require.Equal(t, 2, selectionCount, "dispatcher discovery must persist one selection per group")
			require.Equal(t, 2, runtimeCount, "materialized sources must have runtime owners")
			wantRefresh := 2
			if initialState == subscriptiondomain.SubscriptionStopped {
				wantRefresh = 0
			}
			require.Equal(t, wantRefresh, refreshCount, "refresh eligibility must follow the initial subscription state")
		})
	}
}

// Helper types and functions

type testConnections []configurationdomain.Connection

func (s testConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return append([]configurationdomain.Connection(nil), s...), nil
}

func insertProviderSubscriptionWithBGM(t *testing.T, db *sql.DB, bgmWorkId string) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Test Anime')`,
		`INSERT INTO anime_metadata(managed_anime_id, bgm_work_id) VALUES ('anime-1', '` + bgmWorkId + `')`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-1') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state) VALUES ('sub-1', 'anime-1', 'anibt', '` + bgmWorkId + `', 'active')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("insert subscription fixture: %v", err)
		}
	}
	storedsections.Seed(t, db, "anime-1")
}

func insertSubgroupsGlobal(t *testing.T, db *sql.DB, section generated.SubgroupsSection) {
	t.Helper()
	raw, err := json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO global_config_sections(section_name, schema_version, value) VALUES ('subgroups', 'subgroups-v1', $1)`, raw); err != nil {
		t.Fatalf("insert subgroup section: %v", err)
	}
}
