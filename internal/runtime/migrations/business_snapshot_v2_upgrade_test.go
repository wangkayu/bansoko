package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// businessSnapshotV1 is the owner snapshot shape an installed 0.2.0 database
// holds: migration 063 renamed retention to replacement, migration 142 moved
// the comparison profile to profile-v2, and the retired title-attribute
// program was still part of the contract. The v2 upgrade touches only the
// schema string and programs.attrrules; every other value must survive.
func businessSnapshotV1() string {
	return `{
  "schema_version": "business-snapshot-v1",
  "sections": {
    "subgroups": {},
    "parser": {},
    "release_rules": {"must_contain": "", "not_in": ""},
    "file_rules": {"must_contain": "", "not_in": ""},
    "naming_rules": {},
    "file_effect": {}
  },
  "section_versions": {"subgroups": 2, "parser": 1, "release_rules": 4, "file_rules": 1, "naming_rules": 1, "file_effect": 1},
  "section_schemas": {"subgroups": "subgroups-v1", "parser": "parser-v1", "release_rules": "release-rules-v1", "file_rules": "file-rules-v1", "naming_rules": "naming-rules-v1", "file_effect": "file-effect-v1"},
  "parser": {"kind": "regex", "policy_version": 3, "profile_version": 2, "rules_owner_version": 4, "source_owner": "global_parser_policy_default", "provenance": "frozen_at_candidate_save", "schema_version": "parser-policy-envelope-v1"},
  "replacement": {"admission_mode": "single_copy", "quality_profile": {"version": "profile-v2", "items": [
    {"key": "subgroup.score", "ranks": []},
    {"key": "rt.sub_lang", "ranks": []}
  ]}},
  "replacement_version": 2,
  "unclassified_route": "skip",
  "file_effect_version": 1,
  "programs": {
    "attrrules": {"version": "title-attribute-v1@` + sha256Fixture("a") + `", "digest": "` + sha256Fixture("a") + `"},
    "vars": {"version": "naming-vars-v1@` + sha256Fixture("b") + `", "digest": "` + sha256Fixture("b") + `"}
  }
}`
}

// seedV1Candidate inserts a candidate holding a v1 business draft. The
// candidate guard refuses a stored v1 draft after 142, so the seed uses the
// migration-only fixture switch the same way migrations 063 and 129 do.
func seedV1Candidate(t *testing.T, db *sql.DB, id string, activated sql.NullString, status, instanceLabel string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL bansoko.candidate_fixture = 'on'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
WITH document AS (SELECT $2::jsonb AS business),
     draft AS (
       SELECT jsonb_build_object(
           'origin', 'custom',
           'title', '示例作品',
           'instance_label', $3::text,
           'business', business,
           'business_digest', 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business), 'UTF8'), 'sha256'), 'hex')
       ) AS body
       FROM document)
INSERT INTO anime_config_candidates(
    id, origin, origin_entry_ref, title, instance_label, draft, draft_digest,
    activated_managed_anime_id, status, last_activity, retention_until)
SELECT $1::text, 'custom', NULL, '示例作品', $3::text, body,
       'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(body), 'UTF8'), 'sha256'), 'hex'),
       $4::text, $5::text, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days'
FROM draft`,
		id, businessSnapshotV1(), instanceLabel, activated, status); err != nil {
		t.Fatalf("seed candidate %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A deployed database carries business snapshots written before the v2 shape.
// The evaluator freezes only v2, so without this upgrade every automatic
// admission fails while manual one-shot entries keep working. The upgrade
// rewrites the populated owner row and every working candidate/revision with
// its digest; linked (dormant) candidates are history and keep their bytes.
func TestUpgradeConvertsPopulatedBusinessSnapshotsToTheCurrentContract(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "141_pt_seed_facts.sql")

	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-v1', '示例作品')`); err != nil {
		t.Fatalf("seed the managed anime: %v", err)
	}
	seedV1Candidate(t, db, "cand-v1", sql.NullString{}, "working", "示例作品")
	seedV1Candidate(t, db, "cand-linked", sql.NullString{String: "anime-v1", Valid: true}, "dormant", "示例作品")

	keys := revisionSectionsCurrent
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidate_revisions(
    candidate_id, revision, draft, draft_digest, effective, section_versions,
    section_schemas, effective_digest, parser_policy, parser_policy_digest,
    business_snapshot, business_digest)
SELECT c.id, 1, c.draft, c.draft_digest, $2::jsonb, $3::jsonb, $4::jsonb, $5,
       $6::jsonb, $7, $8::jsonb,
       'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($8::jsonb), 'UTF8'), 'sha256'), 'hex')
FROM anime_config_candidates c WHERE c.id = $1`,
		"cand-v1", keys.effective, keys.versions, keys.schemas, sha256Fixture("c"),
		keys.parserPolicy, sha256Fixture("f"), businessSnapshotV1()); err != nil {
		t.Fatalf("seed the candidate revision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(
    managed_anime_id, candidate_id, revision, business_snapshot, business_digest,
    business_version, updated_at)
SELECT 'anime-v1', 'cand-v1', 1, $1::jsonb,
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
	sectionSchemas, ok := document["section_schemas"].(map[string]any)
	if !ok {
		t.Fatalf("section_schemas = %v after the upgrade, want an object", document["section_schemas"])
	}
	if got := sectionSchemas["parser"]; got != "parser-v1" {
		t.Fatalf("section_schemas.parser = %v after the upgrade, want parser-v1", got)
	}
	if got := document["unclassified_route"]; got != "skip" {
		t.Fatalf("unclassified_route = %v after the upgrade, want skip", got)
	}
	if got := document["replacement"].(map[string]any)["admission_mode"]; got != "single_copy" {
		t.Fatalf("replacement.admission_mode = %v after the upgrade, want single_copy", got)
	}
	if replacementVersion, asNumber := document["replacement_version"].(float64); !asNumber || replacementVersion != 2 {
		t.Fatalf("replacement_version = %v after the upgrade, want 2", document["replacement_version"])
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

	// The working candidate and its saved revision stay activatable, so both
	// carry the v2 body and its digest.
	rewriteExpect(t, db, "upgrade", "the working candidate draft is v2",
		`SELECT draft->'business'->>'schema_version' = 'business-snapshot-v2'
		    AND NOT (draft->'business'->'programs' ? 'attrrules')
		    AND draft->>'business_digest' = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft->'business'), 'UTF8'), 'sha256'), 'hex')
		    AND draft_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex')
		 FROM anime_config_candidates WHERE id = 'cand-v1'`)
	rewriteExpect(t, db, "upgrade", "the saved revision carries the v2 body",
		`SELECT business_snapshot->>'schema_version' = 'business-snapshot-v2'
		    AND NOT (business_snapshot->'programs' ? 'attrrules')
		    AND business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business_snapshot), 'UTF8'), 'sha256'), 'hex')
		    AND draft->'business'->>'schema_version' = 'business-snapshot-v2'
		    AND draft_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex')
		 FROM anime_config_candidate_revisions WHERE candidate_id = 'cand-v1' AND revision = 1`)

	// Immutability is restored after the one upgrade statement.
	_, err := db.ExecContext(ctx, `
UPDATE anime_config_candidate_revisions SET business_snapshot = business_snapshot
WHERE candidate_id = 'cand-v1' AND revision = 1`)
	expectPgErrorCode(t, err, "23514")

	// A linked candidate is history: no admission reads its draft.
	rewriteExpect(t, db, "upgrade", "the dormant candidate keeps its v1 evidence",
		`SELECT draft->'business'->>'schema_version' = 'business-snapshot-v1'
		    AND draft->'business'->'programs' ? 'attrrules'
		 FROM anime_config_candidates WHERE id = 'cand-linked'`)
}
