package migrations

import (
	"context"
	"strings"
	"testing"
)

// migrationNamingLiteral and migrationNamingBody are the same naming document
// before and after 045: the literal form carries the outer backticks around
// every template, the body form does not. The unknown variant of the body
// document keeps a value that merely starts with a backtick: it was never one
// complete literal, so the rewrite leaves it alone. The assertions run after
// the whole remaining migration set, so the body document reads
// `${sub.lang_tag}`: 053 later renamed the lang_bcp47 fact leaf.
var (
	migrationNamingLiteral = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"` + "`" + `/library/${anime.kind}/${anime.title}` + "`" + `","video_filename_template":"` + "`" + `${anime.title}.${file.ext}` + "`" + `"},` +
		`"movie":{"directory_template":"` + "`" + `/library/movie` + "`" + `","video_filename_template":"` + "`" + `${file.stem}.${file.ext}` + "`" + `"},` +
		`"unknown":{"directory_template":"` + "`" + `/library/Extras` + "`" + `","video_filename_template":"` + "`" + `unclosed"},` +
		`"subtitle_suffix_template":"` + "`" + `${sub.lang_tag}` + "`" + `"}`

	migrationNamingBody = `{"language":"js-naming-1",` +
		`"tv":{"directory_template":"/library/${anime.kind}/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},` +
		`"movie":{"directory_template":"/library/movie","video_filename_template":"${file.stem}.${file.ext}"},` +
		`"unknown":{"directory_template":"/library/Extras","video_filename_template":"` + "`" + `unclosed"},` +
		`"subtitle_suffix_template":"${sub.lang_tag}"}`
)

// 045 rewrites the naming template values an owner still edits — Global
// naming, the naming template library, per-anime overrides and the frozen
// business of a live instance — from the earlier full-literal form to the
// template body the engine now stores. Editable rows bump their version, the
// frozen business moves together with its digest so the guard trigger accepts
// it, and a value that is not one complete literal survives untouched.
func TestNamingTemplateBodyUpgradeRemovesOnlyTheLiteralWrapper(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "044_anime_metadata_season.sql")

	for _, seed := range []struct {
		statement string
		args      []any
	}{
		{
			`INSERT INTO global_config_sections(section_name, schema_version, value, version)
			 VALUES ('naming_rules', 'naming-rules-v1', $1::jsonb, 3)`,
			[]any{migrationNamingLiteral},
		},
		{
			`INSERT INTO managed_anime(id, title) VALUES ('naming-body', 'Naming Body'), ('naming-other', 'Naming Other')`,
			nil,
		},
		{
			`INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
			 VALUES ('naming-body', 'rules', 'naming_rules', 'naming-rules-v1', $1::jsonb)`,
			[]any{migrationNamingLiteral},
		},
		{
			`UPDATE naming_template_library
			    SET value = $1::jsonb, version = 2`,
			[]any{`{"default_key":"default","templates":[` +
				`{"key":"default","name":"Default"},` +
				`{"key":"alt","name":"Alt","rules":` + migrationNamingLiteral + `}]}`},
		},
	} {
		if _, err := db.ExecContext(ctx, seed.statement, seed.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, seed.statement)
		}
	}

	// Two live instances carry their own frozen business, each with the literal
	// form and a digest over it; 045 must rewrite each row with its own
	// snapshot (and its own digest) in the same statement.
	seedFixtureCandidate(t, db, "cand-naming-body")
	insertRevision(t, db, "cand-naming-body", 1, sha256Fixture("f"), sha256Fixture("e"))
	seedFixtureCandidate(t, db, "cand-naming-other")
	insertRevision(t, db, "cand-naming-other", 1, sha256Fixture("a"), sha256Fixture("b"))
	for _, instance := range []struct {
		animeID, candidateID, snapshot string
	}{
		{"naming-body", "cand-naming-body", `{"sections":{"naming_rules":` + migrationNamingLiteral + `}}`},
		{"naming-other", "cand-naming-other", `{"sections":{"naming_rules":` + strings.Replace(migrationNamingLiteral, "/library/movie", "/library/other", 1) + `}}`},
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
	var globalVersion int64
	if err := db.QueryRowContext(ctx,
		`SELECT value = $1::jsonb, version FROM global_config_sections WHERE section_name = 'naming_rules'`,
		migrationNamingBody,
	).Scan(&globalMatches, &globalVersion); err != nil {
		t.Fatal(err)
	}
	if !globalMatches {
		t.Fatalf("global naming_rules is not the body form")
	}
	if globalVersion != 4 {
		t.Fatalf("global naming_rules version = %d, want 4 (bumped once)", globalVersion)
	}

	var overrideMatches bool
	if err := db.QueryRowContext(ctx,
		`SELECT value = $1::jsonb FROM anime_config_overrides WHERE managed_anime_id = 'naming-body' AND section_name = 'naming_rules'`,
		migrationNamingBody,
	).Scan(&overrideMatches); err != nil {
		t.Fatal(err)
	}
	if !overrideMatches {
		t.Fatalf("per-anime naming_rules is not the body form")
	}

	var libraryValue string
	var libraryVersion int64
	if err := db.QueryRowContext(ctx,
		`SELECT value::text, version FROM naming_template_library`,
	).Scan(&libraryValue, &libraryVersion); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(libraryValue, "`/library") || strings.Contains(libraryValue, "`${") {
		t.Fatalf("template library still carries literal wrappers: %s", libraryValue)
	}
	if !strings.Contains(libraryValue, "`unclosed") {
		t.Fatalf("template library lost the unclosed-backtick value: %s", libraryValue)
	}
	if libraryVersion != 3 {
		t.Fatalf("template library version = %d, want 3 (bumped once)", libraryVersion)
	}

	// Each instance keeps its own snapshot: the second one names /library/other,
	// and neither row may carry the other's bytes. The business version is the
	// acquisition CAS token, so it moves with the rewritten snapshot.
	for _, want := range []struct {
		animeID, directory string
	}{
		{"naming-body", "/library/movie"},
		{"naming-other", "/library/other"},
	} {
		var snapshot, digest string
		var businessVersion int64
		if err := db.QueryRowContext(ctx,
			`SELECT business_snapshot::text, business_digest, business_version FROM anime_business_configs WHERE managed_anime_id = $1`,
			want.animeID,
		).Scan(&snapshot, &digest, &businessVersion); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(snapshot, "`/library") || strings.Contains(snapshot, "`${") {
			t.Fatalf("%s: frozen business still carries literal wrappers: %s", want.animeID, snapshot)
		}
		var matches bool
		if err := db.QueryRowContext(ctx,
			`SELECT business_snapshot -> 'sections' -> 'naming_rules' -> 'movie' ->> 'directory_template' = $1 FROM anime_business_configs WHERE managed_anime_id = $2`,
			want.directory, want.animeID,
		).Scan(&matches); err != nil {
			t.Fatal(err)
		}
		if !matches {
			t.Fatalf("%s: movie directory is not %q: %s", want.animeID, want.directory, snapshot)
		}
		var wantDigest string
		if err := db.QueryRowContext(ctx,
			`SELECT 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb), 'UTF8'), 'sha256'), 'hex')`,
			snapshot).Scan(&wantDigest); err != nil {
			t.Fatal(err)
		}
		if digest != wantDigest {
			t.Fatalf("%s: business digest = %s, want the canonical hash of the rewritten snapshot %s", want.animeID, digest, wantDigest)
		}
		if businessVersion != 2 {
			t.Fatalf("%s: business version = %d, want 2 (bumped once)", want.animeID, businessVersion)
		}
	}
}
