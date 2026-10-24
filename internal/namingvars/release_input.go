package namingvars

import (
	"strconv"
	"strings"

	"bansoko/internal/templatefacts"
)

// ReleaseEvidence is the release-level evidence one Vars call site supplies.
// Every call site (accept, RSS list preview, backfill, one-shot, forced
// backfill, read-model preview) builds its Input through BuildInput, so two
// sites holding the same evidence evaluate the same input. A blank string or
// nil pointer is a value the site does not have: it reaches the program as
// null, never as a substitute.
type ReleaseEvidence struct {
	AnimeEvidence

	ReleaseTitle string
	Files        []File
	ReleaseID    string

	// Episode is the frozen release episode identity; nil when unresolved.
	Episode *EpisodeEvidence

	HashV1, HashV2 string

	// Tags are the release's frozen rt / anibt value namespaces.
	Tags templatefacts.Facts
}

// AnimeEvidence is the subscribed anime and its metadata binding: the base
// title stored on the record, the title override, the provider catalog title
// frozen at subscribe, the bound work ids and the bound works' recorded titles.
type AnimeEvidence struct {
	AnimeID    string
	AnimeTitle string // the managed anime's stored base title (anime.title_base)
	// AnimeTitleOverride is the subscription's title override
	// (anime.title_override); empty when none was set.
	AnimeTitleOverride string
	// AnimeProviderTitle is the provider catalog title frozen at subscribe
	// (anime.provider_title); empty for custom/oneshot or an unobserved entry.
	AnimeProviderTitle string
	// AnimeYear is the owner-edited year (anime_metadata.year, or the
	// draft's metadata.year); nil when unset. It is anime.year as is.
	AnimeYear *int64
	// AnimeSeason is the owner-edited season (anime_metadata.season, or the
	// draft's metadata.season; 0 = specials); nil when unset. It is
	// anime.season as is, never the TMDB season.
	AnimeSeason *int64

	BgmID, BgmTitle, BgmTitleCN string
	// BgmYear / TmdbYear are the bound works' provider-observed first
	// air/release years; nil when the provider gave none or the work was never
	// observed. They are evidence, never inferred from a title.
	BgmYear *int64

	TmdbID, TmdbKind, TmdbTitleCN, TmdbOriginalTitle string
	TmdbSeason                                       *int64
	TmdbYear                                         *int64
}

// EpisodeEvidence is one frozen release episode identity.
type EpisodeEvidence struct {
	Raw, Number, Display string
	Offset               int64
}

// BuildInput is the one release-level Vars input builder. The files keep
// their order; Facts is the fact object (templatefacts VarsInputObject).
func BuildInput(e ReleaseEvidence) Input {
	files := append([]File{}, e.Files...)
	fs := templatefacts.FactSet{
		Anime:   templatefacts.AnimeFacts{ID: e.AnimeID, TitleOverride: e.AnimeTitleOverride, ProviderTitle: e.AnimeProviderTitle, TitleBase: e.AnimeTitle},
		Bgm:     templatefacts.BgmFacts{ID: e.BgmID, Title: e.BgmTitle, TitleCN: e.BgmTitleCN, Year: intPtr(e.BgmYear)},
		Tmdb:    templatefacts.TmdbFacts{ID: e.TmdbID, TitleCN: e.TmdbTitleCN, OriginalTitle: e.TmdbOriginalTitle, Kind: e.TmdbKind, Season: e.TmdbSeason, Year: intPtr(e.TmdbYear)},
		Release: templatefacts.ReleaseFacts{ID: e.ReleaseID, Title: e.ReleaseTitle},
		Torrent: templatefacts.TorrentFacts{HashV1: strings.TrimSpace(e.HashV1), HashV2: strings.TrimSpace(e.HashV2)},
		Rt:      e.Tags.Reltitle,
		Anibt:   e.Tags.AniBTTags,
	}
	// The same title and year orders naming applies (naming.UnifiedFacts).
	fs.Anime.Title, _ = templatefacts.EffectiveTitle(templatefacts.TitleCandidates{
		Override: e.AnimeTitleOverride, Provider: e.AnimeProviderTitle,
		TmdbCN: e.TmdbTitleCN, BgmCN: e.BgmTitleCN, Bgm: e.BgmTitle, Base: e.AnimeTitle,
	})
	if e.AnimeYear != nil && *e.AnimeYear >= templatefacts.MinYear && *e.AnimeYear <= templatefacts.MaxYear {
		fs.Anime.Year = strconv.FormatInt(*e.AnimeYear, 10)
	}
	if e.AnimeSeason != nil && *e.AnimeSeason >= 0 {
		season := *e.AnimeSeason
		fs.Anime.Season = &season
	}
	if len(files) > 0 {
		var total int64
		for _, file := range files {
			total += file.Size
		}
		count := int64(len(files))
		fs.Torrent.Size, fs.Torrent.FileCount = &total, &count
	}
	if ep := e.Episode; ep != nil && strings.TrimSpace(ep.Number) != "" {
		offset := ep.Offset
		fs.Ep = templatefacts.EpFacts{Raw: ep.Raw, Number: ep.Number, Display: ep.Display, Offset: &offset}
	}
	return Input{ReleaseTitle: e.ReleaseTitle, Files: files, Facts: templatefacts.VarsInputObject(&fs)}
}

// intPtr narrows an optional provider year to the templatefacts projection
// type; nil stays nil.
func intPtr(value *int64) *int {
	if value == nil {
		return nil
	}
	year := int(*value)
	return &year
}
