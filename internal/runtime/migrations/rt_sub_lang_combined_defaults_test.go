package migrations

import (
	"context"
	"testing"
)

// 094 replaces the rt.sub_lang item of a stored quality profile with the
// combined JPSC/JPTC ranking, per item and only while that item still holds
// the old default ranks. It covers the Global default and the per-anime
// copies (the live value and the business snapshot copy, whose digest moves
// with it). An edited rt.sub_lang item is left as it is, and a profile whose
// other items were edited keeps those edits.
func TestRtSubLangCombinedDefaultsUpgrade(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "093_diagnostic_conditions.sql")

	const (
		oldRanks          = `[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]`
		newRanks          = `[["JPSC","JPTC"],["JPSC"],["SC","TC"],["SC"],["JPTC"],["TC"],["JPN"],["ENG"]]`
		editedRanks       = `[["TC"],["SC"]]`
		defaultResolution = `[["2160p"],["1080p"],["720p"],["480p"]]`
		editedResolution  = `[["1080p"],["720p"]]`
	)
	profile := func(resolution, subLang string) string {
		return `{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},` +
			`{"key":"rt.sub_lang","ranks":` + subLang + `},` +
			`{"key":"rt.resolution","ranks":` + resolution + `},{"key":"release.revision","ranks":[]}]}`
	}

	// A Global whose resolution item was edited but whose rt.sub_lang item is
	// still the old default.
	if _, err := db.ExecContext(ctx, `
UPDATE global_config_sections
   SET value = jsonb_set(value, '{quality_profile}', $1::jsonb), version = 5
 WHERE section_name = 'replacement'`, profile(editedResolution, oldRanks)); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('rt-lang-old', 'Old'), ('rt-lang-edited', 'Edited')`); err != nil {
		t.Fatal(err)
	}
	seedFixtureCandidate(t, db, "cand-rt-lang-old")
	insertRevision(t, db, "cand-rt-lang-old", 1, sha256Fixture("f"), sha256Fixture("e"))
	seedFixtureCandidate(t, db, "cand-rt-lang-edited")
	insertRevision(t, db, "cand-rt-lang-edited", 1, sha256Fixture("a"), sha256Fixture("b"))
	for _, instance := range []struct {
		animeID, candidateID, profile string
	}{
		{"rt-lang-old", "cand-rt-lang-old", profile(defaultResolution, oldRanks)},
		{"rt-lang-edited", "cand-rt-lang-edited", profile(defaultResolution, editedRanks)},
	} {
		snapshot := `{"replacement":{"admission_mode":"all","quality_profile":` + instance.profile + `}}`
		if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, candidate_id, revision, business_snapshot, business_digest, business_version,
    admission_mode, quality_profile, replacement_source_default_version, replacement_version)
VALUES ($1, $2, 1, $3::jsonb,
        'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($3::jsonb), 'UTF8'), 'sha256'), 'hex'), 1,
        'all', $4::jsonb, 1, 1)`,
			instance.animeID, instance.candidateID, snapshot, instance.profile); err != nil {
			t.Fatalf("seed per-anime copy of %s: %v", instance.animeID, err)
		}
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var globalOK bool
	var globalVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT value->'quality_profile' = $1::jsonb, version FROM global_config_sections WHERE section_name = 'replacement'`,
		profile(editedResolution, newRanks)).Scan(&globalOK, &globalVersion); err != nil {
		t.Fatal(err)
	}
	if !globalOK || globalVersion != 6 {
		t.Fatalf("global: only the rt.sub_lang item may change, version bumped once: ok=%v version=%d", globalOK, globalVersion)
	}

	for _, want := range []struct {
		animeID, profile string
		version          int64
	}{
		{"rt-lang-old", profile(defaultResolution, newRanks), 2},
		{"rt-lang-edited", profile(defaultResolution, editedRanks), 1},
	} {
		var columnOK, snapshotOK, digestOK bool
		var version int64
		if err := db.QueryRowContext(ctx, `
SELECT quality_profile = $2::jsonb,
       business_snapshot->'replacement'->'quality_profile' = $2::jsonb,
       business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business_snapshot), 'UTF8'), 'sha256'), 'hex'),
       business_version
  FROM anime_business_configs WHERE managed_anime_id = $1`, want.animeID, want.profile).Scan(&columnOK, &snapshotOK, &digestOK, &version); err != nil {
			t.Fatal(err)
		}
		if !columnOK || !snapshotOK || !digestOK || version != want.version {
			t.Fatalf("%s: column=%v snapshot=%v digest=%v business_version=%d, want version %d", want.animeID, columnOK, snapshotOK, digestOK, version, want.version)
		}
	}
}
