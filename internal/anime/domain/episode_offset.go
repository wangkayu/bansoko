package domain

import "fmt"

// MaxEpisodeOffset bounds every episode offset the product accepts: an RSS
// link's episode_offset and a one-shot request's episode_offset. The range is
// symmetric, -MaxEpisodeOffset..MaxEpisodeOffset, and 0 means no offset.
const MaxEpisodeOffset = 1000

// EpisodeOffsetInRange reports whether offset is an accepted episode offset.
func EpisodeOffsetInRange(offset int) bool {
	return offset >= -MaxEpisodeOffset && offset <= MaxEpisodeOffset
}

// EpisodeOffsetRangeMessage is the shared out-of-range explanation.
func EpisodeOffsetRangeMessage() string {
	return fmt.Sprintf("episode_offset must be between -%d and %d", MaxEpisodeOffset, MaxEpisodeOffset)
}

// DefaultEpisodeSpan is the ordinary one-metadata-episode-per-release value.
const DefaultEpisodeSpan = 1

// MaxEpisodeSpan bounds the combined episodes one release file can cover.
const MaxEpisodeSpan = 12

// NormalizeEpisodeSpan maps an omitted legacy value to the default.
func NormalizeEpisodeSpan(span int) int {
	if span == 0 {
		return DefaultEpisodeSpan
	}
	return span
}

// EpisodeSpanInRange reports whether span is an accepted explicit value. Zero
// is accepted only as the omitted value of legacy in-memory/JSON input.
func EpisodeSpanInRange(span int) bool {
	span = NormalizeEpisodeSpan(span)
	return span >= DefaultEpisodeSpan && span <= MaxEpisodeSpan
}

// EpisodeSpanRangeMessage is the shared out-of-range explanation.
func EpisodeSpanRangeMessage() string {
	return fmt.Sprintf("episode_span must be between %d and %d", DefaultEpisodeSpan, MaxEpisodeSpan)
}
