package application

import (
	"context"
	"encoding/json"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/presetdraft"
)

// download_completion is a Global-only setting: the owner rules write never
// stores it and the owner read never shows it, and an owner save leaves no
// download_completion override row.
func TestDownloadCompletionIsNotAnOwnerSection(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-download-completion")
	animeID := subscribe.ManagedAnime.Id
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('download_completion', 'download-completion-v2', '{"action":"seed","seed_limits":{"ratio":2,"at_limit":"stop"}}', 3)`); err != nil {
		t.Fatal(err)
	}
	sections := RulesPatchInput{ExpectedVersion: 1, Sections: presetdraft.DefaultDomainRules(t)}
	sections.Sections.TagFilters = nil
	patched, err := service.PatchAnimeRules(ctx, animeID, sections)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(patched.Rules)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatal(err)
	}
	if _, present := probe["effective_download_completion"]; present {
		t.Fatalf("the owner read still carries download_completion: %s", encoded)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM anime_config_overrides WHERE managed_anime_id = $1 AND section_name = 'download_completion'`, animeID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("download_completion override rows = %d", n)
	}
}
