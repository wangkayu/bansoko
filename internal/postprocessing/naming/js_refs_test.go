package naming

import (
	"strings"
	"testing"
)

func analyzeExpression(t *testing.T, expression string, vars map[string]map[string]string) error {
	t.Helper()
	_, err := validateNamingExpression(expression, KindDirectory, vars)
	return err
}

// TestValidateNamingExpressionRefusesUnknownReferences covers the references
// the analyser is responsible for: a field the catalog does not advertise, an
// identifier nothing introduces, a dynamic index into a context root, and a
// vars key the loaded snapshot does not carry.
func TestValidateNamingExpressionRefusesUnknownReferences(t *testing.T) {
	loaded := map[string]map[string]string{"custom": {"label": "Custom"}}

	cases := []struct {
		name       string
		expression string
		vars       map[string]map[string]string
		want       string
		code       string
	}{
		{
			name:       "reference typo is an undeclared identifier",
			expression: "`${Anmie.Title}`",
			want:       "unknown template identifier Anmie",
		},
		{
			name:       "unadvertised leaf is refused even though the root is known",
			expression: "`${bgm.name}`",
			want:       "unknown context field bgm.name",
		},
		{
			name:       "unknown leaf under a known intermediate node",
			expression: "`${file.sub.foo}`",
			want:       "unknown context field file.sub.foo",
		},
		{
			name:       "intermediate node is not a whitelisted leaf",
			expression: "`${file.sub}`",
			want:       "unknown context field file.sub",
		},
		{
			name:       "computed index into a context root",
			expression: "`${anime['Bo' + 'gus']}`",
			want:       "dynamic context field access is not allowed",
		},
		{
			name:       "computed vars index",
			expression: "`${vars[key]}`",
			vars:       loaded,
			want:       "dynamic or unsupported vars reference",
			code:       ErrCodeNamingVarsUnknown,
		},
		{
			name:       "unloaded vars group",
			expression: "`${vars.other.label}`",
			vars:       loaded,
			want:       `naming vars group "other" is not loaded`,
			code:       ErrCodeNamingVarsUnknown,
		},
		{
			name:       "unloaded vars key",
			expression: "`${vars.custom.missing}`",
			vars:       loaded,
			want:       `naming vars key "custom"."missing" is not loaded`,
			code:       ErrCodeNamingVarsUnknown,
		},
		{
			name:       "nothing loaded refuses every vars reference",
			expression: "`${vars.custom.label}`",
			vars:       map[string]map[string]string{},
			want:       `naming vars group "custom" is not loaded`,
			code:       ErrCodeNamingVarsUnknown,
		},
		{
			name:       "alias root with an unknown field",
			expression: "`${context.Anmie.Title}`",
			want:       "unknown context field context.Anmie",
		},
		{
			name:       "syntax error",
			expression: "`${anime.title`",
			want:       "Unexpected end of input",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := analyzeExpression(t, tc.expression, tc.vars)
			if err == nil {
				t.Fatalf("expression %q was accepted, want a refusal", tc.expression)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
			if tc.code != "" {
				typed, ok := err.(*Error)
				if !ok || typed.Code != tc.code {
					t.Fatalf("error = %#v, want typed code %q", err, tc.code)
				}
			}
		})
	}
}

// TestValidateNamingExpressionAcceptsLegitimateTemplates is the other direction:
// nothing that works today may be refused because the analyser is narrow.
func TestValidateNamingExpressionAcceptsLegitimateTemplates(t *testing.T) {
	loaded := map[string]map[string]string{"custom": {"label": "Custom"}}

	accepted := []struct {
		name       string
		expression string
		vars       map[string]map[string]string
	}{
		{name: "advertised fields", expression: "`${anime.title} - E${pad(ep.display, 2)}`"},
		{name: "deep advertised leaf", expression: "`${media.resolution}`"},
		{name: "removed path inside a string literal", expression: "`${'Anime.BGM.Title'}`"},
		{name: "removed path inside a comment", expression: "`${anime.title /* Anime.BGM.Title */}`"},
		{name: "method call on a whitelisted leaf", expression: "`${file.stem.split(' ')[0]}`"},
		{name: "static bracket access on a leaf", expression: "`${file['ext']}`"},
		{name: "helper call", expression: "`${sanitize(anime.title)}`"},
		{name: "new Error with a documented global", expression: "`${new Error('x').message}`"},
		{name: "arrow function with parameters", expression: "`${((value) => value + 1)(2)}`"},
		{name: "arrow function body declaring a local", expression: "`${(() => { const title = anime.title; return title; })()}`"},
		{name: "hoisted local used before its declaration", expression: "`${(() => { return later; var later = 1; })()}`"},
		{name: "conditional and binary operators", expression: "`${anime.title ? anime.title : 'untitled'}`"},
		{name: "object literal with a shorthand reference", expression: "`${JSON.stringify({ title: anime.title })}`"},
		{name: "loaded vars reference", expression: "`${vars.custom.label}`", vars: loaded},
		{name: "no snapshot means vars are not checked", expression: "`${vars.custom.label}`"},
		{name: "alias root dereferencing an advertised field", expression: "`${anime.title}`"},
		{name: "empty expression", expression: ""},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if err := analyzeExpression(t, tc.expression, tc.vars); err != nil {
				t.Fatalf("expression %q was refused: %v", tc.expression, err)
			}
		})
	}
}

func TestValidateTemplateLimitsKeepsUnboundedExpansionGuard(t *testing.T) {
	if err := validateTemplateLimits("`${title.padEnd(200000, ' ')}`", KindDirectory); err == nil {
		t.Fatal("unbounded padding was accepted, want the limit error")
	}
	if err := validateTemplateLimits("`${upper(anime.title)}`", KindDirectory); err != nil {
		t.Fatalf("legitimate template refused by the limit check: %v", err)
	}
}
