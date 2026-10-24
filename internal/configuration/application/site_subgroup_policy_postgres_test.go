package application

import (
	"context"
	"database/sql"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	ownership "bansoko/internal/anime/ownership/application"
	catalog "bansoko/internal/catalog/application"
	configurationdomain "bansoko/internal/configuration/domain"
	observability "bansoko/internal/observability/application"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/storedsections"
	"bansoko/internal/workitems"
)

// seedSiteInstance creates one anibt instance on catalog entry 777 with three
// automatic groups (alpha selected), its own subgroups section (max_rss 1) and
// the given subscription state.
func seedSiteInstance(t *testing.T, ctx context.Context, db *sql.DB, anime, sub, state string) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO managed_anime(id,title) VALUES('` + anime + `','Site ` + anime + `')`,
		`INSERT INTO subscriptions(id,managed_anime_id,origin,origin_entry_ref,state) VALUES('` + sub + `','` + anime + `','anibt','777','` + state + `')`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES('` + anime + `') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value) VALUES('` + anime + `','subgroups','subgroups','subgroups-v1','{"entries":[],"max_rss":1}')`,
		`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,count_in_limit) VALUES
		('` + sub + `-a','` + sub + `','anibt','alpha','Alpha','auto',true,50,true),
		('` + sub + `-b','` + sub + `','anibt','beta','Beta','auto',false,50,true),
		('` + sub + `-g','` + sub + `','anibt','gamma','Gamma','auto',false,50,true)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, anime)
}

func siteSelectionFingerprint(t *testing.T, ctx context.Context, db *sql.DB, sub string) string {
	t.Helper()
	var fingerprint string
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(string_agg(group_ref||':'||effective_selected||':'||effective_score||':'||version, ',' ORDER BY group_ref),'')
FROM provider_group_selections WHERE subscription_id=$1`, sub).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func siteSelected(t *testing.T, ctx context.Context, db *sql.DB, sub string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT group_ref, effective_selected FROM provider_group_selections WHERE subscription_id=$1`, sub)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var ref string
		var selected bool
		if err := rows.Scan(&ref, &selected); err != nil {
			t.Fatal(err)
		}
		result[ref] = selected
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// The site subgroup policy is live: saving the
// Global provider_defaults or name_keywords recomputes every active instance's
// selection in the save's transaction, while a change to the Global
// entries/max_rss (new-draft defaults) and every retired or stopped instance
// are left alone. The owner read explains the result with the decision's own
// reason and score breakdown.
func TestGlobalSitePolicySaveRecomputesActiveInstancesOnly(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seedSiteInstance(t, ctx, db, "site-active", "site-active-sub", "active")
	seedSiteInstance(t, ctx, db, "site-pending", "site-pending-sub", "pending")
	seedSiteInstance(t, ctx, db, "site-stopped", "site-stopped-sub", "stopped")
	seedSiteInstance(t, ctx, db, "site-retired", "site-retired-sub", "active")
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
    retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1,
    lifecycle_version = lifecycle_version + 1
WHERE id = 'site-retired'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	observed := provider.GroupReadResult{Provider: provider.ProviderAniBT, CatalogEntryRef: "777", ObservedAt: now, SchemaVersion: "fixture-v1"}
	for _, ref := range []string{"alpha", "beta", "gamma"} {
		key := provider.GroupRef{Provider: observed.Provider, ExternalID: ref}
		name := map[string]string{"alpha": "Alpha", "beta": "Beta", "gamma": "Gamma 简繁"}[ref]
		observed.Groups = append(observed.Groups, provider.GroupSnapshotEntry{Ref: key, DisplayName: name, ObservedAt: now, FeedLocator: provider.FeedLocator{Provider: observed.Provider, CatalogEntryRef: observed.CatalogEntryRef, Group: key}})
	}
	if _, err := catalog.PersistGroupObservationTx(ctx, tx, observed); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, newConnectionTestSecretStore(t, db))
	schema := animedomain.SectionSchemaVersions[animedomain.SectionSubgroups]
	untouched := map[string]string{}
	for _, sub := range []string{"site-active-sub", "site-pending-sub", "site-stopped-sub", "site-retired-sub"} {
		untouched[sub] = siteSelectionFingerprint(t, ctx, db, sub)
	}

	// 1. New-draft defaults only: no instance is touched.
	first, err := svc.PutGlobalSection(ctx, animedomain.SectionSubgroups, 0, configurationdomain.GlobalSectionInput{SchemaVersion: schema, Value: map[string]any{
		"max_rss": 5,
		"entries": []any{map[string]any{"provider": "anibt", "group_ref": "beta", "display_name": "Beta", "selection_mode": "manual", "manual_selected": true, "effective_selected": true, "score": 1, "count_in_limit": false}},
	}})
	if err != nil {
		t.Fatalf("save new-draft defaults: %v", err)
	}
	for sub, before := range untouched {
		if after := siteSelectionFingerprint(t, ctx, db, sub); after != before {
			t.Fatalf("a Global max_rss/entries change touched %s:\nbefore %s\nafter  %s", sub, before, after)
		}
	}

	// 2. Blacklist the selected alpha: the next-best group within max_rss 1
	// takes its place on every active instance (pending included).
	blacklist := map[string]any{"anibt": map[string]any{"mode": "blacklist", "score": 50, "groups": []any{"alpha"}}}
	second, err := svc.PutGlobalSection(ctx, animedomain.SectionSubgroups, first.Version, configurationdomain.GlobalSectionInput{SchemaVersion: schema, Value: map[string]any{
		"max_rss": 5, "entries": []any{}, "provider_defaults": blacklist,
	}})
	if err != nil {
		t.Fatalf("save blacklist: %v", err)
	}
	for _, sub := range []string{"site-active-sub", "site-pending-sub"} {
		if got := siteSelected(t, ctx, db, sub); got["alpha"] || !got["beta"] || got["gamma"] {
			t.Fatalf("%s after blacklisting alpha: %+v, want beta only", sub, got)
		}
	}
	for _, sub := range []string{"site-stopped-sub", "site-retired-sub"} {
		if after := siteSelectionFingerprint(t, ctx, db, sub); after != untouched[sub] {
			t.Fatalf("a site policy save touched %s:\nbefore %s\nafter  %s", sub, untouched[sub], after)
		}
	}

	// 3. A name keyword reorders the ranking: gamma (+20) wins the slot.
	keywords := map[string]any{"anibt": []any{map[string]any{"keyword": "简繁", "score": 20}}}
	third, err := svc.PutGlobalSection(ctx, animedomain.SectionSubgroups, second.Version, configurationdomain.GlobalSectionInput{SchemaVersion: schema, Value: map[string]any{
		"max_rss": 5, "entries": []any{}, "provider_defaults": blacklist,
		"name_keywords": keywords,
	}})
	if err != nil {
		t.Fatalf("save keyword: %v", err)
	}
	if got := siteSelected(t, ctx, db, "site-active-sub"); got["alpha"] || got["beta"] || !got["gamma"] {
		t.Fatalf("after the keyword: %+v, want gamma only", got)
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
	read, err := owner.ReadSubgroupsOwnerApplication(ctx, "site-active", "site-active-sub")
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]ownership.ProviderGroupSelection{}
	for _, row := range read.EffectiveSelection {
		byRef[row.GroupRef] = row
	}
	if alpha := byRef["alpha"]; alpha.ReasonCode == nil || *alpha.ReasonCode != "site_blacklisted" || alpha.ScoreBreakdown == nil || alpha.ScoreBreakdown.ListEffect != "blacklisted" {
		t.Fatalf("alpha must be explained as site-blacklisted: %+v", alpha)
	}
	gamma := byRef["gamma"]
	if gamma.ReasonCode == nil || *gamma.ReasonCode != "ranked_within_max_rss" || gamma.ScoreBreakdown == nil || gamma.ScoreBreakdown.FinalScore == nil {
		t.Fatalf("gamma must be explained as ranked: %+v", gamma)
	}
	b := gamma.ScoreBreakdown
	if b.SiteDefaultScore != 50 || len(b.Keywords) != 1 || b.Keywords[0].Score != 20 || *b.FinalScore != 70 || gamma.EffectiveScore != 70 {
		t.Fatalf("gamma breakdown must sum to its score: %+v (score %d)", b, gamma.EffectiveScore)
	}
	if beta := byRef["beta"]; beta.ReasonCode == nil || *beta.ReasonCode != "max_rss_exhausted" {
		t.Fatalf("beta lost the slot: %+v", beta)
	}

	// 4. Pin beta at 90 (and the blacklisted alpha at 100): the pin is beta's
	// final score, so it takes the slot from gamma (50 + 20), while the
	// blacklist still excludes alpha whatever its pin.
	pinned := map[string]any{"anibt": map[string]any{"mode": "blacklist", "score": 50, "groups": []any{"alpha"},
		"pinned_scores": []any{map[string]any{"group_ref": "beta", "score": 90}, map[string]any{"group_ref": "alpha", "score": 100}}}}
	if _, err := svc.PutGlobalSection(ctx, animedomain.SectionSubgroups, third.Version, configurationdomain.GlobalSectionInput{SchemaVersion: schema, Value: map[string]any{
		"max_rss": 5, "entries": []any{}, "provider_defaults": pinned, "name_keywords": keywords,
	}}); err != nil {
		t.Fatalf("save pinned score: %v", err)
	}
	for _, sub := range []string{"site-active-sub", "site-pending-sub"} {
		if got := siteSelected(t, ctx, db, sub); got["alpha"] || !got["beta"] || got["gamma"] {
			t.Fatalf("%s after pinning beta: %+v, want beta only", sub, got)
		}
	}
	for _, sub := range []string{"site-stopped-sub", "site-retired-sub"} {
		if after := siteSelectionFingerprint(t, ctx, db, sub); after != untouched[sub] {
			t.Fatalf("a pinned score save touched %s:\nbefore %s\nafter  %s", sub, untouched[sub], after)
		}
	}
	read, err = owner.ReadSubgroupsOwnerApplication(ctx, "site-active", "site-active-sub")
	if err != nil {
		t.Fatal(err)
	}
	byRef = map[string]ownership.ProviderGroupSelection{}
	for _, row := range read.EffectiveSelection {
		byRef[row.GroupRef] = row
	}
	beta := byRef["beta"]
	if beta.ScoreBreakdown == nil || beta.ScoreBreakdown.PinnedScore == nil || *beta.ScoreBreakdown.PinnedScore != 90 ||
		beta.ScoreBreakdown.FinalScore == nil || *beta.ScoreBreakdown.FinalScore != 90 || len(beta.ScoreBreakdown.Keywords) != 0 || beta.EffectiveScore != 90 {
		t.Fatalf("beta must be explained by its pinned score: %+v", beta)
	}
	if alpha := byRef["alpha"]; alpha.ReasonCode == nil || *alpha.ReasonCode != "site_blacklisted" || alpha.ScoreBreakdown == nil || alpha.ScoreBreakdown.PinnedScore != nil {
		t.Fatalf("a pinned blacklisted alpha stays site-blacklisted: %+v", alpha)
	}
}
