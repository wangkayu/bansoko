package application

import (
	"encoding/json"
	"testing"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

// Every payload key of the file triggers is a key the producer writes;
// the typed success path adds the frozen input and episode facts.
func TestFileNotificationPayloadCoversCatalog(t *testing.T) {
	for _, trigger := range []string{plugintargetport.TriggerFileOperationCompleted, plugintargetport.TriggerFileOperationFailed} {
		fields := fileOperationNotificationFacts{frozen: notification.InputFacts(nil)}
		for key, value := range notification.EpisodeFacts(nil) {
			fields.frozen[key] = value
		}
		raw, err := buildFileOperationNotificationPayload(fields, trigger, "success", "file_operation_completed", "")
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		for _, key := range notification.PayloadKeys(trigger) {
			if key == "event.at" {
				continue // filled by the admission resolver from the ledger row
			}
			if _, ok := payload[key]; !ok {
				t.Errorf("%s: payload never writes %s", trigger, key)
			}
		}
	}
}
