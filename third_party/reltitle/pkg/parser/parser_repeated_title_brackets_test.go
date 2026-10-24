package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

func TestParserRepeatedTitleBrackets(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ raw, title, alias string }{
		{"[黒ネズミたち] Battle Spirits [Re] 絕界之空 / Battle Spirits [Re]: Zekkai no Kuu - 01 (CR 1920x1080 AVC AAC MKV)", "Battle Spirits [Re] 絕界之空", "Battle Spirits [Re] Zekkai no Kuu"},
		{"[Group] 甲乙丙丁[same]/abcd[same] - 01 [1080p AVC AAC]", "甲乙丙丁 [same]", "abcd [same]"},
		{"[Group] 甲乙丙丁[Same]／abcd[same] - 01 [1080p AVC AAC]", "甲乙丙丁 [Same]", "abcd [same]"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.CanonicalTitle(); got != tc.title {
				t.Fatalf("title=%q want=%q", got, tc.title)
			}
			if got := firstAttrValue(res, model.AttrTitleAlias); got != tc.alias {
				t.Fatalf("alias=%q want=%q", got, tc.alias)
			}
			if got := canonicalEpisode(res); got != "1" {
				t.Fatalf("episode=%q", got)
			}
			for _, a := range res.Attributes {
				if a.Type == model.AttrTitle {
					span := res.Tokens[a.TokenStart:a.TokenEnd]
					texts := []string{}
					for _, tok := range span {
						texts = append(texts, tok.Text)
					}
					if !strings.Contains(strings.Join(texts, " "), "[") {
						t.Fatal("title span truncated before its internal bracket")
					}
				}
			}
		})
	}
}

func TestRepeatedTitleBracketProtectionGuards(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"甲乙[same]/abcd[same]", true},
		{"甲乙[same] / abcd[other]", false},
		{"甲乙[same] / abcd", false},
		{"[same] / [same]", false},
		{"甲乙[1080p] / abcd[1080p]", false},
		{"甲乙[01] / abcd[01]", false},
		{"甲乙[简体] / abcd[简体]", false},
		{"甲乙[same / abcd[same]", false},
		{"甲乙 (说明) [same] / abcd[same]", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			tokens := []model.Token{}
			for _, text := range tokenize.Tokenize(tc.raw) {
				tokens = append(tokens, model.Token{Text: text})
			}
			protectRepeatedTitleBrackets(tokens, 0, len(tokens))
			got := false
			for _, tok := range tokens {
				got = got || tok.Protected
			}
			if got != tc.want {
				t.Fatalf("protected=%v want=%v", got, tc.want)
			}
		})
	}
	for _, consumed := range []bool{false, true} {
		tokens := []model.Token{}
		for _, text := range tokenize.Tokenize("甲乙[same]/abcd[same]") {
			tokens = append(tokens, model.Token{Text: text})
		}
		end := len(tokens)
		if consumed {
			tokens[2].Removed = true // An earlier extractor already recognized metadata.
		} else {
			end = 4 // The second bracket lies outside the title slot.
		}
		protectRepeatedTitleBrackets(tokens, 0, end)
		for _, tok := range tokens {
			if tok.Protected {
				t.Fatal("protected a consumed block or matched outside the title slot")
			}
		}
	}
}
