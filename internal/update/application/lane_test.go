package application

import (
	"strings"
	"testing"
)

func TestLaneSourceResolvesThisProjectsChannels(t *testing.T) {
	for _, tc := range []struct {
		lane, branch string
	}{
		{"", "update-channel"},
		{"stable", "update-channel"},
		{"test", "update-channel-test"},
		{" TEST ", "update-channel-test"},
	} {
		t.Run(strings.TrimSpace(tc.lane), func(t *testing.T) {
			documentURL, packageRoot, err := LaneSource(tc.lane, "")
			if err != nil {
				t.Fatal(err)
			}
			wantRoot := channelRepositoryRoot + tc.branch + "/"
			if packageRoot != wantRoot || documentURL != wantRoot+"channel-v1.json" {
				t.Fatalf("lane %q = %q %q, want root %q", tc.lane, documentURL, packageRoot, wantRoot)
			}
		})
	}
}

func TestLaneSourceRefusesAnUnknownLane(t *testing.T) {
	if _, _, err := LaneSource("nightly", ""); err == nil {
		t.Fatal("an undeclared lane was accepted")
	}
}

// The development override must stay a suffix of the same tree, so a local
// server plays the channel without changing the layout the real host serves.
func TestLaneSourceOverrideKeepsTheBranchLayout(t *testing.T) {
	documentURL, packageRoot, err := LaneSource(LaneTest, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if documentURL != "http://127.0.0.1:8080/update-channel-test/channel-v1.json" ||
		packageRoot != "http://127.0.0.1:8080/update-channel-test/" {
		t.Fatalf("override layout = %q %q", documentURL, packageRoot)
	}
}

func TestRetiredUpdateSourceEnvironmentIsRefused(t *testing.T) {
	clean := func(string) string { return "" }
	if err := CheckRetiredSourceEnv(clean); err != nil {
		t.Fatalf("an empty environment was refused: %v", err)
	}
	for _, name := range []string{
		"BANSOKO_UPDATE_MANIFEST_URL",
		"BANSOKO_UPDATE_CHANNEL_URL",
		"BANSOKO_UPDATE_PACKAGE_ROOT",
		"BANSOKO_UPDATE_PUBLIC_KEY_PATH",
	} {
		getenv := func(key string) string {
			if key == name {
				return "https://example.invalid/update-channel/"
			}
			return ""
		}
		if err := CheckRetiredSourceEnv(getenv); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s was not refused: %v", name, err)
		}
	}
}
