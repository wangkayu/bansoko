package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/selection/domain"
	"bansoko/internal/testutil/disposabledb"
)

func openPolicyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

// The policy composes its two owners explicitly:
// the subscription's stored section gives max_rss and the manual entries; the
// live Global section gives each provider's list, default score and name
// keywords. An instance-stored provider_defaults copy and the Global
// entries/max_rss take no part. A subscription without a stored section fails
// closed; an absent Global row is the code default.
func TestReadDiscoveryPolicyComposesInstanceAndLiveSitePolicy(t *testing.T) {
	db := openPolicyTestDB(t)
	ctx := context.Background()
	service := NewService(db)

	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('policy-anime', 'Policy Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ('policy-anime') ON CONFLICT (managed_anime_id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDiscoveryPolicy(ctx, "policy-anime"); err == nil {
		t.Fatal("a subscription without a stored subgroups section must fail closed")
	}

	excluded := false
	anime := persistedSubgroupsSection{
		MaxRss: 7,
		// A stale instance copy of site policy is never read.
		ProviderDefaults: &persistedProviderGroupDefaults{
			Anibt: &persistedProviderGroupDefault{Mode: persistedProviderDefaultWhitelist, Score: 99, Groups: []string{"nobody"}},
		},
		Entries: []persistedSubgroupScoreEntry{{
			Provider: persistedSubgroupProviderAniBT, GroupRef: "explicit", DisplayName: "Explicit",
			SelectionMode: persistedSubgroupSelectionAuto, Score: 91,
		}, {
			Provider: persistedSubgroupProviderAniBT, GroupRef: "manual-off", DisplayName: "Manual Off",
			SelectionMode: persistedSubgroupSelectionManual, ManualSelected: &excluded,
		}},
	}
	insertPolicySection(t, db, `INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value) VALUES ('policy-anime', 'subgroups', 'subgroups', 'subgroups-v1', $1)`, anime)

	// No Global row yet: the code default site policy.
	policy, err := service.ReadDiscoveryPolicy(ctx, "policy-anime")
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.ProviderDefaults[providerdomain.ProviderAniBT]; got.ListMode != domain.SiteBlacklist || got.Score != 50 || len(got.Listed) != 0 {
		t.Fatalf("absent Global row must be the code default, got %+v", got)
	}

	trusted := []persistedNameKeywordRule{{Keyword: "trusted", Score: 12}}
	global := persistedSubgroupsSection{
		MaxRss:  2,
		Entries: []persistedSubgroupScoreEntry{},
		ProviderDefaults: &persistedProviderGroupDefaults{
			Anibt: &persistedProviderGroupDefault{Mode: persistedProviderDefaultBlacklist, Score: 61, Groups: []string{"blocked"}},
			Mikan: &persistedProviderGroupDefault{Mode: persistedProviderDefaultWhitelist, Score: 73, Groups: []string{"42"}},
		},
		NameKeywords: &persistedProviderNameKeywordRules{Anibt: &trusted},
	}
	insertPolicySection(t, db, `INSERT INTO global_config_sections(section_name, schema_version, value) VALUES ('subgroups', 'subgroups-v1', $1)`, global)

	policy, err = service.ReadDiscoveryPolicy(ctx, "policy-anime")
	if err != nil {
		t.Fatal(err)
	}
	if policy.MaxRSS != 7 {
		t.Fatalf("max_rss=%d, want the instance's 7, never Global's", policy.MaxRSS)
	}
	if got := policy.ProviderDefaults[providerdomain.ProviderAniBT]; got.ListMode != domain.SiteBlacklist || got.Score != 61 || len(got.Listed) != 1 || got.Listed[0] != "blocked" {
		t.Fatalf("Global blacklist was not read live: %+v", got)
	}
	if got := policy.ProviderDefaults[providerdomain.ProviderMikan]; got.ListMode != domain.SiteWhitelist || got.Score != 73 {
		t.Fatalf("Global whitelist was not read live: %+v", got)
	}
	if len(policy.NameKeywords[providerdomain.ProviderAniBT]) != 1 {
		t.Fatalf("Global name keywords were not read live: %+v", policy.NameKeywords)
	}
	if len(policy.AnimeRules) != 1 || !hasProviderRule(policy.AnimeRules, providerdomain.ProviderAniBT, "manual-off", domain.Never) {
		t.Fatalf("only the manual entry is a per-anime rule: %+v", policy.AnimeRules)
	}

	decisions, err := domain.RankObservedGroups(providerdomain.ProviderAniBT, []domain.ObservedGroup{
		{ExternalID: "blocked", Name: "Blocked", Available: true},
		{ExternalID: "explicit", Name: "Explicit", Available: true},
		{ExternalID: "trusted-subs", Name: "Trusted Subs", Available: true},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]domain.GroupSelectionDecision{}
	for _, decision := range decisions {
		byRef[decision.Group.Ref.ExternalID] = decision
	}
	if byRef["blocked"].Reason != domain.ReasonSiteBlacklisted {
		t.Fatalf("the Global blacklist excludes blocked: %+v", byRef["blocked"])
	}
	if byRef["explicit"].Group.Score != 61 {
		t.Fatalf("an automatic entry scores at the site default, not its stored 91: %+v", byRef["explicit"].Group)
	}
	if byRef["trusted-subs"].Group.Score != 73 {
		t.Fatalf("a matched keyword adds to the site default: %+v", byRef["trusted-subs"].Group)
	}

	if _, err := service.ReadDiscoveryPolicy(ctx, "missing-anime"); err == nil {
		t.Fatal("an unknown subscription must fail closed")
	}
	if _, err := service.ReadDiscoveryPolicy(ctx, " "); err == nil {
		t.Fatal("a policy read without a subscription must fail closed")
	}
}

func insertPolicySection(t *testing.T, db *sql.DB, statement string, section persistedSubgroupsSection) {
	t.Helper()
	raw, err := json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), statement, raw); err != nil {
		t.Fatalf("insert typed subgroup section: %v", err)
	}
}

func hasProviderRule(rules []domain.ProviderGroupPolicy, provider providerdomain.Kind, group string, mode domain.SelectionMode) bool {
	for _, rule := range rules {
		if rule.Target.ProviderRef != nil && rule.Target.ProviderRef.Provider == provider && rule.Target.ProviderRef.ExternalID == group && rule.Mode == mode {
			return true
		}
	}
	return false
}
