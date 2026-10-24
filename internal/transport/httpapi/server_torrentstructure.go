package httpapi

import (
	"errors"
	"net/http"

	"bansoko/internal/release/torrentstructure"
	"bansoko/internal/release/torrentstructure/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// GetReleaseTorrentStructure returns the structure of the release's retained
// torrent payload. A release whose payload is not retained is a state, not a
// fault: 404 with a stable code, which the record renders as "the payload is
// no longer in its retention window". Read-only; it never fetches anything.
func (s *Server) GetReleaseTorrentStructure(w http.ResponseWriter, r *http.Request, releaseID generated.ReleaseID) {
	result, err := s.deps.TorrentStructure.Get(r.Context(), string(releaseID))
	if err != nil {
		if errors.Is(err, torrentstructure.ErrPayloadUnavailable) {
			writeError(w, r, notFound("TORRENT_STRUCTURE_UNAVAILABLE", "This release no longer has a retained torrent payload."))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, transport.View(result))
}
