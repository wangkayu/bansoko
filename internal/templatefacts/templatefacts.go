// Package templatefacts projects a Release's frozen source facts into the
// template value namespaces: reltitle values (rt) and AniBT tag
// values (anibt). It is template-kind-agnostic: naming consumes it
// today and notification templates can consume it later, and it reads only
// release-level frozen facts (never file plans or download state).
//
// Each namespace carries only its own native values. There is no mapping
// between AniBT and reltitle, no implied value, and no fallback between
// namespaces: a field with no frozen value is missing, never faked.
package templatefacts

import (
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	providerdomain "bansoko/internal/provider/domain"
)

// Namespace root names under the unified fact vocabulary (naming and
// notification templates), with snake_case fields.
const (
	// RootRt is the reltitle value namespace.
	RootRt = "rt"
	// RootAnibt is the AniBT value namespace: the AniBT tag dimensions and the
	// AniBT source's group identity.
	RootAnibt = "anibt"
	// RootMikan is the Mikan provider group identity namespace.
	RootMikan = "mikan"
)

// MultiValueSeparator joins the values of a multi-valued field.
const MultiValueSeparator = "+"

// Field describes one template-visible field of a namespace.
type Field struct {
	// Key is the key of Values.
	Key string
	// Name is the field name under the unified root (rt.<name>).
	Name string
	// Dimension is the native dimension the value is read from.
	Dimension string
	// Description and SampleValue document the field in template catalogs;
	// Label and Text are the unified catalog copy.
	Description string
	SampleValue string
	Label       Text
	Text        Text
}

// ReltitleFields are the exposed reltitle fields, in catalog order.
// Episode-semantics outputs are not exposed at present (the authoritative
// episode stays the system fact); other whitelisted outputs stay unexposed
// until per-field enabling exists. sub_flag is the flat AttrSubFlag projection
// release.subtitle_mode and the sub.lang_name type tail derive from; the
// canonical dictionary pins it to hard / soft / ex.
var ReltitleFields = []Field{
	{Key: "Title", Name: "title", Dimension: acquisitiondomain.TitleValueDimensionTitle, Description: "Title reltitle read from the release title.", SampleValue: "进击的巨人", Label: Text{"标题", "Title"}, Text: Text{"reltitle 从发布标题读出的作品标题。", "Title reltitle read from the release title."}},
	{Key: "Subgroup", Name: "subgroup", Dimension: acquisitiondomain.TitleValueDimensionGroup, Description: "Release group reltitle read from the release title.", SampleValue: "SampleGroup", Label: Text{"字幕组", "Group"}, Text: Text{"reltitle 从发布标题读出的字幕组。", "Group reltitle read from the release title."}},
	{Key: "Version", Name: "version", Dimension: acquisitiondomain.TitleValueDimensionVersion, Description: "Release version reltitle read from the release title.", SampleValue: "v2", Label: Text{"发布版本", "Release version"}, Text: Text{"reltitle 从发布标题读出的发布版本，例如 v2；标题没有版本标记时为空。", "Release version reltitle read from the release title, such as v2. Empty when the title has no version marker."}},
	{Key: "Resolution", Name: "resolution", Dimension: "resolution", Description: "Resolution reltitle read from the release title.", SampleValue: "1080p", Label: Text{"分辨率", "Resolution"}, Text: Text{"reltitle 从发布标题读出的分辨率。", "Resolution reltitle read from the release title."}},
	{Key: "Source", Name: "source", Dimension: "source_type", Description: "Source type reltitle read from the release title.", SampleValue: "WEBRip", Label: Text{"片源", "Source"}, Text: Text{"reltitle 从发布标题读出的片源类型。", "Source type reltitle read from the release title."}},
	{Key: "Codec", Name: "codec", Dimension: "video_codec", Description: "Video codec reltitle read from the release title.", SampleValue: "HEVC", Label: Text{"编码", "Codec"}, Text: Text{"reltitle 从发布标题读出的视频编码。", "Video codec reltitle read from the release title."}},
	{Key: "SubLang", Name: "sub_lang", Dimension: "sub_lang", Description: "Subtitle language reltitle read from the release title; several join with +.", SampleValue: "SC", Label: Text{"字幕语言", "Subtitle language"}, Text: Text{"reltitle 从发布标题读出的字幕语言，多个用 + 连接。", "Subtitle language reltitle read from the release title; several join with +."}},
	{Key: "SubFlag", Name: "sub_flag", Dimension: "sub_flag", Description: "Subtitle mode flag reltitle read from the release title; several join with +.", SampleValue: "soft", Label: Text{"字幕方式标志", "Subtitle flag"}, Text: Text{"reltitle 从发布标题读出的字幕方式标志（soft、hard、ex），多个用 + 连接；标题没有明确的字幕方式时为空。", "Subtitle mode flag reltitle read from the release title (soft, hard, ex); several join with +. Empty when the release title names none explicitly."}},
}

// AniBTTagFields are the exposed AniBT tag fields, one per AniBT tag
// dimension, in catalog order.
var AniBTTagFields = []Field{
	{Key: "Resolution", Name: "resolution", Dimension: acquisitiondomain.AniBTDimensionResolution, Description: "AniBT resolution tag.", SampleValue: "1080p", Label: Text{"分辨率", "Resolution"}, Text: Text{"AniBT 分辨率标签。", "AniBT resolution tag."}},
	{Key: "Subtitle", Name: "subtitle", Dimension: acquisitiondomain.AniBTDimensionSubtitle, Description: "AniBT subtitle tag.", SampleValue: "INTERNAL", Label: Text{"字幕", "Subtitle"}, Text: Text{"AniBT 字幕标签。", "AniBT subtitle tag."}},
	{Key: "Language", Name: "language", Dimension: acquisitiondomain.AniBTDimensionLanguage, Description: "AniBT language tags; several join with +.", SampleValue: "CHS+CHT", Label: Text{"语言", "Language"}, Text: Text{"AniBT 语言标签，多个用 + 连接。", "AniBT language tags; several join with +."}},
	{Key: "Format", Name: "format", Dimension: acquisitiondomain.AniBTDimensionFormat, Description: "AniBT format tag.", SampleValue: "MKV", Label: Text{"格式", "Format"}, Text: Text{"AniBT 格式标签。", "AniBT format tag."}},
	{Key: "Tag", Name: "tag", Dimension: acquisitiondomain.AniBTDimensionTag, Description: "AniBT custom tags; several join with +.", SampleValue: "10bit+AAC+HEVC+WEBRip", Label: Text{"标签", "Tag"}, Text: Text{"AniBT 自定义标签，多个用 + 连接。", "AniBT custom tags; several join with +."}},
}

// AniBTSourceGroupFields are the AniBT provider_group source's group identity
// fields. The slug shares the anibt root with the AniBT tag fields but comes
// from the subscription source rather than from a tag dimension: it is the
// stable identity the subgroup policy keys on. A Release that did not come
// from an AniBT provider_group source has no value; the source's configured
// display name reads anime.group, not a namespace field here.
var AniBTSourceGroupFields = []Field{
	{Key: "GroupSlug", Name: "groupslug", Description: "AniBT provider group slug of the RSS source.", SampleValue: "samplegroup", Label: Text{"来源组 slug", "Source group slug"}, Text: Text{"RSS 来源的 AniBT 字幕组 slug，分组策略使用的稳定键。", "AniBT provider group slug of the RSS source; the stable key the subgroup policy uses."}},
}

// MikanSourceGroupFields are the Mikan provider_group source's group identity
// field: the numeric group id, the stable key the subgroup policy uses. The
// source's configured display name reads anime.group, not a namespace field.
var MikanSourceGroupFields = []Field{
	{Key: "GroupID", Name: "groupid", Description: "Mikan provider group id of the RSS source.", SampleValue: "58", Label: Text{"来源组 ID", "Source group ID"}, Text: Text{"RSS 来源的 Mikan 数字字幕组 ID，分组策略使用的稳定键。", "Mikan numeric provider group id of the RSS source; the stable key the subgroup policy uses."}},
}

// SourceGroupIdentity is the origin RSS source's group identity as templates
// read it: the provider-namespaced external group reference (Mikan's numeric
// group id / AniBT's group slug). It is the same stable identity the subgroup
// policy keys on (mikan:<id> / anibt:<slug>), surfaced to naming as an
// explicit field; the source's configured display group name is anime.group,
// not part of the identity.
type SourceGroupIdentity struct {
	// Provider is anibt | mikan; empty when the source records no provider
	// identity (a custom RSS source, or a provider_group row without
	// provenance).
	Provider string
	// GroupRef is the provider's external group id: Mikan's numeric group id
	// or AniBT's group slug. Empty when the source records none.
	GroupRef string
	// GroupName is the source's configured display name. It is not projected
	// onto a namespace root; the anime.group fact reads it directly.
	GroupName string
}

// Values projects the identity onto the provider group roots. A source with a
// provider identity exposes only that provider's root; a source without one
// exposes neither. Every other root yields no values, so its fields read null
// under the missing-value rule: there is no cross-source fallback and no
// implied value.
func (g SourceGroupIdentity) Values() (anibt, mikan Values) {
	switch providerdomain.Kind(g.Provider) {
	case providerdomain.ProviderAniBT:
		anibt = sourceGroupValues(g, "GroupSlug")
	case providerdomain.ProviderMikan:
		mikan = sourceGroupValues(g, "GroupID")
	}
	return anibt, mikan
}

// sourceGroupValues is the shared provider_group projection: the stable
// external id under idKey, present only when the frozen value exists.
func sourceGroupValues(g SourceGroupIdentity, idKey string) Values {
	values := Values{}
	if g.GroupRef != "" {
		values[idKey] = g.GroupRef
	}
	return values
}

// Values is one namespace's projected values keyed by Field.Key. A key that is
// absent is a missing value; a present key is never blank.
type Values map[string]string

// Get returns one field's value and whether it is present.
func (v Values) Get(key string) (string, bool) {
	value, ok := v[key]
	return value, ok
}

// Clone returns an independent copy (nil stays nil).
func (v Values) Clone() Values {
	if v == nil {
		return nil
	}
	out := make(Values, len(v))
	for key, value := range v {
		out[key] = value
	}
	return out
}

// Object is the plain template object of a namespace: every field key is
// present, a missing value is nil (null), never "".
func Object(fields []Field, values Values) map[string]interface{} {
	out := make(map[string]interface{}, len(fields))
	for _, field := range fields {
		if value, ok := values[field.Key]; ok {
			out[field.Key] = value
		} else {
			out[field.Key] = nil
		}
	}
	return out
}

// Facts is the release-level projection of both namespaces.
type Facts struct {
	Reltitle  Values
	AniBTTags Values
}

// FromSourceFacts projects a persisted Release.source_facts document.
func FromSourceFacts(sourceFacts []byte) (Facts, error) {
	title, anibt, err := acquisitiondomain.DecodeSourceFactTagSets(sourceFacts)
	if err != nil {
		return Facts{}, err
	}
	return FromTagSets(title, anibt), nil
}

// FromTagSets projects the frozen tag sets of one release. A nil set yields
// no values for its namespace.
func FromTagSets(title *acquisitiondomain.TitleTagSet, anibt *acquisitiondomain.AniBTTagSet) Facts {
	return Facts{Reltitle: ReltitleValues(title), AniBTTags: AniBTTagValues(anibt)}
}

// ReltitleValues projects a frozen title tag set. Title, Subgroup and Version
// come only from the ValueTags of a set of the current schema; the other fields
// from its tags.
func ReltitleValues(set *acquisitiondomain.TitleTagSet) Values {
	values := Values{}
	if set == nil {
		return values
	}
	tags := append([]string{}, set.Tags...)
	if set.Schema == acquisitiondomain.TitleTagSchema {
		tags = append(tags, set.ValueTags...)
	}
	project(values, ReltitleFields, tags)
	return values
}

// AniBTTagValues projects a frozen AniBT tag set; a non-AniBT release (nil
// set) has no values.
func AniBTTagValues(set *acquisitiondomain.AniBTTagSet) Values {
	values := Values{}
	if set == nil {
		return values
	}
	// anibt.tag keeps rendering every customTag value: the filter
	// sub-dimensions (codec, audio, ...) fold back into the tag field.
	tags := make([]string, 0, len(set.Tags))
	for _, tag := range set.Tags {
		if dimension, value, ok := acquisitiondomain.SplitTag(tag); ok && acquisitiondomain.IsAniBTCustomTagDimension(dimension) {
			tag = acquisitiondomain.AniBTDimensionTag + ":" + value
		}
		tags = append(tags, tag)
	}
	project(values, AniBTTagFields, tags)
	return values
}

// project groups "dimension:value" tags by field and renders each field with
// JoinValues. Dimensions match exactly; values are kept verbatim.
func project(values Values, fields []Field, tags []string) {
	byDimension := map[string][]string{}
	for _, tag := range tags {
		dimension, value, ok := acquisitiondomain.SplitTag(tag)
		if !ok {
			continue
		}
		byDimension[dimension] = append(byDimension[dimension], value)
	}
	for _, field := range fields {
		if joined := JoinValues(byDimension[field.Dimension]); joined != "" {
			values[field.Key] = joined
		}
	}
}

// JoinValues is the multi-value rule: trimmed non-empty values, de-duplicated
// case-insensitively (first frozen spelling wins), sorted by case-folded text,
// joined with MultiValueSeparator. A single value is
// returned as is; no value is "".
func JoinValues(raw []string) string {
	seen := map[string]struct{}{}
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return strings.ToLower(values[i]) < strings.ToLower(values[j]) })
	return strings.Join(values, MultiValueSeparator)
}
