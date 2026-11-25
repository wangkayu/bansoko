package naming

import (
	"errors"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

// ErrRenderFactsUnavailable marks missing durable evidence required by a naming render.
var ErrRenderFactsUnavailable = errors.New("render facts unavailable")

// NamingContext is the method-free plain object exposed to naming
// templates. It is assembled from persisted facts and resolver outputs.
type NamingContext struct {
	Anime   AnimeNamingFacts       `json:"Anime"`
	Source  SourceNamingFacts      `json:"Source"`
	Release ReleaseNamingFacts     `json:"Release"`
	File    FileNamingFacts        `json:"File"`
	Parsed  ParsedNamingVariables  `json:"Parsed"`
	Derived DerivedNamingVariables `json:"Derived"`
	// Rt and AniBTTags are the frozen reltitle and AniBT tag value namespaces
	// (templatefacts), projected from the Release's frozen source facts. They
	// reach templates under the rt and anibt roots, not under these Go names;
	// a missing value is null there.
	Rt        templatefacts.Values `json:"-"`
	AniBTTags templatefacts.Values `json:"-"`
	// FileTitleTags is the reltitle parse of the file's own name (frt.*). An
	// operation preparation parses it once and hands the same set to the
	// naming render and to the frozen snapshots the notification producers
	// read. Every other render (naming preview, maintenance replay, file
	// manager match) prepares it explicitly from the original file name; nil
	// means no prepared set and renders no frt values — the projection never
	// parses a name on its own.
	FileTitleTags *acquisitiondomain.TitleTagSet `json:"-"`
	// Torrent is the torrent metadata frozen in the acquisition input. It is
	// the torrent.* root.
	Torrent TorrentNamingFacts `json:"-"`
	// EpisodeTitles carries the provider episode titles (bgm.ep_title and
	// tmdb.ep_title) the caller of a whitelisted render resolved before
	// rendering, from the persistent lookup cache. Only the whitelist leaves
	// (the rename render, the anime-files alignment render, the file manager
	// match render and explicitly requested recognition renders) fills them;
	// every other path leaves this empty, and both facts read empty there.
	// Projections like ReplayFactValues never mangle these, they are
	// JSON-ignored like the other roots.
	EpisodeTitles EpisodeTitleFacts `json:"-"`
	// AnimeAbsent marks a render with no anime at all, the file manager's
	// recognition of a file the user did not match. Every anime.* fact is
	// then undefined rather than empty, so a template that reads one names
	// it instead of silently rendering an empty title.
	AnimeAbsent bool `json:"-"`
}

// EpisodeTitleFacts are the queried provider episode titles of one render.
// An empty value is the empty fact both when the lookup was not attempted
// (source unbound, no episode number, not on the network whitelist) and when
// the provider has no title for the episode; the caller that freezes the
// episode_titles block distinguishes the states from its own inputs, never
// from this projection.
type EpisodeTitleFacts struct {
	BGM  string
	TMDB string
}

// TorrentNamingFacts holds the frozen torrent metadata a naming render reads:
// the accept-time manifest totals and the info hashes. A nil total or an empty
// hash is unknown (inputs before schema 4, or a torrent without that hash).
type TorrentNamingFacts struct {
	Size      *int64
	FileCount *int64
	HashV1    string
	HashV2    string
}

// WithTemplateFacts injects the release-level template value namespaces. Every
// runtime naming path calls it with the projection of the same frozen
// source facts, so a render never re-derives them.
func WithTemplateFacts(ctx NamingContext, facts templatefacts.Facts) NamingContext {
	ctx.Rt = facts.Reltitle.Clone()
	ctx.AniBTTags = facts.AniBTTags.Clone()
	return ctx
}

// BuildResolvedContext resolves registered Parsed/Derived values from the
// supplied frozen facts.
func BuildResolvedContext(ctx NamingContext) (ResolvedContext, error) {
	return DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}), nil
}

// AnimeNamingFacts holds the managed-anime facts and metadata bindings.
type AnimeNamingFacts struct {
	ID            string
	Title         string
	TitleOverride string
	// ProviderTitle is the title the subscription's provider catalog entry
	// carried when the anime was subscribed (AniBT/Mikan only).
	ProviderTitle string
	// Year is the owner-edited year (anime_metadata.year) naming reads as
	// anime.year; nil when unset. It is never derived from a provider year.
	Year *int
	// Season is the owner-edited season (anime_metadata.season) naming reads
	// as anime.season since naming-render-4; 0 is the specials season and nil
	// is unset. It is never derived from the TMDB binding (TMDB.Season), except
	// that a frozen input predating the owner season maps to the season
	// naming-render-3 named (FrozenAnimeSeason).
	Season *int
	BGM    BGMNamingFacts
	TMDB   TMDBNamingFacts
}

// BGMNamingFacts holds the confirmed Bangumi binding facts.
type BGMNamingFacts struct {
	WorkID  string
	Title   string
	TitleCN string
	// Year is the subject's provider-observed first-air year; nil when the
	// provider gave none or the work was never observed.
	Year *int
}

// TMDBNamingFacts holds the confirmed TMDB binding facts. MediaKind is the
// bound work kind (tv | movie) and Season is the bound season number.
type TMDBNamingFacts struct {
	WorkID        string
	MediaKind     string
	Season        int
	SeasonPresent bool
	// SeasonBound marks a preserved season 0 (continuous-SP recognition). A
	// season 0 without it is no season (AnimeSeason).
	SeasonBound bool
	// Year is the work's provider-observed first air/release year; nil when
	// the provider gave none or the work was never observed.
	Year *int
	// TitleCN is the title the TMDB request language returned
	// (tmdb.title_cn; every request sends language=zh-CN).
	TitleCN string
	// OriginalTitle is the work's TMDB original title
	// (tmdb.title only).
	OriginalTitle string `json:"-"`
}

// SourceNamingFacts holds the RSS source facts that produced the release.
// Kind is the source_kind enum (provider_group | custom_rss). GroupName is the
// source's configured display name. Group is the provider-namespaced external
// identity of a provider_group source — Mikan's numeric group id or AniBT's
// group slug — the same stable identity the subgroup policy keys on; it is the
// zero value for a custom_rss source and for a provider_group source whose
// provenance records none. Templates read the identity under its provider root
// (mikan.groupid / anibt.groupslug) with the display name beside it; a root
// that does not match the source kind reads null.
type SourceNamingFacts struct {
	Kind      string
	GroupName string
	Group     providerdomain.GroupRef
}

// ReleaseNamingFacts holds the raw release facts. PublishedAt is an
// RFC3339 timestamp or empty when the feed did not carry one. SubtitleMode
// is the resolved embedded/external/mixed/unknown classification. Version
// is the release row optimistic-concurrency counter.
type ReleaseNamingFacts struct {
	ID            string
	OriginalTitle string
	PublishedAt   string
	SubtitleMode  string
	Version       int
	// FrozenEpisode is the pre-resolved EpisodeIdentity from the media facts
	// snapshot when one exists. Naming consumes it instead of re-parsing the
	// title, so naming always agrees with grouping and replacement.
	FrozenEpisode *FrozenEpisodeNamingFacts `json:"FrozenEpisode,omitempty"`
}

// FrozenEpisodeNamingFacts is the atomic episode identity frozen by the
// episodeidentity resolver. Raw is the pre-offset capture, Number the
// canonical final (integer) episode, Offset the applied offset, and
// Display the canonical presentation string. NumberEnd is the last episode of
// a multi-episode file (empty for a single episode).
type FrozenEpisodeNamingFacts struct {
	Raw       string
	Number    string
	Offset    int
	Display   string
	NumberEnd string `json:",omitempty"`
	// Source overrides ep.source when set (a file manager match records
	// "manual"); empty keeps the resolver's default.
	Source string `json:",omitempty"`
}

// MediaNamingFacts holds the locally inspected media facts of one file.
type MediaNamingFacts struct {
	Resolution string
	VideoCodec string
	Container  string
	// AudioCodec reaches templates as media.audio_codec only.
	AudioCodec string `json:"-"`
	// HDR reaches templates as media.hdr only; empty means SDR or unread.
	HDR string `json:"-"`
}

// FileNamingFacts holds the artifact-file facts. Kind is video | subtitle |
// other; Extension is the lowercased extension without the leading dot.
type FileNamingFacts struct {
	OriginalName string
	Basename     string
	Extension    string
	SizeBytes    int64
	Kind         string
	Media        MediaNamingFacts
}

// ParsedNamingVariables holds the resolver outputs derived from the release
// title and file facts.
type ParsedNamingVariables struct {
	Episode  EpisodeNamingVariables
	Language LanguageNamingVariables
	Subtitle SubtitleNamingVariables
}

// EpisodeNamingVariables holds the parsed episode values. Raw is the matched
// text, DetectedNumber the numeric capture before the offset, Display the
// presentation string and Number the integer value after the offset.
type EpisodeNamingVariables struct {
	Raw            string
	DetectedNumber float64
	Display        string
	Number         int
	Offset         int
}

// LanguageNamingVariables holds the release title's language codes: the
// normalized primary code (sc/tc/jpsc/jptc/jpn/eng, or the single code a list
// names), the complete structured language list (combined embedded sets carry
// several codes) and their BCP 47 tags. Templates default to Code; users map
// Languages further with rules.
type LanguageNamingVariables struct {
	Code      string
	BCP47     []string
	Languages []string
}

// SubtitleNamingVariables holds the standalone-subtitle facts. They are only
// populated when File.Kind is subtitle.
type SubtitleNamingVariables struct {
	Format          string
	OriginalSuffix  string
	Languages       []string
	PrimaryLanguage string
	// TagLanguages are the reltitle sub_lang codes of the file's own tag.
	TagLanguages []string
	Flags        SubtitleFlags
}

// SubtitleFlags marks well-known subtitle qualifier tokens.
type SubtitleFlags struct {
	Forced     bool
	SDH        bool
	Commentary bool
	Default    bool
}

// DerivedNamingVariables holds the effective presentation values after the
// distribution-standard fallback chain.
type DerivedNamingVariables struct {
	EffectiveTitle       string
	EffectiveTitleSource string // override | provider | tmdb_cn | bgm_cn | bgm | base
	EffectiveMediaKind   string // tv | movie
	SeasonFolder         string
}
