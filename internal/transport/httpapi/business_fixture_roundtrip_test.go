package httpapi

import (
	domain "bansoko/internal/anime/domain"
	transport "bansoko/internal/anime/transport"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/transport/httpapi/generated"
	"encoding/json"
	"testing"
)

func TestProviderFixtureBusinessDigestSurvivesExactTypedRequestRoundTrip(t *testing.T) {
	b := domain.BusinessSnapshot{SchemaVersion: "business-snapshot-v1", Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: domain.ParserPolicyEnvelope{SchemaVersion: domain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", Provenance: "global", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1}, RetentionVersion: 1, FileEffectVersion: 1, UnclassifiedRoute: "skip"}
	for _, name := range domain.EffectiveSectionOrder {
		value, err := domain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Sections[name], err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		b.SectionVersions[name] = 1
		b.SectionSchemas[name] = domain.SectionSchemaVersions[name]
	}
	b.Retention = domain.RetentionValue{AdmissionMode: "all", QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}`), CleanupPolicy: json.RawMessage(`{"version":"cleanup-v1","effect":"quarantine","grace_seconds":60}`)}
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	b.Programs.Vars = namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}
	b.Programs.AttrRules = namingvars.ProgramRef{Digest: digest, Version: attrrules.ProfileVersion(digest)}
	request := providerDraftPayload("80801", "678")
	d := request["draft"].(map[string]any)
	b.Sections["subgroups"], _ = json.Marshal(d["subgroups"])
	normalized, err := businessWireRoundTrip(b)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := normalized.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d["business"] = normalized
	d["business_digest"] = expected
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire generated.SaveAnimeConfigCandidateInput
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	// Use precisely the production generated-to-domain conversion.
	submitted, err := transport.Draft(wire.Draft)
	if err != nil {
		t.Fatal(err)
	}
	converted := *submitted.Business
	actual, err := converted.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected || submitted.BusinessDigest == nil || *submitted.BusinessDigest != actual {
		t.Fatalf("digest drift %s != %s", actual, expected)
	}
	var subgroups domain.SubgroupsSection
	if err := json.Unmarshal(converted.Sections["subgroups"], &subgroups); err != nil {
		t.Fatal(err)
	}
	if len(subgroups.Entries) != 1 || subgroups.Entries[0].GroupRef != "678" {
		t.Fatalf("provider group lost: %+v", subgroups)
	}
}
