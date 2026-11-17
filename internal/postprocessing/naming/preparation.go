package naming

import (
	"sort"
	"strings"
)

// PreparationState describes acquisition work, not whether a value is nonempty.
// A completed empty result and a failed attempt are terminal, never pending.
type PreparationState uint8

const (
	PreparationPending PreparationState = iota
	PreparationComplete
	PreparationFailed
)

// FactPreparation is the small readiness boundary for facts requiring file or
// provider work. Callers perform the work; templates only project the result.
type FactPreparation struct {
	FileTitle        PreparationState
	Media            PreparationState
	BGMEpisodeTitle  PreparationState
	TMDBEpisodeTitle PreparationState
}

// PendingFacts returns all not-yet-prepared dependencies, even references under
// optional helpers or conditionals. Absence after completed work is not pending.
func (v CompiledVariant) PendingFacts(ctx NamingContext, ready FactPreparation) []string {
	set := map[string]bool{}
	for _, compiled := range []CompiledTemplate{v.Directory, v.VideoFilename} {
		for _, fact := range compiled.ReferencedFacts {
			pending := false
			switch {
			case strings.HasPrefix(fact, "frt."):
				pending = ready.FileTitle == PreparationPending && ctx.FileTitleTags == nil
			case strings.HasPrefix(fact, "media."):
				pending = ready.Media == PreparationPending
			case fact == "bgm.ep_title":
				pending = ctx.Anime.BGM.WorkID != "" && hasEpisode(ctx) && ready.BGMEpisodeTitle == PreparationPending
			case fact == "tmdb.ep_title":
				pending = ctx.Anime.TMDB.WorkID != "" && ctx.Anime.TMDB.MediaKind != "movie" && hasEpisode(ctx) && ready.TMDBEpisodeTitle == PreparationPending
			}
			if pending {
				set[fact] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for fact := range set {
		out = append(out, fact)
	}
	sort.Strings(out)
	return out
}

func hasEpisode(ctx NamingContext) bool {
	return ctx.Release.FrozenEpisode != nil && ctx.Release.FrozenEpisode.Number != ""
}
