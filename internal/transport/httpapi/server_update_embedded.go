package httpapi

// Embedded self-update channel handlers (BANSOKO_UPDATE_MODE=embedded). They stay
// deliberately thin over internal/update/application. When the deployment
// runs a different update mode the service is nil: both endpoints answer 503
// SERVICE_NOT_CONFIGURED so the WebUI can hide the surface without treating
// it as a server fault.
//
// Note on the maintenance write gate: POST /update/admit is not blocked by
// the read-only write patterns because it creates the durable admission fence.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
	updatetransport "bansoko/internal/update/transport"
)

func (s *Server) AdmitUpdate(w http.ResponseWriter, r *http.Request, params generated.AdmitUpdateParams) {
	if s.deps.EmbeddedUpdate == nil {
		writeError(w, r, serviceNotConfigured("embedded update service"))
		return
	}
	var input generated.UpdateAdmitInput
	body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if readErr != nil {
		writeError(w, r, badRequest("REQUEST_INVALID", "The request body could not be read."))
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&input); decodeErr != nil {
		writeError(w, r, badRequest("REQUEST_INVALID", "The admit body is invalid."))
		return
	}
	status, created, admitErr := s.deps.EmbeddedUpdate.AdmitUpdate(r.Context(), updatetransport.AdmitInput(input), string(params.IdempotencyKey))
	if admitErr != nil {
		writeError(w, r, admitErr)
		return
	}
	httpStatus := http.StatusOK
	if created {
		httpStatus = http.StatusCreated
	}
	writeJSON(w, r.Context(), httpStatus, updatetransport.Status(status))
}

func (s *Server) CheckUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.EmbeddedUpdate == nil {
		writeError(w, r, serviceNotConfigured("embedded update service"))
		return
	}
	result, err := s.deps.EmbeddedUpdate.CheckUpdate(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, updatetransport.Check(result))
}

func (s *Server) GetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.EmbeddedUpdate == nil {
		writeError(w, r, serviceNotConfigured("embedded update service"))
		return
	}
	status, found, err := s.deps.EmbeddedUpdate.GetUpdateStatus(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !found {
		writeError(w, r, &apperrors.NotFound{Resource: "update", ID: "current"})
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, updatetransport.Status(status))
}
