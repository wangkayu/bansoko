package application

import (
	"path/filepath"
	"testing"

	domain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// literalFrozenVars is the complete-policy fixture's Vars evaluation written as
// a literal: the program reference names the fixture source, and no evaluator
// runs.
func literalFrozenVars(policy map[string]any) {
	source := `vars.fixture = {title: input.release_title};`
	digest, _ := namingvars.SourceFingerprint(source)
	policy["vars"] = namingvars.FrozenEvaluation{
		Program: namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)},
		Input:   namingvars.Input{ReleaseTitle: "fixture release", Files: []namingvars.File{}},
		Output:  namingvars.Vars{"fixture": {"title": "fixture release"}},
		Status:  "ok",
	}
	policy["fixture_vars_source"] = source
}

// The Rt and AniBTTags namespaces a file plan renders are the Release's
// frozen source facts: values that differ from any re-parse of the release
// title reach the planned target verbatim, and a non-AniBT Release refuses a
// template that requires an AniBT value.
func TestFilePlanRendersFrozenTemplateNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sourceFacts string
		template    string
		wantBase    string
		wantReason  string
	}{
		{
			name:        "frozen_values",
			sourceFacts: `{"group_name":"Test Group","identity_kind":"guid","identity_value":"file-test","anibt":{"schema":"anibt-rss-1.0","tags":["language:CHT","language:CHS","tag:HEVC","tag:10bit"]},"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@ca671af","tags":["resolution:1080p","sub_lang:SC"],"value_tags":["title:Frozen Title","group:Frozen Group"]}}`,
			template:    "[${rt.subgroup}] ${rt.title} [${rt.sub_lang}][${anibt.language}][${anibt.tag}].${file.ext}",
			wantBase:    "[Frozen Group] Frozen Title [SC][CHS+CHT][10bit+HEVC].mkv",
		},
		{
			name:        "non_anibt_release",
			sourceFacts: `{"group_name":"Test Group","identity_kind":"guid","identity_value":"file-test","title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@ca671af","tags":[],"value_tags":["title:Frozen Title"]}}`,
			template:    "${rt.title} [${anibt.language}].${file.ext}",
			wantReason:  "naming_fact_undefined_anibt_language",
		},
		{
			name:        "title_tags_without_value_tags",
			sourceFacts: `{"group_name":"Test Group","identity_kind":"guid","identity_value":"file-test","title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@ca671af","tags":["resolution:1080p"]}}`,
			template:    "${rt.title}.${file.ext}",
			wantReason:  "naming_fact_missing_rt_title",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
				literalFrozenVars(policy)
				rules := naming.DefaultNamingRules()
				rules.TV.VideoFilenameTemplate = tc.template
				rules.Movie.VideoFilenameTemplate = tc.template
				policy["effective_sections"].(map[string]any)[domain.SectionNamingRules] = rules
			})
			defer db.Close()
			if _, err := disposabledb.Owner(t).Exec(`UPDATE releases SET source_facts=$1::jsonb WHERE id='release-file-test'`, tc.sourceFacts); err != nil {
				t.Fatal(err)
			}
			service := newFixtureService(t, db, fixture)
			prepareFixture(t, db, service, fixture.artifactID)
			if tc.wantBase != "" {
				var target string
				if err := db.QueryRow(`SELECT target_path FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&target); err != nil {
					t.Fatalf("read planned target: %v", err)
				}
				if filepath.Base(target) != tc.wantBase {
					t.Fatalf("planned target = %q, want base %q", target, tc.wantBase)
				}
				return
			}
			var operations int
			if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&operations); err != nil {
				t.Fatal(err)
			}
			if operations != 0 {
				t.Fatalf("missing value planned %d operations", operations)
			}
			var reason string
			if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, fixture.artifactID).Scan(&reason); err != nil {
				t.Fatal(err)
			}
			if reason != tc.wantReason {
				t.Fatalf("failed delivery reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
