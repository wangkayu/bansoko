package application

import (
	"encoding/json"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
)

func TestSuccessfulFileNotificationTaskOrigin(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  any
	}{
		{"automatic", "subscription"}, {"subscription_add", "subscription"},
		{"one_shot", "one_shot"}, {"", nil},
	} {
		input := json.RawMessage(`{"source":{"provenance":{"provider":"anibt"}},"delivery":{"operation":"copy"}}`)
		raw, err := successfulFileNotificationPayload(input, artifactFileContext{relativePath: "Sample.mkv"}, protocol.Value{}, protocol.Value{}, "block", tc.entry, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var facts map[string]any
		if err := json.Unmarshal(raw, &facts); err != nil {
			t.Fatal(err)
		}
		if facts["task.origin"] != tc.want || facts["release.src"] != "anibt" {
			t.Fatalf("entry=%q origin=%v provider=%v", tc.entry, facts["task.origin"], facts["release.src"])
		}
	}
}
