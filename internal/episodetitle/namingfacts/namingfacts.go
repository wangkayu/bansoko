// Package namingfacts resolves the provider episode title facts
// (bgm.ep_title, tmdb.ep_title) for the naming renders on the network-query
// whitelist. The whitelist is thin, by owner decree 2026-10-22: the library
// rename render (fileoperation prepare) and the anime-files renders of
// filemaintenance (the alignment target render and the match render) — the
// lanes that also freeze the states into the render snapshot's
// episode_titles block. Every other render, preview or target-admission path
// never calls this package, and callers keep that property asserted in their
// tests.
//
// Only-if-used: a source is queried only when at least one template of the
// render actually reads its fact (a conditional reference is still a
// reference). An unbound source, or an episode number that is not
// resolvable, is its own snapshot state — never a query and never an error.
// A transient lookup failure is an error: the caller's stage machinery knows
// how to retry it, and the snapshot block then never lands for that run.
package namingfacts

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bansoko/internal/episodetitle"
	"bansoko/internal/postprocessing/naming"
)

// Querier resolves episode titles through the persistent cache.
type Querier struct {
	// Cache is the persistent provider episode title store (migration 046).
	Cache *episodetitle.PersistentCache
}

// Snapshot fact paths, one per source. The same names the templates reference
// and the required-fact gate of a legacy replay reads.
const (
	BGMEpTitlePath  = "bgm.ep_title"
	TMDBEpTitlePath = "tmdb.ep_title"
)

// Episode titles block states. `known` and `absent` are the two value states
// of render_episode_title_valid (001); `unavailable` is the third, carrying a
// reason_code. The reasons the validator accepts are the three below plus
// title_invalid, which this resolver never produces: a provider title the
// snapshot bounds refuse reads as a definite absent.
const (
	StateKnown       = "known"
	StateAbsent      = "absent"
	StateUnavailable = "unavailable"
	ReasonNotRequest = "not_requested"
	ReasonNotBound   = "not_bound"
	ReasonNoEpisode  = "no_episode"
)

// SourceState is one source's answer of one render: its block state — known,
// absent, or an unavailable reason (not_requested, not_bound, no_episode) —
// and the known title.
type SourceState struct {
	State string
	Value string
}

// Known reports a title the lookup or cache carries.
func (s SourceState) Known() bool { return s.State == StateKnown }

// ReasonCode is the snapshot reason_code value: null (empty string here) for
// the known and absent value states, the reason otherwise.
func (s SourceState) ReasonCode() string {
	if s.Known() || s.State == StateAbsent {
		return ""
	}
	return s.State
}

// Resolution is the episode title answer of one render: the projected fact
// values, both sources' snapshot states and the snapshot block when anything
// was assessed.
type Resolution struct {
	Facts naming.EpisodeTitleFacts
	BGM   SourceState
	TMDB  SourceState
	// Episode is the episode the lookup read (the block's episode key). It is
	// empty when the render's episode number is not resolvable, in which case
	// the block still writes with a null episode key and no_episode reasons.
	Episode string
}

// Assessed reports whether at least one render template referenced an
// ep_title fact, so the sources carry states and the block is written.
func (r Resolution) Assessed() bool {
	return r.BGM.State != ReasonNotRequest || r.TMDB.State != ReasonNotRequest
}

// EpisodeTitlesBlock builds the render snapshot's episode_titles block, the
// exact shape render_episode_titles_valid accepts under
// renderer_version=fileoperation-render-v1: exact keys bgm, episode and tmdb,
// per-title exact keys state, value and reason_code. It is nil when nothing
// referenced a fact — the withhold-the-block property snapshot note 025 also
// asserts for its absent key.
func (r Resolution) EpisodeTitlesBlock() map[string]any {
	if !r.Assessed() {
		return nil
	}
	var episode any
	if r.Episode != "" {
		episode = r.Episode
	}
	return map[string]any{
		"episode": episode,
		"bgm":     episodeTitleNode(r.BGM),
		"tmdb":    episodeTitleNode(r.TMDB),
	}
}

// episodeTitleNode is one source entry of the snapshot block.
func episodeTitleNode(state SourceState) map[string]any {
	if state.Known() {
		return map[string]any{"state": StateKnown, "value": state.Value, "reason_code": nil}
	}
	if state.State == StateAbsent {
		return map[string]any{"state": StateAbsent, "value": nil, "reason_code": nil}
	}
	return map[string]any{"state": StateUnavailable, "value": nil, "reason_code": state.ReasonCode()}
}

// Resolve assesses both sources for one naming render. templates are the
// compiled templates this render actually renders (its variant, or the
// subtitle suffix); a source neither named is not_requested and never touches
// the network. ctxFacts is the resolved naming context. Renders are always
// cache-first: the only positive-cache bypass (and the negative-cache
// bypass of the redo/repair surface) is PersistentCache.RefreshWork, behind
// the 刷新元数据 action, never this path.
func (q *Querier) Resolve(ctx context.Context, templates []naming.CompiledTemplate, ctxFacts naming.NamingContext) (Resolution, error) {
	facts := naming.UnifiedFacts(ctxFacts)
	// A source its render never referenced stays not_requested — the marker
	// that also decides Assessed and the block.
	out := Resolution{
		Episode: strings.TrimSpace(facts.Ep.Number),
		BGM:     SourceState{State: ReasonNotRequest},
		TMDB:    SourceState{State: ReasonNotRequest},
	}
	episode, parseErr := strconv.Atoi(out.Episode)
	episodeReady := parseErr == nil && episode >= 1
	bound := episodeReady && strings.TrimSpace(facts.Bgm.ID) != ""
	tsBound := episodeReady && strings.EqualFold(strings.TrimSpace(facts.Tmdb.Kind), "tv") &&
		facts.Tmdb.Season != nil && *facts.Tmdb.Season >= 0 && strings.TrimSpace(facts.Tmdb.ID) != ""

	// resolve assesses one source when its fact is referenced: no episode, or
	// no binding, is its own unavailable reason; otherwise the cached lookup runs.
	resolve := func(referenced bool, state *SourceState, source episodetitle.Source, workID string, season int, bound bool) error {
		if !referenced {
			return nil
		}
		switch {
		case !episodeReady:
			state.State = ReasonNoEpisode
		case !bound:
			state.State = ReasonNotBound
		default:
			answer, err := q.lookup(ctx, source, workID, season, episode)
			if err != nil {
				return err
			}
			*state = answer
			if answer.Known() {
				switch source {
				case episodetitle.SourceBGM:
					out.Facts.BGM = answer.Value
				default:
					out.Facts.TMDB = answer.Value
				}
			}
		}
		return nil
	}
	if err := resolve(templatesReference(templates, BGMEpTitlePath), &out.BGM,
		episodetitle.SourceBGM, facts.Bgm.ID, 0, bound); err != nil {
		return Resolution{}, fmt.Errorf("resolve BGM episode title: %w", err)
	}
	// The TMDB binding is per season (naming facts); BGM rows key season 0.
	if err := resolve(templatesReference(templates, TMDBEpTitlePath), &out.TMDB,
		episodetitle.SourceTMDB, facts.Tmdb.ID, seasonValue(facts.Tmdb.Season), tsBound); err != nil {
		return Resolution{}, fmt.Errorf("resolve TMDB episode title: %w", err)
	}
	return out, nil
}

// seasonValue projects the binding season pointer onto the cache key.
func seasonValue(season *int64) int {
	if season == nil || *season < 0 {
		return 0
	}
	return int(*season)
}

// lookup maps a cache answer onto one SourceState. A nil Querier or an
// unwired cache is transient, not a guessed answer.
func (q *Querier) lookup(ctx context.Context, source episodetitle.Source, workID string, season, episode int) (SourceState, error) {
	if q == nil || q.Cache == nil {
		return SourceState{}, fmt.Errorf("%w: the episode title cache is not wired", episodetitle.ErrTransient)
	}
	title, err := q.Cache.EpisodeTitle(ctx, source, workID, season, episode, false)
	if err != nil {
		return SourceState{}, err
	}
	if title.Known {
		return SourceState{State: StateKnown, Value: title.Title}, nil
	}
	return SourceState{State: StateAbsent}, nil
}

// templatesReference reports whether any template of the render reads path
// (conditional or not — the only-if-used rule reads references, not
// requirements).
func templatesReference(templates []naming.CompiledTemplate, path string) bool {
	for _, template := range templates {
		for _, fact := range template.ReferencedFacts {
			if fact == path {
				return true
			}
		}
	}
	return false
}
