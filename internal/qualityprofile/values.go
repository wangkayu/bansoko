package qualityprofile

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ValuesVersion identifies the frozen comparison values shape.
const ValuesVersion = "compare-values-v1"

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
	Version  string              `json:"version"`
	Values   map[string][]string `json:"values"`
	Subgroup *SubgroupSource     `json:"subgroup,omitempty"`
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

// ParseValues decodes a stored compare_values document; an empty or
// unrecognized document has no values.
func ParseValues(raw []byte) FrozenValues {
	var values FrozenValues
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil || values.Version != ValuesVersion {
		return NewValues()
	}
	if values.Values == nil {
		values.Values = map[string][]string{}
	}
	if values.Subgroup != nil && (strings.TrimSpace(values.Subgroup.Provider) == "" || strings.TrimSpace(values.Subgroup.GroupRef) == "") {
		values.Subgroup = nil
	}
	return values
}

// JSON encodes the frozen values.
func (v FrozenValues) JSON() json.RawMessage {
	if v.Values == nil {
		v.Values = map[string][]string{}
	}
	v.Version = ValuesVersion
	raw, _ := json.Marshal(v)
	return raw
}

// revisionPattern reads a stored revision marker: "v2" or "2".
var revisionPattern = regexp.MustCompile(`(?i)^v?(\d+)$`)

// RevisionNumber is the numeric vN of a revision marker; no marker (or an
// unreadable one) is v1.
func RevisionNumber(value string) int64 {
	match := revisionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 {
		return 1
	}
	number, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || number < 1 {
		return 1
	}
	return number
}
