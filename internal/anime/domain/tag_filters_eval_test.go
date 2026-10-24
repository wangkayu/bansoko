package domain

import (
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func anibtItem(tags ...string) acquisitiondomain.FeedItem {
	return acquisitiondomain.FeedItem{Title: "[Group] Show - 03 [1080p]", Provider: "anibt", AniBTTags: &acquisitiondomain.AniBTTagSet{Schema: acquisitiondomain.AniBTTagSchemaV1, Tags: append([]string{}, tags...)}}
}

func TestAniBTTagFilterTruthTable(t *testing.T) {
	full := anibtItem("resolution:1080p", "language:CHS", "language:CHT", "subtitle:INTERNAL", "codec:HEVC")
	untagged := anibtItem()
	legacyAniBT := acquisitiondomain.FeedItem{Title: "x", Provider: "anibt"} // pre-tag AniBT release: no frozen set
	mikan := acquisitiondomain.FeedItem{Title: "x", Provider: "mikan"}
	custom := acquisitiondomain.FeedItem{Title: "x"}
	white := func(tags ...string) TagFilterRow { return TagFilterRow{Require: tags, Exclude: []string{}} }
	black := func(tags ...string) TagFilterRow { return TagFilterRow{Require: []string{}, Exclude: tags} }
	both := func(require, exclude []string) TagFilterRow { return TagFilterRow{Require: require, Exclude: exclude} }
	cases := []struct {
		name   string
		item   acquisitiondomain.FeedItem
		row    TagFilterRow
		code   string
		detail string
	}{
		{"empty whitelist passes tagged", full, white(), "", ""},
		{"empty whitelist passes untagged", untagged, white(), "", ""},
		{"whitelist all present", full, white("language:CHS", "resolution:1080p"), "", ""},
		{"whitelist case-insensitive", full, white("language:chs", "CODEC:hevc"), "", ""},
		{"whitelist other dimension unmet", full, white("language:CHS", "format:MKV"), ReasonAniBTTagWhitelistUnmet, "missing: format:MKV"},
		{"whitelist two dimensions unmet", full, white("format:MKV", "resolution:720p", "language:CHS"), ReasonAniBTTagWhitelistUnmet, "missing: format:MKV, resolution:720p"},
		{"whitelist OR within a dimension", full, white("resolution:720p", "resolution:1080p", "language:CHT", "language:CHS"), "", ""},
		{"whitelist dimension with no hit", full, white("resolution:720p", "resolution:2160p", "language:CHS"), ReasonAniBTTagWhitelistUnmet, "missing: resolution:720p, resolution:2160p"},
		{"whitelist untagged", untagged, white("language:CHS"), ReasonAniBTTagMissing, "missing: language:CHS"},
		{"whitelist legacy AniBT", legacyAniBT, white("language:CHS"), ReasonAniBTTagMissing, "missing: language:CHS"},
		{"empty blacklist", full, black(), "", ""},
		{"blacklist hit", full, black("language:CHT", "format:MP4"), ReasonAniBTTagBlacklisted, "blacklisted: language:CHT"},
		{"blacklist miss", full, black("format:MP4"), "", ""},
		{"blacklist untagged passes", untagged, black("language:CHT"), "", ""},
		{"require and exclude both satisfied", full, both([]string{"language:CHS"}, []string{"format:MP4"}), "", ""},
		{"require met, exclude hit", full, both([]string{"language:CHS"}, []string{"language:CHT"}), ReasonAniBTTagBlacklisted, "blacklisted: language:CHT"},
		{"require unmet beats exclude", full, both([]string{"format:MKV"}, []string{"language:CHT"}), ReasonAniBTTagWhitelistUnmet, "missing: format:MKV"},
		{"require and exclude on untagged", untagged, both([]string{"language:CHS"}, []string{"language:CHT"}), ReasonAniBTTagMissing, "missing: language:CHS"},
		{"mikan unaffected by whitelist", mikan, white("language:CHS"), "", ""},
		{"custom unaffected by blacklist", custom, black("language:CHS"), "", ""},
	}
	for _, tc := range cases {
		code, detail := AniBTTagFilterReason(tc.item, tc.row)
		if code != tc.code || detail != tc.detail {
			t.Fatalf("%s: got (%q, %q), want (%q, %q)", tc.name, code, detail, tc.code, tc.detail)
		}
	}
}

// The AniBT row and the regex release filter are ANDed: the tag row runs
// first and its failure is the recorded reason; a passing tag row still
// leaves the regex filter in force.
func TestEvaluateFeedItemAppliesAniBTRowBeforeReleaseFilter(t *testing.T) {
	identity := tvEpisodeIdentity(t, "3")
	effective := func(row TagFilterRow, mustContain string) EffectiveConfig {
		config := EffectiveConfig{Rules: EffectiveRules{ReleaseFilter: ReleaseFilterRules{MustContain: mustContain}}, TagFilters: DefaultResolvedTagFilters()}
		config.TagFilters.Rules.Anibt = row
		return config
	}
	item := anibtItem("language:CHT")
	both := EvaluateFeedItemWithIdentity(item, SourceEvaluationInput{}, effective(TagFilterRow{Require: []string{"language:CHS"}, Exclude: []string{}}, "720p"), MetadataHold{}, testRules(), identity)
	if both.Verdict != "filtered" || both.ReasonCode != ReasonAniBTTagWhitelistUnmet || both.ReasonDetail == nil || *both.ReasonDetail != "missing: language:CHS" {
		t.Fatalf("tag row must decide first: %+v", both)
	}
	regexOnly := EvaluateFeedItemWithIdentity(item, SourceEvaluationInput{}, effective(TagFilterRow{Require: []string{"language:CHT"}, Exclude: []string{}}, "720p"), MetadataHold{}, testRules(), identity)
	if regexOnly.Verdict != "filtered" || regexOnly.ReasonCode != "filtered_by_rules" {
		t.Fatalf("regex filter must still apply after a passing tag row: %+v", regexOnly)
	}
	accepted := EvaluateFeedItemWithIdentity(item, SourceEvaluationInput{}, effective(TagFilterRow{Require: []string{}, Exclude: []string{"language:CHS"}}, ""), MetadataHold{}, testRules(), identity)
	if accepted.Verdict != "accepted_pending_download" {
		t.Fatalf("passing both rows must accept: %+v", accepted)
	}
	// A non-AniBT release ignores the tag row entirely.
	mikan := acquisitiondomain.FeedItem{Title: item.Title, Provider: "mikan"}
	plain := EvaluateFeedItemWithIdentity(mikan, SourceEvaluationInput{}, effective(TagFilterRow{Require: []string{"language:CHS"}, Exclude: []string{}}, ""), MetadataHold{}, testRules(), identity)
	if plain.Verdict != "accepted_pending_download" {
		t.Fatalf("mikan release must not be tag-filtered: %+v", plain)
	}
	// The zero EffectiveConfig (frozen paths) filters nothing.
	zero := EvaluateFeedItemWithIdentity(anibtItem(), SourceEvaluationInput{}, EffectiveConfig{}, MetadataHold{}, testRules(), identity)
	if zero.Verdict != "accepted_pending_download" {
		t.Fatalf("zero config must not tag-filter: %+v", zero)
	}
}
