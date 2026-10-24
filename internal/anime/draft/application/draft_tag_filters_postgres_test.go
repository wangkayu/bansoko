package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// tag_filters is one-time rules input: a draft carries its copy and the
// subscribed instance stores exactly that value, on both the direct Subscribe
// path and candidate activation (where the saved revision carries it in its
// draft body). A draft without the section stores the code default its preview
// shows. Global is never read.

func draftWithTagFilters(t *testing.T, draft generated.AnimeDraft) generated.AnimeDraft {
	t.Helper()
	var override generated.TagFilterRulesOverride
	if err := override.FromTagFilterRulesReplace(generated.TagFilterRulesReplace{
		Mode: generated.TagFilterRulesReplaceMode("replace"),
		Value: generated.TagFilterRules{
			Anibt: generated.TagFilterRow{Require: generated.TagFilterTags{"resolution:1080p"}, Exclude: generated.TagFilterTags{}},
			Title: generated.TagFilterRow{Require: generated.TagFilterTags{}, Exclude: generated.TagFilterTags{"sub_lang:TC"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.TagFilters = &override
	return draft
}

func storedTagFiltersOverride(t *testing.T, db *sql.DB, animeID string) (json.RawMessage, bool) {
	t.Helper()
	var value []byte
	err := db.QueryRowContext(context.Background(), `SELECT value FROM anime_config_overrides WHERE managed_anime_id = $1 AND section_name = 'tag_filters'`, animeID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return value, true
}

func assertMaterializedTagFilters(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	raw, ok := storedTagFiltersOverride(t, db, animeID)
	if !ok || raw == nil {
		t.Fatal("subscribed instance has no tag_filters override")
	}
	var value generated.TagFilterRules
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Anibt.Require) != 1 || value.Anibt.Require[0] != "resolution:1080p" || len(value.Title.Exclude) != 1 || value.Title.Exclude[0] != "sub_lang:TC" {
		t.Fatalf("materialized tag_filters = %s", raw)
	}
}

func TestDirectSubscribeMaterializesDraftTagFilters(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	result, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftWithTagFilters(t, draftTestDraft(t)))}, "subscribe-draft-tag-filters")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	assertMaterializedTagFilters(t, db, result.ManagedAnime.Id)

	// Loading copies Global tag_filters; drop it again so the draft really
	// omits the section.
	bare := presetdraft.Load(t, service.db, draftTestDraft(t))
	bare.Rules.Sections.TagFilters = nil
	plain, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: bare}, "subscribe-draft-no-tag-filters")
	if err != nil {
		t.Fatalf("subscribe without tag_filters: %v", err)
	}
	raw, ok := storedTagFiltersOverride(t, db, plain.ManagedAnime.Id)
	if !ok || raw == nil {
		t.Fatal("a draft without tag_filters must store the code default")
	}
	var value generated.TagFilterRules
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Anibt.Require)+len(value.Anibt.Exclude)+len(value.Title.Require)+len(value.Title.Exclude) != 0 {
		t.Fatalf("stored tag_filters for a draft without the section = %s, want the empty default", raw)
	}
}

func TestCandidateActivationMaterializesDraftTagFilters(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, draftWithTagFilters(t, providerDraft(t, "mikan-entry-tag-filters", "Show T")))
	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-tag-filters", generated.CandidateSubscribeInput{})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	assertMaterializedTagFilters(t, db, result.ManagedAnime.Id)
}
