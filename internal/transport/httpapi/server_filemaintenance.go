package httpapi

import (
	"net/http"

	filemainttransport "bansoko/internal/filemaintenance/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// GetFileMaintenancePolicy returns the global policy.
func (s *Server) GetFileMaintenancePolicy(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.FileMaintenance.GetPolicyApplication(r.Context(), "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.PolicyView(view))
}

// SaveFileMaintenancePolicy saves the global policy with CAS.
func (s *Server) SaveFileMaintenancePolicy(w http.ResponseWriter, r *http.Request) {
	var input generated.FileMaintenancePolicyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	inputView, err := filemainttransport.PolicyInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SavePolicyApplication(r.Context(), "", inputView, "web", "web")
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.PolicyView(view))
}

// GetFileMaintenanceAnimePolicy returns the anime sparse override.
func (s *Server) GetFileMaintenanceAnimePolicy(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	view, err := s.deps.FileMaintenance.GetPolicyApplication(r.Context(), string(animeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.PolicyView(view))
}

// SaveFileMaintenanceAnimePolicy saves the anime override with CAS.
func (s *Server) SaveFileMaintenanceAnimePolicy(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	var input generated.FileMaintenancePolicyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	inputView, err := filemainttransport.PolicyInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SavePolicyApplication(r.Context(), string(animeID), inputView, "web", "web")
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.PolicyView(view))
}

// GetReplacementPolicyDefault returns the singleton creation default.
func (s *Server) GetReplacementPolicyDefault(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.FileMaintenance.GetReplacementPolicyDefaultApplication(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	wireView, err := filemainttransport.ReplacementPolicyDefaultView(view)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, wireView)
}

// SaveReplacementPolicyDefault saves the creation default with CAS (future
// instances only).
func (s *Server) SaveReplacementPolicyDefault(w http.ResponseWriter, r *http.Request) {
	var input generated.ReplacementPolicyDefaultInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	inputView, err := filemainttransport.ReplacementPolicyDefaultInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SaveReplacementPolicyDefaultApplication(r.Context(), inputView, "web", "web")
	if err != nil {
		writeError(w, r, err)
		return
	}
	wireView, err := filemainttransport.ReplacementPolicyDefaultView(view)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, wireView)
}

// GetInstanceReplacementPolicy returns the complete frozen instance policy.
func (s *Server) GetInstanceReplacementPolicy(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	view, err := s.deps.FileMaintenance.GetInstanceReplacementPolicyApplication(r.Context(), string(animeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	wireView, err := filemainttransport.InstanceReplacementPolicyView(view)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, wireView)
}

// SaveInstanceReplacementPolicy fully replaces one instance policy with CAS.
func (s *Server) SaveInstanceReplacementPolicy(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	var input generated.InstanceReplacementPolicyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	inputView, err := filemainttransport.InstanceReplacementPolicyInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.FileMaintenance.SaveInstanceReplacementPolicyApplication(r.Context(), string(animeID), inputView, "web", "web")
	if err != nil {
		writeError(w, r, err)
		return
	}
	wireView, err := filemainttransport.InstanceReplacementPolicyView(view)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, wireView)
}

// ReplanFileMaintenance runs the deterministic snapshot-only dry-run.
func (s *Server) ReplanFileMaintenance(w http.ResponseWriter, r *http.Request) {
	var input generated.FileMaintenanceReplanInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.deps.FileMaintenance.ReplanApplication(r.Context(), filemainttransport.ReplanInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.ReplanResult(result))
}

// GetFileMaintenanceOverview returns the per-instance FileMaintenance truth
// surface (scan owner, consumer outcomes, placements, decisions, deletions,
// alignments, replacement policy and the global new-instance default).
func (s *Server) GetFileMaintenanceOverview(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	result, err := s.deps.FileMaintenance.GetOverviewApplication(r.Context(), string(animeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := mapFileMaintenanceOverviewView(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
