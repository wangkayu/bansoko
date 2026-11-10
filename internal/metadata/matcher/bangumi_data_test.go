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
