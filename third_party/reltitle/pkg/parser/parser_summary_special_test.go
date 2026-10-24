package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

// "总集篇01_12.5" is a summary special: special_index 01, absolute episode 12.5.
// The decimal part used to be lost because the template adjudication picked the
// already-consumed "12" token as the episode anchor.
func TestParserSummarySpecialKeepsDecimalEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[Group] Some Show [总集篇01_12.5][1080p]")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalEpisode(res); got != "12.5" {
		t.Fatalf("episode=%q want=%q", got, "12.5")
	}
	if got := attrValues(res, model.AttrSpecialFlag); len(got) != 1 || got[0] != "总集篇" {
		t.Fatalf("special_flag=%q want=[总集篇]", got)
	}
	if got := attrValues(res, model.AttrSpecialIndex); len(got) != 1 || got[0] != "1" {
		t.Fatalf("special_index=%q want=[1]", got)
	}
}

// Tokens consumed by earlier extractors must not become the episode anchor.
func TestFindStrongBracketEpisodeAnchorSkipsConsumedTokens(t *testing.T) {
	tokens := []model.Token{
		{Text: "["}, {Text: "ABS:12.5"}, {Text: "]"},
		{Text: "["}, {Text: "12", Removed: true}, {Text: "]"},
		{Text: "["}, {Text: "S02E03"}, {Text: "]"},
	}
	ep, season, idx, ok := findStrongBracketEpisodeAnchor(tokens, 1)
	if !ok || idx != 7 || ep != "3" || season != "2" {
		t.Fatalf("got ep=%q season=%q idx=%d ok=%v, want the S02E03 token (idx 7)", ep, season, idx, ok)
	}

	// With only a consumed candidate left there is no anchor.
	if _, _, idx, ok := findStrongBracketEpisodeAnchor(tokens[:6], 1); ok {
		t.Fatalf("a consumed token was used as an anchor (idx %d)", idx)
	}
}
