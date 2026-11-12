package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Stored and frozen section decoding share the same values, but policies must
// come from explicit inputs rather than being inferred from the envelope.
func TestResolvedSectionsPreserveNonDefaultPolicies(t *testing.T) {
	subgroups, rules := storedDefaultSections(t)
	rules[SectionTagFilters] = OverrideValue{Value: json.RawMessage(`{"anibt":{"require":["language:CHS"],"exclude":["tag:HEVC"]},"title":{"require":["sub_lang:SC"],"exclude":[]}}`)}
	stored, tags, err := ResolveStoredSectionsWithTags(subgroups, rules)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveEffectiveSections(subgroups, rules)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEffectiveSections(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, decoded) {
		t.Fatalf("stored and frozen sections differ: %+v vs %+v", stored, decoded)
	}
	effective, err := NewEffectiveConfig(decoded, tags, DefaultResolvedDownloadCompletion())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effective.TagFilters, tags) || tags.Digest == DefaultResolvedTagFilters().Digest {
		t.Fatalf("explicit non-default tags lost: %+v", effective.TagFilters)
	}
	if _, ok := resolved.Sections[SectionTagFilters]; ok {
		t.Fatal("tags must not enter the six-section envelope")
	}
}

func TestResolvedSectionsPreserveNonDefaultDownloadCompletion(t *testing.T) {
	resolved, err := ResolveDraftEffectiveSections(resolvedTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEffectiveSections(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digestBefore, err := resolved.EffectiveDigest()
	if err != nil {
		t.Fatal(err)
	}
	ratio := 2.0
	for _, policy := range []ResolvedDownloadCompletion{
		{Rules: DownloadCompletionRules{Action: DownloadCompletionRemove}, Version: 3},
		{Rules: DownloadCompletionRules{Action: DownloadCompletionSeed, SeedLimits: &DownloadCompletionSeedLimits{Ratio: &ratio, AtLimit: "remove"}}, Version: 4},
	} {
		effective, err := NewEffectiveConfig(decoded, DefaultResolvedTagFilters(), policy)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(effective.DownloadCompletion, policy) {
			t.Fatalf("explicit completion lost: %+v", effective.DownloadCompletion)
		}
	}
	digestAfter, err := resolved.EffectiveDigest()
	if err != nil || digestAfter != digestBefore {
		t.Fatalf("policy construction changed frozen digest: %s %v", digestAfter, err)
	}
}

func TestNewEffectiveConfigRequiresExplicitPolicies(t *testing.T) {
	resolved, err := ResolveDraftEffectiveSections(resolvedTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEffectiveSections(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		tags       ResolvedTagFilters
		completion ResolvedDownloadCompletion
		wantError  bool
	}{
		{"neither", ResolvedTagFilters{}, ResolvedDownloadCompletion{}, true},
		{"missing tags", ResolvedTagFilters{}, DefaultResolvedDownloadCompletion(), true},
		{"missing completion", DefaultResolvedTagFilters(), ResolvedDownloadCompletion{}, true},
		{"explicit defaults", DefaultResolvedTagFilters(), DefaultResolvedDownloadCompletion(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewEffectiveConfig(decoded, tc.tags, tc.completion)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
			if err == nil && (!reflect.DeepEqual(got.TagFilters, tc.tags) || !reflect.DeepEqual(got.DownloadCompletion, tc.completion)) {
				t.Fatalf("policies changed: %+v", got)
			}
		})
	}
}

func TestResolvedSectionsDigestSensitiveToNonDefaultSections(t *testing.T) {
	draft := resolvedTestDraft(t)
	resolved1, err := ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatal(err)
	}
	digest1, err := resolved1.EffectiveDigest()
	if err != nil {
		t.Fatal(err)
	}
	draft.Subgroups.MaxRss = 3
	resolved2, err := ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatal(err)
	}
	digest2, err := resolved2.EffectiveDigest()
	if err != nil {
		t.Fatal(err)
	}
	if digest1 == digest2 {
		t.Fatal("digest must change when a section value changes")
	}
}
