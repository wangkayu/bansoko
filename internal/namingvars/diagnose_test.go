package namingvars

import (
	"errors"
	"strings"
	"testing"
)

func TestDiagnoseLocatesForbiddenConstructsAndSyntaxErrors(t *testing.T) {
	canonical := func(source string) string { return strings.ReplaceAll(source, "\r\n", "\n") }
	cases := []struct {
		name, source, code, at string
	}{
		{"expansion", "vars.a = {b: 'x'};\nvars.c = {d: [1].map(y => y)};", DiagUnboundedExpansion, ".map("},
		{"dynamic", "vars.a = {b: eval('1')};", DiagDynamicExecution, "eval"},
		{"syntax", "vars.a = {b: 'x'};\nvars.c = {d: };\n", DiagSyntax, "vars.c = {d: };"},
		{"strict only", "vars.a = {b: 010};", DiagSyntax, "010"},
	}
	for _, tc := range cases {
		diagnostics := Diagnose(tc.source)
		if len(diagnostics) == 0 || diagnostics[0].Code != tc.code {
			t.Fatalf("%s: diagnostics = %+v, want code %s", tc.name, diagnostics, tc.code)
		}
		d := diagnostics[0]
		text := canonical(tc.source)
		if d.Start < 0 || d.End <= d.Start || d.End > len(text) || d.Start < strings.Index(text, tc.at[:1]) {
			t.Fatalf("%s: span [%d,%d) of %q does not cover the problem %q", tc.name, d.Start, d.End, text, tc.at)
		}
	}
	if len(Diagnose(validVarsSource)) != 0 {
		t.Fatal("a valid program must have no diagnostics")
	}
	if d := Diagnose(""); len(d) != 1 || d[0].Code != DiagSourceEmpty || d[0].Start != -1 {
		t.Fatalf("empty = %+v", d)
	}
	if d := Diagnose(strings.Repeat("x", MaxSourceBytes+1)); len(d) != 1 || d[0].Code != DiagSourceTooLarge {
		t.Fatalf("oversized = %+v", d)
	}
}

func TestEvaluationDiagnosticPointsAtTheOutputAssignment(t *testing.T) {
	source := "vars.a = {b: 'ok'};\nvars.shown = {ep: extract(input.release_title, \"(never)\")};\n"
	sample, _ := SampleByRef(SampleDefault)
	program, err := NewProgram(ProgramRef{Version: ProfileVersion(mustFingerprint(t, source)), Digest: mustFingerprint(t, source)}, source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = program.Evaluate(BuildInput(sample.Evidence), 0)
	if err == nil {
		t.Fatal("a missing extract must fail in strict evaluation")
	}
	d := EvaluationDiagnostic(source, err)
	if d.Code != DiagOutputMissing || d.Start < 0 || !strings.HasPrefix(source[d.Start:], "ep") {
		t.Fatalf("diagnostic = %+v, want the ep key", d)
	}
	var output *OutputError
	if !errors.As(err, &output) || output.Group != "shown" || output.Key != "ep" {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(err.Error(), "choose an explicit fallback") {
		t.Fatalf("frozen diagnostic text changed: %q", err)
	}

	// A thrown TypeError is located on its line.
	thrown := "vars.a = {b: 'ok'};\nvars.c = {d: input.nothing.deep};\n"
	_, err = Run(thrown, DesignInput(), 0)
	if err == nil {
		t.Fatal("reading through undefined must throw")
	}
	d = EvaluationDiagnostic(thrown, err)
	if d.Code != DiagEvaluationFailed || d.Start < 0 || !strings.Contains(thrown[d.Start:d.End], "input.nothing") {
		t.Fatalf("thrown diagnostic = %+v", d)
	}
}

func mustFingerprint(t *testing.T, source string) string {
	t.Helper()
	fp, err := SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}
