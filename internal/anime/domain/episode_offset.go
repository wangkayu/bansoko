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
