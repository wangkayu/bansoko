package animeconfig

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"encoding/json"
	"testing"
)

func presetFixture(t *testing.T) BusinessSnapshot {
	t.Helper()
	sections := map[string]json.RawMessage{}
	versions := map[string]int64{}
	schemas := map[string]string{}
	for _, name := range animedomain.EffectiveSectionOrder {
		v, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		sections[name] = raw
		versions[name] = 3
		schemas[name] = animedomain.SectionSchemaVersions[name]
	}
	return BusinessSnapshot{SchemaVersion: "business-snapshot-v1", Sections: sections, SectionVersions: versions, SectionSchemas: schemas,
		Parser:    animedomain.ParserPolicyEnvelope{SchemaVersion: animedomain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global_parser_policy_default", PolicyVersion: 2},
		Retention: RetentionValue{AdmissionMode: "all", QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}`)}, RetentionVersion: 4, UnclassifiedRoute: "skip", FileEffectVersion: 5,
		Programs: ProgramReferences{Vars: namingvars.ProgramRef{Version: namingvars.ProfileVersion("sha256:vars"), Digest: "sha256:vars"}, AttrRules: namingvars.ProgramRef{Version: attrrules.ProfileVersion("sha256:attr"), Digest: "sha256:attr"}}}
}
func TestBusinessFreezeDoesNotFollowGlobalMutation(t *testing.T) {
	original := presetFixture(t)
	frozen, err := original.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	before, err := frozen.Digest()
	if err != nil {
		t.Fatal(err)
	}
	original.SectionVersions[animedomain.SectionParser] = 90
	original.Sections[animedomain.SectionParser][0] = 'x'
	original.Retention.QualityProfile[0] = 'x'
	original.Programs.Vars.Digest = "new-global"
	after, err := frozen.Digest()
	if err != nil || before != after {
		t.Fatalf("frozen preset drift: %s %s %v", before, after, err)
	}
	if frozen.RetentionVersion != 4 || frozen.FileEffectVersion != 5 || frozen.Programs.Vars.Digest != "sha256:vars" {
		t.Fatal("business provenance lost")
	}
}
func TestBusinessRejectsIncompleteOrMismatchedProgramReferences(t *testing.T) {
	p := presetFixture(t)
	delete(p.Sections, animedomain.SectionNamingRules)
	if _, err := p.Freeze(); err == nil {
		t.Fatal("incomplete preset accepted")
	}
	p = presetFixture(t)
	p.Programs.Vars.Version = "current"
	if _, err := p.Freeze(); err == nil {
		t.Fatal("mutable current reference accepted")
	}
	p = presetFixture(t)
	p.Retention.QualityProfile = nil
	if _, err := p.Freeze(); err == nil {
		t.Fatal("missing quality profile accepted")
	}
}
