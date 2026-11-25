package httpapi

import (
	"encoding/json"
	"net/http"
	"regexp"

	"bansoko/internal/eventactions"
	"bansoko/internal/transport/httpapi/generated"
)

func (s *Server) SetEventActions(service *eventactions.Service) { s.deps.EventActions = service }

// requireOwner rejects a caller that is not the owner account.
func requireOwner(w http.ResponseWriter, r *http.Request) bool {
	if account, ok := AccountFrom(r.Context()); ok && account.Role == "owner" {
		return true
	}
	writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "OWNER_REQUIRED", Message: "Only the owner may do this."})
	return false
}

func (s *Server) eventActions(w http.ResponseWriter, r *http.Request) *eventactions.Service {
	// Owner only, for every bearer kind; scope authorization (read or admin
	// depending on the operation) runs in the middleware before this handler.
	if !requireOwner(w, r) {
		return nil
	}

	w.Header().Set("Cache-Control", "no-store")
	if s.deps.EventActions == nil {
		writeError(w, r, unavailable("SERVICE_NOT_CONFIGURED", "Event actions are not configured."))
	}
	return s.deps.EventActions
}
func (s *Server) ListEventActions(w http.ResponseWriter, r *http.Request) {
	service := s.eventActions(w, r)
	if service == nil {
		return
	}
	view, err := service.ListBindings(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

func (s *Server) SaveEventAction(w http.ResponseWriter, r *http.Request) {
	service := s.eventActions(w, r)
	if service == nil {
		return
	}
	var input struct {
		Kind           string          `json:"kind"`
		Script         json.RawMessage `json:"script"`
		Enabled        *bool           `json:"enabled"`
		TimeoutSeconds int             `json:"timeout_seconds"`
		RecordResult   bool            `json:"record_result"`
		Version        *int            `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	var script *string
	if input.Enabled == nil || input.Version == nil || json.Unmarshal(input.Script, &script) != nil {
		writeError(w, r, unprocessable("EVENT_ACTION_INVALID", "All event binding fields are required.", nil))
		return
	}
	view, err := service.Save(r.Context(), eventactions.Binding{Kind: input.Kind, Script: script, Enabled: *input.Enabled, TimeoutSeconds: input.TimeoutSeconds, RecordResult: input.RecordResult, Version: *input.Version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

var eventActionFilename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *Server) ReadEventActionScripts(w http.ResponseWriter, r *http.Request, params generated.ReadEventActionScriptsParams) {
	service := s.eventActions(w, r)
	if service == nil {
		return
	}
	if params.File != nil {
		if !eventActionFilename.MatchString(*params.File) {
			writeError(w, r, unprocessable("EVENT_SCRIPT_NAME_INVALID", "The script filename is invalid.", nil))
			return
		}
		writeJSON(w, r.Context(), http.StatusOK, []eventactions.File{service.File(*params.File)})
		return
	}
	view, err := service.Files()
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

func (s *Server) ListEventActionAttempts(w http.ResponseWriter, r *http.Request, params generated.ListEventActionAttemptsParams) {
	service := s.eventActions(w, r)
	if service == nil {
		return
	}
	view, err := service.Attempts(r.Context(), params.Warnings != nil && *params.Warnings)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

func (s *Server) ListAcquisitionScriptReceipts(w http.ResponseWriter, r *http.Request, acquisitionID generated.AcquisitionID) {
	service := s.eventActions(w, r)
	if service == nil {
		return
	}
	view, err := service.RecordReceipts(r.Context(), string(acquisitionID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
