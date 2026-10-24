package application

import (
	"encoding/json"
	"testing"
)

// The file plan finds an operation's approval by target path in the delivery replacements
// list (input schema 7) or the single known replacement of an earlier input;
// a delivery that is not intent replace approves no path.
func TestApprovedReplacementPaths(t *testing.T) {
	approval := func(path string) string {
		return `{"expected_object":{"location":{"canonical_path":"` + path + `"}}}`
	}
	for name, c := range map[string]struct {
		input string
		want  []string
	}{
		"v3 list":     {`{"delivery":{"intent":"replace","replacements":[` + approval("/l/a.mkv") + `,` + approval("/l/b.mkv") + `]}}`, []string{"/l/a.mkv", "/l/b.mkv"}},
		"v2 known":    {`{"delivery":{"intent":"replace","replacement":{"state":"known","value":` + approval("/l/a.mkv") + `}}}`, []string{"/l/a.mkv"}},
		"v2 absent":   {`{"delivery":{"intent":"ensure_present","replacement":{"state":"absent","value":null}}}`, nil},
		"not replace": {`{"delivery":{"intent":"ensure_present","replacements":[` + approval("/l/a.mkv") + `]}}`, nil},
	} {
		got, err := approvedReplacementPaths(json.RawMessage(c.input))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s: paths = %v, want %v", name, got, c.want)
		}
		for _, path := range c.want {
			if !got[path] {
				t.Fatalf("%s: %s not approved", name, path)
			}
		}
	}
}
