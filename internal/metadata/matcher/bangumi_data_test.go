package matcher

import "testing"

func TestBangumiDataIndexesLookupByProviderIDWithoutTitles(t *testing.T) {
	index, err := ParseBangumiData([]byte(`{"items":[
		{"title":"Show","begin":"2024-04-01","sites":[{"site":"bangumi","id":"123"},{"site":"mikan","id":"456"},{"site":"tmdb","id":"tv/42/season/2"}]},
		{"title":"Alias","titleTranslate":{"zh-Hans":["中文名"]},"sites":[{"site":"bangumi","id":"999"},{"site":"tmdb","id":"tv/308934-mofusand"}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	item := index.LookupByBgmID("123")
	if item == nil || item != index.LookupByMikanID("456") || item != index.LookupByTitle("Show") || item.Begin != "2024-04-01" || item.TmdbID != "tv/42/season/2" {
		t.Fatalf("provider indexes must share the parsed item: %+v", item)
	}
	if index.LookupByBgmID("999") == nil || index.LookupByTitle("中文名") == nil {
		t.Fatal("BGM IDs and translated titles must remain independently searchable")
	}
	if kind, id, season := parseTmdbRef(index.LookupByBgmID("999").TmdbID); kind != "" || id != "" || season != nil {
		t.Fatalf("malformed TMDB reference must not become a work ID: %q %q %v", kind, id, season)
	}
}

func TestParseBangumiDataRejectsEmptyOrMalformedPayload(t *testing.T) {
	for _, payload := range []string{`{"items":[]}`, `{}`, `not-json`} {
		if _, err := ParseBangumiData([]byte(payload)); err == nil {
			t.Errorf("payload %q must not publish an empty index", payload)
		}
	}
}

func TestBangumiDataTitleConflictsDoNotHideProviderIDs(t *testing.T) {
	index := NewBangumiDataIndex([]DataItem{
		{BgmID: "1", MikanID: "10", Titles: []string{"Show", "Unique"}},
		{BgmID: "2", MikanID: "20", Titles: []string{" show "}},
		{BgmID: "1", MikanID: "10", Titles: []string{"SHOW", "Unique"}},
	})
	if index.LookupByTitle("Show") != nil || index.LookupByTitle("Unique") == nil || index.LookupByBgmID("1") == nil || index.LookupByMikanID("20") == nil {
		t.Fatal("only the conflicting title should become ambiguous")
	}
}

func TestMatcherBangumiDataTrailingTitleYear(t *testing.T) {
	index := NewBangumiDataIndex([]DataItem{
		{BgmID: "1", MikanID: "101", Titles: []string{"Ranma 1/2"}, Begin: "1989-04-15"},
		{BgmID: "2", Titles: []string{"Ranma 1/2", "Ranma 1／2"}, Begin: "2024-10-06T00:00:00+09:00", TmdbID: "tv/42/season/1"},
		{BgmID: "3", Titles: []string{"Ranma 1/2", "Ranma 1/2 (2024)"}, Begin: "2025-01-01"},
		{BgmID: "4", Titles: []string{"Show"}, Begin: "2024-04-01"},
		{BgmID: "5", Titles: []string{" SHOW "}, Begin: "2024-10-01"},
		{BgmID: "6", Titles: []string{"Show"}, Begin: "2024-11-01"},
		{BgmID: "7", Titles: []string{"Invalid"}, Begin: "2024-02-30"},
	})
	m := NewMatcher(index, nil, nil, nil)
	for _, tt := range []struct {
		name, title, want string
		origin, entryRef  string
	}{
		{name: "new", title: "Ranma 1/2 [2024]", want: "2"},
		{name: "old", title: "Ranma 1/2 (1989)", want: "1"},
		{name: "normalized", title: "Ｒanma 1/2 (2024) ", want: "2"},
		{name: "unqualified conflict", title: "Ranma 1/2"},
		{name: "year does not match", title: "Ranma 1/2 (2023)"},
		{name: "ties stay ambiguous", title: "Show (2024)"},
		{name: "bare year is not stripped", title: "Ranma 1/2 2024"},
		{name: "invalid begin", title: "Invalid (2024)"},
		{name: "original title wins", title: "Ranma 1/2 (2024)", want: "3"},
		{name: "provider ID wins", title: "Ranma 1/2 [2024]", want: "1", origin: "mikan", entryRef: "101"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := &MatchResult{}
			item := m.applyBangumiData(index, MatchRequest{Title: tt.title, Origin: tt.origin, EntryRef: tt.entryRef}, result)
			if tt.want == "" {
				if item != nil || result.HasAny() {
					t.Fatalf("expected no binding, got item=%+v result=%+v", item, result)
				}
				return
			}
			if item == nil || result.BgmID == nil || *result.BgmID != tt.want {
				t.Fatalf("expected BGM %s, got item=%+v result=%+v", tt.want, item, result)
			}
			if tt.want == "2" && (result.TmdbWorkID == nil || *result.TmdbWorkID != "42") {
				t.Fatalf("expected the matched row's TMDB work, got %+v", result)
			}
		})
	}
}
