// Package frozenpolicy builds the complete DownloadTask frozen-policy snapshot a
// real admission writes. Tests that seed download_tasks directly instead of
// going through admission must use it: post-download file-effect preparation
// reads the frozen policy as its only configuration authority and fails closed
// when the snapshot is missing or incomplete.
package frozenpolicy

import (
	"encoding/json"
	"fmt"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// CompleteMap returns the frozen-policy fixture as a mutable map so a test can
// substitute one section (for example an invalid naming rule) before encoding.
func CompleteMap() map[string]any {
	source := `vars.fixture = {title: input.release_title};`
	digest, _ := namingvars.SourceFingerprint(source)
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, source)
	if err != nil {
		panic(err)
	}
	// A literal admission record of `source` over the fixture input. Fixtures
	// never run the time-limited evaluator, so a loaded host cannot turn the
	// fixture into a frozen evaluation error.
	frozen := namingvars.FrozenEvaluation{
		Program: program.Ref(),
		Input:   namingvars.Input{ReleaseTitle: "fixture release", Files: []namingvars.File{{Name: "source.mkv", Path: "source.mkv", Size: 42}}},
		Output:  namingvars.Vars{"fixture": {"title": "fixture release"}},
		Status:  "ok",
	}
	return map[string]any{
		"vars": frozen,
		"config_identity": map[string]any{
			"identity_kind":    "instance_materialized",
			"managed_anime_id": "anime-file-test",
			"effective_digest": "sha256:" + repeat("a"),
		},
		"effective_digest": "sha256:" + repeat("a"),
		"effective_sections": map[string]any{
			animedomain.SectionParser: map[string]any{
				"episode_patterns": acquisitiondomain.DefaultEpisodePatterns,
			},
			animedomain.SectionFileRules:   map[string]any{"must_contain": "", "not_in": ""},
			animedomain.SectionNamingRules: naming.DefaultNamingRules(),
			animedomain.SectionFileEffect:  map[string]any{"operation": "copy", "collision_policy": "fail"},
		},
		"section_versions": map[string]int64{
			animedomain.SectionParser: 1, animedomain.SectionFileRules: 1,
			animedomain.SectionNamingRules: 1, animedomain.SectionFileEffect: 1,
		},
		"section_schemas": map[string]string{
			animedomain.SectionParser:      animedomain.SectionSchemaVersions[animedomain.SectionParser],
			animedomain.SectionFileRules:   animedomain.SectionSchemaVersions[animedomain.SectionFileRules],
			animedomain.SectionNamingRules: animedomain.SectionSchemaVersions[animedomain.SectionNamingRules],
			animedomain.SectionFileEffect:  animedomain.SectionSchemaVersions[animedomain.SectionFileEffect],
		},
	}
}

// CompleteJSON returns a frozen policy carrying the resolved sections a
// file-operation plan needs (parser, file rules, naming rules, file effect),
// their versions/schemas, and a synthetic instance-materialized identity.
func CompleteJSON() (string, error) {
	return Encode(CompleteMap())
}

// Encode marshals one frozen-policy map.
func Encode(policy map[string]any) (string, error) {
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode frozen policy fixture: %w", err)
	}
	return string(raw), nil
}

// MustJSON panics when the fixture cannot be encoded. It exists so a test seed
// helper reads as a single expression.
func MustJSON() string {
	raw, err := CompleteJSON()
	if err != nil {
		panic(err)
	}
	return raw
}

func repeat(character string) string {
	out := make([]byte, 64)
	for index := range out {
		out[index] = character[0]
	}
	return string(out)
}
