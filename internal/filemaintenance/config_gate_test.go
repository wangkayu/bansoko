package filemaintenance

import (
	"fmt"
	"strings"
	"testing"
)

// The template item names the undefined variables in order and keeps whole
// names within configItemNamesLimit; nothing undefined is no item.
func TestTemplateVariableItem(t *testing.T) {
	if got := templateVariableItem(map[string]bool{"vars.b": true, "anibt.language": true, "rt.title": true}); got != "template_variable_undefined:anibt.language,rt.title,vars.b" {
		t.Fatalf("item = %q", got)
	}
	if got := templateVariableItem(map[string]bool{}); got != "" {
		t.Fatalf("nothing undefined gave %q", got)
	}

	long := map[string]bool{}
	for i := range 40 {
		long[fmt.Sprintf("vars.variable_%02d", i)] = true
	}
	got := strings.TrimPrefix(templateVariableItem(long), ConfigItemTemplateVariableUndefinedPrefix)
	if len(got) > configItemNamesLimit || !strings.HasPrefix(got, "vars.variable_00,") {
		t.Fatalf("bounded names = %q (%d)", got, len(got))
	}
	for _, name := range strings.Split(got, ",") {
		if !long[name] {
			t.Fatalf("the bound cut a name: %q", name)
		}
	}
}
