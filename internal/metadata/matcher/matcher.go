package matcher

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	observabilityport "bansoko/internal/observability/port"
)

// MatchStatus describes the outcome of one automatic metadata match attempt.
type MatchStatus string

const (
	StatusComplete MatchStatus = "complete"
	StatusPartial  MatchStatus = "partial"
	StatusNoResult MatchStatus = "no_result"
)

// MatchErrorKind identifies a provider failure that prevented a complete match.
type MatchErrorKind string

const (
	ErrorProvider MatchErrorKind = "provider_error"
	ErrorConfig   MatchErrorKind = "config_error"
)

// MatchError preserves a stable category while retaining the underlying cause.
type MatchError struct {
	Kind  MatchErrorKind
	Layer string
	Err   error
}

func (e *MatchError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("metadata %s %s: %v", e.Layer, e.Kind, e.Err)
}

func (e *MatchError) Unwrap() error { return e.Err }

func IsMatchErrorKind(err error, kind MatchErrorKind) bool {
	var matchErr *MatchError
	return errors.As(err, &matchErr) && matchErr.Kind == kind
}

// MatchResult is the combined output of all three matching layers.
type MatchResult struct {
	BgmID         *string // bangumi subject id (numeric string)
	TmdbWorkID    *string // TMDB database id (numeric string)
	TmdbKind      *string // "tv" or "movie"
	TmdbSeason    *int
	TotalEpisodes *int // planned episode count; from BGM subject eps only
	Status        MatchStatus
	// BgmTitle / BgmTitleCN and TmdbTitleCN / TmdbOriginalTitle are the
	// provider's native names of the matched work, set only by the API layer
	// that produced the id; bangumi-data matches carry none. TmdbTitleCN is
	// the title the TMDB request language returned (zh-CN everywhere).
	BgmTitle, BgmTitleCN           string
	TmdbTitleCN, TmdbOriginalTitle string
	// TmdbOverview is the work summary TMDB reported for the matched work,
	// set only by the same API layer that set the TMDB tuple. Acceptance
	// freezes it alongside the titles (tmdb.summary).
	TmdbOverview string
	// BgmYear / TmdbYear are the first air/release year the provider reported
	// for the work it bound; nil when the provider gave none (or only the
	// bangumi-data static index supplied the id). They are provider facts for
	// the accepted binding, never inferred from the title.
	BgmYear, TmdbYear *int
}

// WorkTitles returns the native names this result carries for the works it
// newly binds: bgm when bindBGM, tmdb when bindTMDB. Callers record them so an
// acceptance can freeze the bound work's titles and year.
func (r *MatchResult) WorkTitles(bindBGM, bindTMDB bool) []identitymetadatadomain.WorkTitles {
	if r == nil {
		return nil
	}
	out := []identitymetadatadomain.WorkTitles{}
	if bindBGM && r.BgmID != nil {
		out = append(out, identitymetadatadomain.WorkTitles{Provider: "bgm", MediaKind: "subject", WorkID: *r.BgmID, Title: r.BgmTitle, TitleCN: r.BgmTitleCN, Year: r.BgmYear})
	}
	if bindTMDB && r.TmdbWorkID != nil && r.TmdbKind != nil {
		out = append(out, identitymetadatadomain.WorkTitles{Provider: "tmdb", MediaKind: *r.TmdbKind, WorkID: *r.TmdbWorkID, TitleCN: r.TmdbTitleCN, OriginalTitle: r.TmdbOriginalTitle, Overview: r.TmdbOverview, Year: r.TmdbYear})
	}
	return out
}

// HasAny returns true when at least one ID was resolved.
func (r *MatchResult) HasAny() bool {
	if r == nil {
		return false
	}
	return r.BgmID != nil || r.TmdbWorkID != nil
}

// IsComplete returns true when both BGM and TMDB IDs are present.
func (r *MatchResult) IsComplete() bool {
	if r == nil {
		return false
	}
	return r.BgmID != nil && r.TmdbWorkID != nil && r.TmdbKind != nil && r.TmdbSeason != nil
}

func (r *MatchResult) setStatus() {
	if r == nil {
		return
	}
	if r.IsComplete() {
		r.Status = StatusComplete
	} else if r.HasAny() {
		r.Status = StatusPartial
	} else {
		r.Status = StatusNoResult
	}
}

// Matcher combines the bangumi-data index, BGM API search, and TMDB API
// search into a single three-layer matching pipeline.
type Matcher struct {
	bangumiIndex *BangumiDataIndex
	bgmMatcher   *BGMMatcher
	tmdbMatcher  *TMDBMatcher
	logger       observabilityport.RuntimeLogger
}

// NewMatcher builds a composite matcher. Any layer may be nil to disable it.
// A nil logger safely discards structured runtime events.
func NewMatcher(bangumiIndex *BangumiDataIndex, bgmMatcher *BGMMatcher, tmdbMatcher *TMDBMatcher, logger observabilityport.RuntimeLogger) *Matcher {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return &Matcher{
		bangumiIndex: bangumiIndex,
		bgmMatcher:   bgmMatcher,
		tmdbMatcher:  tmdbMatcher,
		logger:       logger,
	}
}

// logf emits a structured failure event. Raw error text never enters the
// message; the stable failure category lives in ReasonCode/ErrorClass.
func (m *Matcher) logf(ctx context.Context, event observabilityport.RuntimeEvent) {
	if m == nil || m.logger == nil {
		return
	}
	event.Component = "metadata_matcher"
	event.Outcome = "failure"
	m.logger.Warn(ctx, event)
}

// Match runs the three-layer matching pipeline for a single anime.
//
//   - origin: subscription origin ("anibt", "mikan", "custom")
//   - entryRef: provider catalog entry ref (mikan ID for mikan origin; empty otherwise)
//   - title: the anime title to search with
//   - skipBgm: if true, skip BGM API search
//   - skipTmdb: if true, skip TMDB API search
//
// Layer 1 (bangumi-data index) is always attempted first because it is
// instantaneous. Layer 2 (BGM API) runs only if BGM ID is still missing
// and skipBgm is false. Layer 3 (TMDB API) runs only if TMDB ID is still
// missing and skipTmdb is false.
func (m *Matcher) Match(ctx context.Context, origin, entryRef, title string, skipBgm, skipTmdb bool) (*MatchResult, error) {
	result := &MatchResult{}
	var matchErrs []error

	// --- Layer 1: bangumi-data index lookup ---
	m.applyBangumiData(origin, entryRef, title, result)

	// --- Layer 2: BGM API search ---
	if result.BgmID == nil && !skipBgm && m.bgmMatcher != nil {
		bgmResult, err := m.bgmMatcher.Match(ctx, title)
		if err != nil {
			matchErr := &MatchError{Kind: ErrorProvider, Layer: "bgm", Err: err}
			matchErrs = append(matchErrs, matchErr)
			m.logf(ctx, observabilityport.RuntimeEvent{
				Message:    "bgm api search failed",
				Operation:  "bgm_search",
				ReasonCode: "bgm_search_failed",
				ErrorClass: "bgm",
			})
		} else if bgmResult != nil {
			id := bgmResult.BgmID
			result.BgmID = &id
			if bgmResult.Subject != nil {
				result.BgmTitle, result.BgmTitleCN = strings.TrimSpace(bgmResult.Subject.Name), strings.TrimSpace(bgmResult.Subject.NameCN)
			}
			result.BgmYear = bgmResult.Year
			// BGM subject eps is the sole source of total_episodes. TMDB no
			// longer decodes number_of_episodes because multi-season shows make
			// the TMDB show-level count unreliable for a single cour.
			if result.TotalEpisodes == nil && bgmResult.TotalEpisodes != nil {
				episodes := *bgmResult.TotalEpisodes
				result.TotalEpisodes = &episodes
			}
		}
	}

	// --- Layer 3: TMDB API search ---
	// bangumi-data can provide a TV/movie ID and kind, but never a season.
	// Continue to TMDB while any part of the atomic tuple is missing.
	if (result.TmdbWorkID == nil || result.TmdbKind == nil || result.TmdbSeason == nil) && !skipTmdb && m.tmdbMatcher != nil {
		// If we have a BGM subject with a Japanese title, try it as TMDB query.
		tmdbQuery := title
		tmdbResult, err := m.tmdbMatcher.Match(ctx, tmdbQuery)
		if err != nil {
			var matchErr *MatchError
			if !errors.As(err, &matchErr) {
				matchErr = &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: err}
			}
			matchErrs = append(matchErrs, matchErr)
			m.logf(ctx, observabilityport.RuntimeEvent{
				Message:    "tmdb api search failed",
				Operation:  "tmdb_search",
				ReasonCode: "tmdb_search_failed",
				ErrorClass: "tmdb",
			})
		} else if tmdbResult != nil && tmdbResult.Confidence >= tmdbMinConfidence {
			// A title-search result must be applied as one atomic TMDB tuple. Do
			// not combine a bangumi-data work ID with another work's season.
			if result.TmdbWorkID == nil {
				workID := tmdbResult.WorkID
				result.TmdbWorkID = &workID
				kind := tmdbResult.MediaKind
				result.TmdbKind = &kind
				season := tmdbResult.Season
				result.TmdbSeason = &season
				result.TmdbTitleCN, result.TmdbOriginalTitle = tmdbResult.TitleCN, tmdbResult.OriginalTitle
				result.TmdbOverview = tmdbResult.Overview
				result.TmdbYear = tmdbResult.Year
			} else if result.TmdbKind != nil && *result.TmdbKind == tmdbResult.MediaKind && *result.TmdbWorkID == tmdbResult.WorkID {
				season := tmdbResult.Season
				result.TmdbSeason = &season
				result.TmdbYear = tmdbResult.Year
			}
		}
	}

	result.setStatus()
	if len(matchErrs) > 0 {
		return result, errors.Join(matchErrs...)
	}
	return result, nil
}

// applyBangumiData fills in any missing IDs from the bangumi-data static index.
func (m *Matcher) applyBangumiData(origin, entryRef, title string, result *MatchResult) {
	if m.bangumiIndex == nil {
		return
	}

	var item *DataItem
	if origin == "mikan" && entryRef != "" {
		item = m.bangumiIndex.LookupByMikanID(entryRef)
	}
	if item == nil && title != "" {
		item = m.bangumiIndex.LookupByTitle(title)
	}
	if item == nil {
		return
	}

	if result.BgmID == nil && item.BgmID != "" {
		id := item.BgmID
		result.BgmID = &id
	}
	if result.TmdbWorkID == nil && item.TmdbID != "" {
		// Parse "tv/85937" or "movie/12345". bangumi-data does not carry
		// season info, so leave TV bindings incomplete until TMDB details resolve it.
		kind, id := parseTmdbRef(item.TmdbID)
		if kind != "" && id != "" {
			result.TmdbWorkID = &id
			result.TmdbKind = &kind
			if kind == "movie" {
				season := 0
				result.TmdbSeason = &season
			}
		}
	}
}

// parseTmdbRef splits a bangumi-data reference such as "tv/85937" into
// the normalized media kind and the numeric database ID stored in work_id.
func parseTmdbRef(ref string) (kind, workID string) {
	for _, prefix := range []string{"tv/", "movie/"} {
		if len(ref) > len(prefix) && strings.HasPrefix(ref, prefix) {
			id := strings.TrimSpace(strings.TrimPrefix(ref, prefix))
			if id != "" {
				return strings.TrimSuffix(prefix, "/"), id
			}
		}
	}
	return "", ""
}
