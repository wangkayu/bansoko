package episodeidentity

import (
	"regexp"
	"strconv"
	"strings"
)

// Resolver resolves exactly one frozen EpisodeIdentity from typed evidence.
// It is a pure function: no parsing, database, network, filesystem, naming, or
// metadata lookup. Adapters produce EpisodeEvidence first; the resolver only
// normalizes, selects, applies the offset exactly once, and creates coverage.
type Resolver struct {
	Version string
}

// NewResolver returns the canonical resolver instance.
func NewResolver() *Resolver {
	return &Resolver{Version: ResolverVersion}
}

// Resolve produces one immutable EpisodeIdentity from the episode evidence of
// exactly one subject: a video file's own filename, or (for the release-level
// identity) the release title. The episode of a file comes only from its own
// filename: release evidence is never copied to a file, sibling files never
// supply evidence to each other, and a filename without an episode is
// unresolved. An identity is the instance plus the episode number: coverage
// keys carry no season (season is a naming field, never identity).
func (r *Resolver) Resolve(input EpisodeIdentityInput) EpisodeIdentity {
	base := EpisodeIdentity{
		Kind:            KindTVEpisode,
		ResolverVersion: r.Version,
		InputDigest:     inputDigest(input),
	}

	// Movie short-circuits first: canonical identity 0/0/0 regardless of the
	// offset.
	if input.MediaKind == "movie" {
		return movieIdentity(base)
	}

	evidence := input.FileEvidence
	// Value domain: an episode identity is a non-negative integer. A fraction
	// in the selected episode (single, or any member of a range or list) is
	// refused here, once, for every parser: no offset, no coverage.
	if fractionalEpisode(evidence) {
		base.Shape = evidence.Shape
		if evidence.Episode != nil {
			raw := *evidence.Episode
			base.RawEpisode = &raw
		}
		return unresolved(base, FailEpisodeFractional)
	}
	// A filename range or list of 2..MaxEpisodesPerFile consecutive integer
	// episodes is one file holding those episodes. Release-title ranges keep
	// no identity (a release title names the batch, not one file).
	if members, ok := multiEpisodeMembers(evidence); ok {
		return r.resolveMulti(base, evidence, input, members)
	}
	switch evidence.Status {
	case StatusValue:
		return r.resolveRaw(base, evidence, input)
	case StatusMissing:
		base.Shape = evidence.Shape
		return unresolved(base, FailEpisodeMissing)
	default:
		// invalid/unsupported/conflict evidence is preserved as-is.
		return identityFromFailure(base, evidence)
	}
}

// resolveRaw applies the offset exactly once to the selected raw evidence and
// freezes the final identity.
func (r *Resolver) resolveRaw(base EpisodeIdentity, evidence EpisodeEvidence, input EpisodeIdentityInput) EpisodeIdentity {
	raw := *evidence.Episode
	offset := input.Offset
	final, err := raw.AddOffset(offset)
	if err != nil {
		failure := FailEpisodeInvalid
		base.State = StateInvalid
		base.Shape = evidence.Shape
		base.FailureCode = &failure
		base.RawEpisode = &raw
		base.Offset = offset
		return base
	}
	source := evidence.Source
	base.State = StateResolved
	base.Shape = evidence.Shape
	base.RawEpisode = &raw
	base.Offset = offset
	base.Episode = &final
	base.SelectedSource = &source
	key := coverageKey(final)
	base.CoverageKey = &key
	return base
}

// resolveMulti applies the offset exactly once to every member of a
// multi-episode file. The identity's Episode is the first member and its
// CoverageKeys hold every member's key ascending.
func (r *Resolver) resolveMulti(base EpisodeIdentity, evidence EpisodeEvidence, input EpisodeIdentityInput, members []EpisodeNumber) EpisodeIdentity {
	offset := input.Offset
	keys := make([]string, 0, len(members))
	finals := make([]EpisodeNumber, 0, len(members))
	for _, raw := range members {
		final, err := raw.AddOffset(offset)
		if err != nil {
			failure := FailEpisodeInvalid
			first := members[0]
			base.State = StateInvalid
			base.Shape = evidence.Shape
			base.FailureCode = &failure
			base.RawEpisode = &first
			base.Offset = offset
			return base
		}
		finals = append(finals, final)
		keys = append(keys, coverageKey(final))
	}
	source := evidence.Source
	rawFirst, rawLast := members[0], members[len(members)-1]
	first, last := finals[0], finals[len(finals)-1]
	key := keys[0]
	base.State = StateResolved
	base.Shape = evidence.Shape
	base.RawEpisode = &rawFirst
	base.RawEpisodeEnd = &rawLast
	base.Offset = offset
	base.Episode = &first
	base.EpisodeEnd = &last
	base.SelectedSource = &source
	base.CoverageKey = &key
	base.CoverageKeys = keys
	return base
}

// rangeMember finds the episode numbers inside a range capture; listMember
// the numbers of an `E01E02` or `[01][02]` list after a leading season token.
var (
	rangeMember       = regexp.MustCompile(`[0-9]+`)
	listSeasonPrefix  = regexp.MustCompile(`(?i)^S[0-9]+`)
	listMemberPattern = regexp.MustCompile(`[0-9]+`)
)

// multiEpisodeMembers reads the members of a filename range or list capture
// and reports whether they form one multi-episode file: 2..MaxEpisodesPerFile
// distinct integers, ascending and consecutive. A range names exactly its two
// ends. Anything else (a gap, a descending or oversized span, release-title
// evidence) is not a multi-episode file and keeps its unsupported shape.
func multiEpisodeMembers(evidence EpisodeEvidence) ([]EpisodeNumber, bool) {
	if evidence.Source != EpisodeSourceFileParser || evidence.Status != StatusUnsupported {
		return nil, false
	}
	var texts []string
	switch evidence.Shape {
	case ShapeRange:
		texts = rangeMember.FindAllString(evidence.RawCapture, -1)
		if len(texts) != 2 {
			return nil, false
		}
	case ShapeList:
		capture := strings.TrimSpace(evidence.RawCapture)
		capture = listSeasonPrefix.ReplaceAllString(capture, "")
		texts = listMemberPattern.FindAllString(capture, -1)
	default:
		return nil, false
	}
	if len(texts) < 2 {
		return nil, false
	}
	values := make([]int, 0, len(texts))
	for _, text := range texts {
		value, err := strconv.Atoi(text)
		if err != nil || value < 0 || value > maxEpisode {
			return nil, false
		}
		values = append(values, value)
	}
	first, last := values[0], values[len(values)-1]
	if evidence.Shape == ShapeRange {
		values = values[:0]
		for value := first; value <= last && len(values) <= MaxEpisodesPerFile; value++ {
			values = append(values, value)
		}
		if last <= first {
			return nil, false
		}
	}
	if len(values) < 2 || len(values) > MaxEpisodesPerFile {
		return nil, false
	}
	members := make([]EpisodeNumber, 0, len(values))
	for index, value := range values {
		if index > 0 && value != values[index-1]+1 {
			return nil, false
		}
		members = append(members, EpisodeNumber(strconv.Itoa(value)))
	}
	if values[len(values)-1] != last {
		return nil, false
	}
	return members, true
}

// identityFromFailure maps invalid/unsupported/conflict file evidence to a
// typed failed identity with the matching failure code.
func identityFromFailure(base EpisodeIdentity, file EpisodeEvidence) EpisodeIdentity {
	base.Shape = file.Shape
	switch file.Status {
	case StatusUnsupported:
		code := FailEpisodeShapeUnsupported
		if file.Shape == ShapeSpecial {
			code = FailSpecialUnsupported
		}
		base.State = StateUnsupported
		base.FailureCode = &code
	case StatusInvalid:
		code := FailEpisodeInvalid
		base.State = StateInvalid
		base.FailureCode = &code
	default: // conflict
		code := FailEpisodeAmbiguous
		base.State = StateConflict
		base.FailureCode = &code
	}
	return base
}

// fractionalNumber finds a number with a fraction in a range or list capture.
var fractionalNumber = regexp.MustCompile(`[0-9]\.[0-9]`)

// fractionalEpisode reports whether the evidence's episode carries a
// fraction: the single value, or any member of a captured range or list.
func fractionalEpisode(evidence EpisodeEvidence) bool {
	if evidence.Status == StatusValue && evidence.Episode != nil {
		return evidence.Episode.IsFractional()
	}
	if evidence.Shape == ShapeRange || evidence.Shape == ShapeList {
		return fractionalNumber.MatchString(evidence.RawCapture)
	}
	return false
}

func unresolved(base EpisodeIdentity, code string) EpisodeIdentity {
	base.State = StateUnresolved
	base.FailureCode = &code
	return base
}

func movieIdentity(base EpisodeIdentity) EpisodeIdentity {
	raw := EpisodeNumber("0")
	final := EpisodeNumber("0")
	key := "movie:0"
	source := EpisodeSourceMovie
	base.State = StateResolved
	base.Kind = KindMovie
	base.RawEpisode = &raw
	base.Offset = 0
	base.Episode = &final
	base.CoverageKey = &key
	base.SelectedSource = &source
	return base
}

// coverageKey is the canonical TV coverage key of one episode: tv:e<episode>.
func coverageKey(episode EpisodeNumber) string {
	return "tv:e" + string(episode)
}

// TVCoverageKey is one parsed canonical TV coverage key. Season is set only
// for a key in the sealed v1 shape tv:s<season>:e<episode>; a key in the
// current shape tv:e<episode> carries no season, because season is a naming
// field and never part of an episode identity.
type TVCoverageKey struct {
	Episode string
	Season  *int
}

// DecomposeCoverageKey reads back the episode of a canonical TV coverage key.
// It accepts the current shape tv:e<episode> and, permanently, the sealed v1
// shape tv:s<season>:e<episode> that frozen acquisition inputs keep. It is the
// only sanctioned way to read an episode back from a frozen identity: callers
// must not hand-parse coverage keys or re-parse a title to obtain one.
func DecomposeCoverageKey(key string) (TVCoverageKey, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(key), "tv:")
	if !ok {
		return TVCoverageKey{}, false
	}
	var parsed TVCoverageKey
	if seasoned, isV1 := strings.CutPrefix(rest, "s"); isV1 {
		seasonText, episodeText, ok := strings.Cut(seasoned, ":e")
		if !ok {
			return TVCoverageKey{}, false
		}
		season, err := strconv.Atoi(seasonText)
		if err != nil || season < 0 {
			return TVCoverageKey{}, false
		}
		parsed.Season = &season
		rest = "e" + episodeText
	}
	episodeText, ok := strings.CutPrefix(rest, "e")
	if !ok {
		return TVCoverageKey{}, false
	}
	if episode, err := CanonicalEpisode(episodeText); err != nil || episode.IsFractional() {
		return TVCoverageKey{}, false
	}
	parsed.Episode = episodeText
	return parsed, true
}

func inputDigest(input EpisodeIdentityInput) string {
	parts := []string{
		input.MediaKind,
		input.FileRef,
		evidenceDigest(input.FileEvidence),
	}
	parts = append(parts, toString(input.Offset))
	return InputDigest(parts...)
}

func evidenceDigest(evidence EpisodeEvidence) string {
	value := ""
	if evidence.Episode != nil {
		value = string(*evidence.Episode)
	}
	return InputDigest(string(evidence.Source), evidence.InputKind, evidence.Status,
		evidence.Shape, evidence.RawCapture, value, evidence.Reason)
}

func toString(value int) string {
	return strconv.Itoa(value)
}
