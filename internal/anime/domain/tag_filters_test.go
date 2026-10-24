package domain

import (
	"encoding/json"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestValidateTagFilterRules(t *testing.T) {
	valid := []string{
		`{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
		`{"anibt":{"require":[],"exclude":["language:CHT","tag:HEVC"]},"title":{"require":["sub_lang:SC"],"exclude":[]}}`,
		`{"anibt":{"require":["resolution:1080p","subtitle:INTERNAL","format:MKV"],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
	}
	for _, raw := range valid {
		if err := ValidateSectionValue(SectionTagFilters, json.RawMessage(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	invalid := map[string]string{
		"missing title row":       `{"anibt":{"require":[],"exclude":[]}}`,
		"null require":            `{"anibt":{"require":null,"exclude":[]},"title":{"require":[],"exclude":[]}}`,
		"missing exclude":         `{"anibt":{"require":[]},"title":{"require":[],"exclude":[]}}`,
		"old mode shape":          `{"anibt":{"mode":"whitelist","tags":[]},"title":{"mode":"whitelist","tags":[]}}`,
		"tag in both lists":       `{"anibt":{"require":["tag:HEVC"],"exclude":["tag:hevc"]},"title":{"require":[],"exclude":[]}}`,
		"unknown field":           `{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]},"x":1}`,
		"no dimension":            `{"anibt":{"require":["CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
		"empty value":             `{"anibt":{"require":["language:"],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
		"padded value":            `{"anibt":{"require":["language: CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
		"unknown anibt dimension": `{"anibt":{"require":["episode:9"],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
		"unknown title dimension": `{"anibt":{"require":[],"exclude":[]},"title":{"require":["customTag:HEVC"],"exclude":[]}}`,
		"case-folded duplicate":   `{"anibt":{"require":["tag:HEVC","tag:hevc"],"exclude":[]},"title":{"require":[],"exclude":[]}}`,
	}
	for name, raw := range invalid {
		if err := ValidateSectionValue(SectionTagFilters, json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted: %s", name, raw)
		}
	}
	many := make([]string, MaxTagFilterTags+1)
	for i := range many {
		many[i] = "tag:v" + strings.Repeat("x", i)
	}
	raw, _ := json.Marshal(TagFilterRules{Anibt: TagFilterRow{Require: many, Exclude: []string{}}, Title: TagFilterRow{Require: []string{}, Exclude: []string{}}})
	if err := ValidateSectionValue(SectionTagFilters, raw); err == nil {
		t.Fatal("unbounded tag list accepted")
	}
}

func TestResolveTagFiltersStoredValueAndDefault(t *testing.T) {
	def := DefaultResolvedTagFilters()
	if def.Version != 1 || len(def.Rules.Anibt.Require) != 0 || def.Rules.Anibt.Exclude == nil || def.Rules.Title.Require == nil {
		t.Fatalf("default = %#v", def)
	}
	if _, err := ResolveTagFilters(OverrideValue{}); err == nil {
		t.Fatal("a missing stored value must fail closed")
	}
	stored, err := ResolveTagFilters(OverrideValue{Value: json.RawMessage(`{"anibt":{"require":["language:CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}`)})
	if err != nil || stored.Version != 0 || stored.Rules.Anibt.Require[0] != "language:CHS" {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
	if stored.Digest == def.Digest {
		t.Fatal("distinct resolutions share a digest")
	}

	subgroups, rules := storedDefaultSections(t)
	rules[SectionTagFilters] = OverrideValue{Value: json.RawMessage(`{"anibt":{"require":["language:CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}`)}
	effective, err := ResolveEffective(subgroups, rules)
	if err != nil || effective.TagFilters.Digest != stored.Digest {
		t.Fatalf("ResolveEffective tag filters = %#v, %v", effective.TagFilters, err)
	}
	if _, ok := effective.Versions[SectionTagFilters]; ok {
		t.Fatal("tag_filters must not enter the six-section version map")
	}
	delete(rules, SectionTagFilters)
	if _, err := ResolveEffective(subgroups, rules); err == nil {
		t.Fatal("an instance without a stored tag_filters value must fail closed")
	}
}

// A rule saved while a customTag value was unclassified (`tag:HEVC`) keeps
// working once the classification table lists it: the validator accepts and
// normalizes it, resolution reads it under the new dimension (deduplicated
// against an entry already there), and matching groups it with that
// dimension, so OR applies within codec and AND across dimensions.
func TestAniBTRuleTagsReclassifyAfterTableUpdate(t *testing.T) {
	raw := json.RawMessage(`{"anibt":{"require":["tag:HEVC","codec:AVC","tag:BDRip"],"exclude":["tag:baha"]},"title":{"require":[],"exclude":[]}}`)
	if err := ValidateSectionValue(SectionTagFilters, raw); err != nil {
		t.Fatalf("validator rejected a pre-classification rule: %v", err)
	}
	var normalized TagFilterRules
	if err := json.Unmarshal(NormalizeTagFilterRulesJSON(raw), &normalized); err != nil {
		t.Fatal(err)
	}
	wantRequire := []string{"codec:HEVC", "codec:AVC", "tag:BDRip"}
	wantExclude := []string{"platform:baha"}
	if strings.Join(normalized.Anibt.Require, ",") != strings.Join(wantRequire, ",") || strings.Join(normalized.Anibt.Exclude, ",") != strings.Join(wantExclude, ",") {
		t.Fatalf("normalized = %#v", normalized.Anibt)
	}
	// An old entry that lands on an entry already in the new dimension folds.
	folded := normalizeAniBTTagFilterRow(TagFilterRow{Require: []string{"tag:HEVC"}, Exclude: []string{"codec:hevc"}})
	if len(folded.Require) != 0 || strings.Join(folded.Exclude, ",") != "codec:hevc" {
		t.Fatalf("folded = %#v", folded)
	}
	override := NormalizeTagFiltersOverrideJSON(json.RawMessage(`{"mode":"replace","value":` + string(raw) + `}`))
	if !strings.Contains(string(override), `"codec:HEVC"`) || strings.Contains(string(override), `"tag:HEVC"`) {
		t.Fatalf("override = %s", override)
	}
	resolved, err := ResolveTagFilters(OverrideValue{Value: raw})
	if err != nil || strings.Join(resolved.Rules.Anibt.Require, ",") != strings.Join(wantRequire, ",") {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	// A release frozen with the old `tag:` form and one with the new form both
	// satisfy codec through either value (OR), and still need tag:BDRip (AND).
	row := TagFilterRow{Require: []string{"tag:HEVC", "codec:AVC", "tag:BDRip"}, Exclude: []string{}}
	for _, item := range []acquisitiondomain.FeedItem{anibtItem("tag:AVC", "tag:BDRip"), anibtItem("codec:HEVC", "tag:BDRip")} {
		if code, detail := AniBTTagFilterReason(item, row); code != "" {
			t.Fatalf("%v: got (%q, %q), want pass", item.AniBTTags.Tags, code, detail)
		}
	}
	if code, detail := AniBTTagFilterReason(anibtItem("codec:AVC"), row); code != ReasonAniBTTagWhitelistUnmet || detail != "missing: tag:BDRip" {
		t.Fatalf("got (%q, %q)", code, detail)
	}
}

func TestRulesSectionOverridesTagFiltersOptional(t *testing.T) {
	sections := completeDraftRules(t)
	sections.TagFilters = nil
	overrides, violations := RulesSectionOverrides(sections)
	if len(violations) != 0 {
		t.Fatal(violations)
	}
	if _, present := overrides[SectionTagFilters]; present {
		t.Fatal("absent tag_filters must not produce an override entry")
	}
	sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":["bogus"],"exclude":[]},"title":{"require":[],"exclude":[]}}}`)
	if _, violations := RulesSectionOverrides(sections); len(violations) != 1 || violations[0].Field != "rules.tag_filters" {
		t.Fatalf("violations = %#v", violations)
	}
	sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":[],"exclude":["tag:HEVC"]},"title":{"require":[],"exclude":[]}}}`)
	overrides, violations = RulesSectionOverrides(sections)
	if len(violations) != 0 || len(overrides[SectionTagFilters].Value) == 0 {
		t.Fatalf("overrides = %#v, %#v", overrides, violations)
	}
	sections.TagFilters = json.RawMessage(`{"mode":"inherit"}`)
	if _, violations := RulesSectionOverrides(sections); len(violations) != 1 {
		t.Fatalf("the retired inherit mode must be refused: %#v", violations)
	}
}

func tagFilterViolations(violations []FieldViolation) []FieldViolation {
	var out []FieldViolation
	for _, violation := range violations {
		if violation.Field == "rules.tag_filters" {
			out = append(out, violation)
		}
	}
	return out
}

func TestValidateDraftAcceptsAndValidatesTagFilters(t *testing.T) {
	ref := "1"
	draft := AnimeDraft{Origin: SubscriptionOriginAnibt, OriginEntryRef: &ref, Title: "Show"}
	for name, raw := range map[string]string{
		"replace": `{"mode":"replace","value":{"anibt":{"require":["resolution:1080p"],"exclude":["language:CHT"]},"title":{"require":["sub_lang:SC"],"exclude":[]}}}`,
	} {
		draft.Rules.Sections.TagFilters = json.RawMessage(raw)
		if got := tagFilterViolations(ValidateDraft(draft)); len(got) != 0 {
			t.Fatalf("%s refused: %#v", name, got)
		}
	}
	for name, raw := range map[string]string{
		"bad shape":         `{"mode":"replace","value":{"anibt":{"require":["bogus"],"exclude":[]},"title":{"require":[],"exclude":[]}}}`,
		"bad mode":          `{"mode":"keep"}`,
		"inherit":           `{"mode":"inherit"}`,
		"not in dictionary": `{"mode":"replace","value":{"anibt":{"require":[],"exclude":[]},"title":{"require":["sub_lang:NOT-A-VALUE"],"exclude":[]}}}`,
	} {
		draft.Rules.Sections.TagFilters = json.RawMessage(raw)
		if got := tagFilterViolations(ValidateDraft(draft)); len(got) != 1 {
			t.Fatalf("%s violations = %#v", name, got)
		}
	}
}

func TestValidateStoredDraftTagFiltersSkipsDictionary(t *testing.T) {
	ref := "1"
	draft := AnimeDraft{Origin: SubscriptionOriginAnibt, OriginEntryRef: &ref, Title: "Show"}
	// A stored value the current dictionary no longer emits stays readable.
	draft.Rules.Sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":[],"exclude":[]},"title":{"require":["sub_lang:NOT-A-VALUE"],"exclude":[]}}}`)
	if got := tagFilterViolations(ValidateStoredDraft(draft)); len(got) != 0 {
		t.Fatalf("stored read refused a dictionary-stale value: %#v", got)
	}
	override, present, err := StoredTagFiltersOverride(draft.Rules.Sections.TagFilters)
	if err != nil || !present || len(override.Value) == 0 {
		t.Fatalf("stored override = %#v, %v, %v", override, present, err)
	}
	// Shape is still enforced on read.
	draft.Rules.Sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":["bogus"],"exclude":[]},"title":{"require":[],"exclude":[]}}}`)
	if got := tagFilterViolations(ValidateStoredDraft(draft)); len(got) != 1 {
		t.Fatalf("stored read accepted a bad shape: %#v", got)
	}
	if _, present, err := StoredTagFiltersOverride(nil); err != nil || present {
		t.Fatalf("absent section = %v, %v", present, err)
	}
}

func TestResolveDraftTagFilters(t *testing.T) {
	var draft AnimeDraft
	absent, err := ResolveDraftTagFilters(draft)
	if err != nil || absent.Digest != DefaultResolvedTagFilters().Digest {
		t.Fatalf("absent = %#v, %v", absent, err)
	}
	draft.Rules.Sections.TagFilters = json.RawMessage(`{"mode":"inherit"}`)
	if _, err := ResolveDraftTagFilters(draft); err == nil {
		t.Fatal("the retired inherit mode must be refused")
	}
	draft.Rules.Sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":["language:CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}}`)
	replaced, err := ResolveDraftTagFilters(draft)
	if err != nil || replaced.Version != 0 || replaced.Rules.Anibt.Require[0] != "language:CHS" {
		t.Fatalf("replace = %#v, %v", replaced, err)
	}
}
