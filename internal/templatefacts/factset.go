package templatefacts

import (
	"fmt"
	"strings"
)

// FactSet is the neutral record one template render reads. Each template kind
// fills it from its own frozen inputs (naming from the resolved naming
// context, notification from the frozen delivery payload); the registry
// getters then read it under the unified paths. A blank string, a nil pointer
// and a nil slice are "no value": the getters turn them into null, never into
// a guessed value.
type FactSet struct {
	Anime    AnimeFacts
	Bgm      BgmFacts
	Tmdb     TmdbFacts
	Release  ReleaseFacts
	Ep       EpFacts
	Sub      SubFacts
	Torrent  TorrentFacts
	File     FileFacts
	Info     InfoFacts
	Out      OutFacts
	Op       OpFacts
	Event    EventFacts
	Result   ResultFacts
	Download DownloadFacts
	// Rt, Anibt and Mikan are the value namespaces keyed by Field.Key; Vars
	// the frozen user Vars output. Mikan carries only the origin RSS source's
	// group identity (there is no Mikan tag namespace); the source's
	// configured display group name is anime.group, not a namespace field.
	Rt    Values
	Anibt Values
	Mikan Values
	Vars  map[string]map[string]string
}

type AnimeFacts struct {
	ID            string
	Title         string
	TitleSource   string
	TitleOverride string
	// ProviderTitle is the title the subscription's provider catalog entry
	// carried when the anime was subscribed (AniBT/Mikan only).
	ProviderTitle string
	// TitleBase is the managed anime record's stored title, the last candidate
	// of the title order and not a provider title.
	TitleBase string
	// Group is the provider/custom display group name the origin RSS source
	// recorded (anime.group); empty when the source configures none.
	Group  string
	Kind   string
	Season *int64
	// SeasonFolder is the season folder leaf (anime.season_folder).
	SeasonFolder string
	// SeasonPath is the season folder composite (anime.season_path): the
	// season folder with its leading separator, "" when there is none.
	SeasonPath string
	// Year is the owner-edited year as a decimal string; empty when unset.
	Year string
}

type BgmFacts struct {
	ID string
	// Title and TitleCN are the bound subject's native names, frozen at
	// accept.
	Title   string
	TitleCN string
	// Year is the subject's provider-observed first-air year; nil when the
	// provider gave none. It is a projection input, not a registered fact.
	Year *int
	// EpTitle is the provider episode title (bgm.ep_title) a whitelisted
	// render resolved and cached before rendering; empty when the source is
	// not bound, the episode number is unknown, the provider has no title for
	// that episode, or the lookup has not succeeded yet.
	EpTitle string
}

type TmdbFacts struct {
	ID string
	// TitleCN is the title TMDB returned for the request language (every
	// request sends language=zh-CN); OriginalTitle is the native title.
	TitleCN       string
	OriginalTitle string
	// Summary is the TMDB work summary (tmdb.summary) observed at match/
	// search time and frozen at accept; notification-scope only (the naming
	// scenario does not list the fact). Empty when never observed.
	Summary string
	Kind    string
	Season  *int64
	// Year is the work's provider-observed first air/release year; nil when
	// the provider gave none. It is a projection input, not a registered
	// fact.
	Year *int
	// EpTitle is the provider episode title (tmdb.ep_title) a whitelisted
	// render resolved and cached before rendering; same empty rules as
	// BgmFacts.EpTitle.
	EpTitle string
}

type ReleaseFacts struct {
	ID string
	// Title is the RSS item's original title, frozen at accept.
	Title string
	// Src is the source the release came from (release.src): the provider
	// kind (anibt|mikan) a media event delivered, or the source type
	// (provider_group|custom_rss) a release workflow event delivered; empty
	// when the payload records none.
	Src          string
	PublishedAt  string
	Episodes     string
	SubtitleMode string
	Lang         string
	Langs        []string
	LangTag    []string
}

type EpFacts struct {
	Number string
	// NumberEnd is the last episode of a multi-episode file; empty (null)
	// for a single episode.
	NumberEnd string
	Raw       string
	Display   string
	Offset    *int64
	Source    string
	Kind      string
	// S00E00 is the season/episode composite (ep.s00e00).
	S00E00 string
}

// SubFacts is the system subtitle language of one subtitle file, resolved like
// the episode: the file's own tag first (normalized with the reltitle
// sub_lang vocabulary), else the release title's rt.sub_lang. A video or
// other file has none.
type SubFacts struct {
	// Lang is the codes joined with +, Langs the codes, LangTag their one
	// BCP 47 tag (SubtitleLanguageTag) and LangSource file | release.
	Lang       string
	Langs      []string
	LangTag  string
	LangSource string
	// LangCode is the presentation-shaped short code of the list (sub.lang_code,
	// lowercase, with the jpsc/jptc composites) and LangName the display word
	// composed from the list and the release's subtitle-mode flag set
	// (sub.lang_name). Both are derived in the naming projection.
	LangCode string
	LangName string
}

type TorrentFacts struct {
	Name      string
	Size      *int64
	FileCount *int64
	HashV1    string
	HashV2    string
	// Tree is the rendered one-level listing of the torrent's contents
	// (torrenttree.Render); empty when no payload was retained.
	Tree string
}

type FileFacts struct {
	Name       string
	Stem       string
	Ext        string
	Size       *int64
	Kind       string
	Resolution string
	VideoCodec string
	AudioCodec string
	Container  string
	Sub        SubtitleFacts
}

// InfoFacts holds the read media information (info.*) of the video: HDR's
// empty value is SDR or unread, exactly like the file.* execution facts.
type InfoFacts struct {
	HDR string
}

type SubtitleFacts struct {
	Lang       string
	Langs      []string
	Format     string
	Suffix     string
	Forced     bool
	SDH        bool
	Commentary bool
	Default    bool
}

type OutFacts struct {
	Dir, Name, Path string
	// Count and Files describe the unknown files one acquisition wrote into its
	// own tree: each file is {"path": path relative to that tree's top,
	// "size": bytes}.
	Count *int64
	Files []map[string]any
}

type OpFacts struct{ Kind, Conflict string }

type EventFacts struct {
	Type, At string
	// Action is a subscription transition: started, stopped, resumed or
	// retired. Link is an in-app anchor (#/...) to act on the event.
	Action, Link string
}

type ResultFacts struct {
	Outcome, Reason string
	// Category is a download failure's typed class; Retryable whether the
	// release can be retried from the RSS page.
	Category  string
	Retryable *bool
}

type DownloadFacts struct{ Client, Task string }

// Title sources reported by anime.title_source.
const (
	TitleSourceOverride = "override"
	TitleSourceProvider = "provider"
	TitleSourceTmdbCN   = "tmdb_cn"
	TitleSourceBgmCN    = "bgm_cn"
	TitleSourceBgm      = "bgm"
	TitleSourceBase     = "base"
)

// TitleCandidates are the title inputs in the fixed title order.
type TitleCandidates struct {
	Override, Provider, TmdbCN, BgmCN, Bgm, Base string
}

// EffectiveTitle applies the one title order naming and the Vars input use:
// the title override the subscription stored (anime.title_override), the
// provider catalog title it froze (anime.provider_title), the TMDB title for
// the request language (tmdb.title_cn), the BGM Chinese title (bgm.title_cn),
// the BGM title (bgm.title), and finally the managed anime's
// stored base title (anime.title_base). The base title is a real value on the
// anime record, so it stays last: every provider title above must be able to
// win over it. It returns the first non-blank value and its source, or two
// empty strings.
func EffectiveTitle(c TitleCandidates) (title, source string) {
	for _, candidate := range []struct{ value, source string }{
		{c.Override, TitleSourceOverride}, {c.Provider, TitleSourceProvider},
		{c.TmdbCN, TitleSourceTmdbCN}, {c.BgmCN, TitleSourceBgmCN},
		{c.Bgm, TitleSourceBgm}, {c.Base, TitleSourceBase},
	} {
		if value := strings.TrimSpace(candidate.value); value != "" {
			return value, candidate.source
		}
	}
	return "", ""
}

// Provider year bounds. A year outside them is not an air/release year and is
// treated as unknown by the projection.
const (
	MinYear = 1900
	MaxYear = 2100
)

// EffectiveYear is the provider year order naming-render-1 used for
// anime.year (replay only; anime.year is now the owner-edited year): the TMDB
// work's provider-observed year first, then the BGM subject's. A year
// outside the supported range counts as unknown, so a value that could never
// be a release year is never rendered. It returns 0 and an empty source when
// neither work has an observed year.
func EffectiveYear(tmdb, bgm *int) (year int, source string) {
	if tmdb != nil && *tmdb >= MinYear && *tmdb <= MaxYear {
		return *tmdb, "tmdb"
	}
	if bgm != nil && *bgm >= MinYear && *bgm <= MaxYear {
		return *bgm, "bgm"
	}
	return 0, ""
}

// SeasonFolder is the season directory name for a season number.
func SeasonFolder(season *int64) string {
	if season == nil {
		return ""
	}
	return fmt.Sprintf("Season %02d", *season)
}

// Int64 returns a pointer to v.
func Int64(v int64) *int64 { return &v }

// The provider episode title facts the naming renders resolve through the
// episode title store (migration 046): the registry carries both paths as
// execution-phase, nullable scenario facts whose empty value is the resolver's
// definitive answer. They never entered a render snapshot's replay set, so no
// renderer-version replay ever had evidence for them, and an empty one is a
// rendered fact, never lost evidence.
var episodeTitlePathSet = map[string]bool{"bgm.ep_title": true, "tmdb.ep_title": true}

// IsEpisodeTitleFactPath reports whether the catalog fact path is a provider
// episode title fact.
func IsEpisodeTitleFactPath(path string) bool {
	return episodeTitlePathSet[path]
}
