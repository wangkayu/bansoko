package domain

import (
	"encoding/json"
	"strings"
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

func selectionBoolPointer(value bool) *bool { return &value }

// The section's persisted effective_selected must equal the runtime selection
// the group engine makes from the same modes/scores/max_rss. This is the
// contract the saved-revision preview and the owner selection rows rely on.
func TestSubgroupsSectionWithEffectiveSelectionsMatchesGroupEngine(t *testing.T) {
	section := SubgroupsSection{
		MaxRss: 2,
		Entries: []SubgroupScoreEntry{
			{Provider: ProviderKindAnibt, GroupRef: "always-locked", DisplayName: "Locked", SelectionMode: SelectionModeManual, ManualSelected: selectionBoolPointer(true), Score: 10},
			{Provider: ProviderKindAnibt, GroupRef: "ranked-high", DisplayName: "High", SelectionMode: SelectionModeAuto, Score: 90},
			{Provider: ProviderKindAnibt, GroupRef: "ranked-low", DisplayName: "Low", SelectionMode: SelectionModeAuto, Score: 50},
			{Provider: ProviderKindAnibt, GroupRef: "excluded", DisplayName: "Excluded", SelectionMode: SelectionModeManual, ManualSelected: selectionBoolPointer(false), Score: 99},
		},
	}
	got, err := SubgroupsSectionWithEffectiveSelections(section)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"always-locked": true,  // always is selected and, uncounted, consumes no slot
		"ranked-high":   true,  // slot 1
		"ranked-low":    true,  // slot 2
		"excluded":      false, // never
	}
	for _, entry := range got.Entries {
		if entry.EffectiveSelected != want[entry.GroupRef] {
			t.Fatalf("group %s effective_selected = %v, want %v", entry.GroupRef, entry.EffectiveSelected, want[entry.GroupRef])
		}
	}

	// Cross-check against the ref-based engine on the same data.
	groups := []selectiondomain.EffectiveProviderGroup{
		effectiveTestGroup(t, providerdomain.ProviderAniBT, "always-locked", selectiondomain.Always, 0),
		effectiveTestGroup(t, providerdomain.ProviderAniBT, "ranked-high", selectiondomain.Ranked, 90),
		effectiveTestGroup(t, providerdomain.ProviderAniBT, "ranked-low", selectiondomain.Ranked, 50),
		effectiveTestGroup(t, providerdomain.ProviderAniBT, "excluded", selectiondomain.Never, 0),
	}
	engine, err := selectiondomain.SelectProviderGroups(groups, section.MaxRss)
	if err != nil {
		t.Fatal(err)
	}
	engineSelected := make(map[string]bool, len(engine))
	for _, decision := range engine {
		engineSelected[decision.Group.Ref.ExternalID] = decision.Outcome == selectiondomain.GroupSelected
	}
	for _, entry := range got.Entries {
		if engineSelected[entry.GroupRef] != entry.EffectiveSelected {
			t.Fatalf("group %s: section=%v engine=%v", entry.GroupRef, entry.EffectiveSelected, engineSelected[entry.GroupRef])
		}
	}
}

func TestSubgroupsSectionWithEffectiveSelectionsKeepsUnselectedAndCustom(t *testing.T) {
	empty := SubgroupsSection{MaxRss: 3, Entries: []SubgroupScoreEntry{}}
	got, err := SubgroupsSectionWithEffectiveSelections(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 0 || got.Entries == nil {
		t.Fatalf("a present-but-empty entry list must stay an empty array: %+v", got.Entries)
	}
	// The section validator rejects a null entries value, so an empty list must
	// marshal back as [] rather than being dropped.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("normalized empty section marshaled without an entries array: %s", raw)
	}
	var absent SubgroupsSection
	if absentNormalized, err := SubgroupsSectionWithEffectiveSelections(absent); err != nil || absentNormalized.Entries != nil {
		t.Fatalf("absent entries must stay absent: %+v err=%v", absentNormalized.Entries, err)
	}

	// A manual entry without a manual selection is not selected, and a ranked
	// entry beyond max_rss is not selected either.
	section := SubgroupsSection{
		MaxRss: 1,
		Entries: []SubgroupScoreEntry{
			{Provider: ProviderKindMikan, GroupRef: "1", DisplayName: "First", SelectionMode: SelectionModeAuto, Score: 80},
			{Provider: ProviderKindMikan, GroupRef: "2", DisplayName: "Second", SelectionMode: SelectionModeAuto, Score: 70},
			{Provider: ProviderKindMikan, GroupRef: "3", DisplayName: "Manual unset", SelectionMode: SelectionModeManual, Score: 5},
		},
	}
	got, err = SubgroupsSectionWithEffectiveSelections(section)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Entries[0].EffectiveSelected || got.Entries[1].EffectiveSelected || got.Entries[2].EffectiveSelected {
		t.Fatalf("unexpected selection: %+v", got.Entries)
	}
	if section.Entries[0].EffectiveSelected {
		t.Fatal("input section must not be mutated")
	}
}

// A saved candidate revision must carry the computed selection: the draft the
// UI sends marks the group required but the stored section is derived from the
// selection authority, so the immutable revision records effective_selected.
func TestResolveDraftEffectiveSectionsStoresComputedEffectiveSelections(t *testing.T) {
	draft := resolvedTestDraft(t)
	draft.Origin = SubscriptionOriginAnibt
	draft.Title = "Locked Provider Show"
	draft.Subgroups = SubgroupsSection{
		MaxRss: 3,
		Entries: []SubgroupScoreEntry{
			{Provider: ProviderKindAnibt, GroupRef: "locked-group", DisplayName: "Locked", SelectionMode: SelectionModeManual, ManualSelected: selectionBoolPointer(true), Score: 40, EffectiveSelected: false},
		},
	}
	resolved, err := ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatal(err)
	}
	var stored SubgroupsSection
	if err := json.Unmarshal(resolved.Sections[SectionSubgroups], &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) != 1 || !stored.Entries[0].EffectiveSelected {
		t.Fatalf("stored revision effective_selected = %+v, want true", stored.Entries)
	}
}

func effectiveTestGroup(t *testing.T, provider providerdomain.Kind, ref string, mode selectiondomain.SelectionMode, score int) selectiondomain.EffectiveProviderGroup {
	t.Helper()
	groupRef, err := providerdomain.NewGroupRef(provider, ref)
	if err != nil {
		t.Fatal(err)
	}
	return selectiondomain.EffectiveProviderGroup{
		Ref: groupRef, Observed: true, Mode: mode, Score: score,
		PolicySource: selectiondomain.SourceAnimeExact, PolicyVersion: 1,
	}
}

// The reason a group is or is not subscribed comes from the same decision that
// produced effective_selected, so a caller never reconstructs the rules from
// the flags. This is the contract the per-anime "why isn't this group
// subscribed" hint relies on.
func TestSubgroupsSectionSelectionReasonsMatchTheDecision(t *testing.T) {
	section := SubgroupsSection{
		MaxRss: 1,
		Entries: []SubgroupScoreEntry{
			{Provider: ProviderKindAnibt, GroupRef: "locked", DisplayName: "Locked", SelectionMode: SelectionModeManual, ManualSelected: selectionBoolPointer(true), Score: 10},
			{Provider: ProviderKindAnibt, GroupRef: "winner", DisplayName: "Winner", SelectionMode: SelectionModeAuto, Score: 90},
			{Provider: ProviderKindAnibt, GroupRef: "runner-up", DisplayName: "Runner", SelectionMode: SelectionModeAuto, Score: 70},
			{Provider: ProviderKindAnibt, GroupRef: "excluded", DisplayName: "Excluded", SelectionMode: SelectionModeManual, ManualSelected: selectionBoolPointer(false), Score: 99},
			{Provider: ProviderKindAnibt, GroupRef: "retired", DisplayName: "Retired", SelectionMode: SelectionModeAuto, Score: 95},
		},
	}
	reasons, err := SubgroupsSectionSelectionReasons(section, map[string]struct{}{"anibt:retired": {}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"anibt:locked":    selectiondomain.ReasonPolicyAlways,
		"anibt:winner":    selectiondomain.ReasonRankedWithinMaxRSS,
		"anibt:runner-up": selectiondomain.ReasonMaxRSSExhausted,
		"anibt:excluded":  selectiondomain.ReasonPolicyNever,
		// A retired group is unavailable, never "outscored": it cannot compete
		// for a slot it is not eligible for.
		"anibt:retired": selectiondomain.ReasonProviderGroupNotObserved,
	}
	if len(reasons) != len(want) {
		t.Fatalf("reasons = %+v, want one per entry", reasons)
	}
	for key, reason := range want {
		decision := reasons[key]
		if decision.Reason != reason {
			t.Fatalf("group %s reason = %q, want %q", key, decision.Reason, reason)
		}
		// The decision carries the state too, so a caller can refuse to explain
		// a row whose persisted flag disagrees with the decision it replayed.
		if wantSelected := key != "anibt:runner-up" && key != "anibt:excluded" && key != "anibt:retired"; decision.Selected != wantSelected {
			t.Fatalf("group %s selected = %v, want %v", key, decision.Selected, wantSelected)
		}
	}
	// An empty section has nothing to explain.
	if empty, err := SubgroupsSectionSelectionReasons(SubgroupsSection{}, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty section reasons = %+v err=%v", empty, err)
	}
}

// max_rss is decided per provider. A section that carries both providers'
// groups (a global preset merged with an anime override) must not let one
// provider's entries consume the other's slots: both groups below have the
// same score and would both lose under one shared max_rss of 1, and each is in
// fact the only entrant in its own provider's competition.
func TestSubgroupsSectionSelectionReasonsKeepEachProviderInItsOwnCompetition(t *testing.T) {
	section := SubgroupsSection{
		MaxRss: 1,
		Entries: []SubgroupScoreEntry{
			{Provider: ProviderKindAnibt, GroupRef: "anibt-a", DisplayName: "AniBT A", SelectionMode: SelectionModeAuto, Score: 80},
			{Provider: ProviderKindMikan, GroupRef: "58", DisplayName: "Mikan", SelectionMode: SelectionModeAuto, Score: 80},
		},
	}
	reasons, err := SubgroupsSectionSelectionReasonsForKeys(section, []string{"anibt:anibt-a", "mikan:58"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"anibt:anibt-a", "mikan:58"} {
		decision, ok := reasons[key]
		if !ok {
			t.Fatalf("group %s has no decision", key)
		}
		if !decision.Selected || decision.Reason != selectiondomain.ReasonRankedWithinMaxRSS {
			t.Fatalf("group %s = %+v, want selected within its own provider's max_rss", key, decision)
		}
	}
	// A key the section does not list is answered with nothing, not a guess.
	missing, err := SubgroupsSectionSelectionReasonsForKeys(section, []string{"anibt:absent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := missing["anibt:absent"]; ok {
		t.Fatalf("unlisted group was explained: %+v", missing)
	}
}
