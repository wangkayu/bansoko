package migrations

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// businessSnapshotV1 is the owner snapshot shape an installed database holds
// from before the retired title-attribute program left the contract. The
// upgrade touches only the schema string and programs.attrrules; every other
// value must survive unchanged.
func businessSnapshotV1() string {
	return fmt.Sprintf(`{
  "schema_version": "business-snapshot-v1",
  "sections": {
    "effective": {"subgroups": {}, "parser": {}, "release_rules": {}, "file_rules": {}, "naming_rules": {}, "file_effect": {}},
    "versions": {"subgroups": 2, "parser": 1, "release_rules": 4, "file_rules": 1, "naming_rules": 1, "file_effect": 1},
    "schemas": {"subgroups": "subgroups-v1", "parser": "parser-v1", "release_rules": "release-rules-v1", "file_rules": "file-rules-v1", "naming_rules": "naming-rules-v1", "file_effect": "file-effect-v1"}
  },
  "parser": {"schema_version": "parser-policy-envelope-v1", "kind": "regex", "policy_version": 3, "profile_version": 2, "rules_owner_version": 4, "source_owner": "global_parser_policy_default", "provenance": "frozen_at_candidate_save"},
  "admission": {"mode": "single_copy"},
  "replacement": {"schema_version": "replacement-v1", "quality_profile": %s, "source_default_version": 1, "version": 2},
  "unclassified": {"route": "skip"},
  "programs": {
    "attrrules": {"version": "title-attribute-v1@%s", "digest": "%s"},
    "vars": {"version": "naming-vars-v1@%s", "digest": "%s"}
  }
}`, `{"version":"profile-v2","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[]}]}`,
		sha256Fixture("a"), sha256Fixture("a"), sha256Fixture("b"), sha256Fixture("b"))
}

// A deployed database carries business snapshots written before the v2 shape.
// The evaluator freezes only v2, so without this upgrade every automatic
// admission fails while manual one-shot entries keep working. The upgrade
// rewrites the populated owner row in place, digest included.
func TestUpgradeConvertsPopulatedBusinessSnapshotsToV2(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "141_pt_seed_facts.sql")

	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-v1', '示例作品')`); err != nil {
		t.Fatalf("seed the managed anime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, business_snapshot, business_digest, business_version, updated_at)
SELECT 'anime-v1', $1::jsonb,
       'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb), 'UTF8'), 'sha256'), 'hex'),
       3, CURRENT_TIMESTAMP`, businessSnapshotV1()); err != nil {
		t.Fatalf("seed the v1 business snapshot: %v", err)
	}

	applyMigrationsThrough(t, db, "143_business_snapshot_v2.sql")

	var snapshot []byte
	var digest string
	var version int64
	if err := db.QueryRowContext(ctx, `
SELECT business_snapshot, business_digest, business_version
FROM anime_business_configs WHERE managed_anime_id = 'anime-v1'`).Scan(&snapshot, &digest, &version); err != nil {
		t.Fatalf("read the upgraded owner row: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(snapshot, &document); err != nil {
		t.Fatalf("decode the upgraded snapshot: %v", err)
	}
	if got := document["schema_version"]; got != "business-snapshot-v2" {
		t.Fatalf("schema_version = %v after the upgrade, want business-snapshot-v2", got)
	}
	programs, ok := document["programs"].(map[string]any)
	if !ok {
		t.Fatalf("programs = %v after the upgrade, want an object", document["programs"])
	}
	if _, retired := programs["attrrules"]; retired {
		t.Fatal("the upgraded snapshot still references the retired title-attribute program")
	}
	if _, kept := programs["vars"]; !kept {
		t.Fatal("the upgrade dropped the naming Vars program reference")
	}
	if version != 4 {
		t.Fatalf("business_version = %d after the upgrade, want 4", version)
	}
	// Everything the v2 shape does not retire stays as stored.
	sections, ok := document["sections"].(map[string]any)
	if !ok {
		t.Fatalf("sections = %v after the upgrade, want an object", document["sections"])
	}
	if got := sections["schemas"].(map[string]any)["parser"]; got != "parser-v1" {
		t.Fatalf("sections.schemas.parser = %v after the upgrade, want parser-v1", got)
	}
	if got := document["unclassified"].(map[string]any)["route"]; got != "skip" {
		t.Fatalf("unclassified.route = %v after the upgrade, want skip", got)
	}

	var recomputed string
	if err := db.QueryRowContext(ctx, `
SELECT 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb), 'UTF8'), 'sha256'), 'hex')`,
		snapshot).Scan(&recomputed); err != nil {
		t.Fatalf("recompute the upgraded digest: %v", err)
	}
	if digest != recomputed {
		t.Fatalf("business_digest = %s after the upgrade, want the canonical hash %s", digest, recomputed)
	}
}
