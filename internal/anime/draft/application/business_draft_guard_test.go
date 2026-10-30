package application

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/animetest"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestCandidateDraftGuardAdmitsProviderGroupOffsets exercises the identity
// shape guard directly: the pre-subscribe group offset key is decodable for
// provider origins within the same bounds the application enforces, and every
// malformed variant stays rejected.
func TestCandidateDraftGuardAdmitsProviderGroupOffsets(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	base := func() map[string]any {
		return map[string]any{
			"origin":           "anibt",
			"origin_entry_ref": "3100",
			"title":            "Offset Guard Show",
			"metadata":         map[string]any{},
			"subgroups":        map[string]any{},
			"rss_sources":      []any{},
			"rules":            map[string]any{},
		}
	}
	offsets := func(entries ...map[string]any) []any {
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, e)
		}
		return out
	}
	entry := func(ref string, offset any) map[string]any {
		return map[string]any{"provider_group_ref": ref, "episode_offset": offset}
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		allow  bool
	}{
		{"valid", func(v map[string]any) { v["provider_group_offsets"] = offsets(entry("301", -48), entry("302", 0)) }, true},
		{"absent", func(map[string]any) {}, true},
		{"custom-origin", func(v map[string]any) {
			v["origin"], v["origin_entry_ref"] = "custom", nil
			v["instance_label"] = "label"
			v["provider_group_offsets"] = offsets(entry("301", -48))
		}, false},
		{"not-array", func(v map[string]any) { v["provider_group_offsets"] = entry("301", -48) }, false},
		{"entry-extra-key", func(v map[string]any) {
			e := entry("301", -48)
			e["hidden"] = true
			v["provider_group_offsets"] = offsets(e)
		}, false},
		{"entry-missing-offset", func(v map[string]any) {
			v["provider_group_offsets"] = offsets(map[string]any{"provider_group_ref": "301"})
		}, false},
		{"entry-blank-ref", func(v map[string]any) { v["provider_group_offsets"] = offsets(entry("  ", -48)) }, false},
		{"entry-untrimmed-ref", func(v map[string]any) { v["provider_group_offsets"] = offsets(entry(" 301", -48)) }, false},
		{"entry-fractional-offset", func(v map[string]any) { v["provider_group_offsets"] = offsets(entry("301", 1.5)) }, false},
		{"entry-offset-out-of-range", func(v map[string]any) { v["provider_group_offsets"] = offsets(entry("301", 1001)) }, false},
	} {
		body := base()
		tc.mutate(body)
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var allowed bool
		if err := db.QueryRowContext(ctx, `SELECT anime_config_candidate_identity_shape_ok($1::jsonb,$2,$3,$4)`, encoded, body["origin"], body["origin_entry_ref"], body["instance_label"]).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		if allowed != tc.allow {
			t.Fatalf("%s allowed=%v, want %v", tc.name, allowed, tc.allow)
		}
	}
}

func TestBusinessDraftMigrationRetainsStrictIdentityAndDigest(t *testing.T) {
	raw, err := os.ReadFile("../../../../db/migrations/001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"anime_config_candidate_identity_shape_ok(draft - 'business' - 'business_digest'", "business_exact_keys(b", "anime_config_candidate_canonical_json(b)", "jsonb_typeof(draft->'business_digest')"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("missing strict guard %s", required)
		}
	}
}
func TestBusinessDraftDatabaseGuardRejectsUnknownAndInvalidDigest(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	frozen := frozenBusinessFixture(t)
	draft.Business = frozen.Business
	draft.BusinessDigest = frozen.BusinessDigest
	seedBusinessPrograms(t, db, &draft)
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]json.RawMessage)
	}{
		{"valid", func(map[string]json.RawMessage) {}},
		{"unknown", func(v map[string]json.RawMessage) { v["hidden"] = json.RawMessage(`true`) }},
		{"invalid-digest", func(v map[string]json.RawMessage) { v["business_digest"] = json.RawMessage(`"sha256:bad"`) }},
		{"wrong-digest", func(v map[string]json.RawMessage) {
			v["business_digest"] = json.RawMessage(`"sha256:` + strings.Repeat("0", 64) + `"`)
		}},
		{"unknown-business", func(v map[string]json.RawMessage) {
			var b map[string]json.RawMessage
			_ = json.Unmarshal(v["business"], &b)
			b["hidden"] = json.RawMessage(`true`)
			v["business"], _ = json.Marshal(b)
		}},
	} {
		copy := map[string]json.RawMessage{}
		for k, v := range body {
			copy[k] = v
		}
		tc.mutate(copy)
		encoded, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		var allowed bool
		if err := db.QueryRowContext(ctx, `SELECT anime_config_candidate_draft_shape_ok($1::jsonb,$2,$3,$4)`, encoded, string(draft.Origin), draft.OriginEntryRef, draft.InstanceLabel).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		if allowed != (tc.name == "valid") {
			t.Fatalf("%s allowed=%v", tc.name, allowed)
		}
	}
}

// TestCandidateSaveAndPatchCarryProviderGroupOffsets drives the guarded write
// path end to end: a provider draft whose group offsets are set saves as a
// candidate and its offsets update through patch (10-30 regression: the guard
// rejected the key and every offset save surfaced as a 500).
func TestCandidateSaveAndPatchCarryProviderGroupOffsets(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	draft := frozenBusinessFixture(t)
	ref := "3100"
	draft.Origin = animedomain.SubscriptionOriginAnibt
	draft.OriginEntryRef = &ref
	draft.Title = "Offset Save Show"
	draft.ProviderGroupOffsets = []animedomain.ProviderGroupOffsetDraft{
		{ProviderGroupRef: "301", EpisodeOffset: -48},
	}
	seedBusinessPrograms(t, db, &draft)
	saved, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	storedOffsets := func(t *testing.T, id string) []animedomain.ProviderGroupOffsetDraft {
		t.Helper()
		var raw []byte
		if err := db.QueryRowContext(ctx, `SELECT draft FROM anime_config_candidates WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var body struct {
			ProviderGroupOffsets []animedomain.ProviderGroupOffsetDraft `json:"provider_group_offsets"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		return body.ProviderGroupOffsets
	}
	if got := storedOffsets(t, saved.Id); len(got) != 1 || got[0].ProviderGroupRef != "301" || got[0].EpisodeOffset != -48 {
		t.Fatalf("saved offsets = %+v", got)
	}
	draft.ProviderGroupOffsets = []animedomain.ProviderGroupOffsetDraft{
		{ProviderGroupRef: "301", EpisodeOffset: 24},
		{ProviderGroupRef: "999", EpisodeOffset: 7},
	}
	patched, err := service.PatchAnimeConfigCandidate(ctx, saved.Id, saved.Version, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patched.Version != saved.Version+1 {
		t.Fatalf("patched version = %d, want %d", patched.Version, saved.Version+1)
	}
	got := storedOffsets(t, patched.Id)
	if len(got) != 2 || got[0].EpisodeOffset != 24 || got[1].ProviderGroupRef != "999" {
		t.Fatalf("patched offsets = %+v", got)
	}
}
