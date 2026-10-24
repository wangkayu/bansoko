package namingvars

import (
	"strings"
	"testing"
)

func TestStrictExtractPreservesMissingAndRejectsCallErrors(t *testing.T) {
	for _, source := range []string{
		`vars.x={v:extract("abc","(\\d+)")};`,
		`vars.x={v:extract("abc") || "fallback"};`,
		`vars.x={v:extract(null,"(.*)") || "fallback"};`,
		`vars.x={v:extract(undefined,"(.*)") || "fallback"};`,
		`vars.x={v:extract("abc","abc")};`,
		`vars.x={v:extract("b","(a)?b")};`,
		"vars.x={v:`${extract('abc','(never)')}`};",
		"vars.x={v:`${input.mediainfo}`};",
	} {
		t.Run(source, func(t *testing.T) {
			fp, err := SourceFingerprint(source)
			if err != nil {
				t.Fatal(err)
			}
			p, err := NewProgram(ProgramRef{Version: ProfileVersion(fp), Digest: fp}, source)
			if err != nil {
				t.Fatal(err)
			}
			f := p.FreezeEvaluation(Input{ReleaseTitle: "real title"})
			if f.Status != "error" || f.Output != nil || f.Diagnostic == "" {
				t.Fatalf("missing/error laundered: %+v", f)
			}
		})
	}
	for _, source := range []string{
		"vars.x={v:`前${`内${extract('abc','(never)') ?? 'chosen'}`}`};",
		"__varsPresent = String; vars.x={v:`${null}`};",
	} {
		got, err := evaluateSource(t, source, Input{})
		if strings.HasPrefix(source, "vars.x") {
			if err != nil || got["x"]["v"] != "前内chosen" {
				t.Fatalf("nested fallback %v %v", got, err)
			}
		} else if err == nil {
			t.Fatal("program overwrote interpolation guard")
		}
	}
	for _, expression := range []string{`extract("abc","(\\d+)") ?? "explicit"`, `extract("abc","(\\d+)") || "explicit"`, `extract("abc","(\\d+)") ?? ""`} {
		got, err := evaluateSource(t, `vars.x={v:`+expression+`};`, Input{})
		want := "explicit"
		if strings.HasSuffix(expression, `?? ""`) {
			want = ""
		}
		if err != nil || got["x"]["v"] != want {
			t.Fatalf("explicit fallback %s: %v %v", expression, got, err)
		}
	}
}
