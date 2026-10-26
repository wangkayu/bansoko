// Package qualityprofile owns the replacement comparison profile (profile-v1):
// the registry of namespaced comparison items, the profile value and its
// validation, the frozen per-release comparison values, and the pure
// item-by-item lexicographic comparison. Replacement (filemaintenance), the
// business snapshot (anime/domain) and candidate facts (release/facts) all
// read this one definition.
//
// Every item reads only its own namespaced key. There is no fallback between
// namespaces: a value that is missing or not listed ranks worst.
package qualityprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Version is the only accepted profile version.
const Version = "profile-v1"

// Kind is how an item ranks a release.
type Kind string

const (
	// KindScore ranks by the subscription's group score (no value list).
	KindScore Kind = "score"
	// KindRevision ranks by the vN release revision (no value list).
	KindRevision Kind = "revision"
	// KindSingle ranks by an ordered list of single values.
	KindSingle Kind = "single"
	// KindMulti ranks by an ordered list of value sets (combinations).
	KindMulti Kind = "multi"
)

// Item keys.
const (
	KeySubgroupScore     = "subgroup.score"
	KeyReleaseLang       = "release.lang"
	KeyReleaseResolution = "release.resolution"
	KeyReleaseRevision   = "release.revision"
	KeyAniBTResolution   = "anibt.resolution"
	KeyAniBTLanguage     = "anibt.language"
	KeyAniBTSubtitle     = "anibt.subtitle"
	KeyAniBTFormat       = "anibt.format"
	KeyRtResolution      = "rt.resolution"
	KeyRtSubLang         = "rt.sub_lang"
	KeyRtSubFlag         = "rt.sub_flag"
	KeyRtSource          = "rt.source"
	KeyRtCodec           = "rt.codec"
)

// Namespaces.
const (
	NamespaceSubgroup = "subgroup"
	NamespaceRelease  = "release"
	NamespaceAniBT    = "anibt"
	NamespaceRt       = "rt"
)

// ItemDef is one registered comparison item.
type ItemDef struct {
	Key       string
	Namespace string
	Kind      Kind
}

// Ranked reports whether the item carries a value ranking.
func (d ItemDef) Ranked() bool { return d.Kind == KindSingle || d.Kind == KindMulti }

// Items is the registry in catalog order (namespace, then attribute).
var Items = []ItemDef{
	{KeySubgroupScore, NamespaceSubgroup, KindScore},
	{KeyReleaseLang, NamespaceRelease, KindSingle},
	{KeyReleaseResolution, NamespaceRelease, KindSingle},
	{KeyReleaseRevision, NamespaceRelease, KindRevision},
	{KeyAniBTResolution, NamespaceAniBT, KindSingle},
	{KeyAniBTLanguage, NamespaceAniBT, KindMulti},
	{KeyAniBTSubtitle, NamespaceAniBT, KindSingle},
	{KeyAniBTFormat, NamespaceAniBT, KindSingle},
	{KeyRtResolution, NamespaceRt, KindSingle},
	{KeyRtSubLang, NamespaceRt, KindMulti},
	{KeyRtSubFlag, NamespaceRt, KindMulti},
	{KeyRtSource, NamespaceRt, KindSingle},
	{KeyRtCodec, NamespaceRt, KindSingle},
}

// Lookup returns the registered item.
func Lookup(key string) (ItemDef, bool) {
	for _, item := range Items {
		if item.Key == key {
			return item, true
		}
	}
	return ItemDef{}, false
}

// Bounds of one profile.
const (
	MaxRanks      = 64
	MaxSetValues  = 8
	MaxValueBytes = 128
)

// Item is one configured comparison step: the item key and its ranking, best
// first. Each entry is an order-insensitive value set; a single-value item
// has one value per entry. Score and revision items have no ranking.
type Item struct {
	Key   string     `json:"key"`
	Ranks [][]string `json:"ranks"`
}

// Profile is the ordered comparison configuration: an earlier item decides
// before a later one.
type Profile struct {
	Version string `json:"version"`
	Items   []Item `json:"items"`
}

// Default is the built-in default: subgroup score, then the subtitle
// languages AniBT declares and reltitle reads from the title, then the
// built-in rules' resolution and revision. The two language items are
// separate layers with no fallback between them, each in its own source's
// spelling. Each entry is a language combination; a release hits the first
// entry its languages contain, so a release with Simplified, Traditional and
// Japanese hits the first entry, not the Simplified-only one. A layer the
// release has no data for ties and the next item decides (Compare), so AniBT
// can come first without favouring AniBT releases over Mikan ones.
func Default() Profile {
	return Profile{Version: Version, Items: []Item{
		{Key: KeySubgroupScore, Ranks: [][]string{}},
		{Key: KeyAniBTLanguage, Ranks: [][]string{{"CHS", "CHT", "JPN"}, {"CHS", "JPN"}, {"CHS", "CHT"}, {"CHS"}, {"CHT", "JPN"}, {"CHT"}, {"JPN"}, {"ENG"}}},
		{Key: KeyRtSubLang, Ranks: [][]string{{"SC", "TC", "JPN"}, {"SC", "JPN"}, {"SC", "TC"}, {"SC"}, {"TC", "JPN"}, {"TC"}, {"JPN"}, {"ENG"}}},
		{Key: KeyReleaseResolution, Ranks: [][]string{{"2160p"}, {"1440p"}, {"1080p"}, {"720p"}, {"480p"}}},
		{Key: KeyReleaseRevision, Ranks: [][]string{}},
	}}
}

// DefaultJSON is the default profile document.
func DefaultJSON() json.RawMessage {
	raw, _ := json.Marshal(Default())
	return raw
}

// Parse strictly decodes and validates a stored or submitted profile.
func Parse(raw json.RawMessage) (Profile, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return Profile{}, errors.New("quality profile is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("quality profile: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Validate checks the profile. It never rewrites values: a submitted
// business snapshot is digested by the client as sent.
func (p Profile) Validate() error {
	if p.Version != Version {
		return fmt.Errorf("quality profile version must be %s", Version)
	}
	if p.Items == nil {
		return errors.New("quality profile items are required")
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		def, ok := Lookup(item.Key)
		if !ok {
			return fmt.Errorf("unknown comparison item %q", item.Key)
		}
		if seen[item.Key] {
			return fmt.Errorf("duplicate comparison item %q", item.Key)
		}
		seen[item.Key] = true
		if item.Ranks == nil {
			return fmt.Errorf("comparison item %q must carry a ranking (empty when unused)", item.Key)
		}
		if !def.Ranked() {
			if len(item.Ranks) > 0 {
				return fmt.Errorf("comparison item %q takes no values", item.Key)
			}
			continue
		}
		if len(item.Ranks) > MaxRanks {
			return fmt.Errorf("comparison item %q lists more than %d values", item.Key, MaxRanks)
		}
		sets := map[string]bool{}
		for _, entry := range item.Ranks {
			if def.Kind == KindSingle && len(entry) != 1 {
				return fmt.Errorf("comparison item %q takes single values, not combinations", item.Key)
			}
			if len(entry) == 0 || len(entry) > MaxSetValues {
				return fmt.Errorf("comparison item %q entries hold 1 to %d values", item.Key, MaxSetValues)
			}
			members := map[string]bool{}
			for _, value := range entry {
				trimmed := strings.TrimSpace(value)
				if trimmed == "" || trimmed != value || len(value) > MaxValueBytes || strings.Contains(value, "+") {
					return fmt.Errorf("comparison item %q has an invalid value %q", item.Key, value)
				}
				folded := strings.ToLower(value)
				if members[folded] {
					return fmt.Errorf("comparison item %q repeats %q inside one entry", item.Key, value)
				}
				members[folded] = true
			}
			key := SetKey(entry)
			if sets[key] {
				return fmt.Errorf("comparison item %q lists the same value set twice", item.Key)
			}
			sets[key] = true
		}
	}
	return nil
}

// Without returns the profile without one item (per_group drops the subgroup
// score: inside one group it cannot decide).
func (p Profile) Without(key string) Profile {
	out := Profile{Version: p.Version, Items: make([]Item, 0, len(p.Items))}
	for _, item := range p.Items {
		if item.Key != key {
			out.Items = append(out.Items, item)
		}
	}
	return out
}

// Keys returns the item keys in order.
func (p Profile) Keys() []string {
	keys := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		keys = append(keys, item.Key)
	}
	return keys
}

// SetKey is the order- and case-insensitive identity of a value set.
func SetKey(values []string) string {
	folded := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		folded = append(folded, value)
	}
	sort.Strings(folded)
	return strings.Join(folded, "+")
}
