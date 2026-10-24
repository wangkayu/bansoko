package httpapi

import (
	"net/http"

	configurationtransport "bansoko/internal/configuration/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// ListQBittorrentTorrents proxies the qBittorrent WebUI torrent list. It is
// an operational passthrough: it does not read or mutate DownloadTask owner
// truth.
func (s *Server) ListQBittorrentTorrents(w http.ResponseWriter, r *http.Request, connectionId generated.ConnectionID) {
	torrents, err := s.deps.QBManagement.ListTorrents(r.Context(), connectionId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.QBittorrentTorrents(torrents))
}

// PauseQBittorrentTorrent pauses a single qBittorrent torrent.
func (s *Server) PauseQBittorrentTorrent(w http.ResponseWriter, r *http.Request, connectionId generated.ConnectionID, hash generated.QBHash) {
	if err := s.deps.QBManagement.PauseTorrent(r.Context(), connectionId, hash); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResumeQBittorrentTorrent resumes a single qBittorrent torrent.
func (s *Server) ResumeQBittorrentTorrent(w http.ResponseWriter, r *http.Request, connectionId generated.ConnectionID, hash generated.QBHash) {
	if err := s.deps.QBManagement.ResumeTorrent(r.Context(), connectionId, hash); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteQBittorrentTorrent deletes a qBittorrent torrent, optionally removing
// the downloaded files from disk.
func (s *Server) DeleteQBittorrentTorrent(w http.ResponseWriter, r *http.Request, connectionId generated.ConnectionID, hash generated.QBHash, params generated.DeleteQBittorrentTorrentParams) {
	deleteFiles := params.DeleteFiles != nil && *params.DeleteFiles
	if err := s.deps.QBManagement.DeleteTorrent(r.Context(), connectionId, hash, deleteFiles); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetQBittorrentTorrentFiles lists the files inside a qBittorrent torrent.
func (s *Server) GetQBittorrentTorrentFiles(w http.ResponseWriter, r *http.Request, connectionId generated.ConnectionID, hash generated.QBHash) {
	files, err := s.deps.QBManagement.GetTorrentFiles(r.Context(), connectionId, hash)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.QBittorrentTorrentFiles(files))
}

// GetQBittorrentServerStatus returns the qBittorrent server status (version,
// connection state, transfer speeds, disk space).
func (s *Server) GetQBittorrentServerStatus(w http.ResponseWriter, r *http.Request, connectionId generated.ConnectionID) {
	status, err := s.deps.QBManagement.GetServerStatus(r.Context(), connectionId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.QBittorrentServerStatus(status))
}
