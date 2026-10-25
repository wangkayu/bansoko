// Package reltitletags derives the title-parse tag set of a release title with
// the vendored reltitle parser (third_party/reltitle). It only projects
// non-episode canonical attributes ("type:value"); episode, season and every
// other identity-bearing output stay out, so the title row of tag_filters can
// never influence episode identity. Title, group and release version are
// projected separately (ValueTags) for the template namespace and are never
// filter tags.
package reltitletags

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	reltitleconfigs "github.com/wangkayu/reltitle/configs"
	reltitleconfig "github.com/wangkayu/reltitle/pkg/config"
	reltitlemodel "github.com/wangkayu/reltitle/pkg/model"
	reltitleparser "github.com/wangkayu/reltitle/pkg/parser"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

const (
	// ImplementationVersion pins the vendored parser commit
	// (third_party/reltitle/SOURCE.md).
	ImplementationVersion = "reltitle@1f59ad4"
	// Schema identifies the projection contract.
	Schema = acquisitiondomain.TitleTagSchema
	// MaxTitleBytes bounds the parser input; reltitle has no internal deadline.
	MaxTitleBytes = 2048
	// MaxValueBytes bounds one projected value.
	MaxValueBytes = 128
	// MaxTemplateValueBytes bounds one projected title or group value.
	MaxTemplateValueBytes = 512
)

// Dimensions is the closed title-tag dimension list in display order: every
// attribute type reltitle's canonical dictionary defines. None of them is an
// episode, season, title or group attribute.
var Dimensions = []string{
	"resolution",
	"source_type",
	"video_codec",
	"audio_codec",
	"container",
	"platform",
	"sub_lang",
	"sub_flag",
	"audio_lang",
	"audio_flag",
}

// DimensionVocabulary is one dimension's canonical values from the pinned
// reltitle dictionary (dict_canonical_values.yaml), in dictionary order.
type DimensionVocabulary struct {
	Dimension string
	Values    []string
}

type state struct {
	parser       *reltitleparser.Parser
	configDigest string
	vocabulary   []DimensionVocabulary
	known        map[string]map[string]string // dimension -> folded value -> canonical
	err          error
}

var (
	loadOnce sync.Once
	loaded   state
)

func load() state {
	loadOnce.Do(func() {
		cfg, err := reltitleconfig.LoadAllFS(reltitleconfigs.FS)
		if err != nil {
			loaded.err = fmt.Errorf("load reltitle config: %w", err)
			return
		}
		digest, err := bundleDigest(reltitleconfigs.FS)
		if err != nil {
			loaded.err = err
			return
		}
		vocabulary, known, err := canonicalVocabulary(cfg.DictCanonicalValues.Canonical)
		if err != nil {
			loaded.err = err
			return
		}
		loaded = state{parser: reltitleparser.New(cfg), configDigest: digest, vocabulary: vocabulary, known: known}
	})
	return loaded
}

// bundleDigest is "lexicon:sha256:<hex>" over every embedded YAML file, in
// sorted path order, as path\0len\0bytes.
func bundleDigest(fsys fs.FS) (string, error) {
	paths, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("reltitle config bundle is empty")
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return "", err
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(data)))
		hash.Write([]byte(path))
		hash.Write([]byte{0})
		hash.Write(length[:])
		hash.Write([]byte{0})
		hash.Write(data)
	}
	return "lexicon:sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalVocabulary(canonical map[string]any) ([]DimensionVocabulary, map[string]map[string]string, error) {
	vocabulary := make([]DimensionVocabulary, 0, len(Dimensions))
	known := make(map[string]map[string]string, len(Dimensions))
	for _, dimension := range Dimensions {
		rawValues, ok := canonical[dimension].([]any)
		if !ok || len(rawValues) == 0 {
			return nil, nil, fmt.Errorf("reltitle canonical dictionary has no %s values", dimension)
		}
		entry := DimensionVocabulary{Dimension: dimension, Values: make([]string, 0, len(rawValues))}
		known[dimension] = make(map[string]string, len(rawValues))
		for _, raw := range rawValues {
			value, ok := raw.(string)
			value = strings.TrimSpace(value)
			if !ok || value == "" {
				return nil, nil, fmt.Errorf("reltitle canonical dictionary has a non-string %s value", dimension)
			}
			folded := strings.ToLower(value)
			if _, exists := known[dimension][folded]; exists {
				continue
			}
			known[dimension][folded] = value
			entry.Values = append(entry.Values, value)
		}
		vocabulary = append(vocabulary, entry)
	}
	return vocabulary, known, nil
}

// Ready reports whether the embedded parser and dictionary loaded.
func Ready() error { return load().err }

// ConfigDigest is the digest of the embedded dictionary bundle ("" when the
// bundle failed to load).
func ConfigDigest() string { return load().configDigest }

// Vocabulary returns the canonical values per dimension. It is nil when the
// bundle failed to load.
func Vocabulary() []DimensionVocabulary {
	current := load()
	if current.err != nil {
		return nil
	}
	out := make([]DimensionVocabulary, len(current.vocabulary))
	for index, entry := range current.vocabulary {
		out[index] = DimensionVocabulary{Dimension: entry.Dimension, Values: append([]string(nil), entry.Values...)}
	}
	return out
}

// IsDimension reports whether dimension is a title-tag dimension.
func IsDimension(dimension string) bool {
	for _, candidate := range Dimensions {
		if candidate == dimension {
			return true
		}
	}
	return false
}

// CanonicalValue returns the dictionary spelling of value in dimension
// (case-insensitive lookup) and whether it is in the vocabulary.
func CanonicalValue(dimension, value string) (string, bool) {
	current := load()
	if current.err != nil {
		return "", false
	}
	canonical, ok := current.known[dimension][strings.ToLower(strings.TrimSpace(value))]
	return canonical, ok
}

// Extract parses title with the pinned parser and returns its non-episode
// canonical attributes as "type:value". It never fails: an unavailable
// parser, an oversized title or a parser failure yields an empty set with a
// diagnostic.
func Extract(title string) acquisitiondomain.TitleTagSet {
	current := load()
	set := acquisitiondomain.TitleTagSet{Schema: Schema, Implementation: ImplementationVersion, ConfigDigest: current.configDigest, Tags: []string{}, ValueTags: []string{}}
	if current.err != nil {
		set.Diagnostic = acquisitiondomain.TitleTagDiagnosticParserUnavailable
		return set
	}
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return set
	}
	if len(trimmed) > MaxTitleBytes {
		set.Diagnostic = acquisitiondomain.TitleTagDiagnosticParseFailed
		return set
	}
	result, err := parseSafely(current.parser, trimmed)
	if err != nil || result == nil {
		set.Diagnostic = acquisitiondomain.TitleTagDiagnosticParseFailed
		return set
	}
	set.Tags = project(result.Attributes)
	set.ValueTags = projectValues(result.Attributes)
	return set
}

func parseSafely(parser *reltitleparser.Parser, title string) (result *reltitlemodel.ParseResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = nil, fmt.Errorf("reltitle panicked: %v", recovered)
		}
	}()
	return parser.Parse(title)
}

func project(attributes []reltitlemodel.Attribute) []string {
	order := make(map[string]int, len(Dimensions))
	for index, dimension := range Dimensions {
		order[dimension] = index
	}
	type entry struct {
		dimension string
		value     string
	}
	seen := map[string]struct{}{}
	entries := []entry{}
	for _, attribute := range attributes {
		dimension := string(attribute.Type)
		if _, ok := order[dimension]; !ok {
			continue
		}
		value := strings.TrimSpace(attribute.Normalized)
		if value == "" || len(value) > MaxValueBytes || strings.ContainsAny(value, "\r\n\t") {
			continue
		}
		key := acquisitiondomain.NormalizeTagKey(dimension + ":" + value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry{dimension: dimension, value: value})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].dimension != entries[j].dimension {
			return order[entries[i].dimension] < order[entries[j].dimension]
		}
		return entries[i].value < entries[j].value
	})
	tags := make([]string, 0, len(entries))
	for _, item := range entries {
		tags = append(tags, item.dimension+":"+item.value)
	}
	return tags
}

// valueDimensions maps the reltitle attribute types frozen as ValueTags, in
// output order.
var valueDimensions = []struct {
	attribute reltitlemodel.AttrType
	dimension string
}{
	{reltitlemodel.AttrTitle, acquisitiondomain.TitleValueDimensionTitle},
	{reltitlemodel.AttrGroup, acquisitiondomain.TitleValueDimensionGroup},
	{reltitlemodel.AttrVersion, acquisitiondomain.TitleValueDimensionVersion},
}

// projectValues freezes the template-only values "title:<v>", "group:<v>"
// and "version:<v>": title before group before version, each dimension's values
// de-duplicated (case-insensitively) and sorted. An empty, oversized or
// multi-line value is dropped, so it reads as missing rather than truncated.
func projectValues(attributes []reltitlemodel.Attribute) []string {
	tags := []string{}
	for _, name := range valueDimensions {
		seen := map[string]struct{}{}
		values := []string{}
		for _, attribute := range attributes {
			if attribute.Type != name.attribute {
				continue
			}
			value, ok := templateValue(attribute.Normalized)
			if !ok {
				continue
			}
			key := strings.ToLower(value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			values = append(values, value)
		}
		sort.Strings(values)
		for _, value := range values {
			tags = append(tags, name.dimension+":"+value)
		}
	}
	return tags
}

func templateValue(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > MaxTemplateValueBytes || strings.ContainsAny(value, "\r\n\t") {
		return "", false
	}
	return value, true
}

// subtitleTagCarrier places a subtitle file's own tag as one bracketed tag
// after a placeholder title: reltitle parses release titles, and a bare token
// (or a token bracketed with nothing before it) is not read as a tag.
const subtitleTagCarrier = "x [%s]"

// SubtitleLanguages normalizes a subtitle file's own tag (the text between
// its stem and extension, e.g. "chs&jpn") with the same parser and dictionary
// that produce a release title's sub_lang tags, and returns its sub_lang
// values in tag-set order (e.g. ["JPN", "SC"]). An empty or oversized tag, or
// one reltitle reads no subtitle language from, yields nil.
func SubtitleLanguages(tag string) []string {
	tag = strings.TrimSpace(tag)
	if tag == "" || len(tag) > MaxValueBytes {
		return nil
	}
	var languages []string
	for _, item := range Extract(fmt.Sprintf(subtitleTagCarrier, tag)).Tags {
		if dimension, value, ok := acquisitiondomain.SplitTag(item); ok && dimension == "sub_lang" {
			languages = append(languages, value)
		}
	}
	return languages
}

// Parse runs the pinned parser over title and returns its full result. It is
// the shared, bounded entry the reltitle episode adapter uses so both
// consumers run one loaded parser and one lexicon; this package's own Extract
// still projects non-episode attributes only.
func Parse(title string) (*reltitlemodel.ParseResult, error) {
	current := load()
	if current.err != nil {
		return nil, current.err
	}
	trimmed := strings.TrimSpace(title)
	if len(trimmed) > MaxTitleBytes {
		return nil, fmt.Errorf("title exceeds %d bytes", MaxTitleBytes)
	}
	result, err := parseSafely(current.parser, trimmed)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("reltitle returned no result")
	}
	return result, nil
}
