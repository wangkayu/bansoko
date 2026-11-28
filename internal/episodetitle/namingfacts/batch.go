package namingfacts

import (
	"context"
	"strconv"
	"strings"

	"bansoko/internal/episodetitle"
	"bansoko/internal/postprocessing/naming"
)

// EpisodeDependency returns a cache key for an applicable naming dependency.
// Inapplicable paths (including unbound providers) do not need diagnostics.
func EpisodeDependency(fact string, facts naming.NamingContext) (episodetitle.EpisodeKey, bool) {
	values := naming.UnifiedFacts(facts)
	episode, err := strconv.Atoi(values.Ep.Number)
	if err != nil {
		return episodetitle.EpisodeKey{}, false
	}
	key := episodetitle.EpisodeKey{Episode: episode}
	switch fact {
	case BGMEpTitlePath:
		key.WorkKey = episodetitle.WorkKey{Source: episodetitle.SourceBGM, WorkID: strings.TrimSpace(values.Bgm.ID)}
	case TMDBEpTitlePath:
		if !strings.EqualFold(strings.TrimSpace(values.Tmdb.Kind), "tv") || values.Tmdb.Season == nil || *values.Tmdb.Season < 0 {
			return episodetitle.EpisodeKey{}, false
		}
		key.WorkKey = episodetitle.WorkKey{Source: episodetitle.SourceTMDB, WorkID: strings.TrimSpace(values.Tmdb.ID), Season: seasonValue(values.Tmdb.Season)}
	default:
		return episodetitle.EpisodeKey{}, false
	}
	return key, key.Valid()
}

// ResolveBatch is the recognition lane. It reports provider failures per key
// and still propagates request cancellation and cache infrastructure errors.
func (q *Querier) ResolveBatch(ctx context.Context, work map[episodetitle.WorkKey][]int, cacheOnly map[episodetitle.WorkKey]bool) (map[episodetitle.EpisodeKey]episodetitle.DetailedTitle, error) {
	if q != nil && q.Cache != nil {
		return q.Cache.EpisodeTitles(ctx, work, cacheOnly)
	}
	answers := map[episodetitle.EpisodeKey]episodetitle.DetailedTitle{}
	for key, episodes := range work {
		reason := string(episodetitle.LookupUnconfigured)
		if cacheOnly[key] {
			reason = episodetitle.ReasonCacheMiss
		}
		for _, episode := range episodes {
			answers[episodetitle.EpisodeKey{WorkKey: key, Episode: episode}] = episodetitle.DetailedTitle{Reason: reason}
		}
	}
	return answers, ctx.Err()
}
