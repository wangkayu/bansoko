package naming

import (
	"fmt"
	"strconv"
)

const episodeResolverKey = "parsed.episode"

// episodeValue is the atomic parsed.episode output.
type episodeValue struct {
	Raw     string
	Offset  int
	Number  int
	Display string
}

func (v episodeValue) String() string {
	return fmt.Sprintf("number=%d display=%q raw=%q offset=%d", v.Number, v.Display, v.Raw, v.Offset)
}

// episodeResolver projects the frozen EpisodeIdentity of the file into
// parsed.episode. The episode of a file comes only from its own filename, as
// resolved before naming; without a frozen identity parsed.episode stays null
// and the caller's unknown-file route decides. Naming never parses a title.
type episodeResolver struct{}

func (episodeResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:        episodeResolverKey,
		OutputType: "object",
		Nullable:   true,
	}
}

func (episodeResolver) Resolve(input ResolverInput) ResolverResult {
	result := ResolverResult{Key: episodeResolverKey, State: ResolverStateNull}
	frozen := input.Facts.Release.FrozenEpisode
	if frozen == nil {
		result.Diagnostics = append(result.Diagnostics, NamingDiagnostic{
			Code: "episode_identity_unresolved", Message: "the frozen episode identity is unresolved", Path: "Release.FrozenEpisode"})
		return result
	}
	number, _ := strconv.Atoi(frozen.Number)
	result.State = ResolverStateValue
	result.Value = episodeValue{
		Raw:     frozen.Raw,
		Offset:  frozen.Offset,
		Number:  number,
		Display: frozen.Display,
	}
	result.Evidence = append(result.Evidence, ResolverEvidence{
		ResolverKey: episodeResolverKey,
		RuleID:      "frozen_episode_identity",
		InputPath:   "Release.FrozenEpisode",
	})
	return result
}
