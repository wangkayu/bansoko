package filemaintenance

import (
	"bansoko/internal/testutil/storedsections"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

func seedNamingOwnerWithoutSubscription(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	rules, err := json.Marshal(animedomain.NamingRulesFromDomain(rendererRules()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO anime_business_configs(managed_anime_id, rules_version)
VALUES ($1, 1)
ON CONFLICT (managed_anime_id) DO UPDATE SET rules_version = EXCLUDED.rules_version`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO anime_config_overrides
(managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ($1, 'rules', 'naming_rules', 'naming-rules-v1', $2)`, animeID, rules); err != nil {
		t.Fatal(err)
	}
	storedsections.Seed(t, db, animeID)
}

// TestSubscriptionlessNamingSourceServesPathDiffAndPlanner covers the
// subscription-neutral naming owner: an anime whose rules owner carries no
// subscription-derived override still renders its placed files, and the frozen
// rules on the placement stay the render authority even when that owner is
// repointed afterwards. A release lineage is schema-bound to a subscription, so
// the fixture cannot itself be subscription-less; what it proves is that the
// render never reads the owner for a placement that carries frozen evidence.
func TestSubscriptionlessNamingSourceServesPathDiffAndPlanner(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	animeID := "anime-subscriptionless-naming"

	seedAnimeForRenderer(t, db, animeID, "退订番组", "tv")
	seedNamingOwnerWithoutSubscription(t, db, animeID)
	seedPathDiffRows(t, db, "skip")
	freezeAnimeRenderFixtureBusiness(t, db, "anime-diff")
	// The path-diff fixture uses a separate anime; give it the same rules owner
	// without creating a subscription so preview exercises the production source.
	seedNamingOwnerWithoutSubscription(t, db, "anime-diff")
	seedScanOwner(t, db, animeID, "completed", 1)
	seedObservationCoverage(t, db, animeID, 1, 0, "/library/退订番组/Season 01/EP01.mkv", "fp-planner", 100, "tv:s1:e1")
	// The observed file descends from a completed file operation whose frozen
	// rules are the render authority; the subscription-neutral rules owner stays
	// the source for a placement without frozen evidence.
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "subless-planner", AnimeID: animeID, AnimeTitle: "退订番组",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] 退订番组 - 01", PlacedPath: "/library/退订番组/Season 01/EP01.mkv",
		DownloadName: "EP01.mkv", Rules: rendererRules(), FrozenEpisode: "1",
		Coverage: []string{"tv:s1:e1"},
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	if _, err := db.Exec(`UPDATE reconciliation_files SET facts = facts || jsonb_build_object('placement_id', $1::text) WHERE managed_anime_id = $2 AND generation = 1 AND file_path = $3`,
		chain.PlacementID, animeID, "/library/退订番组/Season 01/EP01.mkv"); err != nil {
		t.Fatal(err)
	}

	store, err := workitems.NewStore(db, 0, observabilityport.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	ownershipService, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	renderer := NewNamingTargetRenderer(db, ownershipService)

	pathDiffs := NewPathDiffService(db, renderer, nil)
	views, err := pathDiffs.ListPathDiffs(ctx, "anime-diff", 1, 100, 0)
	if err != nil {
		t.Fatalf("subscriptionless path diff render: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("path diff rows=%d, want 3", len(views))
	}
	for _, view := range views {
		if view.Disposition == "unresolved" {
			continue
		}
		if view.ExpectedPath == nil {
			t.Fatalf("path diff %q unexpectedly fell back without expected path", view.FilePath)
		}
	}

	planner := NewPlanner(NewReconciliationSnapshotReader(db), renderer)
	plan, err := planner.Plan(ctx, animeID, 1, 1)
	if err != nil {
		t.Fatalf("subscriptionless planner: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("planner candidates=%d, want 1", len(plan.Candidates))
	}
	candidate := plan.Candidates[0]
	if candidate.Excluded || candidate.ExcludeReasonCode == "target_render_failed" {
		t.Fatalf("subscriptionless naming render must remain executable: %+v", candidate)
	}
	if candidate.TargetPath != "/library/退订番组/Season 01/退订番组 - E01.mkv" {
		t.Fatalf("planner target=%q, want subscription-neutral naming target", candidate.TargetPath)
	}

	// The anime's current rules are the render authority: repointing the same
	// anime's subscription-neutral rules owner moves the rendered target of
	// the existing placement, while the release facts still come from its
	// lineage.
	movedRules, err := json.Marshal(animedomain.NamingRulesFromDomain(naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/moved/${sanitize(anime.title)}",
			VideoFilenameTemplate: "E${pad(ep.display, 2)}.${file.ext}",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "/moved",
			VideoFilenameTemplate: "moved.${file.ext}",
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
UPDATE anime_config_overrides SET value = $2
WHERE managed_anime_id = $1 AND owner_type = 'rules' AND section_name = 'naming_rules'`,
		animeID, movedRules); err != nil {
		t.Fatal(err)
	}
	afterDrift, err := planner.Plan(ctx, animeID, 1, 1)
	if err != nil {
		t.Fatalf("planner after rules-owner change: %v", err)
	}
	if len(afterDrift.Candidates) != 1 {
		t.Fatalf("planner candidates after change=%d, want 1", len(afterDrift.Candidates))
	}
	drifted := afterDrift.Candidates[0]
	if drifted.Excluded {
		t.Fatalf("a valid current template must stay executable: %+v", drifted)
	}
	if drifted.TargetPath != "/moved/退订番组/E01.mkv" {
		t.Fatalf("rules-owner change target=%q, want the current template's path", drifted.TargetPath)
	}
}

// TestSubscriptionlessRulesOwnerServesTheAnimeNamingSource covers the
// subscription-neutral rules owner on its own: an anime whose naming rules are
// owned by its own config owner and which has no subscription at all still
// resolves its effective naming rules. A release lineage is schema-bound to a
// subscription, so this property is proved on the owner itself, not through a
// frozen placement.
func TestSubscriptionlessRulesOwnerServesTheAnimeNamingSource(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	animeID := "anime-subless-owner"

	seedAnimeForRenderer(t, db, animeID, "无订阅番组", "tv")
	seedNamingOwnerWithoutSubscription(t, db, animeID)

	store, err := workitems.NewStore(db, 0, observabilityport.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	ownershipService, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ownershipService.EffectiveNamingRules(ctx, animeID)
	if err != nil {
		t.Fatalf("subscriptionless effective naming rules: %v", err)
	}
	want := rendererRules()
	if rules.TV.DirectoryTemplate != want.TV.DirectoryTemplate || rules.TV.VideoFilenameTemplate != want.TV.VideoFilenameTemplate {
		t.Fatalf("subscriptionless rules owner was not served: %+v", rules.TV)
	}

	var subscriptions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 0 {
		t.Fatalf("fixture must stay subscription-free: %d", subscriptions)
	}
}
