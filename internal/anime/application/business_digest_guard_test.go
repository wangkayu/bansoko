package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
)

// A submitted draft may carry a complete business snapshot plus the digest it
// claims for it. Every entry point that admits such a draft must refuse a
// tampered one before writing anything, so the frozen content a user reviewed
// is the content the admission resolves.

func businessSnapshotFixture(t *testing.T) (animedomain.BusinessSnapshot, string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	b := animedomain.BusinessSnapshot{
		SchemaVersion:   "business-snapshot-v1",
		Sections:        map[string]json.RawMessage{},
		SectionVersions: map[string]int64{},
		SectionSchemas:  map[string]string{},
		Parser: animedomain.ParserPolicyEnvelope{
			SchemaVersion: animedomain.ParserPolicyEnvelopeSchemaVersion,
			Kind:          "regex",
			SourceOwner:   "global",
			PolicyVersion: 7,
		},
		Replacement: animedomain.ReplacementValue{
			AdmissionMode:  "single_copy",
			QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[]}]}`),
		},
		ReplacementVersion: 13,
		FileEffectVersion:  17,
		UnclassifiedRoute:  "route",
		Programs: animedomain.ProgramReferences{
			Vars:      namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)},
			AttrRules: namingvars.ProgramRef{Digest: digest, Version: attrrules.ProfileVersion(digest)},
		},
	}
	for _, name := range animedomain.EffectiveSectionOrder {
		value, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Sections[name], err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		b.SectionVersions[name] = 3
		b.SectionSchemas[name] = animedomain.SectionSchemaVersions[name]
	}
	frozen, err := b.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := frozen.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return frozen, hash
}

func TestVerifySubmittedBusinessDigestAcceptsMatchingSnapshot(t *testing.T) {
	business, hash := businessSnapshotFixture(t)
	draft := animedomain.AnimeDraft{Business: &business, BusinessDigest: &hash}
	if err := VerifySubmittedBusinessDigest(draft); err != nil {
		t.Fatalf("matching snapshot refused: %v", err)
	}
}

func TestVerifySubmittedBusinessDigestRejectsEditedSnapshot(t *testing.T) {
	business, hash := businessSnapshotFixture(t)
	// The content moves after the freeze, the digest does not: this is exactly
	// what a caller assembling its own draft would send.
	business.ReplacementVersion = 99
	draft := animedomain.AnimeDraft{Business: &business, BusinessDigest: &hash}
	err := VerifySubmittedBusinessDigest(draft)
	if err == nil {
		t.Fatal("edited snapshot accepted under the original digest")
	}
	var businessErr *apperrors.Business
	if !errors.As(err, &businessErr) || businessErr.Status != 422 || businessErr.Code != "BUSINESS_SNAPSHOT_INVALID" {
		t.Fatalf("error = %v, want 422 BUSINESS_SNAPSHOT_INVALID", err)
	}
}

func TestVerifySubmittedBusinessDigestRejectsDigestWithoutSnapshot(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	draft := animedomain.AnimeDraft{BusinessDigest: &hash}
	if err := VerifySubmittedBusinessDigest(draft); err == nil {
		t.Fatal("digest without a snapshot accepted")
	}
}

func TestVerifySubmittedBusinessDigestAcceptsLegacyDraftWithoutSnapshot(t *testing.T) {
	// The legacy current_input path resolves from Global; it has no snapshot and
	// no digest, and this guard must not reject it on its own.
	if err := VerifySubmittedBusinessDigest(animedomain.AnimeDraft{}); err != nil {
		t.Fatalf("legacy draft refused: %v", err)
	}
}
