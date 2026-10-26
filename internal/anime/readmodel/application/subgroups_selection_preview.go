package application

import (
	"context"
	"strings"

	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/apperrors"
	selectiondomain "bansoko/internal/selection/domain"
)

// SubgroupsSelectionPreviewInput selects the unsaved subgroups value whose
// group selection is previewed.
type SubgroupsSelectionPreviewInput struct {
	InputSource string
	// Draft and Observation, for current_input: the complete unsaved draft and
	// the persisted group observation listed next to its configured groups.
	Draft       *animedomain.AnimeDraft
	Observation *RSSListObservationRef
	// ManagedAnimeID and Subgroups, for owner_draft: the owner and its complete
	// unsaved subgroups value.
	ManagedAnimeID string
	Subgroups      *ownershipapplication.SubgroupsSection
}

// SubgroupSelectionFact is one group's selection under the previewed value.
type SubgroupSelectionFact = ownershipapplication.SubgroupSelectionPreviewFact

// SubgroupsSelectionPreview is the server's selection facts for one unsaved
// subgroups value.
type SubgroupsSelectionPreview struct {
	Mode   string
	Groups []SubgroupSelectionFact
}

// PreviewSubgroupsSelection answers which groups an unsaved subgroups value
// selects, through the decision each mode is already governed by: a draft
// through the group listing the current_input RSS list read uses, an owner
// through the ranking its subgroups save recomputes with. It is a pure read.
func (s *Service) PreviewSubgroupsSelection(ctx context.Context, input SubgroupsSelectionPreviewInput) (SubgroupsSelectionPreview, error) {
	switch input.InputSource {
	case "current_input":
		if input.Draft == nil || input.Subgroups != nil || strings.TrimSpace(input.ManagedAnimeID) != "" {
			return SubgroupsSelectionPreview{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "current_input requires draft and forbids managed_anime_id and subgroups."}
		}
		groups, err := s.previewDraftSubgroupsSelection(ctx, *input.Draft, input.Observation)
		if err != nil {
			return SubgroupsSelectionPreview{}, err
		}
		return SubgroupsSelectionPreview{Mode: input.InputSource, Groups: groups}, nil
	case "owner_draft":
		if strings.TrimSpace(input.ManagedAnimeID) == "" || input.Subgroups == nil || input.Draft != nil || input.Observation != nil {
			return SubgroupsSelectionPreview{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "owner_draft requires managed_anime_id and subgroups and forbids draft and observation."}
		}
		groups, err := s.owners.PreviewSubgroupsSelectionApplication(ctx, input.ManagedAnimeID, *input.Subgroups)
		if err != nil {
			return SubgroupsSelectionPreview{}, err
		}
		return SubgroupsSelectionPreview{Mode: input.InputSource, Groups: groups}, nil
	default:
		return SubgroupsSelectionPreview{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source must be current_input or owner_draft."}
	}
}

// previewDraftSubgroupsSelection lists the draft's provider groups exactly as
// the current_input RSS list read does: the same resolution, the same
// observation union and the same group listing with its decision reasons, only
// without the feed. A custom draft has no provider selection.
func (s *Service) previewDraftSubgroupsSelection(ctx context.Context, draft animedomain.AnimeDraft, observation *RSSListObservationRef) ([]SubgroupSelectionFact, error) {
	if violations := animedomain.ValidateDraft(draft); len(violations) > 0 {
		return nil, draftInvalidError(violations)
	}
	if draft.Origin != animedomain.SubscriptionOriginAnibt && draft.Origin != animedomain.SubscriptionOriginMikan {
		if observation != nil {
			return nil, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "observation applies to a provider draft only."}
		}
		return []SubgroupSelectionFact{}, nil
	}
	effective, err := s.resolveDraftEffective(ctx, draft)
	if err != nil {
		return nil, err
	}
	effective, _, _, decisions, err := s.applyDraftObservation(ctx, draft, effective, observation)
	if err != nil {
		return nil, err
	}
	entries, err := rssListGroupEntries(draft, effective, 0)
	if err != nil {
		return nil, sectionResolutionError(err)
	}
	facts := make([]SubgroupSelectionFact, 0, len(entries))
	for _, entry := range entries {
		fact := SubgroupSelectionFact{
			Provider:          string(entry.provider),
			GroupRef:          entry.groupRef,
			Available:         true,
			EffectiveSelected: entry.selected,
			ReasonCode:        cloneStringPointer(entry.reasonCode),
		}
		// The live decision that set the entry's selection explains it: its
		// reason names the site list where one excluded the group, and its
		// breakdown is the score the site policy gave.
		if decision, ok := decisions[entry.groupRef]; ok && (decision.Outcome == selectiondomain.GroupSelected) == entry.selected {
			reason := decision.Reason
			fact.ReasonCode = &reason
			fact.ScoreBreakdown = ownershipapplication.SubgroupScoreBreakdownFromDomain(decision.Group.Breakdown)
		}
		facts = append(facts, fact)
	}
	return facts, nil
}
