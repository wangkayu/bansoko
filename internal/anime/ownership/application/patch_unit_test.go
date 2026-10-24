package application

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func TestApplyGroupActions(t *testing.T) {
	section := SubgroupsSection{Entries: []SubgroupScoreEntry{{
		GroupRef: "group-1", Provider: string(animedomain.ProviderKindAnibt),
		SelectionMode: string(animedomain.SelectionModeAuto), EffectiveSelected: true,
	}}}
	if err := applyGroupActions(&section, []GroupAction{{
		Action: GroupActionForceExclude, GroupRef: "group-1", Provider: string(animedomain.ProviderKindAnibt),
	}}); err != nil {
		t.Fatalf("exclude group: %v", err)
	}
	entry := section.Entries[0]
	if entry.SelectionMode != string(animedomain.SelectionModeManual) || entry.ManualSelected == nil || *entry.ManualSelected || entry.EffectiveSelected {
		t.Fatalf("excluded entry = %+v", entry)
	}
	if err := applyGroupActions(&section, []GroupAction{{
		Action: GroupActionForceInclude, GroupRef: "group-1", Provider: string(animedomain.ProviderKindAnibt),
	}}); err != nil {
		t.Fatalf("include group: %v", err)
	}
	entry = section.Entries[0]
	if entry.ManualSelected == nil || !*entry.ManualSelected || !entry.EffectiveSelected {
		t.Fatalf("included entry = %+v", entry)
	}
	if err := applyGroupActions(&section, []GroupAction{{
		Action: GroupActionRestoreAuto, GroupRef: "group-1", Provider: string(animedomain.ProviderKindAnibt),
	}}); err != nil {
		t.Fatalf("restore group: %v", err)
	}
	entry = section.Entries[0]
	if entry.SelectionMode != string(animedomain.SelectionModeAuto) || entry.ManualSelected != nil || entry.EffectiveSelected {
		t.Fatalf("restored entry = %+v", entry)
	}
}

func TestApplyGroupActionsRejectsUnknownGroup(t *testing.T) {
	err := applyGroupActions(&SubgroupsSection{}, []GroupAction{{
		Action: GroupActionForceInclude, GroupRef: "missing", Provider: string(animedomain.ProviderKindMikan),
	}})
	if err == nil {
		t.Fatal("expected unknown group error")
	}
}

// One computation feeds the section a client reads and the owner selection
// list, so a caller can never read a stale effective flag and re-derive the
// rule itself.
func TestNormalizeSubgroupsEffectiveSelection(t *testing.T) {
	section := SubgroupsSection{
		MaxRss: 1,
		Entries: []SubgroupScoreEntry{
			{GroupRef: "lower", Provider: string(animedomain.ProviderKindAnibt), SelectionMode: string(animedomain.SelectionModeAuto), Score: 50, CountInLimit: true},
			{GroupRef: "higher", Provider: string(animedomain.ProviderKindAnibt), SelectionMode: string(animedomain.SelectionModeAuto), Score: 80, CountInLimit: true},
			{GroupRef: "manual", Provider: string(animedomain.ProviderKindMikan), SelectionMode: string(animedomain.SelectionModeManual), Score: 1, ManualSelected: boolPointer(true)},
		},
	}
	projected, got, err := normalizeSubgroupsEffectiveSelection(section, "subscription-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("selection count = %d, want 3", len(got))
	}
	if got[0].EffectiveSelected || !got[1].EffectiveSelected || !got[2].EffectiveSelected {
		t.Fatalf("selection state = %+v", got)
	}
	if got[1].ID != "subscription-1:higher" || got[1].Version != 4 {
		t.Fatalf("selection identity = %+v", got[1])
	}
	// The projected section carries the same computed answer, so the client
	// reads one fact instead of re-implementing the rule.
	if projected.Entries[0].EffectiveSelected || !projected.Entries[1].EffectiveSelected || !projected.Entries[2].EffectiveSelected {
		t.Fatalf("projected section flags = %+v", projected.Entries)
	}
	if projected.Entries[1].GroupRef != "higher" || projected.Entries[1].Score != 80 {
		t.Fatalf("projected entry identity = %+v", projected.Entries[1])
	}
}

func boolPointer(value bool) *bool { return &value }
