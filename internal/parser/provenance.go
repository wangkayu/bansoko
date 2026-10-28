package parser

import (
	"encoding/json"

	"bansoko/internal/episodeidentity"
)

// Provenance is the bounded parser evidence frozen into an existing JSON
// contract (target config snapshot, download frozen policy, FileOperation
// frozen config snapshot). Execution and retry rebuild the one explicit
// selection from it instead of reading current instance parser policy. It is
// evidence, not an authority: rebuilding a selection still validates the kind
// and config against the registry, so an offline kind fails closed rather than
// silently falling back.
type Provenance struct {
	Kind string `json:"kind"`
	// Config is the exact frozen parser configuration the selection ran with
	// (nil when the profile carried no config). It is the same canonical config
	// the media fact snapshot stores, so a retry/replay can rebuild the exact
	// selection from this frozen JSON contract alone.
	Config                json.RawMessage `json:"config,omitempty"`
	ConfigFingerprint     string          `json:"config_fingerprint,omitempty"`
	ConfigVersion         string          `json:"config_version,omitempty"`
	ImplementationVersion string          `json:"implementation_version,omitempty"`
	OutputContractVersion string          `json:"output_contract_version,omitempty"`
	NormalizerVersion     string          `json:"normalizer_version,omitempty"`
	ResolverVersion       string          `json:"resolver_version,omitempty"`
	// Offset is the one episode offset this selection resolved with: the
	// offset of the RSS link the release came through, or the one-shot input's
	// own offset. It is frozen so downstream owners consume the same offset
	// evidence instead of reading current configuration; the resolver applies
	// it exactly once. SourceVersion is the RSS link CAS version the offset was
	// read from at selection time (0 when unknown).
	Offset        int   `json:"offset"`
	SourceVersion int64 `json:"source_version,omitempty"`
	// PolicyVersion is the instance parser-policy owner CAS version and
	// ProfileVersion is the resolved effective parser-section version that
	// supplied the episode patterns. RulesOwnerVersion is the CAS version of the
	// anime rules config owner the profile was resolved from. All three are
	// evidence, never authority.
	PolicyVersion     int64  `json:"policy_version,omitempty"`
	ProfileVersion    int64  `json:"profile_version,omitempty"`
	RulesOwnerVersion int64  `json:"rules_owner_version,omitempty"`
	InputDigest       string `json:"input_digest,omitempty"`
	ResultDigest      string `json:"result_digest,omitempty"`
	// MediaKind is the owner media kind the release identity resolved with:
	// "movie" short-circuits to the canonical movie:0 identity, "tv" covers
	// tv:e<episode>. Season and SeasonBound are the TMDB season (and whether a
	// season 0 was preserved) frozen for naming only; they are not part of the
	// identity. All are evidence of the one evaluation, never a live metadata
	// lookup: downstream owners must not re-read anime_metadata after
	// submission.
	MediaKind   string `json:"media_kind,omitempty"`
	Season      int    `json:"season,omitempty"`
	SeasonBound bool   `json:"season_bound,omitempty"`
	// ParseStatus and ParseShape are the selected parser's typed result
	// (value/missing/invalid/unsupported/conflict and
	// single/range/list/season_only/special/unknown) that produced the frozen
	// identity. They are copied from the same parse result as the digests, never
	// re-derived.
	ParseStatus string `json:"parse_status,omitempty"`
	ParseShape  string `json:"parse_shape,omitempty"`
}

// Provenance returns the descriptor and config evidence of this explicit
// selection. InputDigest/ResultDigest stay empty until a caller that ran the
// selection fills them from its own parse result.
func (s Selection) Provenance() Provenance {
	return Provenance{
		Kind:                  string(s.Kind()),
		Config:                append(json.RawMessage(nil), s.Parser.Config...),
		ConfigFingerprint:     s.ConfigFingerprint,
		ConfigVersion:         s.Descriptor.ConfigVersion,
		ImplementationVersion: s.Descriptor.ImplementationVersion,
		OutputContractVersion: s.Descriptor.OutputContractVersion,
		NormalizerVersion:     NormalizerVersion,
		ResolverVersion:       episodeidentity.ResolverVersion,
		Offset:                s.Offset,
	}
}

// DescribeProvenance rebuilds the descriptor part of a provenance from a frozen
// kind plus config fingerprint. An unknown or offline kind keeps the frozen kind
// and fingerprint (they are still what was frozen) and omits the descriptor
// versions rather than inventing them.
func DescribeProvenance(kind ParserKind, configFingerprint string) Provenance {
	provenance := Provenance{Kind: string(kind), ConfigFingerprint: configFingerprint}
	adapter, err := DefaultRegistry().Resolve(string(kind))
	if err != nil {
		return provenance
	}
	descriptor := adapter.Descriptor()
	provenance.ConfigVersion = descriptor.ConfigVersion
	provenance.ImplementationVersion = descriptor.ImplementationVersion
	provenance.OutputContractVersion = descriptor.OutputContractVersion
	return provenance
}
