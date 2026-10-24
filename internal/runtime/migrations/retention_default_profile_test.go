package migrations

import (
	"context"
	"encoding/json"
	"testing"
)

// TestRetentionDefaultQualityProfile proves the seeded global retention default
// is a profile-v1 document with explicit resolution and language ranks, so the
// replacement chain can compare across those dimensions instead of blocking on
// "no explicit rank and values differ".
func TestRetentionDefaultQualityProfile(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	var profileRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT value->'quality_profile' FROM global_config_sections WHERE section_name = 'retention'`).Scan(&profileRaw); err != nil {
		t.Fatalf("read default quality profile: %v", err)
	}
	var profile struct {
		Version string `json:"version"`
		Items   []struct {
			Key   string     `json:"key"`
			Ranks [][]string `json:"ranks"`
		} `json:"items"`
	}
	if err := json.Unmarshal(profileRaw, &profile); err != nil {
		t.Fatalf("decode default quality profile: %v", err)
	}
	if profile.Version != "profile-v1" {
		t.Fatalf("profile version = %q, want profile-v1", profile.Version)
	}
	ranks := map[string][][]string{}
	var keys []string
	for _, item := range profile.Items {
		keys = append(keys, item.Key)
		ranks[item.Key] = item.Ranks
	}
	wantKeys := []string{"subgroup.score", "release.lang", "release.resolution", "release.revision"}
	if len(keys) != len(wantKeys) {
		t.Fatalf("dimension keys = %v, want %v", keys, wantKeys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("dimension keys = %v, want %v", keys, wantKeys)
		}
	}
	wantResolution := []string{"2160p", "1440p", "1080p", "720p", "480p"}
	resolution := ranks["release.resolution"]
	if len(resolution) != len(wantResolution) {
		t.Fatalf("resolution ranks = %v, want %v", resolution, wantResolution)
	}
	for i, want := range wantResolution {
		if len(resolution[i]) != 1 || resolution[i][0] != want {
			t.Fatalf("resolution ranks = %v, want %v", resolution, wantResolution)
		}
	}
	if len(ranks["release.lang"]) == 0 {
		t.Fatal("language ranks missing or empty")
	}
}
