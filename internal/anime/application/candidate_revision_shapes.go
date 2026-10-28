package application

import (
	"encoding/json"
	"time"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
)

// CandidateRevision is one immutable saved effective-configuration revision. It
// is the historical identity of a candidate save: the caller-supplied CAS
// `version` is a concurrency counter, never a revision identity.
//
// The shape and its projections belong to the kernel because three owners carry
// it across their own boundary: the draft package stores and reads it, the
// read model renders a saved revision, and one-shot admission resolves its
// parser identity. Its storage queries and the candidate save/activation
// transaction stay in internal/anime/draft/application.
type CandidateRevision struct {
	CandidateID        string                           `json:"candidate_id"`
	Revision           int64                            `json:"revision"`
	Draft              animedomain.AnimeDraft           `json:"draft"`
	DraftDigest        string                           `json:"draft_digest"`
	Effective          json.RawMessage                  `json:"effective"`
	SectionVersions    json.RawMessage                  `json:"section_versions"`
	SectionSchemas     json.RawMessage                  `json:"section_schemas"`
	EffectiveDigest    string                           `json:"effective_digest"`
	ParserPolicy       animedomain.ParserPolicyEnvelope `json:"parser_policy"`
	ParserPolicyDigest string                           `json:"parser_policy_digest"`
	Business           *animedomain.BusinessSnapshot    `json:"business"`
	BusinessDigest     *string                          `json:"business_digest"`
	CreatedAt          time.Time                        `json:"created_at"`
}

// Resolved returns the persisted sections in their digest/storage form.
func (r CandidateRevision) Resolved() (animedomain.ResolvedSections, error) {
	return animedomain.ResolvedSectionsFromStored(r.Effective, r.SectionVersions, r.SectionSchemas)
}

// EffectiveConfig decodes the stored revision without reading any current
// global configuration. Execution and preview must use this, never a fresh
// ResolveDraftEffective against latest globals.
func (r CandidateRevision) EffectiveConfig() (animedomain.EffectiveConfig, error) {
	resolved, err := r.Resolved()
	if err != nil {
		return animedomain.EffectiveConfig{}, err
	}
	return animedomain.EffectiveConfigFromSections(resolved)
}

// VerifyIntegrity recomputes the draft, effective and parser-policy digests from
// the stored body and fails closed on any disagreement. Every read path that
// turns a stored revision into an execution identity must call it, so a
// tampered or partially written row can never be executed.
func (r CandidateRevision) VerifyIntegrity() error {
	if r.Business != nil {
		digest, err := r.Business.Digest()
		if err != nil || r.BusinessDigest == nil || digest != *r.BusinessDigest {
			return CandidateDigestMismatchError()
		}
		if r.Draft.Business == nil {
			return CandidateDigestMismatchError()
		}
		draftDigest, err := r.Draft.Business.Digest()
		if err != nil || draftDigest != digest {
			return CandidateDigestMismatchError()
		}
	} else if r.BusinessDigest != nil || r.Draft.Business != nil {
		return CandidateDigestMismatchError()
	}
	if err := r.ParserPolicy.Validate(); err != nil {
		return CandidateDigestMismatchError()
	}
	policyDigest, err := r.ParserPolicy.Digest()
	if err != nil || policyDigest != r.ParserPolicyDigest {
		return CandidateDigestMismatchError()
	}
	resolved, err := r.Resolved()
	if err != nil {
		return CandidateDigestMismatchError()
	}
	if r.Business != nil {
		businessResolved := animedomain.ResolvedSections{Sections: r.Business.Sections, Versions: r.Business.SectionVersions, SchemaVersions: r.Business.SectionSchemas}
		businessEffective, err := businessResolved.EffectiveDigest()
		businessParser, parserErr := r.Business.Parser.Digest()
		if err != nil || parserErr != nil || businessEffective != r.EffectiveDigest || businessParser != r.ParserPolicyDigest {
			return CandidateDigestMismatchError()
		}
	}
	effectiveDigest, err := resolved.EffectiveDigest()
	if err != nil || effectiveDigest != r.EffectiveDigest {
		return CandidateDigestMismatchError()
	}
	draftDigest, err := canonicalAnimeDraftHash(r.Draft)
	if err != nil || draftDigest != r.DraftDigest {
		return CandidateDigestMismatchError()
	}
	return nil
}

// ParserSelectionInput builds the one parser selection the revision froze:
// kind comes from the parser-policy envelope, episode patterns from the
// resolved parser section, and the media binding from the
// immutable revision draft's own TMDB binding. It never consults the current
// global parser policy, the current instance policy, or the current
// anime_metadata row.
//
// The frozen media binding is what makes a saved-revision preview and a one-shot
// admission resolve the same identity an ordinary RSS evaluation would:
// `animedomain.FrozenMediaBinding` is the single projection (BGM-only/unbound
// stays the unbound TV default, never a guessed movie).
func (r CandidateRevision) ParserSelectionInput(resolved animedomain.ResolvedSections) (acquisitiontarget.ParserSelectionInput, error) {
	effective, err := animedomain.EffectiveConfigFromSections(resolved)
	if err != nil {
		return acquisitiontarget.ParserSelectionInput{}, err
	}
	mediaKind, season, seasonBound := animedomain.FrozenMediaBinding(r.Draft.Metadata.Tmdb, r.Draft.Metadata.BackfilledMediaKind())
	return acquisitiontarget.ParserSelectionInput{
		Kind:              r.ParserPolicy.Kind,
		EpisodePatterns:   effective.Rules.Parser.EpisodePatterns,
		MediaKind:         mediaKind,
		Season:            season,
		SeasonBound:       seasonBound,
		PolicyVersion:     r.ParserPolicy.PolicyVersion,
		ProfileVersion:    r.ParserPolicy.ProfileVersion,
		RulesOwnerVersion: r.ParserPolicy.RulesOwnerVersion,
	}, nil
}
