package application

import "testing"

func TestDownloadSubmittedTaskOrigin(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  any
	}{
		{"automatic", "subscription"}, {"subscription_add", "subscription"},
		{"one_shot", "one_shot"}, {"", nil},
	} {
		// A shared provider does not determine task origin.
		row := downloadNotificationRow{entryKind: tc.entry, inputSnapshot: []byte(`{"source":{"provenance":{"provider":"anibt"}}}`)}
		facts := downloadSubmittedFacts(row, "anime-1", taskSnapshot{}, providerCorrelation{})
		if facts["task.origin"] != tc.want || facts["release.src"] != "anibt" {
			t.Fatalf("entry=%q origin=%v provider=%v", tc.entry, facts["task.origin"], facts["release.src"])
		}
	}
}
