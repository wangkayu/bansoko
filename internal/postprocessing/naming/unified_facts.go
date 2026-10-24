package naming

import (
	"strconv"
	"strings"

	"bansoko/internal/templatefacts"
)

// UnifiedFacts projects a resolved naming context onto the unified fact set
// the naming language renders. It reads only values the context already
// carries (the frozen facts plus the resolver outputs).
func UnifiedFacts(ctx NamingContext) templatefacts.FactSet {
	title, titleSource := templatefacts.EffectiveTitle(templatefacts.TitleCandidates{
		Override: ctx.Anime.TitleOverride, Provider: ctx.Anime.ProviderTitle,
		TmdbCN: ctx.Anime.TMDB.TitleCN, BgmCN: ctx.Anime.BGM.TitleCN,
		Bgm: ctx.Anime.BGM.Title, Base: ctx.Anime.Title,
	})
	// anime.season is the owner-edited season only (0 = specials); the TMDB
	// binding's season is tmdb.season and never stands in for it.
	season := OwnerSeason(ctx.Anime.Season)
	// anime.year is the owner-edited year only; no provider year stands in.
	yearValue := ""
	if ctx.Anime.Year != nil && *ctx.Anime.Year >= templatefacts.MinYear && *ctx.Anime.Year <= templatefacts.MaxYear {
		yearValue = strconv.Itoa(*ctx.Anime.Year)
	}
	episode := ctx.Parsed.Episode
	resolved := strings.TrimSpace(episode.Display) != "" || strings.TrimSpace(episode.Raw) != ""
	ep := templatefacts.EpFacts{Raw: episode.Raw, Display: episode.Display, Offset: templatefacts.Int64(int64(episode.Offset)), Kind: episode.SpecialKind}
	if resolved {
		ep.Number = episode.Display
		if frozen := ctx.Release.FrozenEpisode; frozen != nil && strings.TrimSpace(frozen.Number) != "" {
			ep.Number = frozen.Number
			ep.NumberEnd = frozen.NumberEnd
			if frozen.Source != "" {
				ep.Source = frozen.Source
			}
		}
	}
	subtitle := ctx.Parsed.Subtitle
	// The source group identity joins the anibt root's tag values (an AniBT
	// release is both); the mikan root carries only that identity.
	anibtGroup, mikanGroup := templatefacts.SourceGroupIdentity{
		Provider: string(ctx.Source.Group.Provider),
		GroupRef: ctx.Source.Group.ExternalID, GroupName: strings.TrimSpace(ctx.Source.GroupName),
	}.Values()
	anibt := templatefacts.Values(nil)
	if len(ctx.AniBTTags) > 0 || len(anibtGroup) > 0 {
		anibt = make(templatefacts.Values, len(ctx.AniBTTags)+len(anibtGroup))
		for key, value := range ctx.AniBTTags {
			anibt[key] = value
		}
		for key, value := range anibtGroup {
			anibt[key] = value
		}
	}
	return templatefacts.FactSet{
		Anime: templatefacts.AnimeFacts{
			ID: ctx.Anime.ID, Title: title, TitleSource: titleSource, TitleOverride: ctx.Anime.TitleOverride, ProviderTitle: ctx.Anime.ProviderTitle, TitleBase: ctx.Anime.Title,
			Group: strings.TrimSpace(ctx.Source.GroupName),
			Kind: ctx.Derived.EffectiveMediaKind, Season: season, SeasonFolder: SeasonFolderFact(ctx.Derived.EffectiveMediaKind, season),
			Year: yearValue,
		},
		Bgm: templatefacts.BgmFacts{ID: ctx.Anime.BGM.WorkID, Title: ctx.Anime.BGM.Title, TitleCN: ctx.Anime.BGM.TitleCN, Year: ctx.Anime.BGM.Year, EpTitle: ctx.EpisodeTitles.BGM},
		Tmdb: templatefacts.TmdbFacts{
			ID: ctx.Anime.TMDB.WorkID, TitleCN: ctx.Anime.TMDB.TitleCN, OriginalTitle: ctx.Anime.TMDB.OriginalTitle,
			Kind: ctx.Anime.TMDB.MediaKind, Season: legacyTMDBSeason(ctx.Anime.TMDB), Year: ctx.Anime.TMDB.Year, EpTitle: ctx.EpisodeTitles.TMDB,
		},
		Torrent: templatefacts.TorrentFacts{
			Size: ctx.Torrent.Size, FileCount: ctx.Torrent.FileCount, HashV1: ctx.Torrent.HashV1, HashV2: ctx.Torrent.HashV2,
		},
		Release: templatefacts.ReleaseFacts{
			ID: ctx.Release.ID, Title: ctx.Release.OriginalTitle, PublishedAt: ctx.Release.PublishedAt,
			SubtitleMode: ctx.Release.SubtitleMode,
			Lang:         ctx.Parsed.Language.Code, Langs: ctx.Parsed.Language.Languages, LangTag: ctx.Parsed.Language.BCP47,
		},
		Ep:  ep,
		Sub: subtitleLanguage(ctx),
		File: templatefacts.FileFacts{
			Name: ctx.File.OriginalName, Stem: ctx.File.Basename, Ext: ctx.File.Extension, Size: templatefacts.Int64(ctx.File.SizeBytes), Kind: ctx.File.Kind,
			Resolution: ctx.File.Media.Resolution, VideoCodec: ctx.File.Media.VideoCodec, AudioCodec: ctx.File.Media.AudioCodec, Container: ctx.File.Media.Container,
			Sub: templatefacts.SubtitleFacts{
				Lang: subtitle.PrimaryLanguage, Langs: subtitle.Languages, Format: subtitle.Format, Suffix: subtitle.OriginalSuffix,
				Forced: subtitle.Flags.Forced, SDH: subtitle.Flags.SDH, Commentary: subtitle.Flags.Commentary, Default: subtitle.Flags.Default,
			},
		},
		Info:  templatefacts.InfoFacts{HDR: ctx.File.Media.HDR},
		Rt:    ctx.Rt,
		Anibt: anibt,
		Mikan: mikanGroup,
		Vars:  ctx.Vars,
	}
}

// SubtitleLanguageFacts is the system subtitle language (sub.*) of a resolved
// naming context, the values the sub.* facts render.
func SubtitleLanguageFacts(ctx NamingContext) templatefacts.SubFacts { return subtitleLanguage(ctx) }

// subtitleLanguage is the system subtitle language (sub.*) of a subtitle
// file, resolved like the episode: the file's own tag first, normalized with
// the reltitle sub_lang vocabulary, else the release title's rt.sub_lang.
// A non-subtitle file, or a subtitle neither names, has none.
func subtitleLanguage(ctx NamingContext) templatefacts.SubFacts {
	if !strings.EqualFold(strings.TrimSpace(ctx.File.Kind), "subtitle") {
		return templatefacts.SubFacts{}
	}
	codes, source := ctx.Parsed.Subtitle.TagLanguages, "file"
	if len(codes) == 0 {
		release, _ := ctx.Rt.Get("SubLang")
		codes, source = templatefacts.SplitValues(release), "release"
	}
	lang := templatefacts.JoinValues(codes)
	if lang == "" {
		return templatefacts.SubFacts{}
	}
	langs := templatefacts.SplitValues(lang)
	subFlag, _ := ctx.Rt.Get("SubFlag")
	rtSubLang, _ := ctx.Rt.Get("SubLang")
	anibtSubtitle, _ := ctx.AniBTTags.Get("Subtitle")
	return templatefacts.SubFacts{
		Lang: lang, Langs: langs, LangTag: templatefacts.SubtitleLanguageTag(langs), LangSource: source,
		LangCode: subtitleLangCode(langs),
		LangName: subtitleLangName(langs, subFlag, rtSubLang, anibtSubtitle),
	}
}
