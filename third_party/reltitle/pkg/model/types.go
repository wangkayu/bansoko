package model

import "strings"

// AttrType names the category of an Attribute, e.g. "title", "group",
// "resolution". See the Attr* constants for the full set.
type AttrType string

// EPKind names the shape of an episode/season conclusion: a plain episode, a
// season, a range, a list, or a special. See the EPKind* constants for the
// full set.
type EPKind string

// EPAxis names the numbering axis of an episode conclusion: SE (seasonal),
// ABS (absolute), TV, OVA, SP, or UNKNOWN. Note EPEntity.Axis may carry
// values beyond this set when derived from SP-kind tokens (e.g. "OAD",
// "ONA", "EX"); compare with Canonical and the EPAxis* constants.
type EPAxis string

// ContentKind names the coarse content classification of a release: a TV
// episode, a movie, a batch pack, a special, or unknown. See the ContentKind*
// constants for the full set.
type ContentKind string

// AudioSource names the pipeline stage that produced an AudioTrack. See the
// AudioSource* constants for the full set.
type AudioSource string

// EPSource names the pipeline stage that produced an episode conclusion
// (EPEntity, EPReference, and the EPSemantics legacy mirror). See the
// EPSource* constants for the full set.
type EPSource string

// AttrSource names the pipeline stage that produced an Attribute. See the
// AttrSource* constants for the full set.
type AttrSource string

// Attribute is one recognized piece of a release title: a typed, normalized
// value with the token span it came from, a heuristic confidence, and the
// pipeline stage that produced it (Source, e.g. "lexicon", "canonical",
// "heuristic"). TokenStart/TokenEnd are -1 when no span applies.
type Attribute struct {
	Type       AttrType   `json:"type"`
	Normalized string     `json:"normalized,omitempty"`
	TokenStart int        `json:"token_start,omitempty"`
	TokenEnd   int        `json:"token_end,omitempty"`
	Confidence float64    `json:"confidence,omitempty"`
	Source     AttrSource `json:"source,omitempty"`
}

// Token is one lexical piece of the raw title produced by tokenize.Tokenize.
// Removed means some extractor has claimed the token; it does NOT mean later
// stages can ignore it (several stages intentionally read consumed tokens).
type Token struct {
	Text    string `json:"text"`
	Removed bool   `json:"removed,omitempty"`

	// Protected marks tokens that belong to a known title span (from title_knowledge.yaml).
	// Episode/season/range detection must never reinterpret them.
	Protected bool `json:"-"`
}

// ParseResult is the full parse output for one release title. The CLI
// serializes it as indented JSON; empty fields are omitted. Episode-related
// attributes are not serialized into Attributes — they live in EPSemantics.
type ParseResult struct {
	RawTitle           string       `json:"raw_title"`
	ContentKind        ContentKind  `json:"content_kind,omitempty"`
	ContentConfidence  float64      `json:"content_confidence,omitempty"`
	TemplateID         string       `json:"template_id,omitempty"`
	TemplateConfidence float64      `json:"template_confidence,omitempty"`
	EPSemantics        *EPSemantics `json:"ep_semantics,omitempty"`
	AudioSlot          *AudioSlot   `json:"audio_slot,omitempty"`
	Attributes         []Attribute  `json:"attributes,omitempty"`
	Tokens             []Token      `json:"tokens,omitempty"`
}

// EPReference is one episode/season/range conclusion: the primary parse
// result or an alternate candidate. TokenStart/TokenEnd are internal only.
type EPReference struct {
	Kind        EPKind   `json:"kind,omitempty"` // NONE | SEASON | EP | EP_LIST | RANGE | SEASON_RANGE | SP
	Axis        EPAxis   `json:"axis,omitempty"` // SE | ABS | TV | OVA | SP | UNKNOWN
	Season      string   `json:"season,omitempty"`
	SeasonRange string   `json:"season_range,omitempty"` // e.g. S1-S3
	Episode     string   `json:"episode,omitempty"`
	EpisodeList []string `json:"episode_list,omitempty"`
	RangeStart  string   `json:"range_start,omitempty"`
	RangeEnd    string   `json:"range_end,omitempty"`
	SPKind      string   `json:"sp_kind,omitempty"`
	Relation    string   `json:"relation,omitempty"` // overall | catalog | alt_cut | etc
	Source      EPSource `json:"source,omitempty"`

	TokenStart int `json:"-"`
	TokenEnd   int `json:"-"`
}

// EPSemantics carries the episode-related outcome of a parse: Primary is the
// chosen conclusion, Alternates the rejected candidates.
type EPSemantics struct {
	Primary    *EPReference  `json:"primary,omitempty"`
	Alternates []EPReference `json:"alternates,omitempty"`
}

// EPSlot is the internal, pre-output form of EPSemantics used while the
// parser adjudicates between episode candidates.
type EPSlot struct {
	Primary    *EPEntity  `json:"primary,omitempty"`
	Candidates []EPEntity `json:"candidates,omitempty"`
}

// EPEntity represents one episode-like candidate. It is intentionally small and
// stable, so we can support composite epslot without encoding semantics into
// Attribute.Normalized strings.
type EPEntity struct {
	Kind         EPKind   `json:"kind,omitempty"` // EP | EP_LIST | RANGE | SP
	Axis         EPAxis   `json:"axis,omitempty"` // SE | ABS | TV | OVA | SP | UNKNOWN
	Season       string   `json:"season,omitempty"`
	Episode      string   `json:"episode,omitempty"`
	EpisodeRange string   `json:"episode_range,omitempty"`
	EpisodeList  string   `json:"episode_list,omitempty"` // comma-separated list, e.g. "804,805"
	SPKind       string   `json:"sp_kind,omitempty"`      // OVA/OAD/ONA/EX/SP
	TokenStart   int      `json:"token_start,omitempty"`
	TokenEnd     int      `json:"token_end,omitempty"`
	Source       EPSource `json:"source,omitempty"` // sxxexx/episode/episode_range/sp_kind/etc
}

// AudioSlot is the recognized set of audio tracks on the release.
type AudioSlot struct {
	Tracks []AudioTrack `json:"tracks,omitempty"`
}

// AudioTrack describes one audio track: language, codec, channel layout and
// object audio (e.g. ATMOS), with the token span and producing stage.
type AudioTrack struct {
	Language   string      `json:"language,omitempty"`
	Codec      string      `json:"codec,omitempty"`
	Channels   string      `json:"channels,omitempty"`
	Object     string      `json:"object,omitempty"` // ATMOS etc
	TokenStart int         `json:"token_start,omitempty"`
	TokenEnd   int         `json:"token_end,omitempty"`
	Source     AudioSource `json:"source,omitempty"` // tech_audio_slot | audio_slot_reconcile
}

const (
	AttrTitle           AttrType = "title"
	AttrTitleAlias      AttrType = "title_alias"
	AttrSearchHint      AttrType = "search_hint"
	AttrGroup           AttrType = "group"
	AttrEpisode         AttrType = "episode"
	AttrEpisodeList     AttrType = "episode_list"
	AttrEpisodeRange    AttrType = "episode_range"
	AttrSeason          AttrType = "season"
	AttrSeasonRange     AttrType = "season_range"
	AttrVersion         AttrType = "version"
	AttrEdition         AttrType = "edition"
	AttrReleaseStatus   AttrType = "release_status"
	AttrPackKind        AttrType = "pack_kind"
	AttrPackProfile     AttrType = "pack_profile"
	AttrSPKind          AttrType = "sp_kind"
	AttrYear            AttrType = "year"
	AttrMovieIndex      AttrType = "movie_index"
	AttrMovieIndexRange AttrType = "movie_index_range"
	AttrMovieFlag       AttrType = "movie_flag"
	AttrSpecialFlag     AttrType = "special_flag"
	AttrSpecialIndex    AttrType = "special_index"
	AttrContents        AttrType = "contents"

	AttrSourceType     AttrType = "source_type"
	AttrResolution     AttrType = "resolution"
	AttrVideoCodec     AttrType = "video_codec"
	AttrAudioCodec     AttrType = "audio_codec"
	AttrBitDepth       AttrType = "bit_depth"
	AttrContainer      AttrType = "container"
	AttrPlatform       AttrType = "platform"
	AttrPlatformRegion AttrType = "platform_region"
	AttrOriginRegion   AttrType = "origin_region"
	AttrHDR            AttrType = "hdr"
	AttrCRC            AttrType = "crc"
	AttrTech           AttrType = "tech"
	AttrTailGroup      AttrType = "tail_group"

	AttrNote          AttrType = "note"
	AttrNoise         AttrType = "noise"
	AttrSubFlag       AttrType = "sub_flag"
	AttrSubLang       AttrType = "sub_lang"
	AttrSubFormat     AttrType = "sub_format"
	AttrSubLangGroup  AttrType = "sub_lang_group"
	AttrAudioFlag     AttrType = "audio_flag"
	AttrAudioLang     AttrType = "audio_lang"
	AttrAudioChannels AttrType = "audio_channels"
	AttrAudioObject   AttrType = "audio_object"
	AttrContentRating AttrType = "content_rating"
)

const (
	EPKindNone        EPKind = "NONE"
	EPKindSeason      EPKind = "SEASON"
	EPKindEP          EPKind = "EP"
	EPKindEPList      EPKind = "EP_LIST"
	EPKindRange       EPKind = "RANGE"
	EPKindSeasonRange EPKind = "SEASON_RANGE"
	EPKindSP          EPKind = "SP"
)

const (
	EPAxisSE      EPAxis = "SE"
	EPAxisABS     EPAxis = "ABS"
	EPAxisTV      EPAxis = "TV"
	EPAxisOVA     EPAxis = "OVA"
	EPAxisSP      EPAxis = "SP"
	EPAxisUnknown EPAxis = "UNKNOWN"
)

// Canonical returns the normalized form of k: trimmed and uppercased. Values
// may arrive lowercase or padded from defensive normalization paths; compare
// with Canonical against the EPKind* constants.
func (k EPKind) Canonical() EPKind {
	return EPKind(strings.ToUpper(strings.TrimSpace(string(k))))
}

// Canonical returns the normalized form of a: trimmed and uppercased. Values
// may arrive lowercase or padded from defensive normalization paths; compare
// with Canonical against the EPAxis* constants.
func (a EPAxis) Canonical() EPAxis {
	return EPAxis(strings.ToUpper(strings.TrimSpace(string(a))))
}

const (
	ContentKindTVEP    ContentKind = "TV_EP"
	ContentKindMovie   ContentKind = "MOVIE"
	ContentKindBatch   ContentKind = "BATCH"
	ContentKindSpecial ContentKind = "SPECIAL"
	ContentKindUnknown ContentKind = "UNKNOWN"
)

const (
	AudioSourceTechAudioSlot      AudioSource = "tech_audio_slot"
	AudioSourceAudioSlotReconcile AudioSource = "audio_slot_reconcile"
)

// Canonical returns the normalized form of s: trimmed and lowercased. Source
// values are lowercase snake_case by convention; compare with Canonical
// against the AudioSource* constants.
func (s AudioSource) Canonical() AudioSource {
	return AudioSource(strings.ToLower(strings.TrimSpace(string(s))))
}

const (
	EPSourceSxxExx             EPSource = "sxxexx"
	EPSourceEpisode            EPSource = "episode"
	EPSourceEpisodeList        EPSource = "episode_list"
	EPSourceEpisodeRange       EPSource = "episode_range"
	EPSourceSPKind             EPSource = "sp_kind"
	EPSourceSPToken            EPSource = "sp_token"
	EPSourceTaggedEP           EPSource = "tagged_ep"
	EPSourceTaggedRange        EPSource = "tagged_range"
	EPSourceRangeToken         EPSource = "range_token"
	EPSourceEPDigitToken       EPSource = "ep_digit_token"
	EPSourceCanonical          EPSource = "canonical"
	EPSourceGroupRouteOverride EPSource = "group_route_override"

	EPSourceSxxExxFinal       EPSource = "sxxexx_final"
	EPSourceEpisodeFinal      EPSource = "episode_final"
	EPSourceEpisodeListFinal  EPSource = "episode_list_final"
	EPSourceEpisodeRangeFinal EPSource = "episode_range_final"
	EPSourceTaggedEPFinal     EPSource = "tagged_ep_final"
	EPSourceTaggedRangeFinal  EPSource = "tagged_range_final"
	EPSourceSPKindFinal       EPSource = "sp_kind_final"

	EPSourceParenAltEpisode       EPSource = "paren_alt_episode"
	EPSourceParenAltRange         EPSource = "paren_alt_range"
	EPSourceSummaryCatalogParen   EPSource = "summary_catalog_paren"
	EPSourceSummaryEpisodePrimary EPSource = "summary_episode_primary"
)

// Canonical returns the normalized form of s: trimmed and lowercased. Source
// values are lowercase snake_case by convention; compare with Canonical
// against the EPSource* constants.
func (s EPSource) Canonical() EPSource {
	return EPSource(strings.ToLower(strings.TrimSpace(string(s))))
}

const (
	AttrSourceLexicon                        AttrSource = "lexicon"
	AttrSourceCanonical                      AttrSource = "canonical"
	AttrSourceHeuristic                      AttrSource = "heuristic"
	AttrSourceHeuristicRaw                   AttrSource = "heuristic_raw"
	AttrSourceHeuristicMultiAudio            AttrSource = "heuristic_multi_audio"
	AttrSourceEditionToken                   AttrSource = "edition_token"
	AttrSourceVersionToken                   AttrSource = "version_token"
	AttrSourceYearToken                      AttrSource = "year_token"
	AttrSourceCRCToken                       AttrSource = "crc_token"
	AttrSourceFileExtension                  AttrSource = "file_extension"
	AttrSourceTechComboSpan                  AttrSource = "tech_combo_span"
	AttrSourceTechComboToken                 AttrSource = "tech_combo_token"
	AttrSourceTechAudioSlot                  AttrSource = "tech_audio_slot"
	AttrSourceTechSubs                       AttrSource = "tech_subs"
	AttrSourceHintKeyword                    AttrSource = "hint_keyword"
	AttrSourceRawAudioMeta                   AttrSource = "raw_audio_meta"
	AttrSourceRawSubMeta                     AttrSource = "raw_sub_meta"
	AttrSourceGroupRouteOverride             AttrSource = "group_route_override"
	AttrSourceGroupSubsX2                    AttrSource = "group_subs_x2"
	AttrSourceNotePromoteDict                AttrSource = "note_promote_dict"
	AttrSourceNotePromoteContext             AttrSource = "note_promote_context"
	AttrSourceTemplateSignalTrace            AttrSource = "template_signal_trace"
	AttrSourceTemplateSignalTechBeforeEP     AttrSource = "template_signal_tech_before_ep"
	AttrSourceTemplateFloatInfer             AttrSource = "template_float_infer"
	AttrSourceRouteStreamingReleaseV1        AttrSource = "route_streaming_release_v1"
	AttrSourceSpecialIndex                   AttrSource = "special_index"
	AttrSourceSpecialSummary                 AttrSource = "special_summary"
	AttrSourcePackExtras                     AttrSource = "pack_extras"
	AttrSourcePackSub                        AttrSource = "pack_sub"
	AttrSourcePackAudio                      AttrSource = "pack_audio"
	AttrSourcePackContents                   AttrSource = "pack_contents"
	AttrSourcePackPlusContents               AttrSource = "pack_plus_contents"
	AttrSourcePackNote                       AttrSource = "pack_note"
	AttrSourcePackKindFallback               AttrSource = "pack_kind_fallback"
	AttrSourceSubtitleNormalize              AttrSource = "subtitle_normalize"
	AttrSourceTVRangeFin                     AttrSource = "tv_range_fin"
	AttrSourceSPKind                         AttrSource = "sp_kind"
	AttrSourceSeasonKeyword                  AttrSource = "season_keyword"
	AttrSourceSxxExx                         AttrSource = "sxxexx"
	AttrSourceEpisode                        AttrSource = "episode"
	AttrSourceEpisodeRange                   AttrSource = "episode_range"
	AttrSourceEpisodeList                    AttrSource = "episode_list"
	AttrSourceEpisodeEndflag                 AttrSource = "episode_endflag"
	AttrSourceEpisodeVersion                 AttrSource = "episode_version"
	AttrSourceEPDigitToken                   AttrSource = "ep_digit_token"
	AttrSourceRangeToken                     AttrSource = "range_token"
	AttrSourceTaggedEP                       AttrSource = "tagged_ep"
	AttrSourceTaggedRange                    AttrSource = "tagged_range"
	AttrSourceMainTitleSeason                AttrSource = "main_title_season"
	AttrSourceAliasSeason                    AttrSource = "alias_season"
	AttrSourceResolutionWxH                  AttrSource = "resolution_wxh"
	AttrSourceCodecDashed                    AttrSource = "codec_dashed"
	AttrSourceAudioCodecStandalone           AttrSource = "audio_codec_standalone"
	AttrSourceBitDepthLoose                  AttrSource = "bit_depth_loose"
	AttrSourceSourceStandalone               AttrSource = "source_standalone"
	AttrSourceRegionRestriction              AttrSource = "region_restriction"
	AttrSourceTailMeta                       AttrSource = "tail_meta"
	AttrSourceDVDRipTag                      AttrSource = "dvdrip_tag"
	AttrSourceCustomTag                      AttrSource = "custom_tag"
	AttrSourceBracketMIndex                  AttrSource = "bracket_m_index"
	AttrSourceBracketMovieContextIndex       AttrSource = "bracket_movie_context_index"
	AttrSourceBracketInlineMovieIndex        AttrSource = "bracket_inline_movie_index"
	AttrSourceBracketInlineMovieIndexRange   AttrSource = "bracket_inline_movie_index_range"
	AttrSourceTemplateSignal                 AttrSource = "template_signal"
	AttrSourceTagCode                        AttrSource = "tag_code"
	AttrSourceTagCodeAudioLang               AttrSource = "tag_code_audio_lang"
	AttrSourceParenContentsMarker            AttrSource = "paren_contents_marker"
	AttrSourceParenSearchHint                AttrSource = "paren_search_hint"
	AttrSourceTailParenSearchHint            AttrSource = "tail_paren_search_hint"
	AttrSourceEPTailSearchHint               AttrSource = "ep_tail_search_hint"
	AttrSourceAliasMetaDemote                AttrSource = "alias_meta_demote"
	AttrSourceAliasContentsMarker            AttrSource = "alias_contents_marker"
	AttrSourceRawParenContentsMarker         AttrSource = "raw_paren_contents_marker"
	AttrSourceTitleCleanup                   AttrSource = "title_cleanup"
	AttrSourceGroupSlot2Platform             AttrSource = "group_slot2_platform"
	AttrSourceGroupSlot2PlatformInferUnknown AttrSource = "group_slot2_platform_infer_unknown"
	AttrSourceEpisodeRangePackSuffix         AttrSource = "episode_range_pack_suffix"
	AttrSourceEpisodeRangePackSuffixEmbedded AttrSource = "episode_range_pack_suffix_embedded"
	AttrSourceEpisodeRangePackSuffixCount    AttrSource = "episode_range_pack_suffix_count"
	AttrSourceEpisodeRangePackSuffixSplit    AttrSource = "episode_range_pack_suffix_split"
)

// Canonical returns the normalized form of s: trimmed and lowercased. Source
// values are lowercase snake_case by convention; compare with Canonical
// against the AttrSource* constants.
func (s AttrSource) Canonical() AttrSource {
	return AttrSource(strings.ToLower(strings.TrimSpace(string(s))))
}

// Canonical returns the normalized form of c: trimmed and uppercased. Values
// may arrive lowercase or padded from defensive normalization paths or config;
// compare with Canonical against the ContentKind* constants.
func (c ContentKind) Canonical() ContentKind {
	return ContentKind(strings.ToUpper(strings.TrimSpace(string(c))))
}
