package migrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestReplacementDefaultQualityProfile proves the installed global replacement default
// is a profile-v1 document with explicit resolution and language ranks, so the
// replacement chain can compare across those dimensions instead of blocking on
// "no explicit rank and values differ".
func TestReplacementDefaultQualityProfile(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	var profileRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT value->'quality_profile' FROM global_config_sections WHERE section_name = 'replacement'`).Scan(&profileRaw); err != nil {
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
	wantKeys := []string{"subgroup.score", "anibt.language", "rt.sub_lang", "anibt.resolution", "rt.resolution", "release.revision"}
	if len(keys) != len(wantKeys) {
		t.Fatalf("dimension keys = %v, want %v", keys, wantKeys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("dimension keys = %v, want %v", keys, wantKeys)
		}
	}
	for key, want := range map[string][]string{
		"anibt.resolution": {"4K", "2160p", "1080p", "720p", "480p", "360p"},
		"rt.resolution":    {"2160p", "1080p", "720p", "480p"},
	} {
		got := ranks[key]
		if len(got) != len(want) {
			t.Fatalf("%s ranks = %v, want %v", key, got, want)
		}
		for i, w := range want {
			if len(got[i]) != 1 || got[i][0] != w {
				t.Fatalf("%s ranks = %v, want %v", key, got, want)
			}
		}
	}
	// Migrations 064 and 067: each language layer ranks combinations, Simplified +
	// Traditional + Japanese first.
	for key, first := range map[string]string{"anibt.language": "CHS+CHT+JP", "rt.sub_lang": "SC+TC+JPN"} {
		if len(ranks[key]) != 8 || strings.Join(ranks[key][0], "+") != first {
			t.Fatalf("%s ranks = %v, want 8 combinations starting with %s", key, ranks[key], first)
		}
	}
}
