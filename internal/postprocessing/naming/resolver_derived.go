package naming

import (
	"fmt"
	"strings"

	"bansoko/internal/templatefacts"
)

// derivedValue payloads. Each derived resolver owns exactly one effective
// presentation value ()
type effectiveTitleValue struct {
	EffectiveTitle       string
	EffectiveTitleSource string
}

func (v effectiveTitleValue) String() string {
	return fmt.Sprintf("%q (%s)", v.EffectiveTitle, v.EffectiveTitleSource)
}

type seasonFolderValue struct{ SeasonFolder string }

func (v seasonFolderValue) String() string { return v.SeasonFolder }

type mediaKindValue struct{ EffectiveMediaKind string }

func (v mediaKindValue) String() string { return v.EffectiveMediaKind }

const (
	effectiveTitleResolverKey     = "derived.effective_title"
	seasonFolderResolverKey       = "derived.season_folder"
	effectiveMediaKindResolverKey = "derived.effective_media_kind"
)

// effectiveTitleResolver owns the one presentation title chain. The order
// itself lives in templatefacts.EffectiveTitle; this resolver only projects it
// onto Derived and reports the winning source.
type effectiveTitleResolver struct{}

func (effectiveTitleResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:        effectiveTitleResolverKey,
		OutputType: "object",
		Nullable:   true,
	}
}

func (effectiveTitleResolver) Resolve(input ResolverInput) ResolverResult {
	result := ResolverResult{Key: effectiveTitleResolverKey, State: ResolverStateNull}
	anime := input.Facts.Anime
	title, source := templatefacts.EffectiveTitle(templatefacts.TitleCandidates{
		Override: anime.TitleOverride, Provider: anime.ProviderTitle,
		TmdbCN: anime.TMDB.TitleCN, BgmCN: anime.BGM.TitleCN,
		Bgm: anime.BGM.Title, Base: anime.Title,
	})
	if title == "" {
		result.Diagnostics = append(result.Diagnostics, NamingDiagnostic{
			Code: "effective_title_missing", Message: "no override, provider, TMDB or BGM title fact is available", Path: "Anime.Title"})
		return result
	}
	result.State = ResolverStateValue
	result.Value = effectiveTitleValue{EffectiveTitle: title, EffectiveTitleSource: source}
	result.Evidence = append(result.Evidence, derivedEvidence(effectiveTitleResolverKey, source, effectiveTitleInputPath(source)))
	return result
}

// effectiveTitleInputPath names the frozen fact the winning title source read.
func effectiveTitleInputPath(source string) string {
	switch source {
	case templatefacts.TitleSourceOverride:
		return "Anime.TitleOverride"
	case templatefacts.TitleSourceProvider:
		return "Anime.ProviderTitle"
	case templatefacts.TitleSourceTmdbCN:
		return "Anime.TMDB.TitleCN"
	case templatefacts.TitleSourceBgmCN:
		return "Anime.BGM.TitleCN"
	case templatefacts.TitleSourceBgm:
		return "Anime.BGM.Title"
	default:
		return "Anime.Title"
	}
}

// seasonFolderResolver renders the TMDB season folder; season 0/unknown stays
// null so templates can apply their own default.
type seasonFolderResolver struct{}

func (seasonFolderResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:        seasonFolderResolverKey,
		OutputType: "string",
		Nullable:   true,
	}
}

func (seasonFolderResolver) Resolve(input ResolverInput) ResolverResult {
	result := ResolverResult{Key: seasonFolderResolverKey, State: ResolverStateNull}
	if input.Facts.Anime.TMDB.SeasonPresent || input.Facts.Anime.TMDB.Season > 0 {
		result.State = ResolverStateValue
		result.Value = seasonFolderValue{SeasonFolder: fmt.Sprintf("Season %02d", input.Facts.Anime.TMDB.Season)}
		result.Evidence = append(result.Evidence, derivedEvidence(seasonFolderResolverKey, "tmdb_season", "Anime.TMDB.Season"))
	}
	return result
}

// effectiveMediaKindResolver selects the TV/movie naming branch. A TMDB movie
// binding or a movie-classified file wins; the default is tv.
type effectiveMediaKindResolver struct{}

func (effectiveMediaKindResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:        effectiveMediaKindResolverKey,
		OutputType: "string",
		Nullable:   false,
	}
}

func (effectiveMediaKindResolver) Resolve(input ResolverInput) ResolverResult {
	kind := "tv"
	ruleID := "default_tv"
	inputPath := "Derived"
	if strings.EqualFold(strings.TrimSpace(input.Facts.Anime.TMDB.MediaKind), "movie") {
		kind = "movie"
		ruleID = "tmdb_media_kind_movie"
		inputPath = "Anime.TMDB.MediaKind"
	} else if strings.EqualFold(strings.TrimSpace(input.Facts.File.Kind), "movie") {
		kind = "movie"
		ruleID = "file_kind_movie"
		inputPath = "File.Kind"
	}
	return ResolverResult{
		Key:      effectiveMediaKindResolverKey,
		State:    ResolverStateValue,
		Value:    mediaKindValue{EffectiveMediaKind: kind},
		Evidence: []ResolverEvidence{derivedEvidence(effectiveMediaKindResolverKey, ruleID, inputPath)},
	}
}

func derivedEvidence(resolverKey, ruleID, inputPath string) ResolverEvidence {
	return ResolverEvidence{
		ResolverKey: resolverKey,
		RuleID:      ruleID,
		RuleIndex:   0,
		InputPath:   inputPath,
	}
}
