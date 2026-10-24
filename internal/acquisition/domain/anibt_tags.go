package domain

import (
	"strings"
)

// FeedExtensionField is one namespaced leaf element of an RSS item, kept
// verbatim (trimmed text) so provider adapters can read provider-owned fields.
type FeedExtensionField struct {
	Space string
	Local string
	Value string
}

// maxFeedExtensionFields bounds the transient extension capture per item.
const maxFeedExtensionFields = 256

// collectExtensionFields captures the namespaced leaf elements of an item in
// document order. Elements without a namespace URI are ordinary RSS fields and
// are not captured; nested elements contribute their leaves.
func collectExtensionFields(elements []rssElement) []FeedExtensionField {
	var result []FeedExtensionField
	var visit func([]rssElement)
	visit = func(items []rssElement) {
		for _, element := range items {
			if len(result) >= maxFeedExtensionFields {
				return
			}
			if len(element.Children) > 0 {
				visit(element.Children)
				continue
			}
			space := strings.TrimSpace(element.XMLName.Space)
			if space == "" {
				continue
			}
			result = append(result, FeedExtensionField{Space: space, Local: strings.TrimSpace(element.XMLName.Local), Value: strings.TrimSpace(element.Text)})
		}
	}
	visit(elements)
	return result
}

// AniBT RSS structured tag namespaces. The current format is recognized by its
// namespace URI; the legacy namespace carries no tag fields.
const (
	AniBTNamespaceV1     = "https://anibt.net/xmlns/rss/1.0/"
	AniBTNamespaceLegacy = "https://anibt.moe/xmlns/0.1/"
	// AniBTTagSchemaV1 identifies a tag set read from the current namespace.
	AniBTTagSchemaV1 = "anibt-rss-1.0"
)

// AniBT tag diagnostics. They are recorded with the frozen tag set; none of
// them is a parse error.
const (
	AniBTTagDiagnosticNamespaceLegacy = "anibt_namespace_legacy"
	AniBTTagDiagnosticNamespaceAbsent = "anibt_namespace_absent"
	AniBTTagDiagnosticTagsAbsent      = "anibt_tags_absent"
	AniBTTagDiagnosticValueDropped    = "anibt_tag_value_dropped"
)

// AniBT tag dimensions. A tag is written "dimension:value". The first four
// mirror AniBT fields; customTag is split by the server-owned classification
// table (anibtCustomTagDimensions) into our own sub-dimensions, and every
// customTag value outside the table stays in the `tag` dimension.
const (
	AniBTDimensionResolution = "resolution"
	AniBTDimensionSubtitle   = "subtitle"
	AniBTDimensionLanguage   = "language"
	AniBTDimensionFormat     = "format"
	AniBTDimensionCodec      = "codec"
	AniBTDimensionBitDepth   = "bitdepth"
	AniBTDimensionAudio      = "audio"
	AniBTDimensionSource     = "source"
	AniBTDimensionPlatform   = "platform"
	AniBTDimensionSubFormat  = "subformat"
	AniBTDimensionTag        = "tag"
)

// AniBTTagDimensions is the closed dimension allowlist in display order.
var AniBTTagDimensions = []string{
	AniBTDimensionResolution,
	AniBTDimensionSubtitle,
	AniBTDimensionLanguage,
	AniBTDimensionFormat,
	AniBTDimensionCodec,
	AniBTDimensionBitDepth,
	AniBTDimensionAudio,
	AniBTDimensionSource,
	AniBTDimensionPlatform,
	AniBTDimensionSubFormat,
	AniBTDimensionTag,
}

// anibtTagFields maps the AniBT element local names to their dimension. Only
// these fields are tags: episode/version/type/group fields are deliberately
// excluded (AniBT never decides episode identity or coverage). customtag maps
// to `tag` here and is then classified by ClassifyAniBTCustomTag.
var anibtTagFields = map[string]string{
	"resolution": AniBTDimensionResolution,
	"subtitle":   AniBTDimensionSubtitle,
	"language":   AniBTDimensionLanguage,
	"format":     AniBTDimensionFormat,
	"customtag":  AniBTDimensionTag,
}

// anibtCustomTagDimensions is the server-owned customTag classification table,
// keyed by the case-folded value. The value token itself is never rewritten.
var anibtCustomTagDimensions = func() map[string]string {
	table := map[string][]string{
		AniBTDimensionCodec:     {"HEVC", "AVC", "AV1"},
		AniBTDimensionBitDepth:  {"10bit", "8bit"},
		AniBTDimensionAudio:     {"AAC", "FLAC", "OPUS"},
		AniBTDimensionSource:    {"WEB-DL", "WEBRip"},
		AniBTDimensionPlatform:  {"CR", "Baha", "B-Global", "BILIBILI", "ABEMA", "NF", "CTHP"},
		AniBTDimensionSubFormat: {"PGS"},
	}
	result := map[string]string{}
	for dimension, values := range table {
		for _, value := range values {
			result[strings.ToLower(value)] = dimension
		}
	}
	return result
}()

// ClassifyAniBTCustomTag is the dimension of one customTag value: the table
// sub-dimension when the trimmed, case-folded value is listed, else `tag`.
func ClassifyAniBTCustomTag(value string) string {
	if dimension, ok := anibtCustomTagDimensions[strings.ToLower(strings.TrimSpace(value))]; ok {
		return dimension
	}
	return AniBTDimensionTag
}

// IsAniBTCustomTagDimension reports whether a dimension holds customTag
// values: `tag` and every table sub-dimension.
func IsAniBTCustomTagDimension(dimension string) bool {
	switch dimension {
	case AniBTDimensionCodec, AniBTDimensionBitDepth, AniBTDimensionAudio, AniBTDimensionSource,
		AniBTDimensionPlatform, AniBTDimensionSubFormat, AniBTDimensionTag:
		return true
	}
	return false
}

// ReclassifyAniBTTags rewrites "tag:<value>" entries frozen before customTag
// was classified into their table sub-dimension, keeping the value spelling
// and order. It is idempotent; other entries are returned unchanged.
func ReclassifyAniBTTags(tags []string) []string {
	if tags == nil {
		return nil
	}
	result := make([]string, len(tags))
	for index, tag := range tags {
		result[index] = tag
		dimension, value, ok := SplitTag(tag)
		if !ok || !strings.EqualFold(dimension, AniBTDimensionTag) {
			continue
		}
		if classified := ClassifyAniBTCustomTag(value); classified != AniBTDimensionTag {
			result[index] = classified + ":" + value
		}
	}
	return result
}

// Tag bounds. A value outside them is dropped with a diagnostic instead of
// failing the item.
const (
	MaxAniBTTagValueBytes = 128
	MaxAniBTTags          = 64
)

// AniBTTagSet is the structured tag set of one AniBT release.
type AniBTTagSet struct {
	// Schema is AniBTTagSchemaV1 when the current namespace was recognized,
	// otherwise empty.
	Schema string
	// Tags are "dimension:value" in source order, original spelling,
	// de-duplicated case-insensitively.
	Tags []string
	// Diagnostic explains an empty or partial set; empty when the set is
	// complete.
	Diagnostic string
}

// NormalizeTagKey is the comparison key of a tag: trimmed and case-folded.
// Storage keeps the original spelling.
func NormalizeTagKey(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

// SplitTag splits "dimension:value" at the first colon.
func SplitTag(tag string) (dimension, value string, ok bool) {
	index := strings.Index(tag, ":")
	if index <= 0 || index == len(tag)-1 {
		return "", "", false
	}
	return tag[:index], tag[index+1:], true
}

// ExtractAniBTTags reads the AniBT structured tags from an item's namespaced
// fields. A legacy or missing namespace yields an empty set with a diagnostic;
// it never fails.
func ExtractAniBTTags(fields []FeedExtensionField) AniBTTagSet {
	set := AniBTTagSet{Tags: []string{}}
	current, legacy := false, false
	seen := map[string]struct{}{}
	for _, field := range fields {
		switch strings.TrimSpace(field.Space) {
		case AniBTNamespaceV1:
			current = true
		case AniBTNamespaceLegacy:
			legacy = true
			continue
		default:
			continue
		}
		dimension, ok := anibtTagFields[strings.ToLower(field.Local)]
		if !ok {
			continue
		}
		value := strings.TrimSpace(field.Value)
		if value == "" {
			continue
		}
		if len(value) > MaxAniBTTagValueBytes || strings.ContainsAny(value, "\r\n\t") || len(set.Tags) >= MaxAniBTTags {
			set.Diagnostic = AniBTTagDiagnosticValueDropped
			continue
		}
		if dimension == AniBTDimensionTag {
			dimension = ClassifyAniBTCustomTag(value)
		}
		tag := dimension + ":" + value
		key := NormalizeTagKey(tag)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		set.Tags = append(set.Tags, tag)
	}
	switch {
	case current:
		set.Schema = AniBTTagSchemaV1
		if len(set.Tags) == 0 && set.Diagnostic == "" {
			set.Diagnostic = AniBTTagDiagnosticTagsAbsent
		}
	case legacy:
		set.Diagnostic = AniBTTagDiagnosticNamespaceLegacy
	default:
		set.Diagnostic = AniBTTagDiagnosticNamespaceAbsent
	}
	return set
}
