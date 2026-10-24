package domain

import "testing"

// A frozen copy of the Global subgroups section may carry the live site's
// pinned scores. The member is optional so a value frozen before it existed
// validates unchanged, and the object stays closed for every other key.
func TestSubgroupsSectionPinnedScoresAreOptional(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"without pins", `{"entries":[],"max_rss":3,"name_keywords":null,"provider_defaults":{"anibt":null,"mikan":{"groups":["1"],"mode":"blacklist","score":50}}}`, true},
		{"with pins", `{"entries":[],"max_rss":3,"name_keywords":null,"provider_defaults":{"anibt":null,"mikan":{"groups":[],"mode":"blacklist","score":50,"pinned_scores":[{"group_ref":"7","score":90}]}}}`, true},
		{"pin out of range", `{"entries":[],"max_rss":3,"name_keywords":null,"provider_defaults":{"anibt":null,"mikan":{"groups":[],"mode":"blacklist","score":50,"pinned_scores":[{"group_ref":"7","score":0}]}}}`, false},
		{"unknown key", `{"entries":[],"max_rss":3,"name_keywords":null,"provider_defaults":{"anibt":null,"mikan":{"groups":[],"mode":"blacklist","score":50,"extra":1}}}`, false},
		{"missing required", `{"entries":[],"max_rss":3,"name_keywords":null,"provider_defaults":{"anibt":null,"mikan":{"mode":"blacklist","score":50}}}`, false},
	}
	for _, tc := range cases {
		v, err := ParseStrict([]byte(tc.raw))
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		err = Validate("SubgroupsSectionV1", v)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: validate err=%v, want ok=%t", tc.name, err, tc.ok)
		}
	}
}
