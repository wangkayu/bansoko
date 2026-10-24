package httpapi

import (
	"testing"

	plugintargetport "bansoko/internal/plugintarget/port"
)

func TestNotificationTriggerKindVocabularyMatchesRegistry(t *testing.T) {
	view := notificationTriggerKindVocabularyView()
	if len(view.TriggerKinds) != len(plugintargetport.Triggers) {
		t.Fatalf("view kinds = %d, registry = %d", len(view.TriggerKinds), len(plugintargetport.Triggers))
	}
	for index, kind := range view.TriggerKinds {
		if string(kind) != plugintargetport.Triggers[index] {
			t.Fatalf("kind %d = %q, registry = %q", index, kind, plugintargetport.Triggers[index])
		}
	}
}
