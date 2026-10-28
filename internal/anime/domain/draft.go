// Package domain holds the single-anime invariants: draft validation,
// the sole metadata hold, section override resolution, and the pure
// parser/evaluator/renderer pipeline shared by preview and execution.
package domain

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"bansoko/internal/redact"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// FieldViolation is one typed draft problem. Code values follow the shared
// contract error matrix (DRAFT_INVALID, RSS_SOURCE_REQUIRED,
// TMDB_BINDING_INCOMPLETE, RSS_LOCATOR_UNSAFE, OVERRIDE_MODE_INVALID).
type FieldViolation struct {
	Field   string
	Code    string
	Message string
}

// DecodeDraftJSON decodes a persisted draft without discarding unknown fields
// and applies the same complete typed validation used for writes.
func DecodeDraftJSON(raw []byte) (AnimeDraft, error) {
	var draft AnimeDraft
	if err := decodeStrictJSON(raw, &draft); err != nil {
		return AnimeDraft{}, err
	}
	if violations := ValidateStoredDraft(draft); len(violations) > 0 {
		return AnimeDraft{}, fmt.Errorf("%s: %s", violations[0].Code, violations[0].Message)
	}
	return draft, nil
}

// ValidateStoredDraft is the read-back form of ValidateDraft for a draft that
// was already accepted (a candidate body, a saved revision's draft): a
// tag_filters title value is checked for shape and dimension only, so a
// reltitle dictionary refresh never invalidates a saved draft. Every other
// rule is the write rule.
func ValidateStoredDraft(draft AnimeDraft) []FieldViolation {
	tagFilters := draft.Rules.Sections.TagFilters
	draft.Rules.Sections.TagFilters = nil
	violations := ValidateDraft(draft)
	return append(violations, ValidateStoredTagFiltersOverride(tagFilters)...)
}

// ValidateDraft checks the complete in-memory anime draft. Dynamic provider
// drafts may have no RSS sources yet; a custom draft must have exactly one
// custom_rss source. Locator values remain opaque in storage; transport safety
// is evaluated only when preview or refresh actually dials a source.
func ValidateDraft(draft AnimeDraft) []FieldViolation {
	var violations []FieldViolation
	// tag_filters is preset rules configuration like the release filter: a
	// draft may carry it (validated below with the owner write rules through
	// ValidateRulesSections). It stays outside the six frozen sections; the
	// saved revision carries it in its draft body and subscription writes it
	// as the new instance's override.
	if draft.Origin != SubscriptionOriginAnibt && draft.Origin != SubscriptionOriginMikan && draft.Origin != SubscriptionOriginCustom && draft.Origin != SubscriptionOriginOneshot {
		violations = append(violations, FieldViolation{Field: "origin", Code: "DRAFT_INVALID", Message: "origin must be anibt, mikan, custom, or agent"})
	}
	if strings.TrimSpace(draft.Title) == "" || len(draft.Title) > 512 {
		violations = append(violations, FieldViolation{Field: "title", Code: "DRAFT_INVALID", Message: "title is required and must be at most 512 characters"})
	}
	if draft.OriginEntryRef != nil && (strings.TrimSpace(*draft.OriginEntryRef) == "" || len(*draft.OriginEntryRef) > 256) {
		violations = append(violations, FieldViolation{Field: "origin_entry_ref", Code: "DRAFT_INVALID", Message: "origin_entry_ref must be non-empty and at most 256 characters when provided"})
	}
	if (draft.Origin == SubscriptionOriginCustom || draft.Origin == SubscriptionOriginOneshot) && draft.OriginEntryRef != nil {
		violations = append(violations, FieldViolation{Field: "origin_entry_ref", Code: "DRAFT_INVALID", Message: "custom and oneshot drafts must not carry an origin_entry_ref"})
	}
	if (draft.Origin == SubscriptionOriginAnibt || draft.Origin == SubscriptionOriginMikan) && (draft.OriginEntryRef == nil || strings.TrimSpace(*draft.OriginEntryRef) == "") {
		violations = append(violations, FieldViolation{Field: "origin_entry_ref", Code: "DRAFT_INVALID", Message: "anibt and mikan drafts must carry an origin_entry_ref"})
	}
	if draft.TitleOverride != nil && (strings.TrimSpace(*draft.TitleOverride) == "" || len(*draft.TitleOverride) > 512) {
		violations = append(violations, FieldViolation{Field: "title_override", Code: "DRAFT_INVALID", Message: "title_override must be non-empty and at most 512 characters when provided"})
	}
	if draft.InstanceLabel != nil && (strings.TrimSpace(*draft.InstanceLabel) == "" || len(*draft.InstanceLabel) > 256) {
		violations = append(violations, FieldViolation{Field: "instance_label", Code: "DRAFT_INVALID", Message: "instance_label must be non-empty and at most 256 characters when provided"})
	}
	if (draft.Origin == SubscriptionOriginCustom || draft.Origin == SubscriptionOriginOneshot) && (draft.InstanceLabel == nil || strings.TrimSpace(*draft.InstanceLabel) == "") {
		violations = append(violations, FieldViolation{Field: "instance_label", Code: "DRAFT_INVALID", Message: "custom and oneshot subscriptions require an instance_label"})
	}
	if draft.Metadata.Bgm != nil && strings.TrimSpace(draft.Metadata.Bgm.WorkId) == "" {
		violations = append(violations, FieldViolation{Field: "metadata.bgm", Code: "DRAFT_INVALID", Message: "bgm work_id must be non-empty"})
	}
	if draft.Metadata.Tmdb != nil {
		tmdb := draft.Metadata.Tmdb
		if strings.TrimSpace(tmdb.WorkId) == "" || (tmdb.MediaKind != TMDBMediaKindTv && tmdb.MediaKind != TMDBMediaKindMovie) || tmdb.Season < 0 {
			violations = append(violations, FieldViolation{Field: "metadata.tmdb", Code: "TMDB_BINDING_INCOMPLETE", Message: "tmdb requires work_id, media_kind, and season together"})
		}
	}
	// media_kind_override mirrors title_override: nil means unset (no pin),
	// and a provided value must be the exact frozen media-kind vocabulary.
	if draft.Metadata.MediaKindOverride != nil {
		// No trimming: the persisted column is CHECKed against the exact
		// vocabulary, so an accept here must be byte-for-byte storable.
		if *draft.Metadata.MediaKindOverride != string(TMDBMediaKindTv) && *draft.Metadata.MediaKindOverride != string(TMDBMediaKindMovie) {
			violations = append(violations, FieldViolation{Field: "metadata.media_kind_override", Code: "DRAFT_INVALID", Message: "media_kind_override must be tv or movie when provided"})
		}
	}
	violations = append(violations, validateAnimeYear("metadata.year", draft.Metadata.Year)...)
	violations = append(violations, ValidateAnimeSeason("metadata.season", draft.Metadata.Season)...)
	if len(draft.RssSources) > 100 {
		violations = append(violations, FieldViolation{Field: "rss_sources", Code: "DRAFT_INVALID", Message: "at most 100 RSS sources are allowed"})
	}
	if draft.Origin == SubscriptionOriginCustom && len(draft.RssSources) != 1 {
		violations = append(violations, FieldViolation{Field: "rss_sources", Code: "RSS_SOURCE_REQUIRED", Message: "custom subscriptions require exactly one RSS source"})
	}
	if (draft.Origin == SubscriptionOriginAnibt || draft.Origin == SubscriptionOriginMikan) && len(draft.RssSources) != 0 {
		violations = append(violations, FieldViolation{Field: "rss_sources", Code: "DRAFT_INVALID", Message: "provider subscriptions carry no RSS sources in the draft; subgroups selection supplies the provider_group sources after discovery"})
	}
	for index, source := range draft.RssSources {
		field := fmt.Sprintf("rss_sources[%d]", index)
		if source.SourceKind != SourceKindCustomRss && source.SourceKind != SourceKindProviderGroup {
			violations = append(violations, FieldViolation{Field: field + ".source_kind", Code: "DRAFT_INVALID", Message: "source_kind must be provider_group or custom_rss"})
		}
		if strings.TrimSpace(source.Locator) == "" || len(source.Locator) > 4096 {
			violations = append(violations, FieldViolation{Field: field + ".locator", Code: "RSS_LOCATOR_UNSAFE", Message: "locator is required and must be at most 4096 characters"})
		}
		if source.Locator == redact.SecretPlaceholder {
			violations = append(violations, FieldViolation{Field: field + ".locator", Code: SecretPlaceholderUnresolved, Message: "locator is the secret placeholder and no stored source matches it; send the real locator"})
		}

		if source.SourceKind == SourceKindCustomRss && draft.Origin != SubscriptionOriginCustom && draft.Origin != SubscriptionOriginOneshot {
			violations = append(violations, FieldViolation{Field: field + ".source_kind", Code: "DRAFT_INVALID", Message: "custom_rss sources require a custom or agent subscription origin"})
		}
		if source.SourceKind == SourceKindProviderGroup && (draft.Origin == SubscriptionOriginCustom || draft.Origin == SubscriptionOriginOneshot) {
			violations = append(violations, FieldViolation{Field: field + ".source_kind", Code: "DRAFT_INVALID", Message: "custom and oneshot subscriptions require a custom_rss source"})
		}
		for name, value := range map[string]*string{
			"http_feed_connection_ref": source.HttpFeedConnectionRef,
			"network_route_ref":        source.NetworkRouteRef,
		} {
			if value != nil && (strings.TrimSpace(*value) == "" || len(*value) > 256) {
				violations = append(violations, FieldViolation{Field: field + "." + name, Code: "DRAFT_INVALID", Message: name + " must be non-empty and at most 256 characters when provided"})
			}
			if value != nil && source.SourceKind != SourceKindCustomRss {
				violations = append(violations, FieldViolation{Field: field + "." + name, Code: "DRAFT_INVALID", Message: name + " is only valid for custom_rss sources"})
			}
		}
		if source.LocatorDisplay != nil && (strings.TrimSpace(*source.LocatorDisplay) == "" || len(*source.LocatorDisplay) > 4096) {
			violations = append(violations, FieldViolation{Field: field + ".locator_display", Code: "DRAFT_INVALID", Message: "locator_display must be non-empty and at most 4096 characters when provided"})
		}
		if !EpisodeOffsetInRange(source.EpisodeOffset) {
			violations = append(violations, FieldViolation{Field: field + ".episode_offset", Code: "DRAFT_INVALID", Message: EpisodeOffsetRangeMessage()})
		}
		if source.AnibtAuxMode != nil {
			switch *source.AnibtAuxMode {
			case RSSSourceDraftAnibtAuxModeDisabled, RSSSourceDraftAnibtAuxModeFillMissing, RSSSourceDraftAnibtAuxModeOverride:
			default:
				violations = append(violations, FieldViolation{Field: field + ".anibt_aux_mode", Code: "DRAFT_INVALID", Message: "anibt_aux_mode must be disabled, fill_missing, or override"})
			}
			if source.SourceKind != SourceKindProviderGroup || source.ProviderProvenance == nil || source.ProviderProvenance.Provider != ProviderKindAnibt {
				violations = append(violations, FieldViolation{Field: field + ".anibt_aux_mode", Code: "DRAFT_INVALID", Message: "anibt_aux_mode is only valid for AniBT provider sources"})
			}
		}
		if strings.TrimSpace(source.GroupName) == "" || len(source.GroupName) > 256 {
			violations = append(violations, FieldViolation{Field: field + ".group_name", Code: "DRAFT_INVALID", Message: "group_name is required and must be at most 256 characters"})
		}
		if strings.TrimSpace(source.Title) == "" || len(source.Title) > 512 {
			violations = append(violations, FieldViolation{Field: field + ".title", Code: "DRAFT_INVALID", Message: "title is required and must be at most 512 characters"})
		}
		if source.ProviderProvenance != nil {
			provenance := source.ProviderProvenance
			if (provenance.Provider != ProviderKindAnibt && provenance.Provider != ProviderKindMikan) ||
				strings.TrimSpace(provenance.ProviderEntryRef) == "" || len(provenance.ProviderEntryRef) > 256 ||
				strings.TrimSpace(provenance.ProviderGroupRef) == "" || len(provenance.ProviderGroupRef) > 256 ||
				strings.TrimSpace(provenance.DiscoverySnapshotVersion) == "" || len(provenance.DiscoverySnapshotVersion) > 128 {
				violations = append(violations, FieldViolation{Field: field + ".provider_provenance", Code: "DRAFT_INVALID", Message: "provider provenance requires provider, entry ref, group ref, and snapshot version"})
			}
		}
	}
	if sectionViolations := ValidateRulesSections(draft.Rules.Sections); len(sectionViolations) > 0 {
		violations = append(violations, sectionViolations...)
	}
	if sectionViolations := ValidateSubgroupsSection(draft.Subgroups); len(sectionViolations) > 0 {
		violations = append(violations, sectionViolations...)
	}
	return violations
}

// ValidateMetadataPatch checks the owner-scoped metadata replacement. TMDB is
// atomic: either null or a complete tuple. total_episodes is an optional
// nullable field; a non-nil value must be valid.
func ValidateMetadataPatch(bgm *NullableBGMBinding, tmdb *NullableTMDBBinding, titleOverride *string, mediaKindOverride *string, totalEpisodes *int, year ...*int) []FieldViolation {
	var violations []FieldViolation
	if bgm != nil && strings.TrimSpace(bgm.WorkId) == "" {
		violations = append(violations, FieldViolation{Field: "bgm", Code: "DRAFT_INVALID", Message: "bgm work_id must be non-empty"})
	}
	if tmdb != nil {
		if strings.TrimSpace(tmdb.WorkId) == "" || (tmdb.MediaKind != TMDBMediaKindTv && tmdb.MediaKind != TMDBMediaKindMovie) || tmdb.Season < 0 {
			violations = append(violations, FieldViolation{Field: "tmdb", Code: "TMDB_BINDING_INCOMPLETE", Message: "tmdb requires work_id, media_kind, and season together"})
		}
	}
	// title_override is the anime's fixed title (written at subscribe): a
	// non-empty string replaces it, and nil (JSON null or an omitted field)
	// keeps the stored title; it cannot be cleared. A blank string is rejected.
	if titleOverride != nil && strings.TrimSpace(*titleOverride) == "" {
		violations = append(violations, FieldViolation{Field: "title_override", Code: "DRAFT_INVALID", Message: "title_override must be non-empty when provided"})
	}
	if titleOverride != nil && len(*titleOverride) > 512 {
		violations = append(violations, FieldViolation{Field: "title_override", Code: "DRAFT_INVALID", Message: "title_override must be at most 512 characters when provided"})
	}
	// total_episodes: a non-nil value (including 0) replaces the column;
	// negative values are rejected so the column never stores an impossible count.
	if totalEpisodes != nil && *totalEpisodes < 0 {
		violations = append(violations, FieldViolation{Field: "total_episodes", Code: "DRAFT_INVALID", Message: "total_episodes must be >= 0 when provided"})
	}
	// media_kind_override follows the same complete-owner-replacement contract as
	// title_override: nil (JSON null, or an omitted field on a full replacement)
	// clears the pin, and a provided value must be tv or movie.
	if mediaKindOverride != nil {
		// No trimming, for the same reason as the draft validation: the column
		// CHECK compares the exact stored bytes.
		if *mediaKindOverride != string(TMDBMediaKindTv) && *mediaKindOverride != string(TMDBMediaKindMovie) {
			violations = append(violations, FieldViolation{Field: "media_kind_override", Code: "DRAFT_INVALID", Message: "media_kind_override must be tv or movie when provided; send null to clear"})
		}
	}
	// year follows the same complete-owner-replacement contract: nil clears it.
	for _, value := range year {
		violations = append(violations, validateAnimeYear("year", value)...)
	}
	return violations
}

// Owner-edited year bounds (anime_metadata.year CHECK).
const (
	MinAnimeYear = 1900
	MaxAnimeYear = 2100
)

// ValidateAnimeSeason checks the owner-edited season (anime_metadata.season
// CHECK): nil clears it, a value must be >= 0 (0 is the specials season).
func ValidateAnimeSeason(field string, season *int) []FieldViolation {
	if season == nil || *season >= 0 {
		return nil
	}
	return []FieldViolation{{Field: field, Code: "DRAFT_INVALID", Message: "season must be >= 0 when provided; send null to clear"}}
}

func validateAnimeYear(field string, year *int) []FieldViolation {
	if year == nil || (*year >= MinAnimeYear && *year <= MaxAnimeYear) {
		return nil
	}
	return []FieldViolation{{Field: field, Code: "DRAFT_INVALID", Message: "year must be between 1900 and 2100 when provided; send null to clear"}}
}

// MediaKind is the media kind an identity resolves with: the owner field
// (anime_metadata.media_kind_override, or a draft's media_kind_override),
// where unset is tv. The TMDB kind is never read: a metadata binding only
// locates the work. Only movie selects the movie branch; the writer validates
// the tv|movie vocabulary, so nothing else is ever stored.
func MediaKind(owner *string) string {
	if owner != nil && strings.TrimSpace(*owner) == string(TMDBMediaKindMovie) {
		return string(TMDBMediaKindMovie)
	}
	return string(TMDBMediaKindTv)
}

// BackfilledMediaKind is the owner media kind after the one-time backfill: a
// set owner value is kept; an unset one takes the kind of the bound TMDB
// tuple. Every path that binds a TMDB tuple applies it, so the owner value is
// set whenever a TMDB tuple is bound, and clearing or replacing the tuple
// later never changes it.
func BackfilledMediaKind(owner *string, tmdb *NullableTMDBBinding) *string {
	if owner != nil || tmdb == nil {
		return owner
	}
	switch tmdb.MediaKind {
	case TMDBMediaKindTv, TMDBMediaKindMovie:
		kind := string(tmdb.MediaKind)
		return &kind
	}
	return nil
}

// BackfilledMediaKind is the draft's owner media kind after the one-time
// backfill: what subscribing writes and what every draft-side identity
// resolves with. A stored draft keeps its own value; the rule is applied where
// the draft is read.
func (m AnimeMetadataDraft) BackfilledMediaKind() *string {
	return BackfilledMediaKind(m.MediaKindOverride, m.Tmdb)
}

// FrozenMediaBinding projects the owner media kind and the TMDB binding into
// the frozen media kind/season context one release resolves with. It mirrors
// the RSS snapshot projection exactly (`rss/application.frozenMediaBinding`
// delegates here) so a saved revision preview, a one-shot admission and an
// ordinary RSS evaluation can never disagree about the binding.
//
//   - The kind is MediaKind(mediaKindOverride): the owner value, unset is tv.
//     A movie kind short-circuits the episode identity resolver to the
//     canonical movie:0 identity. The TMDB kind is never read; a draft whose
//     kind is still unset applies BackfilledMediaKind before calling this.
//   - The season is the TMDB binding's season (0 when unbound), frozen for
//     naming only: an episode identity carries no season. SeasonBound is
//     false (the naming path keeps the historical season-1 default for a
//     bound season 0).
func FrozenMediaBinding(tmdb *NullableTMDBBinding, mediaKindOverride *string) (mediaKind string, season int, seasonBound bool) {
	if tmdb != nil {
		season = tmdb.Season
	}
	return MediaKind(mediaKindOverride), season, false
}

// MetadataHoldFromBindings computes the sole product-level hold: BGM absent or
// TMDB absent. A TMDB value can never be partial at this point.
func MetadataHoldFromBindings(bgm *NullableBGMBinding, tmdb *NullableTMDBBinding) MetadataHold {
	hold := MetadataHold{Missing: []MetadataHoldMissing{}}
	if bgm == nil || strings.TrimSpace(bgm.WorkId) == "" {
		hold.Missing = append(hold.Missing, MetadataHoldMissing("bgm"))
	}
	if tmdb == nil {
		hold.Missing = append(hold.Missing, MetadataHoldMissing("tmdb"))
	}
	hold.Held = len(hold.Missing) > 0
	return hold
}

// LocatorURL validates a custom_rss locator at the current transport safety
// boundary. The persisted locator itself remains the opaque value supplied by
// the user and is not normalized during Subscribe.
func LocatorURL(locator string) (string, error) {
	raw := strings.TrimSpace(locator)
	if raw == "" || len(raw) > 4096 {
		return "", fmt.Errorf("locator is required and bounded")
	}
	if err := subscriptiondomain.ValidateRSSLocatorExecutionScheme(raw); err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("locator must be an absolute URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", subscriptiondomain.ErrRSSLocatorSchemeUnsupported
	}
	if parsed.User != nil || parsed.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", fmt.Errorf("locator must not contain credentials, fragments, or control characters")
	}
	if isPrivateHost(parsed.Hostname()) {
		return "", fmt.Errorf("locator targets a private or local host")
	}
	return raw, nil
}

func isPrivateHost(host string) bool {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

const (
	invalidLocatorDisplay = "invalid locator"
	opaqueLocatorDisplay  = "opaque locator"
)

// LocatorDisplay returns a redacted display value without changing the opaque
// locator stored by the source owner. Non-HTTP(S) values remain opaque on reads.
func LocatorDisplay(locator string) string {
	return SanitizeLocatorDisplay(locator)
}

// SanitizeLocatorDisplay projects both newly derived and legacy stored display
// values to an idempotent ordinary-read value without consulting the raw locator.
func SanitizeLocatorDisplay(display string) string {
	trimmed := strings.TrimSpace(display)
	if trimmed == invalidLocatorDisplay || trimmed == opaqueLocatorDisplay {
		return trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Hostname() == "" {
		return invalidLocatorDisplay
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return opaqueLocatorDisplay
	}
	// Paths and query values can contain credentials under arbitrary names. The
	// ordinary read model therefore exposes only the URL origin.
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.ForceQuery = false
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// ProviderCatalogTitle normalizes an observed provider catalog title into the
// value a subscription freezes as its provider title: the trimmed title, or
// nil when it is blank, not valid UTF-8, or longer than the stored bound.
// Both the subscription transaction and the configuration preview read the
// same catalog row through this one rule.
func ProviderCatalogTitle(title string) *string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > 512 || !utf8.ValidString(trimmed) {
		return nil
	}
	return &trimmed
}
