package filemanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/episodetitle"
	"bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/postprocessing/naming"
	"golang.org/x/sync/errgroup"
)

// RecognitionAnimeFacts reads the shared naming facts of a request.
type RecognitionAnimeFacts interface {
	LoadAnimeFacts(context.Context, string) (naming.AnimeNamingFacts, error)
	FillWorkTitles(context.Context, *naming.AnimeNamingFacts) error
}

type RecognitionEpisodeTitles interface {
	ResolveBatch(context.Context, map[episodetitle.WorkKey][]int, map[episodetitle.WorkKey]bool) (map[episodetitle.EpisodeKey]episodetitle.DetailedTitle, error)
}

// RecognitionRuntimeFacts describes only runtime work required by the plan.
type RecognitionRuntimeFacts struct {
	MediaProbe    []RecognitionMediaDiagnostic
	EpisodeTitles []RecognitionTitleDiagnostic
}

type RecognitionMediaDiagnostic struct {
	ID, Path, State, Reason string
}

type RecognitionTitleDiagnostic struct {
	ID, State, Reason string
	Work              episodetitle.WorkKey
	Episodes          []int
	RetryAfterSeconds *int
}

type recognitionRenderSpec struct {
	facts         naming.NamingContext
	references    []string
	episode       *RecognitionEpisode
	video         int
	unknown, skip bool
}

func (s *Service) recognitionAnimeFacts(ctx context.Context, match *RecognitionMatch) (naming.AnimeNamingFacts, error) {
	var facts naming.AnimeNamingFacts
	if id := recognitionMatchID(match); id != "" {
		if s.recognitionFacts == nil {
			return facts, conflict(CodeRecognitionUnavailable, "recognition anime facts are not available")
		}
		var err error
		facts, err = s.recognitionFacts.LoadAnimeFacts(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return facts, notFound(CodeMatchAnimeUnavailable, "anime does not exist")
		}
		if err != nil {
			return facts, err
		}
	}
	if match == nil || match.Anime == nil {
		return facts, nil
	}
	draft := match.Anime
	facts.Title, facts.TitleOverride = draft.Title, draft.Title
	facts.Year = draft.Year
	facts.BGM = naming.BGMNamingFacts{}
	if draft.BGM != nil {
		facts.BGM.WorkID = strings.TrimSpace(draft.BGM.WorkID)
	}
	facts.TMDB = naming.TMDBNamingFacts{}
	if draft.TMDB != nil {
		facts.TMDB = naming.TMDBNamingFacts{WorkID: strings.TrimSpace(draft.TMDB.WorkID), MediaKind: draft.TMDB.MediaKind, Season: draft.TMDB.Season, SeasonPresent: true}
	}
	if s.recognitionFacts != nil {
		if err := s.recognitionFacts.FillWorkTitles(ctx, &facts); err != nil {
			return facts, err
		}
	}
	return facts, nil
}

func recognitionFileFacts(file recognitionFile, anime naming.AnimeNamingFacts, match *RecognitionMatch, episode *RecognitionEpisode, season *int) naming.NamingContext {
	name := file.inspection.detail.Entry.Name
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if season != nil {
		anime.Season = season
		if match == nil || match.Anime == nil {
			anime.TMDB.Season, anime.TMDB.SeasonPresent = *season, true
		}
	}
	facts := naming.NamingContext{
		Anime:       anime,
		AnimeAbsent: recognitionMatchID(match) == "" && (match == nil || match.Anime == nil),
		File:        naming.FileNamingFacts{OriginalName: name, Basename: stem, Extension: strings.ToLower(strings.TrimPrefix(ext, ".")), Kind: file.kind, SizeBytes: file.inspection.info.Size()},
		Release:     naming.ReleaseNamingFacts{OriginalTitle: stem},
	}
	facts.FileTitleTags = naming.ParseFileTitleTags(facts.File)
	if episode != nil {
		facts.Release.FrozenEpisode = &naming.FrozenEpisodeNamingFacts{
			Raw: episode.Raw, Number: episode.Number, NumberEnd: episode.NumberEnd, Offset: episode.Offset, Source: episode.Source,
			Display: (episodeidentity.Position{Number: episode.Number, End: episode.NumberEnd}).Display(),
		}
	}
	facts = naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: facts}).Context
	if match != nil && match.Anime != nil {
		facts.Derived.EffectiveMediaKind = match.Anime.MediaKind
	}
	return facts
}

func recognitionFactPaths(values ...[]string) []string {
	var all []string
	for _, value := range values {
		all = append(all, value...)
	}
	slices.Sort(all)
	return slices.Compact(all)
}

func recognitionSpecs(plan *recognitionPlan, rules naming.CompiledNamingRules, anime naming.AnimeNamingFacts) []recognitionRenderSpec {
	specs := make([]recognitionRenderSpec, len(plan.files))
	for _, group := range plan.groups {
		if group.video < 0 && len(group.others) == 0 {
			continue
		}
		index := group.video
		var episode *RecognitionEpisode
		season := recognitionMatchSeason(plan.request.Match)
		if index < 0 {
			index = group.others[0]
		} else {
			override := recognitionOverride(plan.request.Overrides, plan.files[index].inspection.absolute)
			episode = recognitionEpisode(plan.files[index].identity, plan.request.Match, override)
			season = recognitionSeason(plan.request.Match, override)
		}
		facts := recognitionFileFacts(plan.files[index], anime, plan.request.Match, episode, season)
		spec := recognitionRenderSpec{facts: facts, episode: episode, video: group.video}
		variant := rules.TV
		switch {
		case naming.MediaKind(facts.Derived.EffectiveMediaKind) == naming.MediaKindMovie:
			variant = rules.Movie
		case episode == nil:
			if plan.unknownRoute != "route" {
				spec.skip = true
				specs[index] = spec
				continue
			}
			spec.unknown = true
			variant = rules.Unknown
		}
		spec.references = recognitionFactPaths(variant.Directory.ReferencedFacts, variant.VideoFilename.ReferencedFacts)
		specs[index] = spec
		for _, subtitle := range group.subtitles {
			specs[subtitle] = recognitionRenderSpec{
				facts:   recognitionFileFacts(plan.files[subtitle], anime, plan.request.Match, episode, season),
				episode: episode, video: index,
			}
			if rules.SubtitleSuffix != nil {
				specs[subtitle].references = rules.SubtitleSuffix.ReferencedFacts
			}
		}
	}
	return specs
}

type recognitionMediaResult struct {
	facts      naming.MediaNamingFacts
	diagnostic RecognitionMediaDiagnostic
}

type recognitionRuntime struct {
	media    map[int]recognitionMediaResult
	titles   map[episodetitle.EpisodeKey]episodetitle.DetailedTitle
	titleIDs map[episodetitle.EpisodeKey]string
}

func recognitionCacheOnly(request RecognitionRequest) (map[episodetitle.WorkKey]bool, error) {
	if len(request.EpisodeTitleCacheOnly) > 512 || request.DisableRuntimeFacts && (request.RefreshMedia || len(request.EpisodeTitleCacheOnly) > 0) {
		return nil, invalid(CodeRecognitionInvalid, "invalid runtime facts options")
	}
	keys := make(map[episodetitle.WorkKey]bool, len(request.EpisodeTitleCacheOnly))
	for _, key := range request.EpisodeTitleCacheOnly {
		key.WorkID = strings.TrimSpace(key.WorkID)
		if !key.Valid() {
			return nil, invalid(CodeRecognitionInvalid, "invalid episode title cache key")
		}
		keys[key] = true
	}
	return keys, nil
}

func (s *Service) recognitionRuntime(ctx context.Context, plan *recognitionPlan, specs []recognitionRenderSpec, cacheOnly map[episodetitle.WorkKey]bool) (recognitionRuntime, error) {
	runtime := recognitionRuntime{media: map[int]recognitionMediaResult{}, titleIDs: map[episodetitle.EpisodeKey]string{}}
	work := map[episodetitle.WorkKey][]int{}
	needsMedia := make([]bool, len(specs))
	for _, spec := range specs {
		for _, fact := range spec.references {
			if key, ok := namingfacts.EpisodeDependency(fact, spec.facts); ok {
				work[key.WorkKey] = append(work[key.WorkKey], key.Episode)
			}
			if spec.video >= 0 && strings.HasPrefix(fact, "media.") {
				needsMedia[spec.video] = true
			}
		}
	}
	for key, episodes := range work {
		slices.Sort(episodes)
		work[key] = slices.Compact(episodes)
		if plan.request.DisableRuntimeFacts {
			cacheOnly[key] = true
		}
	}
	if len(work) > 0 {
		queries := s.recognitionTitles
		if queries == nil {
			queries = (*namingfacts.Querier)(nil)
		}
		var err error
		runtime.titles, err = queries.ResolveBatch(ctx, work, cacheOnly)
		if err != nil {
			return recognitionRuntime{}, err
		}
		for key, episodes := range work {
			for _, episode := range episodes {
				item := episodetitle.EpisodeKey{WorkKey: key, Episode: episode}
				answer, ok := runtime.titles[item]
				if !ok {
					return recognitionRuntime{}, fmt.Errorf("missing requested episode title result")
				}
				if plan.request.DisableRuntimeFacts && answer.Reason == episodetitle.ReasonCacheMiss {
					answer.Reason = "not_loaded"
					runtime.titles[item] = answer
				}
			}
		}
		runtime.indexTitles(plan)
	}
	var mediaVideos []int
	for video, needed := range needsMedia {
		if needed {
			mediaVideos = append(mediaVideos, video)
		}
	}
	if len(mediaVideos) > 0 {
		type probeOutcome struct {
			video  int
			facts  naming.MediaNamingFacts
			reason string
		}
		outcomes := make([]probeOutcome, len(mediaVideos))
		group, runCtx := errgroup.WithContext(ctx)
		group.SetLimit(4)
		for i, video := range mediaVideos {
			idx, v := i, video
			group.Go(func() error {
				file := plan.files[v]
				facts, reason, err := s.recognitionMedia.probe(runCtx, file.inspection, plan.request.RefreshMedia, plan.request.DisableRuntimeFacts)
				if err != nil {
					return err
				}
				outcomes[idx] = probeOutcome{
					video:  v,
					facts:  naming.MediaNamingFacts{Resolution: facts.Resolution, VideoCodec: facts.VideoCodec, AudioCodec: facts.AudioCodec, Container: facts.Container, HDR: facts.HDR},
					reason: reason,
				}
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return recognitionRuntime{}, err
		}
		for _, outcome := range outcomes {
			file := plan.files[outcome.video]
			diagnostic := RecognitionMediaDiagnostic{
				ID:     fmt.Sprintf("media-%d", len(plan.runtimeFacts.MediaProbe)+1),
				Path:   file.inspection.absolute,
				State:  "ready",
				Reason: outcome.reason,
			}
			if outcome.reason != "" {
				diagnostic.State = "unavailable"
			}
			plan.runtimeFacts.MediaProbe = append(plan.runtimeFacts.MediaProbe, diagnostic)
			runtime.media[outcome.video] = recognitionMediaResult{
				facts:      outcome.facts,
				diagnostic: diagnostic,
			}
		}
	}
	return runtime, ctx.Err()
}

func (r recognitionRuntime) indexTitles(plan *recognitionPlan) {
	type bucket struct {
		work          episodetitle.WorkKey
		state, reason string
		retry         time.Duration
	}
	keys := make([]episodetitle.EpisodeKey, 0, len(r.titles))
	for key := range r.titles {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b episodetitle.EpisodeKey) int {
		if a.Source != b.Source {
			return strings.Compare(string(a.Source), string(b.Source))
		}
		if a.WorkID != b.WorkID {
			return strings.Compare(a.WorkID, b.WorkID)
		}
		if a.Season != b.Season {
			if a.Season < b.Season {
				return -1
			}
			return 1
		}
		return a.Episode - b.Episode
	})
	groups := map[bucket]int{}
	for _, key := range keys {
		answer := r.titles[key]
		state := "absent"
		if answer.Reason != "" {
			state = "unavailable"
		} else if answer.Known {
			state = "known"
		}
		b := bucket{key.WorkKey, state, answer.Reason, answer.RetryAfter}
		index, ok := groups[b]
		if !ok {
			index = len(plan.runtimeFacts.EpisodeTitles)
			groups[b] = index
			diagnostic := RecognitionTitleDiagnostic{ID: fmt.Sprintf("titles-%d", index+1), Work: key.WorkKey, State: state, Reason: answer.Reason}
			if answer.RetryAfter > 0 {
				seconds := int((answer.RetryAfter-1)/time.Second + 1)
				diagnostic.RetryAfterSeconds = &seconds
			}
			plan.runtimeFacts.EpisodeTitles = append(plan.runtimeFacts.EpisodeTitles, diagnostic)
		}
		diagnostic := &plan.runtimeFacts.EpisodeTitles[index]
		diagnostic.Episodes = append(diagnostic.Episodes, key.Episode)
		r.titleIDs[key] = diagnostic.ID
	}
}

func (r recognitionRuntime) apply(spec recognitionRenderSpec) (naming.NamingContext, map[string]bool, *RecognitionBlocker) {
	facts := spec.facts
	media := r.media[spec.video]
	facts.File.Media = media.facts
	var unresolved map[string]bool
	blocker := RecognitionBlocker{Code: RecognitionBlockerRuntimeFacts}
	for _, fact := range spec.references {
		var id string
		switch {
		case strings.HasPrefix(fact, "media."):
			if media.diagnostic.Reason != "" {
				id = media.diagnostic.ID
			}
		case fact == namingfacts.BGMEpTitlePath || fact == namingfacts.TMDBEpTitlePath:
			key, ok := namingfacts.EpisodeDependency(fact, facts)
			if !ok {
				continue
			}
			answer := r.titles[key]
			if answer.Reason != "" {
				id = r.titleIDs[key]
			} else if answer.Known {
				if key.Source == episodetitle.SourceBGM {
					facts.EpisodeTitles.BGM = answer.Title.Title
				} else {
					facts.EpisodeTitles.TMDB = answer.Title.Title
				}
			}
		}
		if id != "" {
			if unresolved == nil {
				unresolved = map[string]bool{}
			}
			unresolved[fact] = true
			blocker.Facts = append(blocker.Facts, fact)
			blocker.DiagnosticIDs = append(blocker.DiagnosticIDs, id)
		}
	}
	if len(unresolved) == 0 {
		return facts, nil, nil
	}
	blocker.Facts = recognitionFactPaths(blocker.Facts)
	blocker.DiagnosticIDs = recognitionFactPaths(blocker.DiagnosticIDs)
	return facts, unresolved, &blocker
}
