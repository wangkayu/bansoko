package namingvars

import (
	"strings"
	"testing"
	"time"
)

// InputTooLarge agrees with the evaluator's own input bound: an input it
// reports is refused by every evaluation, one it does not is evaluated.
func TestInputTooLargeMatchesTheEvaluatorBound(t *testing.T) {
	// The payload carries the release title more than once.
	within := Input{ReleaseTitle: strings.Repeat("x", MaxInputBytes/8)}
	over := Input{ReleaseTitle: strings.Repeat("x", MaxInputBytes+1)}
	if InputTooLarge(within) {
		t.Fatal("an input within the bound was reported too large")
	}
	if _, err := Run(`vars.x = {v: "1"};`, within, time.Second); err != nil {
		t.Fatalf("an input within the bound was refused: %v", err)
	}
	if !InputTooLarge(over) {
		t.Fatal("an input over the bound was not reported")
	}
	if _, err := Run(`vars.x = {v: "1"};`, over, time.Second); err == nil {
		t.Fatal("the evaluator accepted an input InputTooLarge reports")
	}
}

func TestRunLoadsStringVarsAndRejectsReservedGroup(t *testing.T) {
	vars, err := Run(`vars.anime = {ep: 12}; vars.info = {tag: " [HDR]"}`, Input{ReleaseTitle: "Show"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if vars["anime"]["ep"] != "12" || vars["info"]["tag"] != " [HDR]" {
		t.Fatalf("unexpected vars: %#v", vars)
	}
	if _, err := Run(`vars.input = {name: "nope"}`, Input{}, time.Second); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved vars group should fail closed: %v", err)
	}
}

func TestRunTimesOutAndDoesNotExposeHostIO(t *testing.T) {
	if _, err := Run(`while (true) {}`, Input{}, 2*time.Millisecond); err == nil || !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("infinite script should be interrupted: %v", err)
	}
	if _, err := Run(`vars.x = {value: typeof require}`, Input{}, time.Second); err != nil {
		t.Fatal(err)
	}
	vars, err := Run(`vars.x = {value: typeof require}`, Input{}, time.Second)
	if err != nil || vars["x"]["value"] != "undefined" {
		t.Fatalf("require must be unavailable: vars=%#v err=%v", vars, err)
	}
}

func TestFingerprintCanonicalVars(t *testing.T) {
	a := Vars{"anime": {"ep": "1"}}
	b := Vars{"anime": {"ep": "1"}}
	first, err := Fingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint(b)
	if err != nil || first != second {
		t.Fatalf("equal vars fingerprints differ: %q %q err=%v", first, second, err)
	}
}

func TestSourceFingerprintChangesWhenSourceChanges(t *testing.T) {
	first, err := SourceFingerprint("vars.x = {value: input.release_title};")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SourceFingerprint("// changed source\nvars.x = {value: input.release_title};")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("source bytes must have distinct fingerprints")
	}
	if _, err := Run(strings.Repeat("x", MaxSourceBytes+1), Input{}, time.Second); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestRunProvidesInputAndRejectsVariableReferences(t *testing.T) {
	vars, err := Run(`vars.anime = {title: input.release_title, file: input.files[0].name}`, Input{}, time.Second)
	if err != nil || vars["anime"]["title"] != DefaultInput.ReleaseTitle || vars["anime"]["file"] != DefaultInput.Files[0].Name {
		t.Fatalf("default input unavailable: vars=%#v err=%v", vars, err)
	}
	if _, err := Run(`vars.a = {value: input.release_title}; vars.b = {value: vars.a.value}`, Input{}, time.Second); err == nil || !strings.Contains(err.Error(), "independent") {
		t.Fatalf("variable reference should fail closed: %v", err)
	}
}

func TestRunBoundsOutputValue(t *testing.T) {
	source := `vars.x = {value: "` + strings.Repeat("x", MaxStringBytes+1) + `"}`
	if _, err := Run(source, Input{}, time.Second); err == nil {
		t.Fatal("oversized output value accepted")
	}
}

func TestRunRejectsIntermediateObjectExpansion(t *testing.T) {
	cases := []string{
		`vars.x = {value: new Array(100000000).join("x")}`,
		`vars.x = {value: "x".repeat(100000000)}`,
		`vars.x = {value: Array.from({length: 100000000}, function() { return "x"; })}`,
	}
	for _, source := range cases {
		if _, err := Run(source, Input{}, time.Second); err == nil || !strings.Contains(err.Error(), "unbounded") {
			t.Fatalf("unbounded intermediate expansion was accepted: %v", err)
		}
	}
}
