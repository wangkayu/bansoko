package notification

import (
	"testing"

	"bansoko/internal/templatefacts"
)

func TestTaskOrigin(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  any
	}{
		{"automatic", "subscription"}, {"subscription_add", "subscription"},
		{"one_shot", "one_shot"}, {"", nil}, {"anibt", nil},
	} {
		if got := TaskOrigin(tc.entry); got != tc.want {
			t.Errorf("TaskOrigin(%q) = %v, want %v", tc.entry, got, tc.want)
		}
	}
}

func TestTaskOriginEventScope(t *testing.T) {
	for _, tc := range []struct {
		trigger string
		allowed bool
	}{
		{"download_submitted", true}, {"download_failed", true},
		{"file_operation_completed", true}, {"file_operation_failed", true},
		{"unknown_files_imported", true}, {"release_discovered", false},
		{"subscription_changed", false}, {"replacement_applied", false},
		{"file_displaced", false}, {"replacement_deletion_failed", false},
	} {
		t.Run(tc.trigger, func(t *testing.T) {
			errs, known := Compile(tc.trigger, "Task", "{{task.origin}}")
			if !known || (len(errs) == 0) != tc.allowed {
				t.Fatalf("known=%v errors=%+v, allowed=%v", known, errs, tc.allowed)
			}
			if !tc.allowed {
				return
			}
			for _, entry := range []string{"automatic", "subscription_add", "one_shot", ""} {
				message, ok := RenderMessage(tc.trigger, "Task", "entry={{task.origin}}", map[string]any{"task.origin": TaskOrigin(entry)})
				if !ok || !message.Valid() || message.Body != "entry="+FormatFact(TaskOrigin(entry)) {
					t.Fatalf("entry=%q message=%+v", entry, message)
				}
			}
		})
	}
	fact, ok := templatefacts.Lookup("task.origin")
	if !ok || fact.In(templatefacts.ScenarioNaming) || fact.Provenance != templatefacts.ProvenanceDerived {
		t.Fatalf("task origin registry entry = %+v", fact)
	}
	if got := fact.Value(&templatefacts.FactSet{Task: templatefacts.TaskFacts{Origin: "one_shot"}}); got != "one_shot" {
		t.Fatalf("registry accessor = %v", got)
	}
}
