package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/workitems"
	"testing"
)

func TestFrozenMissingVarsAndTemplateFailPlanningWithoutRunnableEffect(t *testing.T) {
	for _, tc := range []struct {
		name, varsSource, template string
		success                    bool
		output                     namingvars.Vars
		diagnostic                 string
	}{
		{"missing_vars", `vars.actual={value:extract(input.release_title,"(never)")};`, "${vars.actual.value}", false, nil, "naming vars output value is missing; choose an explicit fallback"},
		{"missing_template", `vars.actual={value:input.release_title};`, "${extract(file.name, '(never)')}", false, namingvars.Vars{"actual": {"value": "actual release"}}, ""},
		{"explicit_fallback", `vars.actual={value:extract(input.release_title,"(never)") ?? "chosen"};`, "${vars.actual.value}", true, namingvars.Vars{"actual": {"value": "chosen"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, _ := namingvars.SourceFingerprint(tc.varsSource)
			program, err := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, tc.varsSource)
			if err != nil {
				t.Fatal(err)
			}
			// A literal admission record of the case's program; fixtures never
			// run the time-limited evaluator.
			frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: namingvars.Input{ReleaseTitle: "actual release", Files: []namingvars.File{}}, Output: tc.output, Status: "ok"}
			if tc.diagnostic != "" {
				frozen.Status, frozen.Diagnostic = "error", tc.diagnostic
			}
			db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
				policy["vars"] = frozen
				policy["fixture_vars_source"] = tc.varsSource
				sections := policy["effective_sections"].(map[string]any)
				rules := naming.DefaultNamingRules()
				rules.TV.DirectoryTemplate = tc.template
				sections[domain.SectionNamingRules] = rules
			})
			defer db.Close()
			service := newFixtureService(t, db, fixture)
			prepareFixture(t, db, service, fixture.artifactID)
			if tc.success {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1 AND state='pending'`, fixture.artifactID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("explicit fallback pending operations=%d", count)
				}
			} else {
				var count int
				var reason string
				if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("missing planning produced %d Operations", count)
				}
				if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, fixture.artifactID).Scan(&reason); err != nil {
					t.Fatal(err)
				}
				if reason == "" {
					t.Fatal("missing planning lacks failed delivery diagnostic")
				}
				var runnable int
				if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind=$1 AND state NOT IN ('failed','cancelled')`, workitems.KindFileExecute).Scan(&runnable); err != nil {
					t.Fatal(err)
				}
				if runnable != 0 {
					t.Fatalf("missing planning scheduled %d effects", runnable)
				}
			}
		})
	}
}
