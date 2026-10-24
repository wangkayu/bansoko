package domain

import (
	"strconv"

	"bansoko/internal/episodeidentity"
)

// integerEpisode projects a resolved identity onto the integer release scalar
// (parsed_facts.episode). A resolved identity is always an integer.
func integerEpisode(identity episodeidentity.EpisodeIdentity) *float32 {
	if identity.State != episodeidentity.StateResolved || identity.Episode == nil {
		return nil
	}
	value, err := strconv.Atoi(string(*identity.Episode))
	if err != nil || value < 0 {
		return nil
	}
	episode := float32(value)
	return &episode
}
