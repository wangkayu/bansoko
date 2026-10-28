package episodeidentity

// EpisodeEvidenceSource names the owner of one episode evidence value.
type EpisodeEvidenceSource string

const (
	EpisodeSourceFileParser    EpisodeEvidenceSource = "file_parser"
	EpisodeSourceReleaseParser EpisodeEvidenceSource = "release_parser"
	// Deprecated: provider structured episode data is never admissible file
	// identity evidence. Kept only so legacy stored/test envelopes decode.
	EpisodeSourceAniBT EpisodeEvidenceSource = "anibt"
	EpisodeSourceMovie EpisodeEvidenceSource = "movie"
)

// Evidence status distinguishes missing, invalid, unsupported, and conflict
// instead of encoding all four as an empty string.
const (
	StatusValue       = "value"
	StatusMissing     = "missing"
	StatusInvalid     = "invalid"
	StatusUnsupported = "unsupported"
	StatusConflict    = "conflict"
)

// Episode shape vocabulary for one captured input.
const (
	ShapeSingle     = "single"
	ShapeRange      = "range"
	ShapeList       = "list"
	ShapeSeasonOnly = "season_only"
	ShapeSpecial    = "special"
	ShapeUnknown    = "unknown"
)

// EpisodeEvidence is one bounded piece of raw episode evidence. The value is
// always pre-offset: parser/AniBT adapters never apply an offset themselves.
type EpisodeEvidence struct {
	Source    EpisodeEvidenceSource
	InputKind string // release_title | file_name | provider
	Status    string // value | missing | invalid | unsupported | conflict
	Shape     string // single | range | list | season_only | special | unknown
	// RawCapture is the captured text: the episode token of a single, the
	// range or list text of a range/list.
	RawCapture string
	Episode    *EpisodeNumber // nil unless Status == value
	Reason     string
}

// NewValueEvidence builds a valid single-value evidence record. raw must be a
// canonicalizable number; invalid input produces an invalid evidence instead
// of panicking.
func NewValueEvidence(source EpisodeEvidenceSource, inputKind, raw string) EpisodeEvidence {
	canonical, err := CanonicalEpisode(raw)
	if err != nil {
		return EpisodeEvidence{
			Source:     source,
			InputKind:  inputKind,
			Status:     StatusInvalid,
			Shape:      ShapeSingle,
			RawCapture: raw,
			Reason:     err.Error(),
		}
	}
	return EpisodeEvidence{
		Source:     source,
		InputKind:  inputKind,
		Status:     StatusValue,
		Shape:      ShapeSingle,
		RawCapture: raw,
		Episode:    &canonical,
	}
}

// NewUnsupportedEvidence builds a typed unsupported evidence record (range,
// list, season-only, special, or unknown shape).
func NewUnsupportedEvidence(source EpisodeEvidenceSource, inputKind, shape, raw, reason string) EpisodeEvidence {
	return EpisodeEvidence{
		Source:     source,
		InputKind:  inputKind,
		Status:     StatusUnsupported,
		Shape:      shape,
		RawCapture: raw,
		Reason:     reason,
	}
}

// NewMissingEvidence builds a typed missing evidence record.
func NewMissingEvidence(source EpisodeEvidenceSource, inputKind string) EpisodeEvidence {
	return EpisodeEvidence{
		Source:    source,
		InputKind: inputKind,
		Status:    StatusMissing,
		Shape:     ShapeUnknown,
	}
}

// Identity states.
const (
	StateResolved    = "resolved"
	StateUnresolved  = "unresolved"
	StateInvalid     = "invalid"
	StateUnsupported = "unsupported"
	StateConflict    = "conflict"
)

// Identity kinds.
const (
	KindTVEpisode = "tv_episode"
	KindMovie     = "movie"
)

// Failure codes.
const (
	FailEpisodeMissing          = "episode_missing"
	FailEpisodeInvalid          = "episode_invalid"
	FailEpisodeShapeUnsupported = "episode_shape_unsupported"
	FailSpecialUnsupported      = "special_identity_unsupported"
	FailEpisodeAmbiguous        = "episode_ambiguous"
	// FailEpisodeFractional: the selected source's episode (single, or any
	// member of a range or list) carries a fraction such as "12.5". An episode
	// identity is a non-negative integer, so the file has none.
	FailEpisodeFractional = "episode_fractional"
)

// EpisodeIdentityInput is the pure resolver input. MediaKind is "tv" or
// "movie". FileEvidence is the episode evidence of the one subject being
// identified: the video's own filename, or the release title for the
// release-level identity; no other evidence participates. No season is an
// input: an identity is the instance plus the episode number.
type EpisodeIdentityInput struct {
	MediaKind    string
	FileRef      string
	FileEvidence EpisodeEvidence
	// Offset is the one episode offset of this identity, chosen by the caller:
	// the offset of the RSS link the release came through, or the one-shot
	// input's own offset; 0 when neither exists. The resolver applies it
	// exactly once and never chooses between offsets.
	Offset int
	// Deprecated compatibility fields. Structured AniBT episode/season data is
	// not admissible evidence about an actual file and is ignored by Resolver.
	AniBTEvidence *EpisodeEvidence
	AniBTMode     string
}

// EpisodeIdentity is the immutable resolver output. RawEpisode is pre-offset;
// Episode is the final value after the offset was applied exactly
// once. Only StateResolved carries a CoverageKey. Shape is the selected
// parser's typed shape vocabulary member that produced this identity, so a
// non-resolved result stays explainable without re-parsing.
type EpisodeIdentity struct {
	State           string
	Kind            string
	Shape           string
	RawEpisode      *EpisodeNumber
	Offset          int
	Episode         *EpisodeNumber
	CoverageKey     *string
	SelectedSource  *EpisodeEvidenceSource
	FailureCode     *string
	ResolverVersion string
	InputDigest     string
	// A multi-episode file (a filename range or list of 2..MaxEpisodesPerFile
	// consecutive episodes) also carries its last episode and every coverage
	// key it occupies, ascending. Episode/RawEpisode/CoverageKey stay the first
	// member, so single-key readers keep their meaning. Absent for a single
	// episode, so a single identity's frozen JSON is unchanged.
	RawEpisodeEnd *EpisodeNumber `json:",omitempty"`
	EpisodeEnd    *EpisodeNumber `json:",omitempty"`
	CoverageKeys  []string       `json:",omitempty"`
}

// MaxEpisodesPerFile bounds a multi-episode file: a filename range or list of
// more than this many episodes is a collection-style name or a misreading, not
// one file holding them all, and keeps no identity.
const MaxEpisodesPerFile = 4

// Keys returns every coverage key the identity occupies: all members of a
// multi-episode file, the one key of a single episode, nothing otherwise. It
// is the one way to read coverage from an identity.
func (identity EpisodeIdentity) Keys() []string {
	if len(identity.CoverageKeys) > 0 {
		return append([]string(nil), identity.CoverageKeys...)
	}
	if identity.CoverageKey != nil && *identity.CoverageKey != "" {
		return []string{*identity.CoverageKey}
	}
	return nil
}

// MultiEpisode reports whether the identity is a resolved multi-episode file.
func (identity EpisodeIdentity) MultiEpisode() bool {
	return identity.State == StateResolved && len(identity.CoverageKeys) > 1
}
