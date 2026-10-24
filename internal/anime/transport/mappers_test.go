package transport

import (
	"encoding/json"
	"reflect"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func assertJSONEqual(t *testing.T, left, right any) {
	t.Helper()
	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(leftJSON, &leftValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rightJSON, &rightValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftValue, rightValue) {
		t.Fatalf("JSON differs:\nleft:  %s\nright: %s", leftJSON, rightJSON)
	}
}

func TestDraftPreservesHTTPShape(t *testing.T) {
	label := "agent-instance"
	bgm := "bgm-1"
	draft := generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginOneshot,
		Title:         "Example Show",
		InstanceLabel: &label,
		Metadata: generated.AnimeMetadataDraft{
			Bgm: &generated.BGMBinding{WorkId: bgm},
		},
		RssSources: []generated.RSSSourceDraft{{
			SourceKind: generated.SourceKindCustomRss,
			Locator:    "https://feeds.example.test/rss",
			GroupName:  "Example",
			Title:      "Example Show",
		}},
	}

	mapped, err := Draft(draft)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, draft, mapped)
}

func TestRulesSectionsPreservesUnionShape(t *testing.T) {
	sections := generated.RulesSections{}
	sections.Parser = &generated.ParserRulesOverride{}
	if err := sections.Parser.FromParserRulesReplace(generated.ParserRulesReplace{
		Mode:  generated.ParserRulesReplaceMode("replace"),
		Value: generated.ParserRules{EpisodePatterns: []string{"(?:ep|episode)[ ._-]*(\\d+)"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Naming and file effect are left out: an absent draft section stays
	// absent through the mappers rather than becoming a JSON null.

	mapped, err := RulesSections(sections)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, sections, mapped)

	roundTrip, err := RulesSectionsToHTTP(mapped)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, sections, roundTrip)
}

func TestMetadataBindingsAndHoldPreserveValues(t *testing.T) {
	bgmHTTP := &generated.NullableBGMBinding{WorkId: "bgm-1"}
	tmdbHTTP := &generated.NullableTMDBBinding{WorkId: "tmdb-1", MediaKind: generated.TMDBMediaKindMovie, Season: 2}
	bgm, err := BGM(bgmHTTP)
	if err != nil {
		t.Fatal(err)
	}
	tmdb, err := TMDB(tmdbHTTP)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, bgmHTTP, bgm)
	assertJSONEqual(t, tmdbHTTP, tmdb)

	hold := animedomain.MetadataHold{
		Held:    true,
		Missing: []animedomain.MetadataHoldMissing{"bgm", "tmdb"},
	}
	holdHTTP, err := MetadataHold(hold)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, hold, holdHTTP)
	holdDomain, err := MetadataHoldFromHTTP(holdHTTP)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, hold, holdDomain)
}

func TestRulesPreserveValues(t *testing.T) {
	preset := "anime_default"
	naming := animedomain.NamingRules{Language: animedomain.NamingRulesLanguageJsNaming1, Preset: &preset, SubtitleSuffixTemplate: "${language}", Tv: animedomain.NamingVariant{DirectoryTemplate: "${anime.title}", VideoFilenameTemplate: "${ep.number}"}}
	effective := animedomain.EffectiveRules{Naming: naming, Parser: animedomain.ParserRules{EpisodePatterns: []string{"(\\d+)"}}, FileEffect: animedomain.FileEffectRules{Operation: animedomain.FileEffectRulesOperationMove, CollisionPolicy: animedomain.CollisionPolicyVersioned}, FileFilter: animedomain.FileFilterRules{MustContain: `\.mkv$`}, ReleaseFilter: animedomain.ReleaseFilterRules{MustContain: "1080p"}}
	effectiveHTTP, err := EffectiveRules(effective)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, effective, effectiveHTTP)
}
