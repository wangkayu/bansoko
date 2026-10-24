package application

import (
	"context"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/presetdraft"
)

// tag_filters is an ordinary rules-owner section and one-time input: the
// instance stores the value it was subscribed with, a Global change never
// reaches it, a replace overwrites it, and an owner save that omits the
// optional section keeps the stored value.
func TestTagFiltersOwnerSectionReadWriteIsOneTimeInput(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-tag-filters")
	animeID := subscribe.ManagedAnime.Id
	ctx := context.Background()

	owner, err := service.ReadRulesOwnerApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.EffectiveTagFilters.Anibt.Require) != 0 || len(owner.EffectiveTagFilters.Anibt.Exclude) != 0 || owner.HasAniBTSource {
		t.Fatalf("fresh owner = %+v", owner)
	}
	stored := string(owner.Sections.TagFilters)

	// A later Global save never reaches the instance.
	if _, err := db.ExecContext(ctx, `
INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('tag_filters', 'tag-filters-v2', '{"anibt":{"require":[],"exclude":["language:CHT"]},"title":{"require":[],"exclude":[]}}', 3)`); err != nil {
		t.Fatal(err)
	}
	owner, err = service.ReadRulesOwnerApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	if string(owner.Sections.TagFilters) != stored || len(owner.EffectiveTagFilters.Anibt.Exclude) != 0 {
		t.Fatalf("instance tag_filters followed Global: %s %+v", owner.Sections.TagFilters, owner.EffectiveTagFilters)
	}

	sections := presetdraft.DefaultDomainRules(t)
	sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":["language:CHS","resolution:1080p"],"exclude":[]},"title":{"require":["sub_lang:SC"],"exclude":[]}}}`)
	patched, err := service.PatchAnimeRules(ctx, animeID, RulesPatchInput{ExpectedVersion: 1, Sections: sections})
	if err != nil {
		t.Fatalf("replace tag_filters: %v", err)
	}
	if len(patched.Rules.EffectiveTagFilters.Anibt.Require) != 2 || len(patched.Rules.EffectiveTagFilters.Title.Require) != 1 {
		t.Fatalf("replace did not win: %+v", patched.Rules.EffectiveTagFilters)
	}
	var schema, value string
	if err := db.QueryRowContext(ctx, `SELECT schema_version, value::text FROM anime_config_overrides WHERE managed_anime_id = $1 AND section_name = 'tag_filters' AND owner_type = 'rules'`, animeID).Scan(&schema, &value); err != nil {
		t.Fatal(err)
	}
	if schema != animedomain.TagFiltersSchemaVersion {
		t.Fatalf("stored schema = %q", schema)
	}

	// Omitting the optional section keeps the stored value.
	sections.TagFilters = nil
	patched, err = service.PatchAnimeRules(ctx, animeID, RulesPatchInput{ExpectedVersion: 2, Sections: sections})
	if err != nil {
		t.Fatal(err)
	}
	if len(patched.Rules.EffectiveTagFilters.Anibt.Require) != 2 {
		t.Fatalf("omitted tag_filters reset the stored value: %+v", patched.Rules.EffectiveTagFilters)
	}

	// There is no inherit mode.
	sections.TagFilters = json.RawMessage(`{"mode":"inherit"}`)
	if _, err := service.PatchAnimeRules(ctx, animeID, RulesPatchInput{ExpectedVersion: 3, Sections: sections}); err == nil {
		t.Fatal("inherit tag_filters accepted")
	}

	// An invalid value is refused before any write.
	sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":["episode:9"],"exclude":[]},"title":{"require":[],"exclude":[]}}}`)
	if _, err := service.PatchAnimeRules(ctx, animeID, RulesPatchInput{ExpectedVersion: 3, Sections: sections}); err == nil {
		t.Fatal("invalid tag_filters dimension accepted")
	}
}
