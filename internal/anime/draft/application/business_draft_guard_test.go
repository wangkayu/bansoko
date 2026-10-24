package application

import (
	"bansoko/internal/testutil/animetest"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

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
