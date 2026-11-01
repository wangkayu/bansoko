package domain

import (
	"encoding/json"
	"testing"
)

func TestSubscriptionDefaultsSectionValue(t *testing.T) {
	if SectionSchemaVersions[SectionSubscriptionDefaults] != SubscriptionDefaultsSchemaVersion {
		t.Fatal("subscription_defaults schema version is not registered")
	}
	value, err := DefaultSectionValue(SectionSubscriptionDefaults)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ignore_multi_video_releases":false}` {
		t.Fatalf("default value = %s", raw)
	}
	if err := ValidateSectionValue(SectionSubscriptionDefaults, raw); err != nil {
		t.Fatalf("default refused: %v", err)
	}
	if err := ValidateSectionValue(SectionSubscriptionDefaults, json.RawMessage(`{"ignore_multi_video_releases":true}`)); err != nil {
		t.Fatalf("true refused: %v", err)
	}
	for _, bad := range []string{
		`{}`,
		`{"ignore_multi_video_releases":"true"}`,
		`{"ignore_multi_video_releases":null}`,
		`{"ignore_multi_video_releases":false,"extra":1}`,
		`null`,
		`[]`,
	} {
		if err := ValidateSectionValue(SectionSubscriptionDefaults, json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	// The section is a one-time-input default, not a rules-owner section: no
	// per-anime override reads it and it stays outside the effective order.
	for _, name := range append(append([]string(nil), RulesOwnerSectionOrder...), EffectiveSectionOrder...) {
		if name == SectionSubscriptionDefaults {
			t.Fatal("subscription_defaults must not be an overridable or effective section")
		}
	}
	if GlobalSectionOrder[len(GlobalSectionOrder)-1] != SectionSubscriptionDefaults {
		t.Fatalf("subscription_defaults is not listed last in GlobalSectionOrder: %v", GlobalSectionOrder)
	}
}
