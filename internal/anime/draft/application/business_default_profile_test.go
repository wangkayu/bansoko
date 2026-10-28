package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/qualityprofile"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBusinessDefaultProfileMatchesFreshMigrationAndTypedContract(t *testing.T) {
	raw, err := os.ReadFile("../../../../db/migrations/001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The baseline seed is pretty-printed; compare the compacted JSON.
	// The baseline is history: it still seeds the section under its old name, and
	// migration 063 renames it. Only the seeded quality profile is read here.
	seed := regexp.MustCompile(`(?s)VALUES \('retention', 'retention-policy-v1',\s*'(\{.*?\})'::jsonb`).FindSubmatch(raw)
	if seed == nil {
		t.Fatal("replacement policy seed not found in the baseline")
	}
	var seeded struct {
		QualityProfile json.RawMessage `json:"quality_profile"`
	}
	if err := json.Unmarshal(seed[1], &seeded); err != nil {
		t.Fatal(err)
	}
	// Migration 064 replaces the seeded profile (the old built-in default) with
	// the current one: the fresh persisted default is the profile 064 writes,
	// guarded by the profile the baseline seeded.
	languages, err := os.ReadFile("../../../../db/migrations/064_language_compare_defaults.sql")
	if err != nil {
		t.Fatal(err)
	}
	update := regexp.MustCompile(`(?s)jsonb_set\(value, '\{quality_profile\}', '(\{.*?\})'::jsonb\).*?value->'quality_profile' = '(\{.*?\})'::jsonb`).FindSubmatch(languages)
	if update == nil {
		t.Fatal("quality profile update not found in migration 064")
	}
	var guard, baseline bytes.Buffer
	if err := json.Compact(&guard, update[2]); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&baseline, seeded.QualityProfile); err != nil {
		t.Fatal(err)
	}
	if guard.String() != baseline.String() {
		t.Fatal("migration 064 does not guard on the profile the baseline seeds")
	}
	// Migration 067 replaces the profile 064 wrote; the fresh persisted default
	// is the profile 067 writes, guarded by the one 064 wrote.
	spelling, err := os.ReadFile("../../../../db/migrations/067_compare_defaults_anibt_spelling.sql")
	if err != nil {
		t.Fatal(err)
	}
	next := regexp.MustCompile(`(?s)jsonb_set\(value, '\{quality_profile\}', '(\{.*?\})'::jsonb\).*?value->'quality_profile' = '(\{.*?\})'::jsonb`).FindSubmatch(spelling)
	if next == nil {
		t.Fatal("quality profile update not found in migration 067")
	}
	var guard067, written064 bytes.Buffer
	if err := json.Compact(&guard067, next[2]); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&written064, update[1]); err != nil {
		t.Fatal(err)
	}
	if guard067.String() != written064.String() {
		t.Fatal("migration 067 does not guard on the profile 064 writes")
	}
	// Migration 068 replaces the profile 067 wrote; the fresh persisted default
	// is the profile 068 writes, guarded by the one 067 wrote.
	resolutions, err := os.ReadFile("../../../../db/migrations/068_anibt_resolution_defaults.sql")
	if err != nil {
		t.Fatal(err)
	}
	last := regexp.MustCompile(`(?s)jsonb_set\(value, '\{quality_profile\}', '(\{.*?\})'::jsonb\).*?value->'quality_profile' = '(\{.*?\})'::jsonb`).FindSubmatch(resolutions)
	if last == nil {
		t.Fatal("quality profile update not found in migration 068")
	}
	var guard068, written067 bytes.Buffer
	if err := json.Compact(&guard068, last[2]); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&written067, next[1]); err != nil {
		t.Fatal(err)
	}
	if guard068.String() != written067.String() {
		t.Fatal("migration 068 does not guard on the profile 067 writes")
	}
	// Migration 094 replaces the rt.sub_lang item of the profile 068 wrote, only
	// while its ranks are exactly the ones 068 wrote; the fresh persisted default
	// is the 068 profile with that one item replaced.
	combined, err := os.ReadFile("../../../../db/migrations/094_rt_sub_lang_combined_defaults.sql")
	if err != nil {
		t.Fatal(err)
	}
	item := regexp.MustCompile(`(?s)i\.item->>'key' = 'rt\.sub_lang'\s+AND i\.item->'ranks' = '(\[.*?\])'::jsonb\s+THEN jsonb_set\(i\.item, '\{ranks\}', '(\[.*?\])'::jsonb\)`).FindSubmatch(combined)
	if item == nil {
		t.Fatal("rt.sub_lang item update not found in migration 094")
	}
	var written068 qualityprofile.Profile
	if err := json.Unmarshal(last[1], &written068); err != nil {
		t.Fatal(err)
	}
	var guard094, replaced094 [][]string
	if err := json.Unmarshal(item[1], &guard094); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(item[2], &replaced094); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range written068.Items {
		if written068.Items[i].Key != qualityprofile.KeyRtSubLang {
			continue
		}
		guardJSON, _ := json.Marshal(guard094)
		writtenJSON, _ := json.Marshal(written068.Items[i].Ranks)
		if string(guardJSON) != string(writtenJSON) {
			t.Fatal("migration 094 does not guard on the rt.sub_lang ranks 068 writes")
		}
		written068.Items[i].Ranks = replaced094
		found = true
	}
	if !found {
		t.Fatal("profile 068 writes has no rt.sub_lang item")
	}
	written094, err := json.Marshal(written068)
	if err != nil {
		t.Fatal(err)
	}
	var persisted, code bytes.Buffer
	if err := json.Compact(&persisted, written094); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&code, filemaintenance.DefaultQualityProfileJSON); err != nil {
		t.Fatal(err)
	}
	if persisted.String() != code.String() {
		t.Fatal("fresh persisted and code quality defaults differ")
	}
	policy := filemaintenance.DefaultReplacementPolicy()
	if err := filemaintenance.ValidateReplacementPolicy(policy); err != nil {
		t.Fatal(err)
	}
	frozen := frozenBusinessFixture(t)
	frozen.Business.Replacement = domain.ReplacementValue{AdmissionMode: string(policy.AdmissionMode), QualityProfile: policy.QualityProfile}
	if _, err := frozen.Business.Freeze(); err != nil {
		t.Fatal(err)
	}
	profile, err := qualityprofile.Parse(frozen.Business.Replacement.QualityProfile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(profile.Keys(), ",") != "subgroup.score,anibt.language,rt.sub_lang,anibt.resolution,rt.resolution,release.revision" {
		t.Fatalf("replacement items=%v", profile.Keys())
	}
}
