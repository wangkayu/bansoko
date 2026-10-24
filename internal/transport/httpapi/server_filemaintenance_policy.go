package httpapi

import (
	"net/http"

	filemainttransport "bansoko/internal/filemaintenance/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// GetInstanceParserPolicy returns the frozen per-instance parser policy.
func (s *Server) GetInstanceParserPolicy(w http.ResponseWriter, r *http.Request, managedAnimeId generated.ManagedAnimeID) {
	view, err := s.deps.FileMaintenance.GetInstanceParserPolicy(r.Context(), managedAnimeId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.ParserPolicyView(view))
}

// SaveInstanceParserPolicy fully replaces one instance parser policy with
// expected-version CAS.
func (s *Server) SaveInstanceParserPolicy(w http.ResponseWriter, r *http.Request, managedAnimeId generated.ManagedAnimeID) {
	var input generated.ParserPolicyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SaveInstanceParserPolicy(r.Context(), string(managedAnimeId), filemainttransport.ParserPolicyInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.ParserPolicyView(view))
}

// GetParserPolicyDefault returns the singleton creation template for future
// instances (absent -> the versioned code default regex with version 0).
func (s *Server) GetParserPolicyDefault(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.FileMaintenance.GetDefaultParserPolicy(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.ParserPolicyDefaultView(view))
}

// SaveParserPolicyDefault replaces the creation template with expected-version
// CAS (future instances only; existing instances keep their frozen kind).
func (s *Server) SaveParserPolicyDefault(w http.ResponseWriter, r *http.Request) {
	var input generated.ParserPolicyDefaultInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SaveDefaultParserPolicy(r.Context(), filemainttransport.ParserPolicyDefaultInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.ParserPolicyDefaultView(view))
}

// GetFileEffectPolicyDefault returns the singleton creation default for
// future instances (absent -> code default skip with version 0).
func (s *Server) GetFileEffectPolicyDefault(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.FileMaintenance.GetFileEffectPolicyDefault(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.FileEffectPolicyDefaultView(view))
}

// SaveFileEffectPolicyDefault replaces the singleton creation default with
// expected-version CAS (future instances only).
func (s *Server) SaveFileEffectPolicyDefault(w http.ResponseWriter, r *http.Request) {
	var input generated.FileEffectPolicyDefaultInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SaveFileEffectPolicyDefault(r.Context(), filemainttransport.FileEffectPolicyDefaultInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.FileEffectPolicyDefaultView(view))
}

// GetInstanceFileEffectPolicy returns the frozen per-instance file-effect
// policy (snapshot; the client never merges a global default).
func (s *Server) GetInstanceFileEffectPolicy(w http.ResponseWriter, r *http.Request, managedAnimeId generated.ManagedAnimeID) {
	view, err := s.deps.FileMaintenance.GetInstanceFileEffectPolicy(r.Context(), string(managedAnimeId))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.InstanceFileEffectPolicyView(view))
}

// SaveInstanceFileEffectPolicy replaces the frozen instance file-effect
// policy with expected-version CAS.
func (s *Server) SaveInstanceFileEffectPolicy(w http.ResponseWriter, r *http.Request, managedAnimeId generated.ManagedAnimeID) {
	var input generated.InstanceFileEffectPolicyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SaveInstanceFileEffectPolicy(r.Context(), string(managedAnimeId), filemainttransport.InstanceFileEffectPolicyInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.InstanceFileEffectPolicyView(view))
}
