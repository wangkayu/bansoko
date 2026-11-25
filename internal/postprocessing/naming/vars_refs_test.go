package naming

import (
	"reflect"
	"testing"
)

func TestTemplateReferencesVars(t *testing.T) {
	positives := []struct {
		name   string
		source string
	}{
		{"dotted vars", "${vars.anime.ep}"},
		{"nested in text", "${anime.title} - ${vars.anime.ep}.${file.ext}"},
		{"bracket vars", `${vars["anime"]["ep"]}`},
		{"optional chaining vars", "${vars?.anime?.ep}"},
		{"bare vars in helper", "${Object.keys(vars).join('-')}"},
		{"shorthand property vars", "${(({vars}) => vars.anime.ep)({vars})}"},
		{"context.vars", "${context.vars.anime.ep}"},
		{"context.Vars", "${context.Vars.anime.ep}"},
		{"context bracket vars", `${context["vars"].anime.ep}`},
		{"__ctx.vars", "${__ctx.vars.anime.ep}"},
		{"__ctx.Vars", "${__ctx.Vars.anime.ep}"},
		{"__ctx bracket Vars", `${__ctx["Vars"]["anime"]["ep"]}`},
		{"destructure from vars", "${(() => { const { anime } = vars; return anime.ep; })()}"},
		{"destructure vars from context", "${(() => { const { vars: v } = context; return v.anime.ep; })()}"},
		{"destructure Vars from __ctx", "${(() => { const { Vars } = __ctx; return Vars.anime.ep; })()}"},
		{"destructure rest from context", "${(() => { const { ...rest } = context; return rest.anime.title; })()}"},
		{"assign destructure from context", "${(() => { let vars; ({ vars } = context); return vars; })()}"},
		{"vars inside dynamic bracket", "${['a', 'b'][vars.anime.ep]}"},
		{"vars in initializer of shadowed local", "${(() => { const vars = vars.anime.ep; return vars; })()}"},
		{"vars in conditional", "${vars.anime.ep ? vars.anime.ep : ep.number}"},
	}
	for _, tc := range positives {
		t.Run("hit/"+tc.name, func(t *testing.T) {
			if !TemplateReferencesVars(tc.source) {
				t.Fatalf("TemplateReferencesVars(%q) = false, want true", tc.source)
			}
		})
	}

	negatives := []struct {
		name   string
		source string
	}{
		{"empty", ""},
		{"default tv filename", DefaultNamingRules().TV.VideoFilenameTemplate},
		{"default tv directory", DefaultNamingRules().TV.DirectoryTemplate},
		{"plain text vars", "/library/vars/vars.anime.ep/${anime.title}"},
		{"string literal double quote", `${"vars.anime.ep"}`},
		{"string literal single quote", `${'context.vars.anime.ep'}`},
		{"block comment", `${/* vars.anime.ep */ anime.title}`},
		{"line comment", "${// vars.anime.ep\nanime.title}"},
		{"object literal key", `${({ vars: "ok" }).vars}`},
		{"shadowed arrow param", `${((vars) => vars.anime.ep)({ anime: { ep: "01" } })}`},
		{"shadowed const", `${(() => { const vars = { a: "1" }; return vars.a; })()}`},
		{"shadowed context param", `${((context) => context.vars)({ vars: "1" })}`},
		{"shadowed __ctx param", `${((__ctx) => __ctx.vars)({ vars: "1" })}`},
		{"unrelated context access", `${context.anime.title}`},
		{"unrelated property named vars", `${(() => { const x = { vars: "1" }; return x.vars; })()}`},
	}
	for _, tc := range negatives {
		t.Run("clean/"+tc.name, func(t *testing.T) {
			if TemplateReferencesVars(tc.source) {
				t.Fatalf("TemplateReferencesVars(%q) = true, want false", tc.source)
			}
		})
	}
}

func TestRulesVarsReferences(t *testing.T) {
	rules := DefaultNamingRules()
	got, err := RulesVarsReferences(rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("default rules hits = %v, want none", got)
	}
	rules.TV.VideoFilenameTemplate = "${vars.anime.ep}.${file.ext}"
	rules.SubtitleSuffixTemplate = "${context.vars.sub.tag}"
	want := []string{"subtitle_suffix_template", "tv.video_filename_template"}
	got, err = RulesVarsReferences(rules)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RulesVarsReferences = %v, want %v", got, want)
	}
}

func TestCheckTemplateReferencesVarsRejectsInvalidExpression(t *testing.T) {
	const source = "${("
	if _, err := CheckTemplateReferencesVars(source); err == nil {
		t.Fatal("CheckTemplateReferencesVars accepted an invalid expression")
	}
	if !TemplateReferencesVars(source) {
		t.Fatal("boolean wrapper must conservatively report an unparseable template")
	}
	rules := DefaultNamingRules()
	rules.TV.VideoFilenameTemplate = source
	if _, err := RulesVarsReferences(rules); err == nil {
		t.Fatal("RulesVarsReferences accepted an invalid expression")
	}
}
