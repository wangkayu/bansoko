package naming

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"bansoko/internal/namingvars"
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
	// Vars contains user-defined presentation variables. It is intentionally
	// separate from identity-owned fields and is never used by resolvers.
	Vars map[string]map[string]string `json:"Vars,omitempty"`
	// Rt and AniBTTags are the frozen reltitle and AniBT tag value namespaces
	// (templatefacts), projected from the Release's frozen source facts. They
	// reach templates under the rt and anibt roots, not under these Go names;
	// a missing value is null there.
	Rt        templatefacts.Values `json:"-"`
	AniBTTags templatefacts.Values `json:"-"`
	// Torrent is the torrent metadata frozen in the acquisition input. It is
	// the torrent.* root.
	Torrent TorrentNamingFacts `json:"-"`
	// EpisodeTitles carries the provider episode titles (bgm.ep_title and
	// tmdb.ep_title) the caller of a whitelisted render resolved before
	// rendering, from the persistent lookup cache. Only the whitelist leaves
	// (the rename render, the anime-files alignment render and the file
	// manager match render) fills them; every other path leaves this empty,
	// and both facts read empty there. Projections like ReplayFactValues
	// never mangle these, they are JSON-ignored like the other roots.
	EpisodeTitles EpisodeTitleFacts `json:"-"`
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

// WithVars injects a validated Vars snapshot into a naming context. It is the
// shared, pure assembly boundary for runtime naming paths; callers that need
// deployment Vars should obtain them with WithCurrentVars first. A defensive
// copy prevents a render from observing later mutation of the active snapshot.
func WithVars(ctx NamingContext, vars map[string]map[string]string) (NamingContext, error) {
	validated := namingvars.Vars(vars)
	if err := validated.Validate(); err != nil {
		return NamingContext{}, fmt.Errorf("inject naming vars: %w", err)
	}
	ctx.Vars = make(map[string]map[string]string, len(vars))
	for group, values := range vars {
		ctx.Vars[group] = make(map[string]string, len(values))
		for name, value := range values {
			ctx.Vars[group][name] = value
		}
	}
	return ctx, nil
}

// WithCurrentVars injects the initialized deployment Vars snapshot. Missing
// initialization is an explicit error: runtime naming must never silently
// render with an empty Vars object.
func WithCurrentVars(ctx NamingContext) (NamingContext, error) {
	vars, _, err := namingvars.Current()
	if err != nil {
		return NamingContext{}, fmt.Errorf("load active naming vars: %w", err)
	}
	return WithVars(ctx, vars)
}

// BuildResolvedContext injects active presentation Vars and resolves registered
// Parsed/Derived values from the supplied frozen facts.
func BuildResolvedContext(ctx NamingContext) (ResolvedContext, error) {
	withVars, err := WithCurrentVars(ctx)
	if err != nil {
		return ResolvedContext{}, err
	}
	return DefaultRegistry().ResolveAll(ResolverInput{Facts: withVars}), nil
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
	// (tmdb.original_title only).
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
// presentation string, Number the integer value after the offset, and
// SpecialKind the ncop/nced/special classification or empty.
type EpisodeNamingVariables struct {
	Raw            string
	DetectedNumber float64
	Display        string
	Number         int
	Offset         int
	SpecialKind    string
}

// LanguageNamingVariables holds the detected release language: the normalized
// primary code (sc/tc/jpsc/jptc/jpn/eng), description, BCP47 tags, and the
// complete structured language set (combined embedded sets carry several
// codes). Templates default to Code; users map Languages further with rules.
type LanguageNamingVariables struct {
	Code        string
	Description string
	BCP47       []string
	Languages   []string
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

// VarsEpisode converts a frozen release episode identity into Vars evidence;
// nil stays nil.
func VarsEpisode(f *FrozenEpisodeNamingFacts) *namingvars.EpisodeEvidence {
	if f == nil {
		return nil
	}
	return &namingvars.EpisodeEvidence{Raw: f.Raw, Number: f.Number, Display: f.Display, Offset: int64(f.Offset)}
}

// WithFrozenReleaseFacts fills the release-level binding and torrent facts a
// pre-download render reads from the release's frozen Vars input (the
// namingvars release fact object every admission and preview site builds):
// anime.id, the bound work ids and native titles, the bound season and the
// torrent metadata. The media kind keeps its own frozen source (the parser
// identity); a nil or older input leaves every value unknown.
// FrozenAnimeYear reads the owner-edited year a frozen Vars input recorded
// (input.anime.year, a decimal string); nil when it recorded none.
func FrozenAnimeYear(facts map[string]any) *int {
	object, _ := facts["anime"].(map[string]any)
	text, _ := object["year"].(string)
	year, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || year < templatefacts.MinYear || year > templatefacts.MaxYear {
		return nil
	}
	return &year
}

// FrozenAnimeSeason reads the owner-edited season a frozen Vars input
// recorded (input.anime.season; nil when it recorded null). A frozen input
// without the member predates the owner season (naming-render-4): it maps to
// the season naming-render-3 named from its TMDB binding (AnimeSeason without
// the unbound default), so rendering it with the current renderer gives the
// same season, season folder and ep.s00e00 as before. tmdb is the binding
// restored from the same frozen evidence.
func FrozenAnimeSeason(facts map[string]any, tmdb TMDBNamingFacts) *int {
	object, _ := facts["anime"].(map[string]any)
	value, present := object["season"]
	if !present {
		legacy := AnimeSeason("", tmdb, false)
		if legacy == nil {
			return nil
		}
		season := int(*legacy)
		return &season
	}
	var season int
	switch typed := value.(type) {
	case int64:
		season = int(typed)
	case int:
		season = typed
	case float64:
		season = int(typed)
	default:
		return nil
	}
	if season < 0 {
		return nil
	}
	return &season
}

func WithFrozenReleaseFacts(ctx NamingContext, facts map[string]any) NamingContext {
	member := func(root, key string) any {
		object, _ := facts[root].(map[string]any)
		return object[key]
	}
	text := func(root, key string) string {
		value, _ := member(root, key).(string)
		return value
	}
	number := func(root, key string) *int64 {
		switch value := member(root, key).(type) {
		case int64:
			return &value
		case int:
			n := int64(value)
			return &n
		case float64:
			n := int64(value)
			return &n
		}
		return nil
	}
	if facts == nil {
		return ctx
	}
	if id := text("anime", "id"); id != "" {
		ctx.Anime.ID = id
	}
	// The anime's own titles come from the same frozen facts, so a rendering
	// that reads only the frozen input derives the same effective title as the
	// acceptance did. A frozen input without title_base (older schema) keeps the
	// caller's title.
	if base := text("anime", "title_base"); base != "" {
		ctx.Anime.Title = base
	}
	ctx.Anime.TitleOverride = text("anime", "title_override")
	ctx.Anime.ProviderTitle = text("anime", "provider_title")
	ctx.Anime.BGM.WorkID, ctx.Anime.BGM.Title, ctx.Anime.BGM.TitleCN = text("bgm", "id"), text("bgm", "title"), text("bgm", "title_cn")
	ctx.Anime.TMDB.WorkID, ctx.Anime.TMDB.TitleCN, ctx.Anime.TMDB.OriginalTitle = text("tmdb", "id"), text("tmdb", "title_cn"), text("tmdb", "original_title")
	// anime.year is the owner-edited year frozen with the input. An older
	// input froze the provider projection with its source (anime.year_source);
	// that source is restored on its work so a naming-render-1 replay
	// recomputes the same value.
	if year, err := strconv.Atoi(text("anime", "year")); err == nil {
		ctx.Anime.Year = &year
		switch text("anime", "year_source") {
		case "tmdb":
			ctx.Anime.TMDB.Year = &year
		case "bgm":
			ctx.Anime.BGM.Year = &year
		}
	}
	// The bound season is frozen as tmdb.season. Restoring it (and its
	// presence) is what lets season-derived values — anime.season and
	// anime.season_folder — resolve for a pre-download render, exactly as the
	// artifact render reads the same binding from the frozen snapshot. A null
	// season (an unbound anime) stays unknown instead of defaulting.
	if season := number("tmdb", "season"); season != nil {
		ctx.Anime.TMDB.Season = int(*season)
		ctx.Anime.TMDB.SeasonPresent = true
	}
	// anime.season is the owner-edited season frozen with the input; an
	// older input that never froze it names the season its TMDB binding named.
	ctx.Anime.Season = FrozenAnimeSeason(facts, ctx.Anime.TMDB)
	ctx.Torrent = TorrentNamingFacts{
		Size: number("torrent", "size"), FileCount: number("torrent", "file_count"),
		HashV1: text("torrent", "hash_v1"), HashV2: text("torrent", "hash_v2"),
	}
	return ctx
}
