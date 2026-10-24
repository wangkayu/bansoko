package domain

import (
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestTitleTagRowValidatesAgainstReltitleDictionary(t *testing.T) {
	valid := `{"anibt":{"require":[],"exclude":[]},"title":{"require":["resolution:1080p","sub_lang:SC","source_type:WEBRip"],"exclude":[]}}`
	if err := ValidateSectionValue(SectionTagFilters, json.RawMessage(valid)); err != nil {
		t.Fatalf("dictionary values rejected: %v", err)
	}
	invalid := map[string]string{
		"value outside dictionary": `{"anibt":{"require":[],"exclude":[]},"title":{"require":["resolution:1081p"],"exclude":[]}}`,
		"episode dimension":        `{"anibt":{"require":[],"exclude":[]},"title":{"require":["episode:3"],"exclude":[]}}`,
		"retired provisional dim":  `{"anibt":{"require":[],"exclude":[]},"title":{"require":["bit_depth:10bit"],"exclude":[]}}`,
	}
	for name, raw := range invalid {
		if err := ValidateSectionValue(SectionTagFilters, json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted: %s", name, raw)
		}
	}
	// A stored value the dictionary no longer lists still resolves; it simply
	// never matches.
	stale := json.RawMessage(`{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":["resolution:1081p"]}}`)
	resolved, err := ResolveTagFilters(OverrideValue{Value: stale})
	if err != nil || resolved.Rules.Title.Exclude[0] != "resolution:1081p" {
		t.Fatalf("stale title value must resolve: %#v, %v", resolved, err)
	}
}

func TestTitleTagFilterTruthTable(t *testing.T) {
	title := "[SampleGroup] Show - 05 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]"
	parsed := acquisitiondomain.FeedItem{Title: title} // no frozen set: derived from the title
	frozen := acquisitiondomain.FeedItem{Title: title, TitleTags: &acquisitiondomain.TitleTagSet{Tags: []string{"resolution:720p"}}}
	untagged := acquisitiondomain.FeedItem{Title: "Show 05"}
	anibt := acquisitiondomain.FeedItem{Title: title, Provider: "anibt", AniBTTags: &acquisitiondomain.AniBTTagSet{Tags: []string{}}}
	white := func(tags ...string) TagFilterRow { return TagFilterRow{Require: tags, Exclude: []string{}} }
	black := func(tags ...string) TagFilterRow { return TagFilterRow{Require: []string{}, Exclude: tags} }
	cases := []struct {
		name   string
		item   acquisitiondomain.FeedItem
		row    TagFilterRow
		code   string
		detail string
	}{
		{"empty whitelist", untagged, white(), "", ""},
		{"whitelist all present", parsed, white("resolution:1080p", "sub_lang:SC", "source_type:webrip"), "", ""},
		{"whitelist unmet", parsed, white("resolution:1080p", "container:MKV"), ReasonTitleTagWhitelistUnmet, "missing: container:MKV"},
		{"whitelist OR within a dimension", parsed, white("resolution:720p", "resolution:1080p"), "", ""},
		{"whitelist untagged", untagged, white("resolution:1080p"), ReasonTitleTagMissing, "missing: resolution:1080p"},
		{"frozen set wins over the title", frozen, white("resolution:1080p"), ReasonTitleTagWhitelistUnmet, "missing: resolution:1080p"},
		{"blacklist hit", parsed, black("sub_lang:TC", "container:MP4"), ReasonTitleTagBlacklisted, "blacklisted: sub_lang:TC"},
		{"blacklist untagged passes", untagged, black("sub_lang:TC"), "", ""},
		{"applies to AniBT releases", anibt, black("sub_lang:TC"), ReasonTitleTagBlacklisted, "blacklisted: sub_lang:TC"},
	}
	for _, tc := range cases {
		code, detail := TitleTagFilterReason(tc.item, tc.row)
		if code != tc.code || detail != tc.detail {
			t.Fatalf("%s: got (%q, %q), want (%q, %q)", tc.name, code, detail, tc.code, tc.detail)
		}
	}
}

// Both rows must pass: the AniBT row decides first for AniBT releases, the
// title row applies to every release, and the regex filter still follows.
func TestEvaluateFeedItemAppliesBothTagRows(t *testing.T) {
	identity := tvEpisodeIdentity(t, "3")
	effective := func(anibt, title TagFilterRow) EffectiveConfig {
		config := EffectiveConfig{TagFilters: DefaultResolvedTagFilters()}
		config.TagFilters.Rules.Anibt = anibt
		config.TagFilters.Rules.Title = title
		return config
	}
	none := TagFilterRow{Require: []string{}, Exclude: []string{}}
	anibtItem := anibtItem("language:CHS")
	anibtItem.Title = "[Group] Show - 03 [1080p][CHT]"

	both := EvaluateFeedItemWithIdentity(anibtItem, SourceEvaluationInput{}, effective(TagFilterRow{Require: []string{"language:CHT"}, Exclude: []string{}}, TagFilterRow{Require: []string{"resolution:720p"}, Exclude: []string{}}), MetadataHold{}, testRules(), identity)
	if both.ReasonCode != ReasonAniBTTagWhitelistUnmet {
		t.Fatalf("AniBT row must decide first: %+v", both)
	}
	titleOnly := EvaluateFeedItemWithIdentity(anibtItem, SourceEvaluationInput{}, effective(TagFilterRow{Require: []string{"language:CHS"}, Exclude: []string{}}, TagFilterRow{Require: []string{}, Exclude: []string{"sub_lang:TC"}}), MetadataHold{}, testRules(), identity)
	if titleOnly.Verdict != "filtered" || titleOnly.ReasonCode != ReasonTitleTagBlacklisted || titleOnly.ReasonDetail == nil || *titleOnly.ReasonDetail != "blacklisted: sub_lang:TC" {
		t.Fatalf("title row must apply to an AniBT release that passed its own row: %+v", titleOnly)
	}
	mikan := acquisitiondomain.FeedItem{Title: "[Group] Show - 03 [720p]", Provider: "mikan"}
	filtered := EvaluateFeedItemWithIdentity(mikan, SourceEvaluationInput{}, effective(none, TagFilterRow{Require: []string{"resolution:1080p"}, Exclude: []string{}}), MetadataHold{}, testRules(), identity)
	if filtered.ReasonCode != ReasonTitleTagWhitelistUnmet {
		t.Fatalf("title row must apply to non-AniBT releases: %+v", filtered)
	}
	accepted := EvaluateFeedItemWithIdentity(mikan, SourceEvaluationInput{}, effective(none, TagFilterRow{Require: []string{"resolution:720p"}, Exclude: []string{}}), MetadataHold{}, testRules(), identity)
	if accepted.Verdict != "accepted_pending_download" {
		t.Fatalf("passing title row must accept: %+v", accepted)
	}
}
