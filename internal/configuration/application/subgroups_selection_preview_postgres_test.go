package application

import (
	ownership "bansoko/internal/anime/ownership/application"
	catalog "bansoko/internal/catalog/application"
	observability "bansoko/internal/observability/application"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/storedsections"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"testing"
	"time"
)

// The subgroup tab previews an unsaved owner value through the same ranking the
// save recomputes with. This pins that the preview writes nothing and that
// saving the very value it previewed selects exactly what it showed.
func TestSubgroupsSelectionPreviewMatchesTheSaveAndWritesNothing(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, stmt := range []string{
		`INSERT INTO managed_anime(id,title) VALUES('preview-anime','Preview')`,
		`INSERT INTO subscriptions(id,managed_anime_id,origin,origin_entry_ref,state) VALUES('preview-sub','preview-anime','anibt','555','active')`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES('preview-anime') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score) VALUES('preview-a','preview-sub','anibt','alpha','Alpha','auto',true,50),('preview-b','preview-sub','anibt','beta','Beta','auto',true,50)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, "preview-anime")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	result := provider.GroupReadResult{Provider: provider.ProviderAniBT, CatalogEntryRef: "555", ObservedAt: now, SchemaVersion: "fixture-v1"}
	for _, ref := range []string{"alpha", "beta", "gamma"} {
		key := provider.GroupRef{Provider: result.Provider, ExternalID: ref}
		result.Groups = append(result.Groups, provider.GroupSnapshotEntry{Ref: key, DisplayName: ref, ObservedAt: now, FeedLocator: provider.FeedLocator{Provider: result.Provider, CatalogEntryRef: result.CatalogEntryRef, Group: key}})
	}
	if _, err := catalog.PersistGroupObservationTx(ctx, tx, result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	activity, err := observability.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := ownership.NewService(db, ownership.Dependencies{WorkItems: store, ActivityWriter: activity})
	if err != nil {
		t.Fatal(err)
	}

	always := true
	never := false
	section := ownership.SubgroupsSection{MaxRss: 1, Entries: []ownership.SubgroupScoreEntry{
		{Provider: "anibt", GroupRef: "alpha", DisplayName: "Alpha", SelectionMode: "manual", ManualSelected: &never},
		{Provider: "anibt", GroupRef: "beta", DisplayName: "Beta", SelectionMode: "manual", ManualSelected: &always},
	}}

	rowsBefore := selectionRowsFingerprint(t, ctx, db)
	facts, err := owner.PreviewSubgroupsSelectionApplication(ctx, "preview-anime", section)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if after := selectionRowsFingerprint(t, ctx, db); after != rowsBefore {
		t.Fatalf("preview wrote provider selections:\nbefore %s\nafter  %s", rowsBefore, after)
	}
	previewed := map[string]ownership.SubgroupSelectionPreviewFact{}
	for _, fact := range facts {
		previewed[fact.GroupRef] = fact
	}
	if len(previewed) != 3 {
		t.Fatalf("every persisted and observed group has a fact: %+v", facts)
	}
	if previewed["alpha"].EffectiveSelected || previewed["alpha"].ReasonCode == nil || *previewed["alpha"].ReasonCode != "policy_never" {
		t.Fatalf("alpha is never: %+v", previewed["alpha"])
	}
	if !previewed["beta"].EffectiveSelected {
		t.Fatalf("beta is always and does not count: %+v", previewed["beta"])
	}
	if !previewed["gamma"].EffectiveSelected {
		t.Fatalf("gamma takes the one counted slot: %+v", previewed["gamma"])
	}

	saved, err := owner.PatchAnimeSubgroupsApplication(ctx, "preview-anime", ownership.SubgroupsPatchInput{ExpectedVersion: 1, Subgroups: section})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, row := range saved.EffectiveSelection {
		fact, ok := previewed[row.GroupRef]
		if !ok {
			t.Fatalf("saved group %s was not previewed", row.GroupRef)
		}
		if fact.EffectiveSelected != row.EffectiveSelected || fact.Available != row.Available {
			t.Fatalf("saved %s = %+v, previewed %+v", row.GroupRef, row, fact)
		}
	}
}

func selectionRowsFingerprint(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var fingerprint string
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(string_agg(group_ref||':'||selection_mode||':'||COALESCE(manual_selected::text,'-')||':'||effective_selected||':'||version, ',' ORDER BY group_ref),'')
  || '|' || (SELECT subgroups_version FROM anime_business_configs WHERE managed_anime_id='preview-anime')
  || '|' || (SELECT count(*) FROM anime_config_overrides WHERE managed_anime_id='preview-anime')
FROM provider_group_selections WHERE subscription_id='preview-sub'`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
