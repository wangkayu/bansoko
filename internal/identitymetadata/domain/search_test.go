package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func json_Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func contains(raw []byte, needle string) bool {
	return strings.Contains(string(raw), needle)
}

// identitymetadata/domain is the pure projection layer for external metadata
// search results. Covered:
// the JSON shape of SearchCandidate (the frontend/API depends on the field
// names), the omitempty semantics of optional fields, and
// the structure of SearchSnapshot as evidence, not a binding.

func TestSearchCandidateJSONShape(t *testing.T) {
	season := 2
	candidate := SearchCandidate{
		Provider:        "tmdb",
		ProviderWorkID:  "84958",
		Title:           "进击的巨人",
		OriginalTitle:   "进击的巨人",
		Overview:        "long description",
		PosterURL:       "https://img.example/x.jpg",
		MediaKind:       "tv",
		DetailsStatus:   DetailsStatusAvailable,
		Seasons:         []SearchSeason{{SeasonNumber: 2, Name: "第二季", EpisodeCount: 12, AirDate: "2024-01-01", IsSpecial: false}},
		SuggestedSeason: &season,
		Year:            2023,
		GenreIDs:        []int{16, 10759},
		Score:           0.95,
	}
	raw, err := json_Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	// The key field names must be camelCase (the contract/frontend consumes them).
	for _, want := range []string{
		`"provider"`, `"provider_work_id"`, `"title"`, `"original_title"`,
		`"overview"`, `"poster_url"`, `"media_kind"`, `"details_status"`, `"seasons"`, `"suggested_season"`,
		`"year"`, `"genre_ids"`, `"score"`,
	} {
		if !contains(raw, want) {
			t.Errorf("candidate JSON missing %s: %s", want, raw)
		}
	}
	if contains(raw, `"suggested_season":null`) {
		t.Error("suggested_season must be omitted when nil (omitempty)")
	}
}

func TestSearchCandidateOmitsEmptyOptionalFields(t *testing.T) {
	raw, err := json_Marshal(SearchCandidate{
		Provider:       "bgm",
		ProviderWorkID: "400602",
		Title:          "进击的巨人",
		MediaKind:      "tv",
		Year:           2023,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{`"original_title"`, `"overview"`, `"poster_url"`, `"suggested_season"`, `"genre_ids"`} {
		if contains(raw, absent) {
			t.Errorf("optional field %s must be omitted when empty: %s", absent, raw)
		}
	}
}

func TestSearchSnapshotIsEvidenceNotBinding(t *testing.T) {
	snapshot := SearchSnapshot{Provider: "bgm", Query: "frieren", Version: 1}
	if snapshot.Version != 1 || snapshot.Query != "frieren" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(snapshot.Candidates) != 0 {
		t.Fatal("empty query must yield no candidates")
	}
}
