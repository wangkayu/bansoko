package domain

import (
	"encoding/json"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

type SubscriptionOrigin string

const (
	SubscriptionOriginAnibt   SubscriptionOrigin = "anibt"
	SubscriptionOriginMikan   SubscriptionOrigin = "mikan"
	SubscriptionOriginCustom  SubscriptionOrigin = "custom"
	SubscriptionOriginOneshot SubscriptionOrigin = "oneshot"
)

type SourceKind string

const (
	SourceKindProviderGroup SourceKind = "provider_group"
	SourceKindCustomRss     SourceKind = "custom_rss"
)

type ProviderKind string

const (
	ProviderKindAnibt ProviderKind = "anibt"
	ProviderKindMikan ProviderKind = "mikan"
	// ProviderKindNyaa is a seed-provenance member for a seed read from a Nyaa
	// keyword search snapshot. Nyaa has no work catalog, so like custom it never
	// becomes a discoverable provider, a subgroup or a subscribable origin.
	ProviderKindNyaa ProviderKind = "nyaa"
	// ProviderKindJackett is likewise search-only seed provenance. Jackett owns
	// its tracker aggregation and never becomes a work catalog or subscription.
	ProviderKindJackett ProviderKind = "jackett"
	// ProviderKindCustom is a seed-provenance vocabulary member only: it names
	// the frozen feed of a custom/oneshot draft source, so a custom seed keeps a
	// provider-shaped identity without ever becoming a discoverable provider, a
	// subgroup or a subscribable origin. Catalog discovery stays anibt/mikan.
	ProviderKindCustom ProviderKind = "custom"
)

type SelectionMode string

const (
	SelectionModeAuto   SelectionMode = "auto"
	SelectionModeManual SelectionMode = "manual"
)

type TMDBMediaKind string

const (
	TMDBMediaKindTv    TMDBMediaKind = "tv"
	TMDBMediaKindMovie TMDBMediaKind = "movie"
)

type RSSSourceDraftAnibtAuxMode string

const (
	RSSSourceDraftAnibtAuxModeDisabled    RSSSourceDraftAnibtAuxMode = "disabled"
	RSSSourceDraftAnibtAuxModeFillMissing RSSSourceDraftAnibtAuxMode = "fill_missing"
	RSSSourceDraftAnibtAuxModeOverride    RSSSourceDraftAnibtAuxMode = "override"
)

type BGMBinding struct {
	WorkId string `json:"work_id"`
}

type TMDBBinding struct {
	MediaKind TMDBMediaKind `json:"media_kind"`
	Season    int           `json:"season"`
	WorkId    string        `json:"work_id"`
}

type NullableBGMBinding = BGMBinding
type NullableTMDBBinding = TMDBBinding

type AnimeMetadataDraft struct {
	Bgm *BGMBinding `json:"bgm"`
	// MediaKind pins the naming branch selector (tv | movie). Nil
	// means unset; it never rewrites the TMDB tuple.
	MediaKind     *string      `json:"media_kind"`
	Tmdb          *TMDBBinding `json:"tmdb"`
	TotalEpisodes *int         `json:"total_episodes"`
	// Year is the owner-edited year naming reads as anime.year. Nil means
	// unset (anime.year renders empty); it is never read from metadata.
	Year *int `json:"year"`
	// Season is the owner-edited season naming reads as anime.season (and
	// anime.season_folder, anime.season_path, ep.s00e00); 0 is the specials
	// season. Nil means unset (no season folder, ep.s00e00 is the episode
	// alone). It is independent of Tmdb.Season, the bound TMDB season.
	Season *int `json:"season"`
}

type AnimeDraft struct {
	Business       *BusinessSnapshot  `json:"business,omitempty"`
	BusinessDigest *string            `json:"business_digest,omitempty"`
	InstanceLabel  *string            `json:"instance_label"`
	Metadata       AnimeMetadataDraft `json:"metadata"`
	// NewReleasesOnly is the new-releases-only option the draft carries until
	// subscribe freezes it onto the instance. Absent means off.
	NewReleasesOnly bool               `json:"new_releases_only,omitempty"`
	Origin          SubscriptionOrigin `json:"origin"`
	OriginEntryRef  *string            `json:"origin_entry_ref"`
	// ProviderGroupOffsets seeds the link rows subscribe creates for a provider
	// origin's groups; the link row stays the only offset authority.
	ProviderGroupOffsets []ProviderGroupOffsetDraft `json:"provider_group_offsets,omitempty"`
	RssSources           []RSSSourceDraft           `json:"rss_sources"`
	Rules                RulesDraft                 `json:"rules"`
	Subgroups            SubgroupsSection           `json:"subgroups"`
	Title                string                     `json:"title"`
	TitleOverride        *string                    `json:"title_override"`
}

// ProviderGroupOffsetDraft is one provider group's episode offset in a draft.
type ProviderGroupOffsetDraft struct {
	EpisodeOffset    int    `json:"episode_offset"`
	ProviderGroupRef string `json:"provider_group_ref"`
}

type RSSSourceDraft struct {
	AnibtAuxMode          *RSSSourceDraftAnibtAuxMode `json:"anibt_aux_mode"`
	GroupName             string                      `json:"group_name"`
	HttpFeedConnectionRef *string                     `json:"http_feed_connection_ref"`
	Locator               string                      `json:"locator"`
	LocatorDisplay        *string                     `json:"locator_display"`
	NetworkRouteRef       *string                     `json:"network_route_ref"`
	EpisodeOffset         int                         `json:"episode_offset"`
	ProviderProvenance    *ProviderProvenance         `json:"provider_provenance"`
	SourceKind            SourceKind                  `json:"source_kind"`
	Title                 string                      `json:"title"`
}

type ProviderProvenance struct {
	DiscoverySnapshotVersion string       `json:"discovery_snapshot_version"`
	Provider                 ProviderKind `json:"provider"`
	ProviderEntryRef         string       `json:"provider_entry_ref"`
	ProviderGroupRef         string       `json:"provider_group_ref"`
}

type NameKeywordRule struct {
	Keyword string `json:"keyword"`
	Score   int    `json:"score"`
}

type ProviderNameKeywordRules struct {
	Anibt *[]NameKeywordRule `json:"anibt,omitempty"`
	Mikan *[]NameKeywordRule `json:"mikan,omitempty"`
}

type ProviderGroupDefaultMode string

// PinnedGroupScore fixes one site group's final score: the group's score is
// Score, with no site default and no name keyword applied; the site list
// still gates it and a per-anime manual intent still wins. GroupRef is the
// provider's own group key.
type PinnedGroupScore struct {
	GroupRef string `json:"group_ref"`
	Score    int    `json:"score"`
}

type ProviderGroupDefault struct {
	Groups       []string                 `json:"groups"`
	Mode         ProviderGroupDefaultMode `json:"mode"`
	Score        int                      `json:"score"`
	PinnedScores []PinnedGroupScore       `json:"pinned_scores,omitempty"`
}

type ProviderGroupDefaults struct {
	Anibt *ProviderGroupDefault `json:"anibt,omitempty"`
	Mikan *ProviderGroupDefault `json:"mikan,omitempty"`
}

type SubgroupScoreEntry struct {
	DisplayName       string        `json:"display_name"`
	EffectiveSelected bool          `json:"effective_selected"`
	GroupRef          string        `json:"group_ref"`
	ManualSelected    *bool         `json:"manual_selected"`
	Provider          ProviderKind  `json:"provider"`
	Score             int           `json:"score"`
	SelectionMode     SelectionMode `json:"selection_mode"`
}

type SubgroupsSection struct {
	Entries          []SubgroupScoreEntry      `json:"entries"`
	MaxRss           int                       `json:"max_rss"`
	NameKeywords     *ProviderNameKeywordRules `json:"name_keywords,omitempty"`
	ProviderDefaults *ProviderGroupDefaults    `json:"provider_defaults,omitempty"`
}

type ParserRules struct {
	EpisodePatterns []string `json:"episode_patterns"`
}

type ReleaseFilterRules = RegexFilter

type FileFilterRules = RegexFilter

type FileEffectRulesOperation string

const (
	FileEffectRulesOperationCopy     FileEffectRulesOperation = "copy"
	FileEffectRulesOperationMove     FileEffectRulesOperation = "move"
	FileEffectRulesOperationHardlink FileEffectRulesOperation = "hardlink"
)

type CollisionPolicy string

const (
	CollisionPolicyFail      CollisionPolicy = "fail"
	CollisionPolicyVersioned CollisionPolicy = "versioned"
	CollisionPolicyOverwrite CollisionPolicy = "overwrite"
)

type FileEffectRules struct {
	CollisionPolicy CollisionPolicy          `json:"collision_policy"`
	Operation       FileEffectRulesOperation `json:"operation"`
}

type NamingVariant struct {
	DirectoryTemplate     string `json:"directory_template"`
	VideoFilenameTemplate string `json:"video_filename_template"`
}

type NamingRulesLanguage string

const (
	NamingRulesLanguageJsNaming1 NamingRulesLanguage = "js-naming-1"
)

type NamingRules struct {
	Language               NamingRulesLanguage `json:"language"`
	Movie                  NamingVariant       `json:"movie"`
	Unknown                NamingVariant       `json:"unknown"`
	Preset                 *string             `json:"preset,omitempty"`
	SubtitleSuffixTemplate string              `json:"subtitle_suffix_template"`
	Tv                     NamingVariant       `json:"tv"`
}

// RulesSections holds the per-anime rules sections as complete replace values.
// A draft may leave a section absent until load_business_preset copies the
// Global default into it; an owner value always carries all five.
type RulesSections struct {
	FileEffect    json.RawMessage `json:"file_effect,omitempty"`
	FileFilter    json.RawMessage `json:"file_filter,omitempty"`
	Naming        json.RawMessage `json:"naming,omitempty"`
	Parser        json.RawMessage `json:"parser,omitempty"`
	ReleaseFilter json.RawMessage `json:"release_filter,omitempty"`
	// TagFilters is optional: on an owner save an absent section keeps the
	// stored value; a draft carries its one-time copy, and a draft without it
	// stores the code default when subscribed.
	TagFilters json.RawMessage `json:"tag_filters,omitempty"`
}

type RulesDraft struct {
	Sections RulesSections `json:"sections"`
}

type EffectiveRules struct {
	FileEffect    FileEffectRules    `json:"file_effect"`
	FileFilter    FileFilterRules    `json:"file_filter"`
	Naming        NamingRules        `json:"naming"`
	Parser        ParserRules        `json:"parser"`
	ReleaseFilter ReleaseFilterRules `json:"release_filter"`
}

type MetadataHoldMissing string

type MetadataHold struct {
	Held    bool                  `json:"held"`
	Missing []MetadataHoldMissing `json:"missing"`
}

type RawReleaseFacts struct {
	EnclosureLength *int64     `json:"enclosure_length,omitempty"`
	EnclosureType   *string    `json:"enclosure_type,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	Title           string     `json:"title"`
}

type ParsedReleaseFacts struct {
	// Coverage is the batch plan's placeable episodes when the release places
	// more than one; Episode is set only when it places exactly one.
	Coverage  *ReleaseCoverage `json:"coverage,omitempty"`
	Episode   *float32         `json:"episode,omitempty"`
	GroupName *string          `json:"group_name,omitempty"`
	Languages *[]string        `json:"languages,omitempty"`
	Quality   *string          `json:"quality,omitempty"`
	Special   *bool            `json:"special,omitempty"`
}

// ReleaseCoverage is the sorted episode numbers a batch release places
// (display only; coverage keys are the authority).
type ReleaseCoverage struct {
	Episodes []int `json:"episodes"`
}

type ReleaseVerdict string

const (
	ReleaseVerdictFiltered ReleaseVerdict = "filtered"
)

type PreviewReleaseResult struct {
	ParsedFacts   *ParsedReleaseFacts `json:"parsed_facts,omitempty"`
	RawFacts      RawReleaseFacts     `json:"raw_facts"`
	ReasonCode    string              `json:"reason_code"`
	ReasonDetail  *string             `json:"reason_detail,omitempty"`
	SourceIndex   int                 `json:"source_index"`
	StableItemKey string              `json:"stable_item_key"`
	Verdict       ReleaseVerdict      `json:"verdict"`
	// FrozenEpisode is the episode identity the one selected parser/profile
	// already resolved for this Release. The pre-download target/naming plan
	// consumes it instead of re-parsing the title with another implementation,
	// so the rendered target agrees with grouping and replacement. It is
	// process-local evidence and never part of the transport contract.
	FrozenEpisode *naming.FrozenEpisodeNamingFacts `json:"-"`
	FrozenVars    *namingvars.FrozenEvaluation     `json:"-"`
	// FrozenMediaKind is the naming branch selector derived from the frozen
	// identity: "movie" when the resolved identity is the canonical movie:0
	// identity, empty for the TV default. It lets the pre-download target
	// render select the same TV/movie naming branch the artifact render uses.
	// Process-local evidence, never part of the transport contract.
	FrozenMediaKind string `json:"-"`
}

type PreviewFileResultSafety string

type PreviewFileResult struct {
	Included       bool                    `json:"included"`
	ReasonCode     *string                 `json:"reason_code,omitempty"`
	RenderedTarget *string                 `json:"rendered_target,omitempty"`
	RenderError    *string                 `json:"render_error,omitempty"`
	Safety         PreviewFileResultSafety `json:"safety"`
	SourceIndex    int                     `json:"source_index"`
	SourcePath     string                  `json:"source_path"`
	StableItemKey  string                  `json:"stable_item_key"`
	// BoundVideo is the source path of the video a bound subtitle follows
	// ("" for a video or an unbound subtitle).
	BoundVideo string `json:"bound_video,omitempty"`
	// EmptyFacts are the naming facts that had no value and rendered empty in
	// RenderedTarget (sorted catalog paths).
	EmptyFacts []string `json:"empty_facts,omitempty"`
	// SubLang, SubLangTag and SubLangSource are a subtitle's resolved
	// language (sub.lang, sub.lang_tag, sub.lang_source); empty for a video
	// or a subtitle with none.
	SubLang       string `json:"sub_lang,omitempty"`
	SubLangTag    string `json:"sub_lang_tag,omitempty"`
	SubLangSource string `json:"sub_lang_source,omitempty"`
}

// ApplyPlanCoverage rewrites the release's display episode from its batch
// plan: exactly one placeable key is that episode; several keys are a coverage
// (season plus episodes) and no single episode; none clears the title's
// episode, which never counts toward progress.
func ApplyPlanCoverage(result *PreviewReleaseResult, plan *batchplan.Plan) {
	if result.ParsedFacts == nil {
		result.ParsedFacts = &ParsedReleaseFacts{}
	}
	result.ParsedFacts.Episode = nil
	result.ParsedFacts.Coverage = nil
	if plan == nil {
		return
	}
	if len(plan.CoverageKeys) == 1 && plan.CoverageKeys[0] == "movie:0" {
		// A movie's single key keeps the canonical episode 0.
		episode := float32(0)
		result.ParsedFacts.Episode = &episode
		return
	}
	episodes, ok := plan.Episodes()
	if !ok {
		return
	}
	if len(episodes) == 1 {
		episode := float32(episodes[0])
		result.ParsedFacts.Episode = &episode
		return
	}
	result.ParsedFacts.Coverage = &ReleaseCoverage{Episodes: episodes}
}
