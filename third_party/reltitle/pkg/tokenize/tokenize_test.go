package tokenize

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"brackets and separators", "[LoliHouse] Sample - 03 [1080p]", []string{"[", "LoliHouse", "]", "Sample", "-", "03", "[", "1080p", "]"}},
		{"full-width brackets", "【字幕组】标题", []string{"【", "字幕组", "】", "标题"}},
		{"parentheses", "Show S01E02 (BDRip)", []string{"Show", "S01E02", "(", "BDRip", ")"}},
		{"separator runes", "A_B-C|D", []string{"A", "_", "B", "-", "C", "|", "D"}},
		{"slash", "22/7", []string{"22", "/", "7"}},
		{"star", "标题 / Title ★ 特别篇", []string{"标题", "/", "Title", "★", "特别篇"}},
		{"dash variants", "a－b–c—d", []string{"a", "－", "b", "–", "c", "—", "d"}},
		{"dot splits like a separator", "AAC2.0", []string{"AAC2", ".", "0"}},
		{"collapses runs of spaces", "  a   b  ", []string{"a", "b"}},

		// A token never mixes CJK and Latin letters.
		{"CJK then Latin", "进击的巨人Attack on Titan", []string{"进击的巨人", "Attack", "on", "Titan"}},
		{"Latin then CJK", "Attack on Titan进击的巨人", []string{"Attack", "on", "Titan", "进击的巨人"}},
		{"kana then Latin", "ワンピースOne Piece", []string{"ワンピース", "One", "Piece"}},
		{"hangul then Latin", "한글Hangul", []string{"한글", "Hangul"}},
		{"Latin CJK Latin", "ABC漢字DEF", []string{"ABC", "漢字", "DEF"}},

		// Digits belong to neither script, so they stay with the surrounding token.
		{"digit inside CJK", "第3季", []string{"第3季"}},
		{"digits after CJK stay, Latin splits", "漢字123abc", []string{"漢字123", "abc"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Tokenize(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Tokenize(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTokenizeBlankInput(t *testing.T) {
	for _, in := range []string{"", " ", "   ", "\t\n"} {
		if got := Tokenize(in); len(got) != 0 {
			t.Errorf("Tokenize(%q) = %q, want no tokens", in, got)
		}
	}
}

// Tokenizing only splits: concatenating the tokens must give back the input minus whitespace.
func TestTokenizeKeepsEveryNonSpaceRune(t *testing.T) {
	inputs := []string{
		"[LoliHouse] Sample - 03 [1080p]",
		"【字幕组】关于我转生变成史莱姆这档事 第四季 / Tensei Shitara Slime Datta Ken 4th Season - 01(73)",
		"[Group] Some.Movie.2020.1080p.BluRay.x264-GROUP",
		"ワンピース　One Piece ★ 特別篇 ｜ 한글",
	}
	for _, in := range inputs {
		want := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, in)
		if got := strings.Join(Tokenize(in), ""); got != want {
			t.Errorf("Tokenize(%q) lost or changed characters:\n got  %q\n want %q", in, got, want)
		}
	}
}
