package httpapi

import (
	"errors"
	"net/http"

	"bansoko/internal/transport/httpapi/generated"
)

// ReadAnimeRSSListView composes the one RSS page read model for one
// configuration identity. It is a side-effect-free read: no product write, no
// snapshot write and no provider or torrent HTTP request.
func (s *Server) ReadAnimeRSSListView(w http.ResponseWriter, r *http.Request) {
	defer deferHandlerTiming(r, "read_anime_rss_list_view")()
	if s.deps.Readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	var input generated.RSSListViewInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	applicationInput, err := mapRSSListReadInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.Readmodel.ReadAnimeRSSListView(r.Context(), applicationInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapRSSListView(view)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}
