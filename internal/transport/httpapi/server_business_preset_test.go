package httpapi

import (
	"bansoko/internal/anime/animeconfig"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"encoding/json"
	"strings"
	"testing"
)

func TestBusinessPresetPublicShapePreservesFrozenEvidence(t *testing.T) {
	value := animeconfig.BusinessSnapshot{SchemaVersion: "business-snapshot-v1", Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: animedomain.ParserPolicyEnvelope{SchemaVersion: animedomain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", PolicyVersion: 12}, Retention: animeconfig.RetentionValue{AdmissionMode: "all", QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["1080p"],["720p"]]}]}`), CleanupPolicy: json.RawMessage(`{"version":"cleanup-v1","effect":"quarantine","grace_seconds":3600}`)}, RetentionVersion: 17, FileEffectVersion: 19, UnclassifiedRoute: "route"}
	digest := "sha256:" + strings.Repeat("a", 64)
	value.Programs = animeconfig.ProgramReferences{Vars: namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, AttrRules: namingvars.ProgramRef{Digest: digest, Version: attrrules.ProfileVersion(digest)}}
	for _, name := range animedomain.EffectiveSectionOrder {
		v, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		value.Sections[name], err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		value.SectionVersions[name] = 5
		value.SectionSchemas[name] = animedomain.SectionSchemaVersions[name]
	}
	tagFilters := json.RawMessage(`{"anibt":{"require":["resolution:1080p"],"exclude":[]},"title":{"require":[],"exclude":["source:cam"]}}`)
	view, err := businessPresetView(value, tagFilters)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.TagFilters.Anibt.Require) != 1 || view.TagFilters.Anibt.Require[0] != "resolution:1080p" || len(view.TagFilters.Title.Exclude) != 1 {
		t.Fatalf("lost tag_filters default: %+v", view.TagFilters)
	}
	if _, err := businessPresetView(value, json.RawMessage(`{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]},"hidden":true}`)); err == nil {
		t.Fatal("hidden tag_filters field crossed public boundary")
	}
	if view.Snapshot.RetentionVersion != 17 || view.Snapshot.FileEffectVersion != 19 || view.Snapshot.Parser.PolicyVersion != 12 || view.Snapshot.Programs.Vars.Digest != digest || len(view.Snapshot.Retention.QualityProfile.Items) != 1 || view.Snapshot.Retention.QualityProfile.Items[0].Ranks[0][0] != "1080p" {
		t.Fatalf("lost frozen evidence: %+v", view)
	}
	if view.Digest == "" {
		t.Fatal("missing digest")
	}
	value.Retention.QualityProfile = json.RawMessage(`{"version":"profile-v1","items":[],"hidden":true}`)
	if _, err := businessPresetView(value, tagFilters); err == nil {
		t.Fatal("hidden retention field crossed public boundary")
	}
}
