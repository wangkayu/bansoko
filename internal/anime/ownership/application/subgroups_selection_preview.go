package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	automationapp "bansoko/internal/automation/application"
	selectionapp "bansoko/internal/selection/application"
	selectiondomain "bansoko/internal/selection/domain"
)

// SubgroupSelectionPreviewFact is one group's selection under an unsaved
// subgroups value, exactly as the group-selection decision produced it.
type SubgroupSelectionPreviewFact struct {
	Provider          string
	GroupRef          string
	Available         bool
	EffectiveSelected bool
	CountInLimit      bool
	ReasonCode        *string
	ScoreBreakdown    *SubgroupScoreBreakdown
}

// PreviewSubgroupsSelectionApplication answers what saving section would
// select, without saving it. The section is validated and normalized like the
// subgroups PATCH, turned into the discovery policy the saved override would
// be read back as, and ranked by the same recompute ranking over the owner's
// persisted provider groups and latest group snapshot, with the section's
// per-group modes overlaid on the existing rows as the save syncs them. Pure
// read: no lock, no write, no provider request.
func (s *Service) PreviewSubgroupsSelectionApplication(ctx context.Context, managedAnimeID string, section SubgroupsSection) ([]SubgroupSelectionPreviewFact, error) {
	domainSection := domainSubgroupsSection(section)
	if violations := animedomain.ValidateSubgroupsSection(domainSection); len(violations) > 0 {
		return nil, animeapplication.DraftInvalidError(violations)
	}
	override, violations := animedomain.SubgroupsSectionOverride(domainSection)
	if len(violations) > 0 {
		return nil, animeapplication.DraftInvalidError(violations)
	}
	var subscriptionID string
	var ownerVersion int64
	err := s.db.QueryRowContext(ctx, `
SELECT o.subgroups_version, sub.id
FROM anime_business_configs o
JOIN subscriptions sub ON sub.managed_anime_id = o.managed_anime_id
WHERE o.managed_anime_id = $1`, managedAnimeID).Scan(&ownerVersion, &subscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apperrors.NotFound{Resource: "subgroups_owner", ID: managedAnimeID}
	}
	if err != nil {
		return nil, fmt.Errorf("read subgroups owner for preview: %w", err)
	}
	// The saved value is read back by the recompute together with the live
	// site policy, at the version the save would write.
	policy, err := selectionapp.ReadLiveDiscoveryPolicy(ctx, s.db, override.Value, ownerVersion+1)
	if err != nil {
		return nil, animeapplication.SectionResolutionError(err)
	}
	preview, err := automationapp.PreviewSubscriptionSelections(ctx, s.db, subscriptionID, policy, subgroupsSelectionOverlay(section))
	if err != nil {
		return nil, err
	}
	facts := make([]SubgroupSelectionPreviewFact, 0, len(preview.Decisions))
	for _, decision := range preview.Decisions {
		reason := decision.Reason
		facts = append(facts, SubgroupSelectionPreviewFact{
			Provider:          preview.Provider,
			GroupRef:          decision.Group.Ref.ExternalID,
			Available:         decision.Group.Observed,
			EffectiveSelected: decision.Outcome == selectiondomain.GroupSelected,
			CountInLimit:      decision.Group.CountInLimit,
			ReasonCode:        &reason,
			ScoreBreakdown:    SubgroupScoreBreakdownFromDomain(decision.Group.Breakdown),
		})
	}
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].GroupRef < facts[j].GroupRef })
	return facts, nil
}

// subgroupsSelectionOverlay is the per-group mode the save syncs onto each
// existing provider-group row: an explicit manual intent, or automatic.
func subgroupsSelectionOverlay(section SubgroupsSection) map[string]automationapp.ProviderGroupOverlay {
	overlay := make(map[string]automationapp.ProviderGroupOverlay, len(section.Entries))
	for _, entry := range section.Entries {
		value := automationapp.ProviderGroupOverlay{CountInLimit: entry.CountInLimit}
		if entry.SelectionMode == string(animedomain.SelectionModeManual) && entry.ManualSelected != nil {
			manual := *entry.ManualSelected
			value.Manual = &manual
		}
		overlay[applicationSubgroupEntryKey(entry.Provider, entry.GroupRef)] = value
	}
	return overlay
}
