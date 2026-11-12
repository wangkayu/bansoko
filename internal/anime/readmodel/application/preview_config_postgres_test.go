package application

import (
	"context"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/animetest"
)

func TestOwnerPreviewConfigReadsGlobalCompletion(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	row := revisionConfigFixture(t)
	resolved, err := row.Resolved()
	if err != nil {
		t.Fatal(err)
	}
	subgroups := animedomain.OverrideValue{Value: resolved.Sections[animedomain.SectionSubgroups]}
	rules := map[string]animedomain.OverrideValue{}
	for key, value := range resolved.Sections {
		rules[key] = animedomain.OverrideValue{Value: value}
	}
	tags, err := animedomain.ResolveDraftTagFilters(row.Draft)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tags.Rules)
	if err != nil {
		t.Fatal(err)
	}
	rules[animedomain.SectionTagFilters] = animedomain.OverrideValue{Value: raw}
	for _, action := range []string{"remove", "pause"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('download_completion', 'download-completion-v2', $1::jsonb, 1)
ON CONFLICT (section_name) DO UPDATE SET value = EXCLUDED.value, version = global_config_sections.version + 1`, `{"action":"`+action+`"}`); err != nil {
			t.Fatal(err)
		}
		owner, err := ownerPreviewConfig(ctx, db, subgroups, rules)
		if err != nil {
			t.Fatal(err)
		}
		if string(owner.DownloadCompletion.Rules.Action) != action || owner.TagFilters.Digest != tags.Digest {
			t.Fatalf("owner policies = %+v / %+v", owner.DownloadCompletion, owner.TagFilters)
		}
		revision, err := revisionPreviewConfig(row)
		if err != nil {
			t.Fatal(err)
		}
		draft, err := (&Service{}).resolveDraftEffective(ctx, row.Draft)
		if err != nil {
			t.Fatal(err)
		}
		for _, preview := range []animedomain.EffectiveConfig{revision, draft} {
			if preview.DownloadCompletion.Rules.Action != animedomain.DownloadCompletionSeed || preview.TagFilters.Digest != tags.Digest {
				t.Fatalf("draft/revision followed globals: %+v", preview)
			}
		}
	}
}
