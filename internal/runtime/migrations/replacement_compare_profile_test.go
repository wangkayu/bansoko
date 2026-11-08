package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bansoko/internal/qualityprofile"
)

// The seeded instance-wide retention profile is byte for byte the Go default.
func TestSeededReplacementProfileIsTheDefault(t *testing.T) {
	db := installedDB(t)
	var profile []byte
	if err := db.QueryRow(`SELECT value->'quality_profile' FROM global_config_sections WHERE section_name='replacement'`).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	parsed, err := qualityprofile.Parse(profile)
	if err != nil {
		t.Fatalf("seeded profile is not a valid profile: %v", err)
	}
	want, _ := json.Marshal(qualityprofile.Default())
	got, _ := json.Marshal(parsed)
	if string(got) != string(want) {
		t.Fatalf("seeded profile = %s, want the default %s", got, want)
	}
}

// `-` binds tighter than `->`, so `x->'a' - 'b'` parses as `x -> ('a' - 'b')`
// (operator is not unique: unknown - unknown). Key removal must parenthesize
// the arrow operand.
func TestMigrationsParenthesizeKeyRemovalAfterArrow(t *testing.T) {
	unparenthesized := regexp.MustCompile(`->>?\s*'[^']*'\s*-\s*'`)
	for _, name := range shippedMigrationFiles(t) {
		raw, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if match := unparenthesized.Find(raw); match != nil {
			t.Fatalf("%s: unparenthesized key removal after an arrow: %q", name, match)
		}
	}
}

// The retention profile validator accepts only the current profile shape, and
// retention policies carry no cutoff column.
func TestReplacementProfileValidatorAndNoCutoff(t *testing.T) {
	db := installedDB(t)
	for raw, want := range map[string]bool{
		string(qualityprofile.DefaultJSON()):                                                                                      true,
		`{"version":"profile-v1","items":[]}`:                                                                                     true,
		`{"version":"profile-v1","dimensions":[],"ranks":{},"codec_enabled":false}`:                                               false,
		`{"version":"profile-v1","items":[{"key":"file.codec","ranks":[]}]}`:                                                      false,
		`{"version":"profile-v1","items":[{"key":"rt.codec","ranks":[]},{"key":"rt.codec","ranks":[]}]}`:                          false,
		`{"version":"profile-v1","items":[{"key":"rt.codec","ranks":[[]]}]}`:                                                      false,
		`{"version":"profile-v1","items":[{"key":"rt.codec","ranks":[["HEVC"]]}],"cutoff":{}}`:                                    false,
		`{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS","CHT"],["CHS"]]}]}`:                             true,
		`{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS",1]]}]}`:                                         false,
		`{"version":"profile-v2","items":[]}`:                                                                                     false,
		`{"version":"profile-v1","items":[{"key":"rt.sub_flag","ranks":[["soft","ex"],["hard"]]}]}`:                               true,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[["true"]]}]}`:                                           false,
		`{"version":"profile-v1","items":[{"key":"rt.content_rating","ranks":[["UNCENSORED"]]}]}`:                                 true,
		`{"version":"profile-v1","items":[{"key":"rt.content_rating","ranks":[["UNCENSORED"],["AGE_RESTRICTED"],["CENSORED"]]}]}`: true,
	} {
		var ok bool
		if err := db.QueryRow(`SELECT replacement_quality_profile_valid($1::jsonb)`, raw).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok != want {
			t.Errorf("validator(%s) = %v, want %v", raw, ok, want)
		}
	}
	if columnPresent(t, db, "anime_business_configs", "cutoff") {
		t.Fatal("retention policies must not carry a cutoff column")
	}
}

// Replacement comparison values default to an empty object.
func TestReleaseCandidateFactsCompareValuesDefault(t *testing.T) {
	db := installedDB(t)
	var compareDefault string
	if err := db.QueryRow(`SELECT column_default FROM information_schema.columns WHERE table_name='release_candidate_facts' AND column_name='compare_values'`).Scan(&compareDefault); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compareDefault, "'{}'") {
		t.Fatalf("compare_values default = %q", compareDefault)
	}
}

func columnPresent(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var present bool
	if err := db.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&present); err != nil {
		t.Fatal(err)
	}
	return present
}

// Migration 129 retires rt.uncensored from the Global default and from each
// instance's frozen policy column, so an upgraded database keeps evaluating
// against a valid profile. The versions that describe those documents move with
// the content change.
func TestMigration129CleansStoredUncensoredComparisonItem(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "128_github_update_connection.sql")
	legacy := legacyUncensoredProfileJSON(t)

	var sectionVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM global_config_sections WHERE section_name='replacement'`).Scan(&sectionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value = jsonb_set(value,'{quality_profile}',$1::jsonb) WHERE section_name='replacement'`, legacy); err != nil {
		t.Fatalf("seed legacy Global profile: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id,title) VALUES ('anime-129','Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, admission_mode, quality_profile,
    replacement_source_default_version, replacement_version)
VALUES ('anime-129', 'single_copy', $1::jsonb, 1, 1)`, legacy); err != nil {
		t.Fatalf("seed legacy instance profile: %v", err)
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("apply migration 129: %v", err)
	}

	var globalProfile, instanceProfile []byte
	if err := db.QueryRowContext(ctx, `SELECT value->'quality_profile' FROM global_config_sections WHERE section_name='replacement'`).Scan(&globalProfile); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT quality_profile FROM anime_business_configs WHERE managed_anime_id='anime-129'`).Scan(&instanceProfile); err != nil {
		t.Fatal(err)
	}
	cleaned := cleanedUncensoredProfileJSON(t)
	for name, profile := range map[string][]byte{"global": globalProfile, "instance": instanceProfile} {
		var same bool
		if err := db.QueryRowContext(ctx, `SELECT $1::jsonb = $2::jsonb`, profile, cleaned).Scan(&same); err != nil {
			t.Fatal(err)
		}
		if !same {
			t.Errorf("%s profile = %s, want the profile without rt.uncensored", name, profile)
		}
	}
	if _, err := qualityprofile.Parse(globalProfile); err != nil {
		t.Errorf("cleaned Global profile is invalid: %v", err)
	}

	var gotSectionVersion, replacementVersion, businessVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM global_config_sections WHERE section_name='replacement'`).Scan(&gotSectionVersion); err != nil {
		t.Fatal(err)
	}
	if gotSectionVersion != sectionVersion+1 {
		t.Errorf("Global section version = %d, want %d", gotSectionVersion, sectionVersion+1)
	}
	if err := db.QueryRowContext(ctx, `SELECT replacement_version, business_version FROM anime_business_configs WHERE managed_anime_id='anime-129'`).Scan(&replacementVersion, &businessVersion); err != nil {
		t.Fatal(err)
	}
	if replacementVersion != 2 {
		t.Errorf("replacement_version = %d, want 2", replacementVersion)
	}
	if businessVersion != 0 {
		t.Errorf("business_version = %d, want 0 (no snapshot was stored)", businessVersion)
	}
}

// Migration 129 also rewrites the frozen business snapshot, the candidate draft
// and the candidate revision bodies, and every stored digest follows its
// rewritten body. Re-running the migration file exercises those statements
// against seeded legacy bodies.
func TestMigration129CleansBusinessSnapshotsAndCandidateDrafts(t *testing.T) {
	ctx := context.Background()
	db := installedDB(t)
	legacy := legacyUncensoredProfileJSON(t)
	cleaned := cleanedUncensoredProfileJSON(t)

	snapshot := mustJSON(t, map[string]any{"replacement": map[string]any{
		"admission_mode":  "single_copy",
		"quality_profile": json.RawMessage(legacy),
	}})
	draft := mustJSON(t, map[string]any{
		"business": map[string]any{
			"replacement": map[string]any{
				"admission_mode":  "single_copy",
				"quality_profile": json.RawMessage(legacy),
			},
		},
		"business_digest": "sha256:" + strings.Repeat("0", 64),
	})

	keys := revisionSectionsCurrent
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The candidate guard refuses a draft outside the typed contract; the
	// fixture opt-out lets this test seed the pre-migration body.
	if _, err := tx.ExecContext(ctx, `SET LOCAL bansoko.candidate_fixture = 'on'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_config_candidates(id, origin, origin_entry_ref, title, instance_label, draft, draft_digest)
VALUES ('cand-129', 'custom', NULL, 'Show', 'lab', $1::jsonb, '')`, draft); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_config_candidate_revisions(
    candidate_id, revision, draft, draft_digest, effective, section_versions,
    section_schemas, effective_digest, parser_policy, parser_policy_digest,
    business_snapshot, business_digest)
VALUES ('cand-129', 1, $1::jsonb,
        'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb), 'UTF8'), 'sha256'), 'hex'),
        $2::jsonb, $3::jsonb, $4::jsonb, $5, $6::jsonb, $5, $7::jsonb,
        'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($7::jsonb), 'UTF8'), 'sha256'), 'hex'))`,
		draft, keys.effective, keys.versions, keys.schemas, sha256Fixture("f"), keys.parserPolicy, snapshot); err != nil {
		t.Fatalf("seed candidate revision: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_anime(id,title) VALUES ('anime-129','Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, admission_mode, quality_profile,
    replacement_source_default_version, replacement_version,
    candidate_id, revision, business_snapshot, business_digest)
VALUES ('anime-129', 'single_copy', $1::jsonb, 1, 1, 'cand-129', 1, $2::jsonb,
        'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($2::jsonb), 'UTF8'), 'sha256'), 'hex'))`,
		cleaned, snapshot); err != nil {
		t.Fatalf("seed instance business snapshot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "129_rt_content_rating.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rewrite, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rewrite.Rollback()
	if _, err := rewrite.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("re-run migration 129: %v", err)
	}
	if err := rewrite.Commit(); err != nil {
		t.Fatal(err)
	}

	var draftClean, draftSiblingOnly, draftDigestOK, draftBusinessDigestOK bool
	if err := db.QueryRowContext(ctx, `
SELECT draft->'business'->'replacement'->'quality_profile' = $1::jsonb,
       NOT (draft->'business' ? 'business_digest'),
       draft_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex'),
       draft->>'business_digest' = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft->'business'), 'UTF8'), 'sha256'), 'hex')
  FROM anime_config_candidates WHERE id = 'cand-129'`, cleaned).Scan(&draftClean, &draftSiblingOnly, &draftDigestOK, &draftBusinessDigestOK); err != nil {
		t.Fatal(err)
	}
	if !draftClean || !draftSiblingOnly || !draftDigestOK || !draftBusinessDigestOK {
		t.Errorf("candidate draft = cleaned:%v sibling_digest:%v digest:%v business_digest:%v",
			draftClean, draftSiblingOnly, draftDigestOK, draftBusinessDigestOK)
	}

	var revisionSnapshotClean, revisionDraftClean, revisionDraftSiblingOnly, revisionBusinessDigestOK, revisionDraftDigestOK, revisionDraftBusinessDigestOK bool
	if err := db.QueryRowContext(ctx, `
SELECT business_snapshot->'replacement'->'quality_profile' = $1::jsonb,
       draft->'business'->'replacement'->'quality_profile' = $1::jsonb,
       NOT (draft->'business' ? 'business_digest'),
       business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business_snapshot), 'UTF8'), 'sha256'), 'hex'),
       draft_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex'),
       draft->>'business_digest' = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft->'business'), 'UTF8'), 'sha256'), 'hex')
  FROM anime_config_candidate_revisions WHERE candidate_id = 'cand-129' AND revision = 1`, cleaned).
		Scan(&revisionSnapshotClean, &revisionDraftClean, &revisionDraftSiblingOnly, &revisionBusinessDigestOK, &revisionDraftDigestOK, &revisionDraftBusinessDigestOK); err != nil {
		t.Fatal(err)
	}
	if !revisionSnapshotClean || !revisionDraftClean || !revisionDraftSiblingOnly || !revisionBusinessDigestOK || !revisionDraftDigestOK || !revisionDraftBusinessDigestOK {
		t.Errorf("candidate revision = snapshot:%v draft:%v sibling_digest:%v business_digest:%v draft_digest:%v draft_business_digest:%v",
			revisionSnapshotClean, revisionDraftClean, revisionDraftSiblingOnly, revisionBusinessDigestOK, revisionDraftDigestOK, revisionDraftBusinessDigestOK)
	}

	var instanceSnapshotClean, instanceDigestOK bool
	var businessVersion, replacementVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT business_snapshot->'replacement'->'quality_profile' = $1::jsonb,
       business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business_snapshot), 'UTF8'), 'sha256'), 'hex'),
       business_version, replacement_version
  FROM anime_business_configs WHERE managed_anime_id = 'anime-129'`, cleaned).
		Scan(&instanceSnapshotClean, &instanceDigestOK, &businessVersion, &replacementVersion); err != nil {
		t.Fatal(err)
	}
	if !instanceSnapshotClean || !instanceDigestOK || businessVersion != 1 || replacementVersion != 1 {
		t.Errorf("instance snapshot = cleaned:%v digest:%v business_version:%d replacement_version:%d",
			instanceSnapshotClean, instanceDigestOK, businessVersion, replacementVersion)
	}
}

// legacyUncensoredProfileJSON is the Go default plus the retired boolean item.
func legacyUncensoredProfileJSON(t *testing.T) []byte {
	t.Helper()
	return mustJSON(t, profileWithUncensored(t, true))
}

// cleanedUncensoredProfileJSON is that same profile with the item dropped.
func cleanedUncensoredProfileJSON(t *testing.T) []byte {
	t.Helper()
	return mustJSON(t, profileWithUncensored(t, false))
}

func profileWithUncensored(t *testing.T, withItem bool) map[string]any {
	t.Helper()
	var profile map[string]any
	if err := json.Unmarshal(qualityprofile.DefaultJSON(), &profile); err != nil {
		t.Fatal(err)
	}
	items, ok := profile["items"].([]any)
	if !ok {
		t.Fatal("default profile items are not an array")
	}
	if withItem {
		items = append(items, map[string]any{"key": "rt.uncensored", "ranks": [][]string{{"true"}}})
	}
	profile["items"] = items
	return profile
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
