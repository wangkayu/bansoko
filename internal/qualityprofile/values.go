package qualityprofile

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ValuesVersion identifies the frozen comparison values shape.
const ValuesVersion = "compare-values-v2"

// SubgroupSource is the frozen RSS source group identity of a release: the
// provider and its group ref, or CustomSubgroupProvider and the RSS source id
// of a custom RSS source. The subscription's score for it is looked up at
// comparison time; a release without one (or a custom source, which has no
// score) ranks lowest.
type SubgroupSource struct {
	Provider string `json:"provider"`
	GroupRef string `json:"group_ref"`
}

// CustomSubgroupProvider is the subgroup provider of a custom RSS source; its
// group ref is the RSS source id.
const CustomSubgroupProvider = "custom"

// Key is the stable group identity string "provider:group_ref".
func (s SubgroupSource) Key() string {
	return s.Provider + ":" + s.GroupRef
}

// FrozenValues are the comparison values one release contributes, frozen
// with its candidate facts at acceptance: item key -> values (multi-value
// fields split on "+"), plus the subgroup source.
type FrozenValues struct {
	Version      string              `json:"version"`
	SourceDigest string              `json:"source_digest"`
	Values       map[string][]string `json:"values"`
	Subgroup     *SubgroupSource     `json:"subgroup,omitempty"`
}

// NewValues starts an empty frozen value set.
func NewValues() FrozenValues {
	return FrozenValues{Version: ValuesVersion, Values: map[string][]string{}}
}

// Set records one item's values: trimmed, non-empty, de-duplicated
// case-insensitively (first spelling wins). Empty input records nothing.
func (v *FrozenValues) Set(key string, values ...string) {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		folded := strings.ToLower(value)
		if value == "" || seen[folded] {
			continue
		}
		seen[folded] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		return
	}
	if v.Values == nil {
		v.Values = map[string][]string{}
	}
	v.Values[key] = out
}

// SetJoined records a "+"-joined value.
func (v *FrozenValues) SetJoined(key, joined string) {
	v.Set(key, strings.Split(joined, "+")...)
}

// ParseValues rejects stale or malformed caches. Migration/refresh must
// explicitly rebuild them from frozen source tags, never silently read them empty.
func ParseValues(raw []byte) (FrozenValues, error) {
	var values FrozenValues
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&values); err != nil {
		return FrozenValues{}, fmt.Errorf("decode comparison values: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return FrozenValues{}, fmt.Errorf("comparison values must contain exactly one JSON document")
	}
	if values.Version != ValuesVersion {
		return FrozenValues{}, fmt.Errorf("comparison values version must be %s, got %q", ValuesVersion, values.Version)
	}
	if digest, err := hex.DecodeString(values.SourceDigest); err != nil || len(digest) != 32 || values.SourceDigest != strings.ToLower(values.SourceDigest) {
		return FrozenValues{}, fmt.Errorf("comparison values source digest must be a lowercase SHA256")
	}
	if values.Values == nil {
		return FrozenValues{}, fmt.Errorf("comparison values map is required")
	}
	for key, items := range values.Values {
		definition, ok := Lookup(key)
		if !ok || definition.Kind == KindScore || len(items) == 0 {
			return FrozenValues{}, fmt.Errorf("invalid comparison values key %q", key)
		}
		seen := map[string]bool{}
		for _, item := range items {
			folded := strings.ToLower(item)
			if item == "" || item != strings.TrimSpace(item) || seen[folded] {
				return FrozenValues{}, fmt.Errorf("invalid comparison value for %q", key)
			}
			seen[folded] = true
		}
	}
	if group := values.Subgroup; group != nil {
		if (group.Provider != "mikan" && group.Provider != "anibt" && group.Provider != CustomSubgroupProvider) || group.GroupRef == "" || group.GroupRef != strings.TrimSpace(group.GroupRef) {
			return FrozenValues{}, fmt.Errorf("invalid comparison source group")
		}
	}
	return values, nil
}

// JSON encodes the frozen values.
func (v FrozenValues) JSON() json.RawMessage {
	if v.Values == nil {
		v.Values = map[string][]string{}
	}
	raw, _ := json.Marshal(v)
	return raw
}

// RevisionNumber interprets only a frozen rt Version value. Missing/invalid
// input has no rank; it is never defaulted to v1 or parsed from a title.
func RevisionNumber(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") || strings.HasPrefix(value, "V") {
		value = value[1:]
	}
	if value == "" {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil && number > 0
}
