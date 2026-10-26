package application

import (
	"context"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	selectiondomain "bansoko/internal/selection/domain"
)

// The subgroup tab shows the selection an unsaved value would make. These
// tests pin that the draft preview is the current_input RSS list read's own
// group decision (no second implementation) and that it follows the edit.

func previewFactsByRef(t *testing.T, preview SubgroupsSelectionPreview) map[string]SubgroupSelectionFact {
	t.Helper()
	byRef := make(map[string]SubgroupSelectionFact, len(preview.Groups))
	for _, fact := range preview.Groups {
		byRef[fact.GroupRef] = fact
	}
	return byRef
}

func observedDraftService() *Service {
	return observationService(&fakeObservationReader{found: true, observation: GroupObservation{
		ObservedAt: time.Unix(1700000000, 0).UTC(),
		Groups:     observedFacts("12", "34", "56"),
	}})
}

var previewObservation = &RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 4}

func TestPreviewSubgroupsSelectionDraftAgreesWithTheRSSListRead(t *testing.T) {
	draft := businessDraft(t, 2)
	service := observedDraftService()
	view, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "current_input", Draft: &draft, Observation: previewObservation})
	if err != nil {
		t.Fatalf("ReadAnimeRSSListView: %v", err)
	}
	preview, err := service.PreviewSubgroupsSelection(context.Background(), SubgroupsSelectionPreviewInput{InputSource: "current_input", Draft: &draft, Observation: previewObservation})
	if err != nil {
		t.Fatalf("PreviewSubgroupsSelection: %v", err)
	}
	if preview.Mode != "current_input" || len(preview.Groups) != len(view.Groups) || len(view.Groups) != 3 {
		t.Fatalf("preview %+v must list the read's groups %+v", preview, view.Groups)
	}
	facts := previewFactsByRef(t, preview)
	for _, group := range view.Groups {
		fact, ok := facts[group.Scope.ProviderGroupRef]
		if !ok {
			t.Fatalf("group %s missing from preview", group.Scope.ProviderGroupRef)
		}
		if fact.EffectiveSelected != group.Selection.EffectiveSelected || !fact.Available {
			t.Fatalf("preview %+v disagrees with read %+v", fact, group.Selection)
		}
		if (fact.ReasonCode == nil) != (group.Selection.ReasonCode == nil) || (fact.ReasonCode != nil && *fact.ReasonCode != *group.Selection.ReasonCode) {
			t.Fatalf("reason %v disagrees with read %v", fact.ReasonCode, group.Selection.ReasonCode)
		}
	}
}

func TestPreviewSubgroupsSelectionDraftFollowsUnsavedEdits(t *testing.T) {
	service := observedDraftService()
	count := func(draft animedomain.AnimeDraft) (int, map[string]SubgroupSelectionFact) {
		t.Helper()
		preview, err := service.PreviewSubgroupsSelection(context.Background(), SubgroupsSelectionPreviewInput{InputSource: "current_input", Draft: &draft, Observation: previewObservation})
		if err != nil {
			t.Fatalf("PreviewSubgroupsSelection: %v", err)
		}
		selected := 0
		for _, fact := range preview.Groups {
			if fact.EffectiveSelected && fact.ReasonCode != nil && *fact.ReasonCode == selectiondomain.ReasonRankedWithinMaxRSS {
				selected++
			}
		}
		return selected, previewFactsByRef(t, preview)
	}
	// A draft carrying its business snapshot resolves its own subgroups value,
	// exactly as the subgroup tab edits it.
	draft := businessDraft(t, 1)
	if n, _ := count(draft); n != 1 {
		t.Fatalf("max_rss=1 selects one, got %d", n)
	}
	draft.Subgroups.MaxRss = 3
	if n, _ := count(draft); n != 3 {
		t.Fatalf("raising the unsaved max_rss to 3 selects all three, got %d", n)
	}
	never := false
	draft.Subgroups.Entries = append(draft.Subgroups.Entries, animedomain.SubgroupScoreEntry{
		GroupRef: "34", DisplayName: "Observed 34", Provider: animedomain.ProviderKindAnibt,
		SelectionMode: animedomain.SelectionModeManual, ManualSelected: &never,
	})
	n, facts := count(draft)
	if n != 2 {
		t.Fatalf("an unsaved never leaves two selected, got %d", n)
	}
	if fact := facts["34"]; fact.EffectiveSelected || fact.ReasonCode == nil || *fact.ReasonCode != "policy_never" {
		t.Fatalf("the never group carries the decision's reason: %+v", fact)
	}
}

type previewOwnerReader struct {
	OwnerReader
	animeID string
	section ownershipapplication.SubgroupsSection
	facts   []ownershipapplication.SubgroupSelectionPreviewFact
}

func (r *previewOwnerReader) PreviewSubgroupsSelectionApplication(_ context.Context, animeID string, section ownershipapplication.SubgroupsSection) ([]ownershipapplication.SubgroupSelectionPreviewFact, error) {
	r.animeID, r.section = animeID, section
	return r.facts, nil
}

func TestPreviewSubgroupsSelectionOwnerDraftDelegatesToTheOwnerDecision(t *testing.T) {
	reason := "max_rss_exhausted"
	owners := &previewOwnerReader{facts: []ownershipapplication.SubgroupSelectionPreviewFact{{Provider: "mikan", GroupRef: "7", Available: true, ReasonCode: &reason}}}
	service := &Service{owners: owners}
	section := ownershipapplication.SubgroupsSection{MaxRss: 0, Entries: []ownershipapplication.SubgroupScoreEntry{}}
	preview, err := service.PreviewSubgroupsSelection(context.Background(), SubgroupsSelectionPreviewInput{InputSource: "owner_draft", ManagedAnimeID: "anime-1", Subgroups: &section})
	if err != nil {
		t.Fatalf("PreviewSubgroupsSelection: %v", err)
	}
	if owners.animeID != "anime-1" || owners.section.MaxRss != 0 {
		t.Fatalf("owner and unsaved value must be forwarded: %+v", owners)
	}
	if preview.Mode != "owner_draft" || len(preview.Groups) != 1 || *preview.Groups[0].ReasonCode != reason {
		t.Fatalf("preview = %+v", preview)
	}
}

func TestPreviewSubgroupsSelectionRejectsMixedModes(t *testing.T) {
	draft := readableDraft(t, animedomain.ProviderKindAnibt)
	section := ownershipapplication.SubgroupsSection{}
	for name, input := range map[string]SubgroupsSelectionPreviewInput{
		"current_input without draft":  {InputSource: "current_input"},
		"current_input with subgroups": {InputSource: "current_input", Draft: &draft, Subgroups: &section},
		"owner_draft without value":    {InputSource: "owner_draft", ManagedAnimeID: "anime-1"},
		"owner_draft with draft":       {InputSource: "owner_draft", ManagedAnimeID: "anime-1", Subgroups: &section, Draft: &draft},
		"unknown":                      {InputSource: "owner"},
	} {
		_, err := (&Service{owners: &previewOwnerReader{}}).PreviewSubgroupsSelection(context.Background(), input)
		if err == nil || businessCode(t, err) != "INPUT_SOURCE_INVALID" {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}
