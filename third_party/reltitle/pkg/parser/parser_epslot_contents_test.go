package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
)

func TestNormalizeSPKindFromContents_Whitelist(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "SPECIAL", want: "SP", ok: true},
		{in: "SP", want: "SP", ok: true},
		{in: "OVA", want: "OVA", ok: true},
		{in: "OAD", want: "OAD", ok: true},
		{in: "ONA", want: "ONA", ok: true},
		{in: "NCOP", want: "NCOP", ok: true},
		{in: "NCED", want: "NCED", ok: true},
		{in: "PV", want: "PV", ok: true},
		{in: "EX", want: "", ok: false},
		{in: "TV 动画 (1989年首播版)", want: "", ok: false},
		{in: "Movies + Specials", want: "", ok: false},
	}

	for _, tc := range cases {
		got, ok := normalizeSPKindFromContents(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("normalizeSPKindFromContents(%q)=(%q,%v), want=(%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestInferSPKindFromContentsAttrs_UsesWhitelistOnly(t *testing.T) {
	attrs := []model.Attribute{
		{Type: model.AttrContents, Normalized: "TV 动画 (1989年首播版)"},
		{Type: model.AttrContents, Normalized: "EX"},
		{Type: model.AttrContents, Normalized: "SPECIAL"},
	}
	if got := inferSPKindFromContentsAttrs(attrs); got != "SP" {
		t.Fatalf("inferSPKindFromContentsAttrs()=%q want=%q", got, "SP")
	}

	attrs = []model.Attribute{
		{Type: model.AttrContents, Normalized: "EX"},
		{Type: model.AttrContents, Normalized: "TV"},
	}
	if got := inferSPKindFromContentsAttrs(attrs); got != "" {
		t.Fatalf("inferSPKindFromContentsAttrs(EX-only)=%q want empty", got)
	}
}

func TestExtractParenContentMarkers_BonusAsSpecial(t *testing.T) {
	cases := []string{
		"Bonus",
		"Bouns",
		"TOKUTEN",
		"特典",
		"特典映像",
		"Movies + Specials",
		"Movie + Bonus",
	}
	for _, in := range cases {
		got := extractParenContentMarkers(in)
		if len(got) == 0 {
			t.Fatalf("extractParenContentMarkers(%q) got empty", in)
		}
		hasSpecial := false
		for _, v := range got {
			if v == "SPECIAL" {
				hasSpecial = true
				break
			}
		}
		if !hasSpecial {
			t.Fatalf("extractParenContentMarkers(%q) should contain SPECIAL, got=%v", in, got)
		}
	}
}
