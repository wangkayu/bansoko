package domain

import (
	"encoding/json"
	"testing"

	"bansoko/internal/batchplan"
)

func TestValidateDownloadCompletionRules(t *testing.T) {
	for _, raw := range []string{
		`{"action":"seed"}`, `{"action":"pause"}`, `{"action":"remove"}`, `{"action":"remove_with_data"}`,
		`{"action":"seed","seed_limits":{"seeding_time_minutes":1440,"at_limit":"stop"}}`,
		`{"action":"seed","seed_limits":{"ratio":1.5,"at_limit":"remove"}}`,
		`{"action":"seed","seed_limits":{"seeding_time_minutes":60,"ratio":2,"at_limit":"remove_with_data"}}`,
	} {
		if err := ValidateSectionValue(SectionDownloadCompletion, json.RawMessage(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for name, raw := range map[string]string{
		"missing action":        `{}`,
		"empty action":          `{"action":""}`,
		"unknown action":        `{"action":"wipe"}`,
		"case":                  `{"action":"Seed"}`,
		"unknown field":         `{"action":"seed","delete":true}`,
		"not an object":         `"seed"`,
		"null action":           `{"action":null}`,
		"limits on pause":       `{"action":"pause","seed_limits":{"ratio":1,"at_limit":"stop"}}`,
		"limits without limit":  `{"action":"seed","seed_limits":{"at_limit":"stop"}}`,
		"limits without action": `{"action":"seed","seed_limits":{"ratio":1}}`,
		"unknown at_limit":      `{"action":"seed","seed_limits":{"ratio":1,"at_limit":"pause"}}`,
		"zero minutes":          `{"action":"seed","seed_limits":{"seeding_time_minutes":0,"at_limit":"stop"}}`,
		"zero ratio":            `{"action":"seed","seed_limits":{"ratio":0,"at_limit":"stop"}}`,
		"minutes over a year":   `{"action":"seed","seed_limits":{"seeding_time_minutes":525601,"at_limit":"stop"}}`,
		"ratio over the max":    `{"action":"seed","seed_limits":{"ratio":9999,"at_limit":"stop"}}`,
		"limits unknown field":  `{"action":"seed","seed_limits":{"ratio":1,"at_limit":"stop","inactive":5}}`,
	} {
		if err := ValidateSectionValue(SectionDownloadCompletion, json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted: %s", name, raw)
		}
	}
}

// The closed action set the section accepts is the one the batch plan freezes.
func TestDownloadCompletionActionsMatchTheBatchPlan(t *testing.T) {
	for _, action := range DownloadCompletionActions {
		if !batchplan.ValidCompletionAction(string(action)) {
			t.Fatalf("batch plan does not know %q", action)
		}
	}
	for _, value := range []string{batchplan.CompletionSeed, batchplan.CompletionPause, batchplan.CompletionRemove, batchplan.CompletionRemoveWithData} {
		if !DownloadCompletionAction(value).Valid() {
			t.Fatalf("section does not know %q", value)
		}
	}
	if len(DownloadCompletionActions) != 4 {
		t.Fatalf("actions = %v", DownloadCompletionActions)
	}
}

func TestResolveDownloadCompletionGlobalAndDefault(t *testing.T) {
	def := DefaultResolvedDownloadCompletion()
	if def.Version != 1 || def.Rules.Action != DownloadCompletionSeed || def.Rules.SeedLimits != nil {
		t.Fatalf("default = %#v", def)
	}
	globals := map[string]GlobalSection{SectionDownloadCompletion: {SchemaVersion: DownloadCompletionSchemaVersion, Value: json.RawMessage(`{"action":"seed","seed_limits":{"ratio":2,"at_limit":"remove"}}`), Version: 4}}
	resolved, err := ResolveDownloadCompletion(globals)
	if err != nil || resolved.Version != 4 || resolved.Rules.SeedLimits == nil || *resolved.Rules.SeedLimits.Ratio != 2 {
		t.Fatalf("Global row = %#v %v", resolved, err)
	}
	// A v1 value ({action} only) is still a valid v2 value.
	old, err := ResolveDownloadCompletion(map[string]GlobalSection{SectionDownloadCompletion: {Value: json.RawMessage(`{"action":"remove"}`), Version: 2}})
	if err != nil || old.Rules.Action != DownloadCompletionRemove || old.Rules.SeedLimits != nil {
		t.Fatalf("v1 value = %#v %v", old, err)
	}
	// A stored value that is no longer valid fails closed, it is not replaced by seed.
	if _, err := ResolveDownloadCompletion(map[string]GlobalSection{SectionDownloadCompletion: {Value: json.RawMessage(`{"action":"wipe"}`), Version: 2}}); err == nil {
		t.Fatal("an invalid stored value resolved")
	}
	// Anime sections play no part: ResolveEffective never resolves it, even
	// when a stale stored map carries the section; callers apply the Global row.
	subgroups, rules := storedDefaultSections(t)
	rules[SectionDownloadCompletion] = OverrideValue{Value: json.RawMessage(`{"action":"pause"}`)}
	effective, err := ResolveEffective(subgroups, rules)
	if err != nil || effective.DownloadCompletion.Rules.Action != "" {
		t.Fatalf("effective = %#v %v", effective.DownloadCompletion, err)
	}
}

func TestDownloadCompletionRulesEqualComparesLimitsByValue(t *testing.T) {
	minutes, other := int64(60), int64(60)
	a := DownloadCompletionRules{Action: DownloadCompletionSeed, SeedLimits: &DownloadCompletionSeedLimits{SeedingTimeMinutes: &minutes, AtLimit: "stop"}}
	b := DownloadCompletionRules{Action: DownloadCompletionSeed, SeedLimits: &DownloadCompletionSeedLimits{SeedingTimeMinutes: &other, AtLimit: "stop"}}
	if !a.Equal(b) {
		t.Fatal("equal limits compare unequal")
	}
	b.SeedLimits.AtLimit = "remove"
	if a.Equal(b) || a.Equal(DownloadCompletionRules{Action: DownloadCompletionSeed}) {
		t.Fatal("different limits compare equal")
	}
}

func TestDownloadCompletionIsAGlobalOnlySetting(t *testing.T) {
	for _, section := range EffectiveSectionOrder {
		if section == SectionDownloadCompletion {
			t.Fatal("download_completion must not join the six frozen effective sections")
		}
	}
	for _, section := range RulesOwnerSectionOrder {
		if section == SectionDownloadCompletion {
			t.Fatal("download_completion must not be a per-anime rules-owner section")
		}
	}
	found := false
	for _, section := range GlobalSectionOrder {
		found = found || section == SectionDownloadCompletion
	}
	if !found {
		t.Fatal("download_completion is not a Global section")
	}
	if SectionSchemaVersions[SectionDownloadCompletion] != "download-completion-v2" {
		t.Fatalf("schema = %q", SectionSchemaVersions[SectionDownloadCompletion])
	}
	// A frozen envelope decodes it as the code default (seed).
	subgroups, rules := storedDefaultSections(t)
	resolved, err := ResolveEffectiveSections(subgroups, rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := resolved.Sections[SectionDownloadCompletion]; present {
		t.Fatal("download_completion entered the frozen envelope")
	}
	effective, err := EffectiveConfigFromSections(resolved)
	if err != nil || effective.DownloadCompletion.Rules.Action != DownloadCompletionSeed {
		t.Fatalf("frozen envelope = %#v %v", effective.DownloadCompletion, err)
	}
}
