package notification

import "testing"

func TestTemplateReferencesVars(t *testing.T) {
	positives := []struct {
		name   string
		source string
	}{
		{"direct vars", "EP {{vars.anime.ep}}"},
		{"spaced vars", "{{ vars.custom.tag }}"},
		{"in default call", "{{default(vars.anime.ep, ep.number)}}"},
		{"in showIf line directive", "{{showIf(vars.anime.ep)}}Episode: {{ep.number}}"},
		{"nested function call", "{{upper(default(vars.anime.ep, 'x'))}}"},
		{"bare vars root", "{{vars}}"},
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
		{"plain text vars", "vars.anime.ep is literal text"},
		{"string literal inside expr", "{{default(ep.number, 'vars.anime.ep')}}"},
		{"normal fact", "{{anime.title}} {{ep.s00e00}}"},
		{"showIf normal fact", "{{showIf(rt.res)}}Resolution: {{rt.res}}"},
	}
	for _, tc := range negatives {
		t.Run("clean/"+tc.name, func(t *testing.T) {
			if TemplateReferencesVars(tc.source) {
				t.Fatalf("TemplateReferencesVars(%q) = true, want false", tc.source)
			}
		})
	}
}

func TestCheckTemplateReferencesVarsRejectsInvalidExpression(t *testing.T) {
	for _, source := range []string{"{{vars.anime.ep", "{{default(vars.anime.ep,))}}"} {
		if _, err := CheckTemplateReferencesVars(source); err == nil {
			t.Fatalf("CheckTemplateReferencesVars(%q) accepted an invalid expression", source)
		}
		if !TemplateReferencesVars(source) {
			t.Fatalf("TemplateReferencesVars(%q) must conservatively report an unparseable template", source)
		}
	}
}
