package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/parser/reltitletags"
)

// SectionTagFilters is the rules section holding the structured tag filter
// rows. It is a rules-owner section (the Global value is only the default a
// new draft copies; each draft and subscription stores its own complete value)
// but deliberately stays outside EffectiveSectionOrder: the six-section frozen
// envelopes (candidate revisions, business snapshots, acquisition inputs) do
// not carry it. A saved revision keeps it in its draft body and subscription
// writes it as the new instance's stored value; see ResolveTagFilters.
const SectionTagFilters = "tag_filters"

// TagFiltersSchemaVersion pins the stored tag_filters value shape.
const TagFiltersSchemaVersion = "tag-filters-v2"

// RulesOwnerSectionOrder is every rules-owner section a global default or an
// anime override may hold, in canonical order: the six frozen effective
// sections followed by the live-only tag_filters section.
var RulesOwnerSectionOrder = append(append([]string(nil), EffectiveSectionOrder...), SectionTagFilters)

// TagFilterRow is one independent filter row. Require tags group by their
// dimension (the part before ':'): within a dimension any one tag satisfies
// it (OR), and every dimension the list names must be satisfied (AND). Empty
// require constrains nothing; any exclude tag present filters the release. A
// tag never sits in both lists.
type TagFilterRow struct {
	Require []string `json:"require"`
	Exclude []string `json:"exclude"`
}

// TagFilterRules is the complete tag_filters section value. The anibt row
// applies to AniBT-source releases only; the title row applies to every
// release, AniBT ones included, using the tags the pinned reltitle parser read
// from the release title. Both rows must pass.
type TagFilterRules struct {
	Anibt TagFilterRow `json:"anibt"`
	Title TagFilterRow `json:"title"`
}

// TitleTagDimensions is the title-row dimension allowlist: the canonical
// dictionary dimensions of the pinned reltitle parser.
var TitleTagDimensions = reltitletags.Dimensions

// Tag filter bounds.
const (
	MaxTagFilterTags       = 64
	MaxTagFilterValueBytes = 128
)

// DefaultTagFilterRules is the code default: both rows are empty, which
// filters nothing.
func DefaultTagFilterRules() TagFilterRules {
	return TagFilterRules{
		Anibt: TagFilterRow{Require: []string{}, Exclude: []string{}},
		Title: TagFilterRow{Require: []string{}, Exclude: []string{}},
	}
}

// ValidateTagFilterRulesJSON enforces the complete typed tag_filters value on
// write: on top of the shape, every title-row value must be in the pinned
// reltitle dictionary.
func ValidateTagFilterRulesJSON(raw json.RawMessage) error {
	value, err := decodeTagFilterRules(raw)
	if err != nil {
		return err
	}
	for _, list := range []struct {
		name string
		tags []string
	}{{"require", value.Title.Require}, {"exclude", value.Title.Exclude}} {
		for index, tag := range list.tags {
			dimension, tagValue, _ := acquisitiondomain.SplitTag(tag)
			if _, ok := reltitletags.CanonicalValue(dimension, tagValue); !ok {
				return fmt.Errorf("tag_filters.title.%s[%d] value %q is not in the title-parse dictionary for %s", list.name, index, tagValue, dimension)
			}
		}
	}
	return nil
}

// decodeTagFilterRules enforces the typed shape and the dimension allowlists.
// Reading a stored value does not re-check title values against the current
// dictionary, so a dictionary refresh never invalidates a saved rule; a value
// the parser no longer emits simply never matches. The AniBT row is
// normalized through the customTag classification table first, so a rule
// saved before a table update reads, matches and groups under the current
// dimension.
func decodeTagFilterRules(raw json.RawMessage) (TagFilterRules, error) {
	var value TagFilterRules
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return TagFilterRules{}, fmt.Errorf("tag_filters value must be typed tag filter rules")
	}
	value.Anibt = normalizeAniBTTagFilterRow(value.Anibt)
	if err := validateTagFilterRow("anibt", value.Anibt, acquisitiondomain.AniBTTagDimensions); err != nil {
		return TagFilterRules{}, err
	}
	if err := validateTagFilterRow("title", value.Title, TitleTagDimensions); err != nil {
		return TagFilterRules{}, err
	}
	return value, nil
}

// normalizeAniBTTagFilterRow reclassifies "tag:<value>" entries whose value
// the customTag classification table lists into their sub-dimension. Spelling
// and order are kept. A reclassified entry that duplicates another entry of
// the row (case-insensitively) is dropped; genuine duplicates are left for
// validation to reject.
func normalizeAniBTTagFilterRow(row TagFilterRow) TagFilterRow {
	unchanged := map[string]struct{}{}
	for _, tags := range [][]string{row.Require, row.Exclude} {
		for _, tag := range tags {
			if reclassified := acquisitiondomain.ReclassifyAniBTTags([]string{tag})[0]; reclassified == tag {
				unchanged[acquisitiondomain.NormalizeTagKey(tag)] = struct{}{}
			}
		}
	}
	normalize := func(tags []string) []string {
		if tags == nil {
			return nil
		}
		result := make([]string, 0, len(tags))
		for index, tag := range acquisitiondomain.ReclassifyAniBTTags(tags) {
			if tag != tags[index] {
				if _, exists := unchanged[acquisitiondomain.NormalizeTagKey(tag)]; exists {
					continue
				}
			}
			result = append(result, tag)
		}
		return result
	}
	return TagFilterRow{Require: normalize(row.Require), Exclude: normalize(row.Exclude)}
}

// NormalizeTagFilterRulesJSON returns a tag_filters value with its AniBT row
// normalized (see normalizeAniBTTagFilterRow). A value that does not decode is
// returned unchanged for the caller's validation to report.
func NormalizeTagFilterRulesJSON(raw json.RawMessage) json.RawMessage {
	value, err := decodeTagFilterRules(raw)
	if err != nil {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

// NormalizeTagFiltersOverrideJSON normalizes the value of a stored
// replace tag_filters override; anything else is returned unchanged.
func NormalizeTagFiltersOverrideJSON(raw json.RawMessage) json.RawMessage {
	override, err := decodeOverride(raw)
	if err != nil {
		return raw
	}
	encoded, err := json.Marshal(struct {
		Mode  string          `json:"mode"`
		Value json.RawMessage `json:"value"`
	}{"replace", NormalizeTagFilterRulesJSON(override.Value)})
	if err != nil {
		return raw
	}
	return encoded
}

func validateTagFilterRow(name string, row TagFilterRow, dimensions []string) error {
	if row.Require == nil || row.Exclude == nil {
		return fmt.Errorf("tag_filters.%s requires both require and exclude lists", name)
	}
	seen := map[string]struct{}{}
	for _, list := range []struct {
		field string
		tags  []string
	}{{"require", row.Require}, {"exclude", row.Exclude}} {
		if len(list.tags) > MaxTagFilterTags {
			return fmt.Errorf("tag_filters.%s.%s exceeds %d entries", name, list.field, MaxTagFilterTags)
		}
		for index, tag := range list.tags {
			dimension, value, ok := acquisitiondomain.SplitTag(tag)
			if !ok || strings.TrimSpace(dimension) != dimension || strings.TrimSpace(value) != value || value == "" {
				return fmt.Errorf("tag_filters.%s.%s[%d] must be \"dimension:value\" without surrounding whitespace", name, list.field, index)
			}
			if !containsString(dimensions, dimension) {
				return fmt.Errorf("tag_filters.%s.%s[%d] dimension %q is not one of %s", name, list.field, index, dimension, strings.Join(dimensions, ", "))
			}
			if len(value) > MaxTagFilterValueBytes || strings.ContainsAny(value, "\r\n\t") {
				return fmt.Errorf("tag_filters.%s.%s[%d] value must be at most %d bytes without control characters", name, list.field, index, MaxTagFilterValueBytes)
			}
			key := acquisitiondomain.NormalizeTagKey(tag)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("tag_filters.%s.%s[%d] is duplicated or listed in both require and exclude", name, list.field, index)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// ValidateStoredTagFiltersOverride checks a stored (already accepted) draft
// tag_filters override: shape and dimensions only, never the current title
// dictionary. An absent section is valid.
func ValidateStoredTagFiltersOverride(raw json.RawMessage) []FieldViolation {
	if _, _, err := StoredTagFiltersOverride(raw); err != nil {
		return []FieldViolation{{Field: "rules." + SectionTagFilters, Code: "OVERRIDE_MODE_INVALID", Message: err.Error()}}
	}
	return nil
}

// StoredTagFiltersOverride decodes a draft's tag_filters value the way a
// stored value is read (no dictionary check). present is false when the draft
// omits the section; subscribing such a draft is refused.
func StoredTagFiltersOverride(raw json.RawMessage) (override OverrideValue, present bool, err error) {
	if SectionAbsent(raw) {
		return OverrideValue{}, false, nil
	}
	override, err = decodeOverride(raw)
	if err != nil {
		return OverrideValue{}, false, fmt.Errorf("tag_filters must be a complete replace value")
	}
	if _, err := decodeTagFilterRules(override.Value); err != nil {
		return OverrideValue{}, false, err
	}
	override.Value = NormalizeTagFilterRulesJSON(override.Value)
	return override, true, nil
}

// ResolveDraftTagFilters resolves the tag_filters value a draft stores. A
// draft without the section (a one-shot draft, which never filters by tags at
// admission) resolves as the code default; Global is never read.
func ResolveDraftTagFilters(draft AnimeDraft) (ResolvedTagFilters, error) {
	override, present, err := StoredTagFiltersOverride(draft.Rules.Sections.TagFilters)
	if err != nil {
		return ResolvedTagFilters{}, err
	}
	if !present {
		return DefaultResolvedTagFilters(), nil
	}
	return ResolveTagFilters(override)
}

// ResolvedTagFilters is the effective tag_filters value with its source
// version (0 = the stored draft or instance value, 1 = the code default of a
// draft without the section) and a content digest used to detect a change between an
// out-of-transaction evaluation and its owner write.
type ResolvedTagFilters struct {
	Rules   TagFilterRules
	Version int64
	Digest  string
}

// ResolveTagFilters resolves a stored tag_filters value. A missing value
// fails closed.
func ResolveTagFilters(override OverrideValue) (ResolvedTagFilters, error) {
	raw, version, err := storedSectionValue(SectionTagFilters, override)
	if err != nil {
		return ResolvedTagFilters{}, err
	}
	return resolvedTagFilters(raw, version)
}

func resolvedTagFilters(raw json.RawMessage, version int64) (ResolvedTagFilters, error) {
	rules, err := decodeTagFilterRules(raw)
	if err != nil {
		return ResolvedTagFilters{}, fmt.Errorf("resolved tag_filters is not a valid typed value: %w", err)
	}
	canonical, err := json.Marshal(rules)
	if err != nil {
		return ResolvedTagFilters{}, err
	}
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("tag-filters:%d:", version)), canonical...))
	return ResolvedTagFilters{Rules: rules, Version: version, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// DefaultResolvedTagFilters is the code default (filter nothing), used by
// frozen envelopes and drafts that carry no tag_filters section.
func DefaultResolvedTagFilters() ResolvedTagFilters {
	raw, err := json.Marshal(DefaultTagFilterRules())
	if err != nil {
		panic(err)
	}
	resolved, err := resolvedTagFilters(raw, 1)
	if err != nil {
		panic(err)
	}
	return resolved
}

// Tag row reason codes (Release verdict `filtered`). Each row has its own
// codes so the reason names the row that failed.
const (
	ReasonAniBTTagMissing        = "anibt_tag_missing"
	ReasonAniBTTagWhitelistUnmet = "anibt_tag_whitelist_unmet"
	ReasonAniBTTagBlacklisted    = "anibt_tag_blacklisted"
	ReasonTitleTagMissing        = "title_tag_missing"
	ReasonTitleTagWhitelistUnmet = "title_tag_whitelist_unmet"
	ReasonTitleTagBlacklisted    = "title_tag_blacklisted"
	tagReasonDetailMaxEntries    = 32
)

// IsAniBTRelease reports whether the AniBT tag row applies to a release: it
// came from an AniBT source (frozen provider fact) or carries a frozen AniBT
// tag set. Mikan and custom RSS releases never match.
func IsAniBTRelease(item acquisitiondomain.FeedItem) bool {
	return item.AniBTTags != nil || item.Provider == "anibt"
}

// AniBTTagFilterReason evaluates the AniBT tag row against one release. It
// returns an empty code when the release passes; non-AniBT releases always
// pass. See tagRowReason for the row semantics.
func AniBTTagFilterReason(item acquisitiondomain.FeedItem, row TagFilterRow) (code, detail string) {
	if !IsAniBTRelease(item) {
		return "", ""
	}
	var tags []string
	if item.AniBTTags != nil {
		tags = acquisitiondomain.ReclassifyAniBTTags(item.AniBTTags.Tags)
	}
	// Both sides go through the current classification table, so a rule or a
	// release frozen before a table update still matches and groups by the
	// current dimension.
	row = normalizeAniBTTagFilterRow(row)
	return tagRowReason(tags, row, ReasonAniBTTagMissing, ReasonAniBTTagWhitelistUnmet, ReasonAniBTTagBlacklisted)
}

// ReleaseTitleTags is the title-parse tag set evaluated for a release: the
// frozen set when the Release carries one, otherwise the pinned parser's
// reading of the release title (a release persisted before the title row, or
// a fresh feed item). The derivation is deterministic for one pinned parser.
func ReleaseTitleTags(item acquisitiondomain.FeedItem) acquisitiondomain.TitleTagSet {
	if item.TitleTags != nil {
		return *item.TitleTags
	}
	return reltitletags.Extract(item.Title)
}

// TitleTagFilterReason evaluates the title-parse row against one release. It
// applies to every release, AniBT ones included.
func TitleTagFilterReason(item acquisitiondomain.FeedItem, row TagFilterRow) (code, detail string) {
	if len(row.Require) == 0 && len(row.Exclude) == 0 {
		return "", ""
	}
	return tagRowReason(ReleaseTitleTags(item).Tags, row, ReasonTitleTagMissing, ReasonTitleTagWhitelistUnmet, ReasonTitleTagBlacklisted)
}

// tagRowReason is the shared row semantics:
//   - an empty row filters nothing (tag-less releases pass);
//   - a non-empty require list is OR within a dimension and AND across
//     dimensions; a release without any tag fails as
//     the row's "missing" code, and the detail lists the tags of every
//     unsatisfied dimension;
//   - an exclude list filters a release carrying any listed tag.
//
// Require is judged first. Comparison is case-insensitive on the trimmed
// "dimension:value" form.
func tagRowReason(tags []string, row TagFilterRow, missingCode, unmetCode, blacklistedCode string) (code, detail string) {
	if len(row.Require) == 0 && len(row.Exclude) == 0 {
		return "", ""
	}
	present := map[string]struct{}{}
	for _, tag := range tags {
		present[acquisitiondomain.NormalizeTagKey(tag)] = struct{}{}
	}
	if missing := unmetRequireTags(present, row.Require); len(missing) > 0 {
		if len(present) == 0 {
			return missingCode, "missing: " + joinBoundedTags(missing)
		}
		return unmetCode, "missing: " + joinBoundedTags(missing)
	}
	var hit []string
	for _, tag := range row.Exclude {
		if _, ok := present[acquisitiondomain.NormalizeTagKey(tag)]; ok {
			hit = append(hit, tag)
		}
	}
	if len(hit) > 0 {
		return blacklistedCode, "blacklisted: " + joinBoundedTags(hit)
	}
	return "", ""
}

// unmetRequireTags returns the require tags of every dimension the release
// does not satisfy, in list order; a dimension is satisfied by any one of its
// tags. A tag without a dimension prefix forms its own group.
func unmetRequireTags(present map[string]struct{}, require []string) []string {
	satisfied := map[string]bool{}
	for _, tag := range require {
		key := acquisitiondomain.NormalizeTagKey(tag)
		if _, ok := present[key]; ok {
			satisfied[tagDimension(key)] = true
		}
	}
	var missing []string
	for _, tag := range require {
		if !satisfied[tagDimension(acquisitiondomain.NormalizeTagKey(tag))] {
			missing = append(missing, tag)
		}
	}
	return missing
}

// tagDimension is the dimension of a normalised "dimension:value" key, or the
// whole key when it has no prefix.
func tagDimension(key string) string {
	if dimension, _, ok := strings.Cut(key, ":"); ok {
		return dimension
	}
	return key
}

func joinBoundedTags(tags []string) string {
	if len(tags) > tagReasonDetailMaxEntries {
		return strings.Join(tags[:tagReasonDetailMaxEntries], ", ") + fmt.Sprintf(", … (+%d)", len(tags)-tagReasonDetailMaxEntries)
	}
	return strings.Join(tags, ", ")
}
