package namingvars

import (
	"strings"
	"testing"
	"time"
)

// Variables must be independent: a Vars value may be computed from the immutable
// input, never from another Vars value. The rule is enforced by the write-only
// proxy around `vars` rather than by scanning the source text, because a scan for
// `vars.` cannot see every way a read can be spelled.
func TestVarsValuesCannotReferenceOtherVars(t *testing.T) {
	cases := map[string]string{
		"direct member read":     "vars.a = {x: input.release_title}; vars.b = {y: vars.a.x}",
		"template literal read":  "vars.a = {x: input.release_title}; vars.b = {y: `${vars.a.x}`}",
		"computed member read":   "vars.a = {x: input.release_title}; vars.b = {y: vars['a'].x}",
		"parenthesised read":     "vars.a = {x: input.release_title}; vars.b = {y: (0, vars.a).x}",
		"read inside a function": "vars.a = {x: input.release_title}; vars.b = {y: `${[vars.a.x].join()}`}",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			vars, err := Run(source, DefaultInput, time.Second)
			if err == nil {
				t.Fatalf("inter-variable reference was accepted: %v", vars)
			}
			if !strings.Contains(err.Error(), "independent") {
				t.Fatalf("error %q must explain the independence rule", err)
			}
		})
	}
}

func TestVarsTemplateLiteralWithoutCrossReferenceIsAllowed(t *testing.T) {
	// The guard must reject reads of vars, not template literals as such: the
	// documented way to compose a string from the frozen input still works.
	vars, err := Run("vars.c = {z: `pre-${input.release_title}-post`}", DefaultInput, time.Second)
	if err != nil {
		t.Fatalf("a template literal over the immutable input must be allowed: %v", err)
	}
	if vars["c"]["z"] != "pre-"+DefaultInput.ReleaseTitle+"-post" {
		t.Fatalf("composed value = %q", vars["c"]["z"])
	}
}

func TestVarsGroupMayBeExtendedByAssignment(t *testing.T) {
	// Assigning into an already declared group is a write, not a read of another
	// variable, so the documented single-group form keeps working.
	vars, err := Run("vars.a = {x: '1'}; vars.a.y = '2';", DefaultInput, time.Second)
	if err != nil {
		t.Fatalf("assigning into a declared group must be allowed: %v", err)
	}
	if vars["a"]["x"] != "1" || vars["a"]["y"] != "2" {
		t.Fatalf("group content = %v", vars["a"])
	}
}

// A Vars program runs against a frozen input snapshot: it must not be able to
// rewrite the identity facts the same evaluation reads.
func TestRunCannotMutateTheFrozenInput(t *testing.T) {
	source := "input.release_title = 'forged'; vars.forged = {title: input.release_title};"
	vars, err := Run(source, DefaultInput, time.Second)
	if err != nil {
		// A strict runtime may reject the write outright. The observation below is
		// the contract, so re-evaluate with a program that only reads.
		source = "vars.forged = {title: input.release_title};"
		if vars, err = Run(source, DefaultInput, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if got := vars["forged"]["title"]; got != DefaultInput.ReleaseTitle {
		t.Fatalf("observed release title = %q, want the frozen original %q", got, DefaultInput.ReleaseTitle)
	}
}

// Vars hold presentation strings only: no group, key, or value may reach the
// identity facts, and the collection itself is bounded and name-checked.
func TestVarsRejectsInvalidShapeAndReservedGroups(t *testing.T) {
	cases := map[string]string{
		"reserved group": "vars.input = {title: 'forged'};",
		"invalid group":  "vars.Anime = {title: 'forged'};",
		"invalid key":    "vars.anime = {'Title': 'forged'};",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			vars, err := Run(source, DefaultInput, time.Second)
			if err == nil {
				t.Fatalf("source was accepted and produced %v", vars)
			}
		})
	}
}
