package config

// Config is the aggregate of the 13 YAML files loaded by LoadAll; the parser
// precompiles it into rule structures in New().
type Config struct {
	Lexicon            LexiconConfig            `json:"lexicon"`
	TemplateCatalog    TemplateCatalogConfig    `json:"template_catalog"`
	Inference          InferenceRulesConfig     `json:"inference"`
	GroupList          GroupListConfig          `json:"group_list"`
	GroupRoute         GroupRouteConfig         `json:"group_route,omitempty"`
	RouteFamilies      RouteFamiliesConfig      `json:"route_families,omitempty"`
	GroupSlot2Platform GroupSlot2PlatformConfig `json:"group_slot2_platform,omitempty"`
	PackProfileRules   PackProfileRulesConfig   `json:"pack_profile_rules,omitempty"`
	TitleKnowledge     TitleKnowledgeConfig     `json:"title_knowledge,omitempty"`

	DictCanonicalValues DictCanonicalValuesConfig `json:"dict_canonical_values,omitempty"`
	DictTokenSynonyms   DictTokenSynonymsConfig   `json:"dict_token_synonyms,omitempty"`
	DictAliasNoiseWords DictAliasNoiseWordsConfig `json:"dict_alias_noise_words,omitempty"`
	DictNotePromotion   DictNotePromotionConfig   `json:"dict_note_promotion,omitempty"`
}

// TitleKnowledgeConfig is title_knowledge.yaml: known works whose title spans
// must be protected from episode/season detection.
type TitleKnowledgeConfig struct {
	UpdatedAt string                `yaml:"updated_at" json:"updated_at"`
	Entries   []TitleKnowledgeEntry `yaml:"entries" json:"entries"`
}

// TitleKnowledgeEntry is one known work. The entry applies only when the raw
// title contains every fragment in RawMustContainAll.
type TitleKnowledgeEntry struct {
	ID                string   `yaml:"id" json:"id"`
	Enabled           bool     `yaml:"enabled" json:"enabled"`
	CanonicalTitle    string   `yaml:"canonical_title" json:"canonical_title"`
	Aliases           []string `yaml:"aliases" json:"aliases"`
	RawMustContainAll []string `yaml:"raw_must_contain_all" json:"raw_must_contain_all"`
}

// LexiconConfig is lexicon.yaml: token-sequence rules that emit attributes.
type LexiconConfig struct {
	Version   string         `yaml:"version" json:"version"`
	UpdatedAt string         `yaml:"updated_at" json:"updated_at"`
	Entries   []LexiconEntry `yaml:"entries" json:"entries"`
}

// LexiconEntry matches an ordered token sequence and emits attributes.
// AnyOf holds alternative clauses that share the entry ID (e.g. script
// variants). Remove marks matched tokens consumed.
type LexiconEntry struct {
	ID         string          `yaml:"id" json:"id"`
	Scope      string          `yaml:"scope" json:"scope"`
	Tokens     []string        `yaml:"tokens" json:"tokens"`
	AnyOf      []LexiconClause `yaml:"any_of" json:"any_of"`
	Emit       []LexiconEmit   `yaml:"emit" json:"emit"`
	Remove     bool            `yaml:"remove" json:"remove"`
	Confidence float64         `yaml:"confidence" json:"confidence"`
}

// LexiconClause is one alternative inside LexiconEntry.AnyOf.
type LexiconClause struct {
	Scope      string        `yaml:"scope" json:"scope"`
	Tokens     []string      `yaml:"tokens" json:"tokens"`
	Emit       []LexiconEmit `yaml:"emit" json:"emit"`
	Remove     bool          `yaml:"remove" json:"remove"`
	Confidence float64       `yaml:"confidence" json:"confidence"`
}

// LexiconEmit is one attribute produced by a lexicon match; TokenIndex selects
// which matched token the attribute spans (-1 for none).
type LexiconEmit struct {
	Type       string `yaml:"type" json:"type"`
	Normalized string `yaml:"normalized" json:"normalized"`
	TokenIndex int    `yaml:"token_index" json:"token_index"`
}

// InferenceRulesConfig is inference_rules.yaml. Only ContentTypeInference is
// read by the parser; the old design-note sections live under
// docs/dev/reference/.
type InferenceRulesConfig struct {
	Version              string                     `yaml:"version" json:"version"`
	UpdatedAt            string                     `yaml:"updated_at" json:"updated_at"`
	Description          string                     `yaml:"description" json:"description"`
	ContentTypeInference []ContentTypeInferenceRule `yaml:"content_type_inference" json:"content_type_inference"`
}

// ContentTypeInferenceRule maps a condition to a content kind
// (MOVIE / BATCH / SPECIAL / TV_EP / UNKNOWN); higher Priority wins.
type ContentTypeInferenceRule struct {
	Name         string                         `yaml:"name" json:"name"`
	Priority     int                            `yaml:"priority" json:"priority"`
	Condition    *ContentTypeInferenceCondition `yaml:"condition" json:"condition"`
	Result       string                         `yaml:"result" json:"result"`
	Confidence   float64                        `yaml:"confidence" json:"confidence"`
	Description  string                         `yaml:"description" json:"description"`
	Keywords     []string                       `yaml:"keywords" json:"keywords"`
	PackKeywords []string                       `yaml:"pack_keywords" json:"pack_keywords"`

	Examples any `yaml:"examples" json:"-"` // reference-only, never read by the parser
}

// ContentTypeInferenceCondition is the typed condition of a
// ContentTypeInferenceRule. A nil *bool means "not constrained"; a set field
// must hold for the rule to match. Note the asymmetry: HasX requires the flag
// to equal the value, while NoX requires the flag to differ from the value
// (so NoEpisode: false requires an episode to be present). Because decoding
// is strict (KnownFields), a misspelled key fails at load time.
type ContentTypeInferenceCondition struct {
	Default            *bool  `yaml:"default" json:"default,omitempty"`
	HasEpisode         *bool  `yaml:"has_episode" json:"has_episode,omitempty"`
	NoEpisode          *bool  `yaml:"no_episode" json:"no_episode,omitempty"`
	HasEpisodeRange    *bool  `yaml:"has_episode_range" json:"has_episode_range,omitempty"`
	NoEpisodeRange     *bool  `yaml:"no_episode_range" json:"no_episode_range,omitempty"`
	HasSeason          *bool  `yaml:"has_season" json:"has_season,omitempty"`
	HasSeasonRange     *bool  `yaml:"has_season_range" json:"has_season_range,omitempty"`
	HasTech            *bool  `yaml:"has_tech" json:"has_tech,omitempty"`
	HasMovieKeyword    *bool  `yaml:"has_movie_keyword" json:"has_movie_keyword,omitempty"`
	NoMovieKeyword     *bool  `yaml:"no_movie_keyword" json:"no_movie_keyword,omitempty"`
	HasPackKind        *bool  `yaml:"has_pack_kind" json:"has_pack_kind,omitempty"`
	HasSPKind          *bool  `yaml:"has_sp_kind" json:"has_sp_kind,omitempty"`
	HasMovieIndex      *bool  `yaml:"has_movie_index" json:"has_movie_index,omitempty"`
	HasMovieIndexRange *bool  `yaml:"has_movie_index_range" json:"has_movie_index_range,omitempty"`
	NoMovieIndexRange  *bool  `yaml:"no_movie_index_range" json:"no_movie_index_range,omitempty"`
	HasYear            *bool  `yaml:"has_year" json:"has_year,omitempty"`
	NoYear             *bool  `yaml:"no_year" json:"no_year,omitempty"`
	SPKind             string `yaml:"sp_kind" json:"sp_kind,omitempty"`
}

// TemplateCatalogConfig is template_catalog.yaml: the title-layout templates,
// grouped by the content kind they apply to.
type TemplateCatalogConfig struct {
	Version            string        `yaml:"version" json:"version"`
	UpdatedAt          string        `yaml:"updated_at" json:"updated_at"`
	Description        string        `yaml:"description" json:"description"`
	TVEpisodeTemplates []TemplateDef `yaml:"tv_episode_templates" json:"tv_episode_templates"`
	BatchTemplates     []TemplateDef `yaml:"batch_templates" json:"batch_templates"`
	MovieTemplates     []TemplateDef `yaml:"movie_templates" json:"movie_templates"`
	SpecialTemplates   []TemplateDef `yaml:"special_templates" json:"special_templates"`
	UnknownTemplates   []TemplateDef `yaml:"unknown_templates" json:"unknown_templates"`

	Statistics any `yaml:"statistics" json:"-"` // reference-only, never read by the parser
}

// TemplateDef is one title layout. Signature lists the required slots
// (G T E R S SE M I X C SP); Conditions gate on platform/source/signals;
// RouteID selects the downstream route.
type TemplateDef struct {
	ID           string              `yaml:"id" json:"id"`
	Name         string              `yaml:"name" json:"name"`
	ContentTypes []string            `yaml:"content_types" json:"content_types"`
	Signature    string              `yaml:"signature" json:"signature"`
	Confidence   float64             `yaml:"confidence" json:"confidence"`
	Description  string              `yaml:"description" json:"description"`
	Examples     []string            `yaml:"examples" json:"examples"`
	RouteID      string              `yaml:"route_id" json:"route_id"`
	Conditions   *TemplateConditions `yaml:"conditions" json:"conditions"`
	Signals      *TemplateSignals    `yaml:"signals" json:"signals"`

	// Reference-only keys: documentation in template_catalog.yaml, never read by the parser.
	Pattern          any    `yaml:"pattern" json:"-"`
	KeySignals       any    `yaml:"key_signals" json:"-"`
	ContentsHandling any    `yaml:"contents_handling" json:"-"`
	SPKind           string `yaml:"sp_kind" json:"-"`
}

// TemplateSignals describes layout traits of a template (slot order, group
// position, etc.) used by the template-signal adjudication.
type TemplateSignals struct {
	SlotOrder               []string `yaml:"slot_order" json:"slot_order"`
	GroupPosition           string   `yaml:"group_position" json:"group_position"`
	GroupAfterEPOnly        bool     `yaml:"group_after_ep_only" json:"group_after_ep_only"`
	ForbidGroupInTitleSpan  bool     `yaml:"forbid_group_in_title_span" json:"forbid_group_in_title_span"`
	EpAnchorPriority        bool     `yaml:"ep_anchor_priority" json:"ep_anchor_priority"`
	TitleOverGroupDefault   bool     `yaml:"title_over_group_default" json:"title_over_group_default"`
	TechStartsAfterEPStrict bool     `yaml:"tech_starts_after_ep" json:"tech_starts_after_ep"`
}

// TemplateConditions gates a template on parse-state requirements.
type TemplateConditions struct {
	Require *TemplateConditionRequire `yaml:"require" json:"require"`
}

// TemplateConditionRequire lists the hard requirements of a template; every
// set field must hold for the template to be eligible.
type TemplateConditionRequire struct {
	PlatformIn   []string `yaml:"platform_in" json:"platform_in"`
	SourceTypeIn []string `yaml:"source_type_in" json:"source_type_in"`
	HasSxxExx    bool     `yaml:"has_sxxexx" json:"has_sxxexx"`
	HasTailGroup bool     `yaml:"has_tail_group" json:"has_tail_group"`
}

// GroupListConfig represents the group list configuration
type GroupListConfig struct {
	Version     string         `yaml:"version" json:"version"`
	UpdatedAt   string         `yaml:"last_updated" json:"last_updated"`
	Description string         `yaml:"description" json:"description"`
	Groups      []GroupDef     `yaml:"groups" json:"groups"`
	Statistics  map[string]any `yaml:"statistics,omitempty" json:"statistics,omitempty"`
	UsageNotes  string         `yaml:"usage_notes,omitempty" json:"usage_notes,omitempty"`
}

// GroupDef represents a subtitle group definition
type GroupDef struct {
	Name            string   `yaml:"name" json:"name"`
	Patterns        []string `yaml:"patterns" json:"patterns"`
	Frequency       string   `yaml:"frequency" json:"frequency"`
	ConfidenceBoost float64  `yaml:"confidence_boost" json:"confidence_boost"`
}

// DictCanonicalValuesConfig is dict_canonical_values.yaml: canonical value
// lists per attribute type and their alias mappings.
type DictCanonicalValuesConfig struct {
	Version     string         `yaml:"version" json:"version"`
	UpdatedAt   string         `yaml:"updated_at" json:"updated_at"`
	Description string         `yaml:"description" json:"description"`
	Canonical   map[string]any `yaml:"canonical" json:"canonical"`
}

// DictTokenSynonymsConfig is dict_token_synonyms.yaml: raw-token to canonical
// value mappings (e.g. "web-dl" -> "WEB-DL").
type DictTokenSynonymsConfig struct {
	Version     string             `yaml:"version" json:"version"`
	UpdatedAt   string             `yaml:"updated_at" json:"updated_at"`
	Description string             `yaml:"description" json:"description"`
	Rules       []TokenSynonymRule `yaml:"rules" json:"rules"`
}

// TokenSynonymRule maps the raw tokens in RawTokens to one canonical value;
// higher Priority wins on overlap.
type TokenSynonymRule struct {
	ID        string             `yaml:"id" json:"id"`
	RawTokens []string           `yaml:"raw_tokens" json:"raw_tokens"`
	Emit      TokenSynonymTarget `yaml:"emit" json:"emit"`
	Priority  int                `yaml:"priority" json:"priority"`
}

// TokenSynonymTarget is the canonical value a TokenSynonymRule emits;
// NormalizedFromToken takes the value from the matched token instead.
type TokenSynonymTarget struct {
	Type                string `yaml:"type" json:"type"`
	Normalized          string `yaml:"normalized" json:"normalized"`
	NormalizedFromToken bool   `yaml:"normalized_from_token" json:"normalized_from_token"`
}

// DictAliasNoiseWordsConfig is dict_alias_noise_words.yaml: noise words and
// patterns stripped from inferred aliases.
type DictAliasNoiseWordsConfig struct {
	Version     string           `yaml:"version" json:"version"`
	UpdatedAt   string           `yaml:"updated_at" json:"updated_at"`
	Description string           `yaml:"description" json:"description"`
	AliasNoise  AliasNoiseConfig `yaml:"alias_noise" json:"alias_noise"`
}

// AliasNoiseConfig lists exact words, regex patterns, and inline strip words
// treated as noise when cleaning up aliases.
type AliasNoiseConfig struct {
	ExactWordsCaseInsensitive       []string `yaml:"exact_words_case_insensitive" json:"exact_words_case_insensitive"`
	RegexPatterns                   []string `yaml:"regex_patterns" json:"regex_patterns"`
	InlineStripWordsCaseInsensitive []string `yaml:"inline_strip_words_case_insensitive" json:"inline_strip_words_case_insensitive"`
}

// DictNotePromotionConfig is dict_note_promotion.yaml: promotes untyped note
// attributes to typed ones (e.g. note "WEB" -> source_type "WEBRip").
type DictNotePromotionConfig struct {
	Version       string              `yaml:"version" json:"version"`
	UpdatedAt     string              `yaml:"updated_at" json:"updated_at"`
	Description   string              `yaml:"description" json:"description"`
	Defaults      map[string]any      `yaml:"defaults" json:"defaults"`
	Rules         []NotePromotionRule `yaml:"rules" json:"rules"`
	NoteWhitelist []string            `yaml:"note_whitelist" json:"note_whitelist"`
}

// NotePromotionRule promotes a matching note attribute to a typed attribute.
type NotePromotionRule struct {
	ID        string             `yaml:"id" json:"id"`
	Match     NotePromotionMatch `yaml:"match" json:"match"`
	PromoteTo NotePromotionTo    `yaml:"promote_to" json:"promote_to"`
}

// NotePromotionMatch selects the note values a rule applies to.
type NotePromotionMatch struct {
	NoteValuesCaseInsensitive []string `yaml:"note_values_case_insensitive" json:"note_values_case_insensitive"`
}

// NotePromotionTo is the typed attribute produced by a promotion;
// NormalizedFromToken takes the value from the note token instead.
type NotePromotionTo struct {
	Type                string `yaml:"type" json:"type"`
	Normalized          string `yaml:"normalized" json:"normalized"`
	NormalizedFromToken bool   `yaml:"normalized_from_token" json:"normalized_from_token"`
}

// GroupRouteConfig is group_route_overrides.yaml: per-group/layout overrides
// that force title, template, content kind and more.
type GroupRouteConfig struct {
	Version     string               `yaml:"version" json:"version"`
	UpdatedAt   string               `yaml:"updated_at" json:"updated_at"`
	Description string               `yaml:"description" json:"description"`
	Defaults    map[string]any       `yaml:"defaults" json:"defaults"`
	Overrides   []GroupRouteOverride `yaml:"overrides" json:"overrides"`
}

// GroupRouteOverride is one override; higher Priority matches first.
type GroupRouteOverride struct {
	ID       string              `yaml:"id" json:"id"`
	Enabled  bool                `yaml:"enabled" json:"enabled"`
	Priority int                 `yaml:"priority" json:"priority"`
	Match    GroupRouteMatch     `yaml:"match" json:"match"`
	Output   GroupRouteOutput    `yaml:"output" json:"output"`
	Guard    map[string][]string `yaml:"guard" json:"guard"`
}

// GroupRouteMatch selects the releases an override applies to.
type GroupRouteMatch struct {
	GroupIn                 []string `yaml:"group_in" json:"group_in"`
	RawMustContainAny       []string `yaml:"raw_must_contain_any" json:"raw_must_contain_any"`
	RequireEpisodeRange     bool     `yaml:"require_episode_range" json:"require_episode_range"`
	RequireEpisodeLike      bool     `yaml:"require_episode_like" json:"require_episode_like"`
	RequireEpisodeWithTitle bool     `yaml:"require_episode_with_title" json:"require_episode_with_title"`
	EpisodeTitleSepIn       []string `yaml:"episode_title_sep_in" json:"episode_title_sep_in"`
}

// GroupRouteOutput is the forced result of an override. ForceTemplateID may
// name a virtual template not present in the catalog.
type GroupRouteOutput struct {
	ForceGroup         string   `yaml:"force_group" json:"force_group"`
	ForceTitle         string   `yaml:"force_title" json:"force_title"`
	ClearAliases       bool     `yaml:"clear_aliases" json:"clear_aliases"`
	ForceTemplateID    string   `yaml:"force_template_id" json:"force_template_id"`
	ForceContentKind   string   `yaml:"force_content_kind" json:"force_content_kind"`
	ForceRouteID       string   `yaml:"force_route_id" json:"force_route_id"`
	TitleStripPrefixes []string `yaml:"title_strip_prefixes" json:"title_strip_prefixes"`
	AliasStripPrefixes []string `yaml:"alias_strip_prefixes" json:"alias_strip_prefixes"`
}

// RouteFamiliesConfig is route_families.yaml: narrows the eligible templates
// before template matching, by raw-title keywords, source type, etc.
type RouteFamiliesConfig struct {
	Version     string            `yaml:"version" json:"version"`
	UpdatedAt   string            `yaml:"updated_at" json:"updated_at"`
	Description string            `yaml:"description" json:"description"`
	Defaults    map[string]any    `yaml:"defaults" json:"defaults"`
	Families    []RouteFamilyRule `yaml:"families" json:"families"`
}

// RouteFamilyRule is one route family; higher Priority matches first.
type RouteFamilyRule struct {
	ID       string            `yaml:"id" json:"id"`
	Enabled  bool              `yaml:"enabled" json:"enabled"`
	Priority int               `yaml:"priority" json:"priority"`
	Match    RouteFamilyMatch  `yaml:"match" json:"match"`
	Select   RouteFamilySelect `yaml:"select" json:"select"`
}

// RouteFamilyMatch selects the releases a route family applies to.
type RouteFamilyMatch struct {
	RawMustContainAny    []string `yaml:"raw_must_contain_any" json:"raw_must_contain_any"`
	RawMustNotContainAny []string `yaml:"raw_must_not_contain_any" json:"raw_must_not_contain_any"`
	SourceTypeIn         []string `yaml:"source_type_in" json:"source_type_in"`
	NoteIn               []string `yaml:"note_in" json:"note_in"`
}

// RouteFamilySelect constrains template choice for a matched family.
type RouteFamilySelect struct {
	PreferredTemplateCategories []string `yaml:"preferred_template_categories" json:"preferred_template_categories"`
	StrictTemplateFilter        bool     `yaml:"strict_template_filter" json:"strict_template_filter"`
	AllowedRouteIDs             []string `yaml:"allowed_route_ids" json:"allowed_route_ids"`
}

// GroupSlot2PlatformConfig is group_slot2_platform_rules.yaml: per-group
// platform/language inference from the second bracket slot.
type GroupSlot2PlatformConfig struct {
	Version     string                         `yaml:"version" json:"version"`
	UpdatedAt   string                         `yaml:"updated_at" json:"updated_at"`
	Description string                         `yaml:"description" json:"description"`
	Rules       []GroupSlot2PlatformRuleConfig `yaml:"rules" json:"rules"`
}

// GroupSlot2PlatformRuleConfig is one slot2 rule; higher Priority matches
// first.
type GroupSlot2PlatformRuleConfig struct {
	ID        string                              `yaml:"id" json:"id"`
	Enabled   bool                                `yaml:"enabled" json:"enabled"`
	Priority  int                                 `yaml:"priority" json:"priority"`
	Match     GroupSlot2PlatformMatchConfig       `yaml:"match" json:"match"`
	Keywords  GroupSlot2PlatformKeywordsConfig    `yaml:"keywords" json:"keywords"`
	Whitelist []GroupSlot2PlatformWhitelistConfig `yaml:"platform_whitelist" json:"platform_whitelist"`
	Output    GroupSlot2PlatformOutputConfig      `yaml:"output" json:"output"`
}

// GroupSlot2PlatformMatchConfig selects the group and bracket slot a rule
// inspects (SlotIndex defaults to 2).
type GroupSlot2PlatformMatchConfig struct {
	GroupIn    []string `yaml:"group_in" json:"group_in"`
	GroupRegex string   `yaml:"group_regex" json:"group_regex"`
	SlotIndex  int      `yaml:"slot_index" json:"slot_index"`
}

// GroupSlot2PlatformKeywordsConfig lists the keyword classes used when
// interpreting the slot text (Cantonese, distributor, language metadata, and
// words to trim before inference).
type GroupSlot2PlatformKeywordsConfig struct {
	Yue          []string `yaml:"yue" json:"yue"`
	Distributor  []string `yaml:"distributor" json:"distributor"`
	LanguageMeta []string `yaml:"language_meta" json:"language_meta"`
	TrimForInfer []string `yaml:"trim_for_infer" json:"trim_for_infer"`
}

// GroupSlot2PlatformWhitelistConfig maps slot text containing any of
// ContainsAny to a normalized platform value.
type GroupSlot2PlatformWhitelistConfig struct {
	ContainsAny []string `yaml:"contains_any" json:"contains_any"`
	Normalized  string   `yaml:"normalized" json:"normalized"`
}

// GroupSlot2PlatformOutputConfig controls what a matched rule emits.
type GroupSlot2PlatformOutputConfig struct {
	EmitAudioLang             string  `yaml:"emit_audio_lang" json:"emit_audio_lang"`
	NoteOnDistributor         string  `yaml:"note_on_distributor" json:"note_on_distributor"`
	InferUnknownPlatform      bool    `yaml:"infer_unknown_platform" json:"infer_unknown_platform"`
	UnknownPlatformConfidence float64 `yaml:"unknown_platform_confidence" json:"unknown_platform_confidence"`
}

// PackProfileRulesConfig is pack_profile_rules.yaml: infers a pack profile
// (e.g. movie_series_mix) for batch content.
type PackProfileRulesConfig struct {
	UpdatedAt   string                  `yaml:"updated_at" json:"updated_at"`
	Description string                  `yaml:"description" json:"description"`
	Rules       []PackProfileRuleConfig `yaml:"rules" json:"rules"`
}

// PackProfileRuleConfig is one pack-profile rule; higher Priority matches
// first. Emit is the profile value.
type PackProfileRuleConfig struct {
	ID       string                `yaml:"id" json:"id"`
	Enabled  bool                  `yaml:"enabled" json:"enabled"`
	Priority int                   `yaml:"priority" json:"priority"`
	When     PackProfileWhenConfig `yaml:"when" json:"when"`
	Emit     string                `yaml:"emit" json:"emit"`
}

// PackProfileWhenConfig is the condition under which a pack-profile rule
// fires; every set field must hold.
type PackProfileWhenConfig struct {
	ContentKindIn       []string `yaml:"content_kind_in" json:"content_kind_in"`
	PackKindIn          []string `yaml:"pack_kind_in" json:"pack_kind_in"`
	PackKindNotIn       []string `yaml:"pack_kind_not_in" json:"pack_kind_not_in"`
	RequiresContentsAll []string `yaml:"requires_contents_all" json:"requires_contents_all"`
	RequiresContentsAny []string `yaml:"requires_contents_any" json:"requires_contents_any"`
	ForbidContentsAny   []string `yaml:"forbid_contents_any" json:"forbid_contents_any"`
	HasSeason           *bool    `yaml:"has_season" json:"has_season"`
	HasSeasonRange      *bool    `yaml:"has_season_range" json:"has_season_range"`
	MinContentsDistinct int      `yaml:"min_contents_distinct" json:"min_contents_distinct"`
}
