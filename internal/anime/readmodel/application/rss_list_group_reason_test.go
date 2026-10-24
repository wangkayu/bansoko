package application

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

// A group header says more than selected/unselected: the browser shows why the
// group is in that state, and the answer is the decision core's own reason.
// The read model must carry it per group rather than let a client re-derive the
// rules from count_in_limit and the score it sees.
func TestRSSListGroupEntriesCarryTheSelectionReason(t *testing.T) {
	// One slot, three equally scored groups: the lowest key wins it and the
	// other two lost on max_rss — the reason a person needs to see.
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13", "14")
	draft.Subgroups.MaxRss = 1
	// A served section already carries the computed flag; the read explains a
	// stored state, it never decides one.
	normalized, err := animedomain.SubgroupsSectionWithEffectiveSelections(draft.Subgroups)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	effective := animedomain.EffectiveConfig{Selection: normalized}

	entries, err := rssListGroupEntries(draft, effective, 0)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	byRef := make(map[string]*string, len(entries))
	for _, entry := range entries {
		byRef[entry.groupRef] = entry.reasonCode
	}
	for group, want := range map[string]string{
		"12": "ranked_within_max_rss",
		"13": "max_rss_exhausted",
		"14": "max_rss_exhausted",
	} {
		if byRef[group] == nil || *byRef[group] != want {
			t.Fatalf("group %s reason = %v, want %q", group, byRef[group], want)
		}
	}

	// A stored flag the replay does not reproduce gets no explanation: the hint
	// must never contradict the badge it sits next to. Group 12 claims to have
	// won its slot; the decision gives that slot to the higher-scored group 13.
	draft.Subgroups = animedomain.SubgroupsSection{MaxRss: 1, Entries: []animedomain.SubgroupScoreEntry{
		{Provider: animedomain.ProviderKindAnibt, GroupRef: "12", DisplayName: "Group 12", SelectionMode: animedomain.SelectionModeAuto, Score: 50, CountInLimit: true, EffectiveSelected: true},
		{Provider: animedomain.ProviderKindAnibt, GroupRef: "13", DisplayName: "Group 13", SelectionMode: animedomain.SelectionModeAuto, Score: 60, CountInLimit: true, EffectiveSelected: true},
	}}
	effective = animedomain.EffectiveConfig{Selection: draft.Subgroups}
	entries, err = rssListGroupEntries(draft, effective, 0)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	explained := 0
	for _, entry := range entries {
		if entry.reasonCode == nil {
			continue
		}
		explained++
		if entry.groupRef != "13" {
			t.Fatalf("group %s was explained although its flag contradicts the decision: %q", entry.groupRef, *entry.reasonCode)
		}
	}
	if explained != 1 {
		t.Fatalf("explained %d groups, want only the one the decision agrees with", explained)
	}
}
