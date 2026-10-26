package application

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/presetdraft"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func frozenBusinessFixture(t *testing.T) animedomain.AnimeDraft {
	t.Helper()
	b := animedomain.BusinessSnapshot{SchemaVersion: "business-snapshot-v1", Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: animedomain.ParserPolicyEnvelope{SchemaVersion: animedomain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", PolicyVersion: 7}, Retention: animedomain.RetentionValue{AdmissionMode: "single_copy", QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[]}]}`)}, RetentionVersion: 13, FileEffectVersion: 17, UnclassifiedRoute: "route"}
	digest := "sha256:" + strings.Repeat("a", 64)
	b.Programs = animedomain.ProgramReferences{Vars: namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, AttrRules: namingvars.ProgramRef{Digest: digest, Version: attrrules.ProfileVersion(digest)}}
	for _, name := range animedomain.EffectiveSectionOrder {
		v, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Sections[name], err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		b.SectionVersions[name] = 3
		b.SectionSchemas[name] = animedomain.SectionSchemaVersions[name]
	}
	hash, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// The draft carries the same complete section values its snapshot froze.
	return animedomain.AnimeDraft{
		Subgroups: animedomain.SubgroupsSection{Entries: []animedomain.SubgroupScoreEntry{}, MaxRss: 3},
		Rules:     animedomain.RulesDraft{Sections: presetdraft.DefaultDomainRules(t)},
		Business:  &b, BusinessDigest: &hash,
	}
}
func TestSubmittedBusinessResolvesWithoutGlobalDatabase(t *testing.T) {
	draft := frozenBusinessFixture(t)
	resolved, parser, err := (&Service{}).resolveCandidateInputTx(context.Background(), nil, draft)
	if err != nil {
		t.Fatal(err)
	}
	if parser.PolicyVersion != 7 || resolved.Versions[animedomain.SectionFileRules] != 3 {
		t.Fatalf("lost provenance: %+v %+v", parser, resolved)
	}
	frozen, err := submittedBusiness(draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Business.RetentionVersion = 99
	if frozen.RetentionVersion != 13 {
		t.Fatal("snapshot alias follows mutable draft")
	}
	if _, err := submittedBusiness(draft); err == nil {
		t.Fatal("mutated content accepted with old digest")
	}
}
func TestRevisionBusinessIntegrityRejectsIndependentTampering(t *testing.T) {
	draft := frozenBusinessFixture(t)
	revision := CandidateRevision{Draft: draft, Business: draft.Business, BusinessDigest: draft.BusinessDigest}
	// Other digests are deliberately absent; it must refuse, never panic.
	if err := revision.VerifyIntegrity(); err == nil {
		t.Fatal("partial revision accepted")
	}
	revision.Draft.Business = nil
	if err := revision.VerifyIntegrity(); err == nil {
		t.Fatal("missing draft business accepted")
	}
}
