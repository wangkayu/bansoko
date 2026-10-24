package migrations

import (
	"context"
	"strings"
	"testing"
)

// 053 rewrites the naming templates an owner still edits — Global naming, the
// naming template library, per-anime overrides and the frozen business of a
// live instance — off the retired fact path `${sub.lang_bcp47}` (and
// `${release.lang_bcp47}`) onto `${sub.lang_tag}` (`${release.lang_tag}`). The
// registry no longer knows the old leaf, so a stored template that keeps it
// fails compilation with "unknown context field" the next time the current
// rules compile. Editable rows bump their version exactly once, the frozen
// business moves together with its digest so the guard trigger accepts it, and
// the word lang_bcp47 as plain output text — no `${...}` wrapper, not a fact
// reference — is left byte-identical.
//
// migrationLangTagOld carries the retired token in the subtitle suffix and the
// word lang_bcp47 as plain text in the movie filename; migrationLangTagNew is
// the same document after 053. migrationLangTagClean never held the token and
// must survive byte-identical with its version.
var (
	migrationLangTagOld = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"/library/${anime.kind}/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},` +
		`"movie":{"directory_template":"/library/movie","video_filename_template":"${anime.title} lang_bcp47 cut.${file.ext}"},` +
		`"unknown":{"directory_template":"/library/Extras","video_filename_template":"${fn}"},` +
		`"subtitle_suffix_template":"${sub.lang_bcp47}"}`

	migrationLangTagNew = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"/library/${anime.kind}/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},` +
		`"movie":{"directory_template":"/library/movie","video_filename_template":"${anime.title} lang_bcp47 cut.${file.ext}"},` +
		`"unknown":{"directory_template":"/library/Extras","video_filename_template":"${fn}"},` +
		`"subtitle_suffix_template":"${sub.lang_tag}"}`

	migrationLangTagClean = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"/library/clean/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},` +
		`"movie":{"directory_template":"/library/clean-movie","video_filename_template":"${fn}"},` +
		`"unknown":{"directory_template":"/library/clean-extras","video_filename_template":"${fn}"},` +
		`"subtitle_suffix_template":"${sub.lang}"}`

	// migrationLangTagRelease covers the release-side rename: both retired
	// tokens in one library template.
	migrationLangTagRelease = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"/library/${release.lang_bcp47}","video_filename_template":"${anime.title}.${file.ext}"},` +
		`"movie":{"directory_template":"/library/alt-movie","video_filename_template":"${fn}"},` +
		`"unknown":{"directory_template":"/library/alt-extras","video_filename_template":"${fn}"},` +
		`"subtitle_suffix_template":"${sub.lang_bcp47}.${sub.lang_source}"}`

	migrationLangTagReleaseNew = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"/library/${release.lang_tag}","video_filename_template":"${anime.title}.${file.ext}"},` +
		`"movie":{"directory_template":"/library/alt-movie","video_filename_template":"${fn}"},` +
		`"unknown":{"directory_template":"/library/alt-extras","video_filename_template":"${fn}"},` +
		`"subtitle_suffix_template":"${sub.lang_tag}.${sub.lang_source}"}`
)

func TestLangTagFactRenameUpgrade(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "052_metadata_refresh_global.sql")

	for _, seed := range []struct {
		statement string
		args      []any
	}{
		{
			`INSERT INTO global_config_sections(section_name, schema_version, value, version)
			 VALUES ('naming_rules', 'naming-rules-v1', $1::jsonb, 3)`,
			[]any{migrationLangTagOld},
		},
		{
			`INSERT INTO managed_anime(id, title) VALUES ('lang-tag-a', 'Lang Tag A'), ('lang-tag-b', 'Lang Tag B')`,
			nil,
		},
		{
			`INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
			 VALUES ('lang-tag-a', 'rules', 'naming_rules', 'naming-rules-v1', $1::jsonb),
			        ('lang-tag-b', 'rules', 'naming_rules', 'naming-rules-v1', $2::jsonb)`,
			[]any{migrationLangTagOld, migrationLangTagClean},
		},
		{
			`UPDATE naming_template_library
			    SET value = $1::jsonb, version = 2`,
			[]any{`{"default_key":"default","templates":[` +
				`{"key":"default","name":"Default"},` +
				`{"key":"alt","name":"Alt","rules":` + migrationLangTagRelease + `},` +
				`{"key":"plain","name":"Plain","rules":` + migrationLangTagClean + `}]}`},
		},
	} {
		if _, err := db.ExecContext(ctx, seed.statement, seed.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, seed.statement)
		}
	}

	// Two live instances carry their own frozen business: lang-tag-a holds the
	// retired token, lang-tag-b never did. 053 must rewrite the first with its
	// digest in one statement and leave the second at its seeded version.
	seedFixtureCandidate(t, db, "cand-lang-tag-a")
	insertRevision(t, db, "cand-lang-tag-a", 1, sha256Fixture("f"), sha256Fixture("e"))
	seedFixtureCandidate(t, db, "cand-lang-tag-b")
	insertRevision(t, db, "cand-lang-tag-b", 1, sha256Fixture("a"), sha256Fixture("b"))
	for _, instance := range []struct {
		animeID, candidateID, snapshot string
	}{
		{"lang-tag-a", "cand-lang-tag-a", `{"sections":{"naming_rules":` + migrationLangTagOld + `}}`},
		{"lang-tag-b", "cand-lang-tag-b", `{"sections":{"naming_rules":` + migrationLangTagClean + `}}`},
	} {
		if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, candidate_id, revision, business_snapshot, business_digest, business_version)
VALUES ($1, $2, 1, $3::jsonb,
        'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($3::jsonb), 'UTF8'), 'sha256'), 'hex'), 1)`,
			instance.animeID, instance.candidateID, instance.snapshot); err != nil {
			t.Fatalf("seed the frozen business of %s: %v", instance.animeID, err)
		}
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var globalMatches bool
	var globalValue string
	var globalVersion int64
	if err := db.QueryRowContext(ctx,
		`SELECT value = $1::jsonb, value::text, version FROM global_config_sections WHERE section_name = 'naming_rules'`,
		migrationLangTagNew,
	).Scan(&globalMatches, &globalValue, &globalVersion); err != nil {
		t.Fatal(err)
	}
	if !globalMatches {
		t.Fatalf("global naming_rules still reads the retired path: %s", globalValue)
	}
	if globalVersion != 4 {
		t.Fatalf("global naming_rules version = %d, want 4 (bumped once)", globalVersion)
	}

	for _, want := range []struct {
		animeID, document string
	}{
		{"lang-tag-a", migrationLangTagNew},
		{"lang-tag-b", migrationLangTagClean},
	} {
		var matches bool
		if err := db.QueryRowContext(ctx,
			`SELECT value = $1::jsonb FROM anime_config_overrides WHERE managed_anime_id = $2 AND section_name = 'naming_rules'`,
			want.document, want.animeID,
		).Scan(&matches); err != nil {
			t.Fatal(err)
		}
		if !matches {
			t.Fatalf("%s: per-anime naming_rules is not the expected document", want.animeID)
		}
	}

	var libraryVersion int64
	if err := db.QueryRowContext(ctx,
		`SELECT version FROM naming_template_library`,
	).Scan(&libraryVersion); err != nil {
		t.Fatal(err)
	}
	if libraryVersion != 3 {
		t.Fatalf("template library version = %d, want 3 (bumped once)", libraryVersion)
	}
	for _, want := range []struct {
		key, rules string
	}{
		{"alt", migrationLangTagReleaseNew},
		{"plain", migrationLangTagClean},
	} {
		var matches bool
		if err := db.QueryRowContext(ctx,
			`SELECT (SELECT item -> 'rules' FROM jsonb_array_elements(value -> 'templates') AS item
			          WHERE item ->> 'key' = $1) = $2::jsonb
			   FROM naming_template_library`,
			want.key, want.rules,
		).Scan(&matches); err != nil {
			t.Fatal(err)
		}
		if !matches {
			t.Fatalf("template %q rules are not the expected document", want.key)
		}
	}

	// The plain-text mention is output text, not a fact reference: it survives
	// in the rewritten global document, and no store keeps a retired
	// interpolation token.
	if !strings.Contains(globalValue, "lang_bcp47 cut") {
		t.Fatalf("plain-text lang_bcp47 mention was rewritten: %s", globalValue)
	}
	var retiredLeft bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM global_config_sections
               WHERE value::text LIKE '%${sub.lang_bcp47}%' OR value::text LIKE '%${release.lang_bcp47}%')
    OR EXISTS(SELECT 1 FROM anime_config_overrides
               WHERE value::text LIKE '%${sub.lang_bcp47}%' OR value::text LIKE '%${release.lang_bcp47}%')
    OR EXISTS(SELECT 1 FROM naming_template_library
               WHERE value::text LIKE '%${sub.lang_bcp47}%' OR value::text LIKE '%${release.lang_bcp47}%')
    OR EXISTS(SELECT 1 FROM anime_business_configs
               WHERE business_snapshot::text LIKE '%${sub.lang_bcp47}%'
                  OR business_snapshot::text LIKE '%${release.lang_bcp47}%')`,
	).Scan(&retiredLeft); err != nil {
		t.Fatal(err)
	}
	if retiredLeft {
		t.Fatal("a stored naming template still interpolates the retired lang_bcp47 path")
	}

	// Each instance keeps its own snapshot. lang-tag-a moved with its digest
	// and CAS version; lang-tag-b never carried the token, so its version and
	// bytes are the seeded ones.
	for _, want := range []struct {
		animeID, document string
		version           int64
	}{
		{"lang-tag-a", migrationLangTagNew, 2},
		{"lang-tag-b", migrationLangTagClean, 1},
	} {
		var snapshot, digest string
		var businessVersion int64
		var sectionMatches bool
		if err := db.QueryRowContext(ctx,
			`SELECT business_snapshot::text, business_digest, business_version,
			        business_snapshot -> 'sections' -> 'naming_rules' = $1::jsonb
			   FROM anime_business_configs WHERE managed_anime_id = $2`,
			want.document, want.animeID,
		).Scan(&snapshot, &digest, &businessVersion, &sectionMatches); err != nil {
			t.Fatal(err)
		}
		if !sectionMatches {
			t.Fatalf("%s: frozen business naming_rules is not the expected document: %s", want.animeID, snapshot)
		}
		var wantDigest string
		if err := db.QueryRowContext(ctx,
			`SELECT 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb), 'UTF8'), 'sha256'), 'hex')`,
			snapshot).Scan(&wantDigest); err != nil {
			t.Fatal(err)
		}
		if digest != wantDigest {
			t.Fatalf("%s: business digest = %s, want the canonical hash of the stored snapshot %s", want.animeID, digest, wantDigest)
		}
		if businessVersion != want.version {
			t.Fatalf("%s: business version = %d, want %d", want.animeID, businessVersion, want.version)
		}
	}
}
