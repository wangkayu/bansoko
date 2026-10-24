package httpapi

import (
	"errors"
	"net/http"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// GetBootstrapStatus exposes the minimal anonymous bootstrap fact.
func (s *Server) GetBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.deps.Access.Status(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	state := generated.BootstrapStatusState("bootstrap_required")
	if status.State == accessdomain.StateReady {
		state = generated.BootstrapStatusState("owner_exists")
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.BootstrapStatus{State: state})
}

// CreateBootstrapOwner directly creates the first owner account on the first
// visit and returns a normal owner session. The endpoint is anonymous: the
// deployer is responsible for network-level trust until the owner exists.
func (s *Server) CreateBootstrapOwner(w http.ResponseWriter, r *http.Request) {
	var input generated.CreateOwnerInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	account, sessionToken, expiresAt, err := s.deps.Access.CompleteBootstrapOwner(r.Context(), input.Username, input.Password, s.deps.SessionTTL)
	if err != nil {
		switch {
		case errors.Is(err, accessdomain.ErrSetupComplete), errors.Is(err, accessdomain.ErrOwnerExists):
			writeError(w, r, conflict("OWNER_ALREADY_EXISTS", "The instance owner already exists.", nil))
		case errors.Is(err, accessdomain.ErrOwnerInputInvalid):
			writeError(w, r, unprocessable("OWNER_INPUT_INVALID", "The owner input is invalid.", []generated.ErrorDetail{{Code: "OWNER_INPUT_INVALID", Message: "Username must be 1-128 characters; password must be 12-1024 characters."}}))
		default:
			writeError(w, r, err)
		}
		return
	}
	result := generated.LoginResult{
		ExpiresAt: expiresAt,
		Owner: generated.OwnerAccount{
			Id:       account.ID,
			Username: account.Username,
			Role:     generated.OwnerAccountRole("owner"),
		},
	}
	// The first owner signs in through the same browser the cookie serves, so
	// bootstrap issues one exactly like login does. Script never receives the
	// credential: the gate fires a follow-up /auth/session probe. A non-browser
	// caller (a provisioning script) asks for the token explicitly.
	if input.SessionTransport != nil && *input.SessionTransport == generated.CreateOwnerInputSessionTransportToken {
		result.SessionToken = &sessionToken
	} else if err := setSessionCookies(w, r, sessionToken, s.deps.SessionTTL); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, result)
}
