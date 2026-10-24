package httpapi

import (
	"bansoko/internal/transport/httpapi/generated"
	"encoding/json"
	"net/http"
)

func (s *Server) GetAnimeBusinessEdit(w http.ResponseWriter, r *http.Request, id generated.ManagedAnimeID) {
	service := s.draftService(w, r)
	if service == nil {
		return
	}
	result, err := service.GetAnimeBusinessEdit(r.Context(), string(id))
	if err != nil {
		writeError(w, r, err)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var view generated.BusinessEditView
	if err := json.Unmarshal(raw, &view); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
