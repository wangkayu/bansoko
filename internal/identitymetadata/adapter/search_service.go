package adapter

import (
	"context"
	"errors"
	"strings"

	"bansoko/internal/episodetitle"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/metadata/matcher"
)

// SearchService adapts ConfiguredCandidateSearcher to the application boundary
// by exposing normalized identity-metadata candidate types.
type SearchService struct {
	searcher ConfiguredCandidateSearcher
	titles   WorkTitleRecorder
	matcher  *matcher.Matcher
	episodes EpisodeListLookup
}

// EpisodeListLookup reads provider episode lists for candidate browsing.
// *episodetitle.Lookup implements it.
type EpisodeListLookup interface {
	Season(ctx context.Context, key episodetitle.Key, bypassCache bool) (episodetitle.List, error)
	SectionedEpisodes(ctx context.Context, key episodetitle.Key, bypassCache bool) (episodetitle.SectionedList, error)
}

// SetEpisodeListLookup enables read-only candidate episode browsing. The
// lookup is the in-process cache shared with no naming render: browsing never
// writes the persistent episode-title cache.
func (s *SearchService) SetEpisodeListLookup(lookup EpisodeListLookup) { s.episodes = lookup }

// WorkTitleRecorder records the native titles a search observed.
type WorkTitleRecorder interface {
	RecordWorkTitles(context.Context, []identitymetadatadomain.WorkTitles) error
}

// SetTitleRecorder makes every successful search record its candidates'
// native titles, so an acceptance can freeze the titles of a bound work.
func (s *SearchService) SetTitleRecorder(recorder WorkTitleRecorder) { s.titles = recorder }

// SetMatcher enables the read-only automatic candidate search without giving
// the dialog any subscription write capability.
func (s *SearchService) SetMatcher(metadataMatcher *matcher.Matcher) { s.matcher = metadataMatcher }

// NewSearchService wires the configured connection source and secret resolver
// into a metadata search service. A nil secret resolver is acceptable when
// only Bangumi (public) searches are used.
func NewSearchService(connections ConnectionSource, secrets SecretResolver, clientResolver ...HTTPClientResolver) *SearchService {
	var resolver HTTPClientResolver
	if len(clientResolver) > 0 {
		resolver = clientResolver[0]
	}
	return &SearchService{
		searcher: ConfiguredCandidateSearcher{
			Connections:    connections,
			Secrets:        secrets,
			ClientResolver: resolver,
		},
	}
}

func (s *SearchService) SearchMetadata(ctx context.Context, provider, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error) {
	candidates, err := s.searcher.Search(ctx, provider, query, limit)
	s.recordTitles(ctx, candidates, err)
	return candidates, err
}

// SearchMetadataAutomatically runs the same matcher as automatic retries but
// only projects the selected provider into a candidate. No binding is applied.
func (s *SearchService) SearchMetadataAutomatically(ctx context.Context, input identitymetadatadomain.AutomaticSearchInput) ([]identitymetadatadomain.SearchCandidate, error) {
	if s.matcher == nil {
		return nil, errors.New("metadata automatic search is not configured")
	}
	result, err := s.matcher.MatchRequest(ctx, matcher.MatchRequest{
		Origin: input.Origin, EntryRef: input.EntryRef, Title: input.Title,
		BgmID: input.BgmID, TmdbWorkID: input.TmdbWorkID, TmdbKind: input.TmdbKind, TmdbSeason: input.TmdbSeason,
		LoadTmdbDetails: input.Provider == "tmdb",
		SkipTmdb:        input.Provider == "bgm",
	})
	candidate := automaticSearchCandidate(input, result)
	if candidate == nil {
		if err != nil {
			return nil, err
		}
		return []identitymetadatadomain.SearchCandidate{}, nil
	}
	if err != nil {
		return nil, err
	}
	candidates := []identitymetadatadomain.SearchCandidate{*candidate}
	s.recordTitles(ctx, candidates, nil)
	return candidates, nil
}

// TMDBCandidateEpisodes lists one season of an arbitrary TMDB TV work. It is
// independent of any anime binding and read-only; an unknown season is empty.
func (s *SearchService) TMDBCandidateEpisodes(ctx context.Context, workID string, season int) ([]identitymetadatadomain.CandidateEpisode, error) {
	if s.episodes == nil {
		return nil, errors.New("candidate episode lookup is not configured")
	}
	list, err := s.episodes.Season(ctx, episodetitle.Key{
		Source: episodetitle.SourceTMDB, WorkID: strings.TrimSpace(workID), Season: season,
	}, false)
	if err != nil {
		return nil, err
	}
	episodes := make([]identitymetadatadomain.CandidateEpisode, 0, len(list.Episodes))
	for _, episode := range list.Episodes {
		episodes = append(episodes, identitymetadatadomain.CandidateEpisode{
			Number: episode.Number, Title: episode.Title, AirDate: episode.AirDate,
		})
	}
	return episodes, nil
}

// BGMCandidateEpisodes lists a Bangumi subject's main episodes and specials.
// Bangumi has no season of its own, so the whole subject is the lookup scope.
func (s *SearchService) BGMCandidateEpisodes(ctx context.Context, workID string) ([]identitymetadatadomain.CandidateEpisode, error) {
	if s.episodes == nil {
		return nil, errors.New("candidate episode lookup is not configured")
	}
	list, err := s.episodes.SectionedEpisodes(ctx, episodetitle.Key{
		Source: episodetitle.SourceBGM, WorkID: strings.TrimSpace(workID),
	}, false)
	if err != nil {
		return nil, err
	}
	episodes := make([]identitymetadatadomain.CandidateEpisode, 0, len(list.Episodes))
	for _, episode := range list.Episodes {
		episodes = append(episodes, identitymetadatadomain.CandidateEpisode{
			Number: episode.Number, Title: episode.Title, AirDate: episode.AirDate, Section: string(episode.Section),
		})
	}
	return episodes, nil
}

func automaticSearchCandidate(input identitymetadatadomain.AutomaticSearchInput, result *matcher.MatchResult) *identitymetadatadomain.SearchCandidate {
	if result == nil {
		return nil
	}
	var candidate identitymetadatadomain.SearchCandidate
	switch input.Provider {
	case "bgm":
		if result.BgmID == nil {
			return nil
		}
		title := strings.TrimSpace(result.BgmTitleCN)
		if title == "" {
			title = strings.TrimSpace(result.BgmTitle)
		}
		if title == "" {
			title = strings.TrimSpace(input.Title)
		}
		year := 0
		if result.BgmYear != nil {
			year = *result.BgmYear
		}
		candidate = identitymetadatadomain.SearchCandidate{
			Provider: "bgm", ProviderWorkID: *result.BgmID, Title: title,
			OriginalTitle: result.BgmTitle, PosterURL: result.BgmPosterURL,
			MediaKind: "tv", DetailsStatus: identitymetadatadomain.DetailsStatusNotApplicable,
			Year: candidateYear(year), Score: 1,
		}
	case "tmdb":
		if result.TmdbWorkID == nil || result.TmdbKind == nil || result.TmdbSeason == nil {
			return nil
		}
		title := strings.TrimSpace(result.TmdbTitleCN)
		if title == "" {
			title = strings.TrimSpace(result.TmdbOriginalTitle)
		}
		if title == "" {
			title = strings.TrimSpace(input.Title)
		}
		detailsStatus := identitymetadatadomain.DetailsStatusNotApplicable
		if *result.TmdbKind == "tv" {
			detailsStatus = identitymetadatadomain.DetailsStatusAvailable
			if len(result.TmdbSeasons) == 0 {
				detailsStatus = identitymetadatadomain.DetailsStatusEmpty
			}
		}
		year := 0
		if result.TmdbYear != nil {
			year = *result.TmdbYear
		}
		candidate = identitymetadatadomain.SearchCandidate{
			Provider: "tmdb", ProviderWorkID: *result.TmdbWorkID, Title: title,
			OriginalTitle: result.TmdbOriginalTitle, Overview: result.TmdbOverview,
			MediaKind: *result.TmdbKind, DetailsStatus: detailsStatus,
			Seasons: result.TmdbSeasons, SuggestedSeason: result.TmdbSeason,
			Year: candidateYear(year), Score: 1,
		}
	default:
		return nil
	}
	titles := result.WorkTitles(input.Provider == "bgm", input.Provider == "tmdb")
	if len(titles) == 1 {
		candidate.Native = titles[0]
	}
	return &candidate
}

func (s *SearchService) recordTitles(ctx context.Context, candidates []identitymetadatadomain.SearchCandidate, searchErr error) {
	if searchErr != nil || s.titles == nil {
		return
	}
	titles := make([]identitymetadatadomain.WorkTitles, 0, len(candidates))
	for _, candidate := range candidates {
		titles = append(titles, candidate.Native)
	}
	// Recording is best effort: a search result never fails because its
	// titles could not be stored; the work's titles then stay unknown.
	_ = s.titles.RecordWorkTitles(ctx, titles)
}
