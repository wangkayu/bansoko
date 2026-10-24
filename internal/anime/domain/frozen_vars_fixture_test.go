package domain

import "bansoko/internal/namingvars"

// explicitTestFrozenVars is the frozen Vars evidence these preview fixtures
// carry: a literal admission record (fixtures never run the time-limited
// evaluator) over the release-level fact object every admission site builds.
// The facts are the ones the default render reads for its layout — the bound
// season and the projected year — so the fixture exercises the same path a
// release admitted from a metadata binding does instead of an unbound anime.
func explicitTestFrozenVars() *namingvars.FrozenEvaluation {
	source := `vars.test = {marker: input.release_title};`
	digest, _ := namingvars.SourceFingerprint(source)
	program, _ := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, source)
	season, year := int64(1), int64(2025)
	input := namingvars.BuildInput(namingvars.ReleaseEvidence{
		AnimeEvidence: namingvars.AnimeEvidence{AnimeTitle: "Preview Show", AnimeYear: &year, AnimeSeason: &season, TmdbID: "1", TmdbKind: "tv", TmdbSeason: &season},
		ReleaseTitle:  "fixture",
	})
	frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: input, Output: namingvars.Vars{"test": {"marker": "fixture"}}, Status: "ok"}
	return &frozen
}
