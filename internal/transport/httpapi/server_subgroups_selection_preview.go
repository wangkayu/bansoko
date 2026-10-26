package httpapi

import (
	"errors"
	"net/http"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

// PreviewAnimeSubgroupsSelection answers which groups an unsaved subgroups
// value selects. It is a side-effect-free read: nothing is written and no
// provider is contacted.
func (s *Server) PreviewAnimeSubgroupsSelection(w http.ResponseWriter, r *http.Request) {
	defer deferHandlerTiming(r, "preview_anime_subgroups_selection")()
	if s.deps.Readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	var input generated.SubgroupsSelectionPreviewInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	applicationInput, err := mapSubgroupsSelectionPreviewInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	preview, err := s.deps.Readmodel.PreviewSubgroupsSelection(r.Context(), applicationInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSubgroupsSelectionPreview(preview))
}

func mapSubgroupsSelectionPreviewInput(value generated.SubgroupsSelectionPreviewInput) (readmodelapplication.SubgroupsSelectionPreviewInput, error) {
	result := readmodelapplication.SubgroupsSelectionPreviewInput{InputSource: string(value.InputSource)}
	if value.Draft != nil {
		draft, err := mapAnimeDraft(*value.Draft)
		if err != nil {
			return result, err
		}
		result.Draft = &draft
	}
	if value.Observation != nil {
		result.Observation = &readmodelapplication.RSSListObservationRef{
			Provider:         string(value.Observation.Provider),
			ProviderEntryRef: value.Observation.ProviderEntryRef,
			SnapshotVersion:  int64(value.Observation.SnapshotVersion),
		}
	}
	if value.ManagedAnimeId != nil {
		result.ManagedAnimeID = string(*value.ManagedAnimeId)
	}
	if value.Subgroups != nil {
		section := mapSubgroupsSection(*value.Subgroups)
		result.Subgroups = &section
	}
	return result, nil
}

func mapSubgroupsSelectionPreview(value readmodelapplication.SubgroupsSelectionPreview) generated.SubgroupsSelectionPreview {
	groups := make([]generated.SubgroupSelectionFact, len(value.Groups))
	for i, group := range value.Groups {
		groups[i] = generated.SubgroupSelectionFact{
			Provider:          generated.ProviderKind(group.Provider),
			GroupRef:          group.GroupRef,
			Available:         group.Available,
			EffectiveSelected: group.EffectiveSelected,
			ReasonCode:        cloneStringPtr(group.ReasonCode),
			ScoreBreakdown:    ownershipapplication.SubgroupScoreBreakdownToGenerated(group.ScoreBreakdown),
		}
	}
	return generated.SubgroupsSelectionPreview{Mode: generated.SubgroupsSelectionPreviewMode(value.Mode), Groups: groups}
}
