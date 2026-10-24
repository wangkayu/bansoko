package application

import "bansoko/internal/namingvars"

func realFrozenVarsFixture() *namingvars.FrozenEvaluation {
	source := `vars.test = {marker: input.release_title};`
	digest, _ := namingvars.SourceFingerprint(source)
	program, _ := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, source)
	// A literal admission record: fixtures never run the time-limited evaluator.
	frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: namingvars.Input{ReleaseTitle: "fixture", Files: []namingvars.File{}}, Output: namingvars.Vars{"test": {"marker": "fixture"}}, Status: "ok"}
	return &frozen
}
