package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFrozenVarsPlanAndReplayIgnoreActiveProgramDrift(t *testing.T) {
	source := `vars.actual = {title: input.release_title, path: input.files[0].path};`
	digest, _ := namingvars.SourceFingerprint(source)
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, source)
	if err != nil {
		t.Fatal(err)
	}
	// A literal admission record of `source`; fixtures never run the
	// time-limited evaluator.
	frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: namingvars.Input{ReleaseTitle: "Frozen release", Files: []namingvars.File{{Name: "source.mkv", Path: "real/source.mkv", Size: 14}}}, Output: namingvars.Vars{"actual": {"title": "Frozen release", "path": "real/source.mkv"}}, Status: "ok"}
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		policy["vars"] = frozen
		policy["fixture_vars_source"] = source
		sections := policy["effective_sections"].(map[string]any)
		rules := naming.DefaultNamingRules()
		rules.TV.DirectoryTemplate = "${vars.actual.title}"
		rules.TV.VideoFilenameTemplate = "${sanitize(vars.actual.path)}.mkv"
		sections[domain.SectionNamingRules] = rules
	})
	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)
	var beforeTarget string
	var beforeConfig []byte
	if err := db.QueryRowContext(context.Background(), `SELECT target_path,frozen_config_snapshot FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&beforeTarget, &beforeConfig); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(beforeTarget, "Frozen release/real_source.mkv.mkv") {
		t.Fatalf("actual target=%s", beforeTarget)
	}
	var config struct {
		Vars namingvars.FrozenEvaluation `json:"vars"`
	}
	if err := json.Unmarshal(beforeConfig, &config); err != nil {
		t.Fatal(err)
	}
	if config.Vars.Program.Digest != digest || config.Vars.Output["actual"]["title"] != "Frozen release" || config.Vars.Input.Files[0].Size != 14 {
		t.Fatalf("config=%s", beforeConfig)
	}
	if err := namingvars.SetActive(namingvars.Vars{"actual": {"title": "WRONG", "path": "WRONG"}}, "new-program"); err != nil {
		t.Fatal(err)
	}
	// Change only active publication; admitted DownloadTask policy is immutable.
	prepareFixture(t, db, service, fixture.artifactID)
	var afterTarget string
	var afterConfig []byte
	if err := db.QueryRowContext(context.Background(), `SELECT target_path,frozen_config_snapshot FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&afterTarget, &afterConfig); err != nil {
		t.Fatal(err)
	}
	if beforeTarget != afterTarget || string(beforeConfig) != string(afterConfig) {
		t.Fatal("replay reinterpreted frozen Vars")
	}
}
