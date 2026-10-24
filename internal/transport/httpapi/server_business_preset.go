package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"bansoko/internal/anime/animeconfig"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

// businessPresetView validates both the internal frozen value and the exact
// public typed shape. Never emit arbitrary JSON fields across the API boundary.
// tagFilters is the Global tag_filters default a new draft copies; it is outside
// the snapshot and its digest.
func businessPresetView(value animeconfig.BusinessSnapshot, tagFilters json.RawMessage) (generated.BusinessPresetView, error) {
	frozen, err := value.Freeze()
	if err != nil {
		return generated.BusinessPresetView{}, err
	}
	digest, err := frozen.Digest()
	if err != nil {
		return generated.BusinessPresetView{}, err
	}
	raw, err := json.Marshal(frozen)
	if err != nil {
		return generated.BusinessPresetView{}, err
	}
	var snapshot generated.BusinessSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return generated.BusinessPresetView{}, fmt.Errorf("business preset public shape: %w", err)
	}
	var filters generated.TagFilterRules
	filterDecoder := json.NewDecoder(bytes.NewReader(tagFilters))
	filterDecoder.DisallowUnknownFields()
	if err := filterDecoder.Decode(&filters); err != nil {
		return generated.BusinessPresetView{}, fmt.Errorf("business preset tag_filters public shape: %w", err)
	}
	return generated.BusinessPresetView{Snapshot: snapshot, Digest: digest, TagFilters: filters}, nil
}

func (s *Server) GetBusinessPreset(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.deps.GlobalSections.ReadBusinessPreset(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	tagFilters, err := s.deps.GlobalSections.ReadGlobalTagFilters(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := businessPresetView(snapshot, tagFilters)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

// LoadBusinessPresetIntoDraft returns the submitted draft with the current
// Global business preset loaded and the draft's edits frozen into it
// (animedomain.LoadBusinessIntoDraft). It reads the preset and writes nothing.
func (s *Server) LoadBusinessPresetIntoDraft(w http.ResponseWriter, r *http.Request) {
	if s.deps.GlobalSections == nil {
		writeError(w, r, errors.New("global section service is not configured"))
		return
	}
	var input generated.LoadBusinessPresetInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := mapAnimeDraft(input.Draft)
	if err != nil {
		writeError(w, r, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "DRAFT_INVALID", Message: fmt.Sprintf("draft transport mapping failed: %v", err)})
		return
	}
	preset, err := s.deps.GlobalSections.ReadBusinessPreset(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	tagFilters, err := s.deps.GlobalSections.ReadGlobalTagFilters(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	loaded, err := animedomain.LoadBusinessIntoDraft(draft, preset, tagFilters)
	if err != nil {
		writeError(w, r, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "DRAFT_INVALID", Message: err.Error()})
		return
	}
	raw, err := json.Marshal(loaded)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var view generated.AnimeDraft
	if err := json.Unmarshal(raw, &view); err != nil {
		writeError(w, r, fmt.Errorf("loaded draft public shape: %w", err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
