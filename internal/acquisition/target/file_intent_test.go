package target

import (
	"testing"

	"bansoko/internal/batchplan"
)

// A planned video replaces only when every key it covers has an admitted
// replace decision; any other video keeps the request intent.
func TestFileIntentReplacesOnlyWholeFiles(t *testing.T) {
	input := PlanInput{Intent: IntentEnsurePresent, ReplaceKeys: map[string]bool{"tv:s1:e1": true, "tv:s1:e3": true}}
	for _, test := range []struct {
		name string
		file batchplan.File
		want Intent
	}{
		{"single replaced", batchplan.File{CoverageKey: "tv:s1:e1"}, IntentReplace},
		{"single filled", batchplan.File{CoverageKey: "tv:s1:e2"}, IntentEnsurePresent},
		{"double half replaced", batchplan.File{CoverageKey: "tv:s1:e2", CoverageKeys: []string{"tv:s1:e2", "tv:s1:e3"}}, IntentEnsurePresent},
		{"no key", batchplan.File{}, IntentEnsurePresent},
	} {
		if got := fileIntent(input, test.file); got != test.want {
			t.Errorf("%s: intent = %s, want %s", test.name, got, test.want)
		}
	}
	input.ReplaceKeys["tv:s1:e2"] = true
	if got := fileIntent(input, batchplan.File{CoverageKey: "tv:s1:e2", CoverageKeys: []string{"tv:s1:e2", "tv:s1:e3"}}); got != IntentReplace {
		t.Fatalf("double fully replaced: intent = %s", got)
	}
	if got := fileIntent(PlanInput{Intent: IntentReplace}, batchplan.File{CoverageKey: "tv:s1:e9"}); got != IntentReplace {
		t.Fatalf("request intent without replace keys: %s", got)
	}
}
