package matcher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

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
	BgmPosterURL                   string
	TmdbTitleCN, TmdbOriginalTitle string
	// TmdbSeasons carries the provider details used to resolve the season so a
	// read-only interactive match can present the same real season choices.
	TmdbSeasons []identitymetadatadomain.SearchSeason
	// TmdbOverview is the work summary TMDB reported for the matched work,
	// set only by the same API layer that set the TMDB tuple. Acceptance
	// freezes it alongside the titles (tmdb.summary).
	TmdbOverview string
	// BgmYear / TmdbYear are the first air/release year the provider reported
	// for the work it bound; nil when the provider gave none (or only the
	// bangumi-data static index supplied the id). They are provider facts for
	// the accepted binding, never inferred from the title.
	BgmYear, TmdbYear *int
	bgmAirDate        string
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
	bangumiIndex atomic.Pointer[BangumiDataIndex]
	bgmMatcher   *BGMMatcher
	tmdbMatcher  *TMDBMatcher
	logger       observabilityport.RuntimeLogger
	diag         *diagnosticTracker
}

// NewMatcher builds a composite matcher. Any layer may be nil to disable it.
// A nil logger safely discards structured runtime events.
func NewMatcher(bangumiIndex *BangumiDataIndex, bgmMatcher *BGMMatcher, tmdbMatcher *TMDBMatcher, logger observabilityport.RuntimeLogger) *Matcher {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	m := &Matcher{
		bgmMatcher:  bgmMatcher,
		tmdbMatcher: tmdbMatcher,
		logger:      logger,
	}
	m.bangumiIndex.Store(bangumiIndex)
	return m
}

// ReplaceBangumiDataIndex atomically publishes a complete immutable index.
// In-flight matches keep using the index snapshot they started with.
func (m *Matcher) ReplaceBangumiDataIndex(index *BangumiDataIndex) {
	if m != nil {
		m.bangumiIndex.Store(index)
	}
}

// MatchRequest carries both provider lookup keys and already confirmed IDs.
// SkipBgm suppresses title search, not detail lookup for an existing BGM ID.
type MatchRequest struct {
	Origin, EntryRef, Title string
	BgmID                   string
	TmdbWorkID, TmdbKind    string
	TmdbSeason              *int
	// LoadTmdbDetails refreshes provider facts for a complete existing binding
	// in an interactive preview; the existing season remains the strong mapping.
	LoadTmdbDetails   bool
	SkipBgm, SkipTmdb bool
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

// Match retains the one-click matching API. Automatic retries use MatchRequest
// so an already confirmed BGM ID remains available as an index key.
func (m *Matcher) Match(ctx context.Context, origin, entryRef, title string, skipBgm, skipTmdb bool) (*MatchResult, error) {
	return m.MatchRequest(ctx, MatchRequest{Origin: origin, EntryRef: entryRef, Title: title, SkipBgm: skipBgm, SkipTmdb: skipTmdb})
}

// MatchRequest runs one match against a single immutable index snapshot.
func (m *Matcher) MatchRequest(ctx context.Context, request MatchRequest) (*MatchResult, error) {
	result := &MatchResult{}
	if id := strings.TrimSpace(request.BgmID); id != "" {
		result.BgmID = &id
	}
	if id := strings.TrimSpace(request.TmdbWorkID); id != "" {
		result.TmdbWorkID = &id
	}
	if kind := strings.TrimSpace(request.TmdbKind); kind != "" {
		result.TmdbKind = &kind
	}
	result.TmdbSeason = request.TmdbSeason

	var matchErrs []error
	var bgmDetailErr error
	index := m.bangumiIndex.Load()
	item := m.applyBangumiData(index, request, result)

	// A BGM ID alone is not enough: actual matching still needs the subject's
	// title, episode count and air date. Existing IDs therefore fetch details
	// directly instead of repeating a title search.
	if result.BgmID != nil && m.bgmMatcher != nil {
		bgmResult, err := m.bgmMatcher.MatchByID(ctx, *result.BgmID)
		m.observe(ctx, sourceBGM, err)
		if err != nil {
			bgmDetailErr = &MatchError{Kind: ErrorProvider, Layer: "bgm", Err: err}
			m.logf(ctx, observabilityport.RuntimeEvent{Message: "bgm subject detail failed", Operation: "bgm_detail", ReasonCode: "bgm_detail_failed", ErrorClass: "bgm"})
		} else {
			applyBGMResult(result, bgmResult)
		}
	} else if result.BgmID == nil && !request.SkipBgm && m.bgmMatcher != nil {
		bgmResult, err := m.bgmMatcher.Match(ctx, request.Title)
		m.observe(ctx, sourceBGM, err)
		if err != nil {
			matchErrs = append(matchErrs, &MatchError{Kind: ErrorProvider, Layer: "bgm", Err: err})
			m.logf(ctx, observabilityport.RuntimeEvent{Message: "bgm api search failed", Operation: "bgm_search", ReasonCode: "bgm_search_failed", ErrorClass: "bgm"})
		} else if bgmResult != nil {
			applyBGMResult(result, bgmResult)
			if item == nil && index != nil {
				item = index.LookupByBgmID(bgmResult.BgmID)
				applyDataItem(item, result)
			}
		}
	}

	targetAirDate := ""
	if result.BgmID != nil && m.bgmMatcher != nil {
		// applyBGMResult does not retain the full subject in the public result;
		// the season matcher receives the date through this private lookup below.
		targetAirDate = result.bgmAirDate
	}
	if targetAirDate == "" && item != nil {
		targetAirDate = item.Begin
	}

	tmdbIncomplete := result.TmdbWorkID == nil || result.TmdbKind == nil || result.TmdbSeason == nil
	if (tmdbIncomplete || request.LoadTmdbDetails) && !request.SkipTmdb && m.tmdbMatcher != nil {
		var tmdbResult *TMDBMatchResult
		var err error
		if result.TmdbWorkID != nil && result.TmdbKind != nil {
			mappedSeason := request.TmdbSeason
			if item != nil && mappedSeason == nil {
				refKind, refID, refSeason := parseTmdbRef(item.TmdbID)
				if refKind == *result.TmdbKind && refID == *result.TmdbWorkID {
					mappedSeason = refSeason
				}
			}
			tmdbResult, err = m.tmdbMatcher.MatchReference(ctx, request.Title, *result.TmdbKind, *result.TmdbWorkID, mappedSeason, targetAirDate)
		} else {
			tmdbResult, err = m.tmdbMatcher.MatchWithTargetDate(ctx, request.Title, targetAirDate)
		}
		m.observe(ctx, sourceTMDB, err)
		if err != nil {
			var matchErr *MatchError
			if !errors.As(err, &matchErr) {
				matchErr = &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: err}
			}
			matchErrs = append(matchErrs, matchErr)
			m.logf(ctx, observabilityport.RuntimeEvent{Message: "tmdb api match failed", Operation: "tmdb_match", ReasonCode: "tmdb_match_failed", ErrorClass: "tmdb"})
		} else if tmdbResult != nil && tmdbResult.Confidence >= tmdbMinConfidence {
			applyTMDBResult(result, tmdbResult)
		}
	}

	result.setStatus()
	if bgmDetailErr != nil && !result.IsComplete() {
		// A completed TMDB tuple remains usable when the supplemental BGM
		// detail request fails. Otherwise keep the failure retryable instead of
		// silently accepting a season without its date clue.
		matchErrs = append(matchErrs, bgmDetailErr)
	}
	if len(matchErrs) > 0 {
		return result, errors.Join(matchErrs...)
	}
	return result, nil
}

func (m *Matcher) applyBangumiData(index *BangumiDataIndex, request MatchRequest, result *MatchResult) *DataItem {
	if index == nil {
		return nil
	}
	var item *DataItem
	if result.BgmID != nil {
		item = index.LookupByBgmID(*result.BgmID)
	}
	if item == nil && request.Origin == "mikan" && request.EntryRef != "" {
		item = index.LookupByMikanID(request.EntryRef)
	}
	if item == nil && request.Title != "" {
		item = index.LookupByTitle(request.Title)
	}
	if item != nil && result.BgmID != nil && item.BgmID != "" && item.BgmID != *result.BgmID {
		// A title/Mikan hit for another BGM subject must not lend its TMDB
		// identity to the already confirmed BGM binding.
		return nil
	}
	applyDataItem(item, result)
	return item
}

func applyDataItem(item *DataItem, result *MatchResult) {
	if item == nil {
		return
	}
	if result.BgmID == nil && item.BgmID != "" {
		id := item.BgmID
		result.BgmID = &id
	}
	if result.TmdbWorkID == nil && item.TmdbID != "" {
		kind, id, _ := parseTmdbRef(item.TmdbID)
		if kind != "" && id != "" {
			result.TmdbWorkID = &id
			result.TmdbKind = &kind
			if kind == "movie" {
				zero := 0
				result.TmdbSeason = &zero
			}
		}
	}
}

func applyBGMResult(result *MatchResult, bgmResult *BGMMatchResult) {
	if bgmResult == nil {
		return
	}
	id := bgmResult.BgmID
	result.BgmID = &id
	if bgmResult.Subject != nil {
		result.BgmTitle = strings.TrimSpace(bgmResult.Subject.Name)
		result.BgmTitleCN = strings.TrimSpace(bgmResult.Subject.NameCN)
		result.BgmPosterURL = strings.TrimSpace(bgmResult.Subject.Images.Common)
		result.bgmAirDate = strings.TrimSpace(bgmResult.Subject.AirDate)
	}
	result.BgmYear = bgmResult.Year
	if result.TotalEpisodes == nil && bgmResult.TotalEpisodes != nil {
		episodes := *bgmResult.TotalEpisodes
		result.TotalEpisodes = &episodes
	}
}

func applyTMDBResult(result *MatchResult, matched *TMDBMatchResult) {
	workID, kind, season := matched.WorkID, matched.MediaKind, matched.Season
	result.TmdbWorkID, result.TmdbKind, result.TmdbSeason = &workID, &kind, &season
	result.TmdbTitleCN, result.TmdbOriginalTitle = matched.TitleCN, matched.OriginalTitle
	result.TmdbSeasons = append([]identitymetadatadomain.SearchSeason(nil), matched.Seasons...)
	result.TmdbOverview, result.TmdbYear = matched.Overview, matched.Year
}

// parseTmdbRef accepts only numeric TMDB work references and optional TV
// season/episode suffixes. The episode component is deliberately not an offset.
func parseTmdbRef(ref string) (kind, workID string, season *int) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(ref), "/"), "/")
	if len(parts) != 2 && len(parts) != 4 && len(parts) != 6 {
		return "", "", nil
	}
	if parts[0] != "tv" && parts[0] != "movie" || !numericID(parts[1]) {
		return "", "", nil
	}
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	if parts[0] != "tv" || parts[2] != "season" || !numericID(parts[3]) {
		return "", "", nil
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 0 {
		return "", "", nil
	}
	if len(parts) == 6 && (parts[4] != "episode" || !numericID(parts[5])) {
		return "", "", nil
	}
	return "tv", parts[1], &n
}

func numericID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
