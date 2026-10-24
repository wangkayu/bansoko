package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// Login authenticates the owner and opens a bearer session.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var input generated.LoginInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" {
		writeError(w, r, badRequest("CREDENTIALS_REQUIRED", "Username and password are required."))
		return
	}
	limiter := s.loginLimiter
	if limiter == nil {
		// newServer always installs one; a hand-built Server in a unit test
		// still gets a working (per-request) limiter rather than a panic.
		limiter = newLoginLimiter(nil)
	}
	ip := clientIP(r)
	if wait, blocked := limiter.allow(ip, input.Username); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, r, &APIError{
			Status:  http.StatusTooManyRequests,
			Code:    "LOGIN_RATE_LIMITED",
			Message: "Too many failed sign-in attempts. Try again later.",
		})
		return
	}
	token, err := s.deps.Access.Login(r.Context(), input.Username, input.Password, s.deps.SessionTTL)
	if err != nil {
		switch {
		case errors.Is(err, accessdomain.ErrInvalidCredentials):
			limiter.recordFailure(ip, input.Username)
			writeError(w, r, unauthorized("CREDENTIALS_INVALID", "The username or password is incorrect."))
		case errors.Is(err, accessdomain.ErrSetupNotFinalized):
			writeError(w, r, conflict("BOOTSTRAP_REQUIRED", "Instance bootstrap is not complete.", nil))
		default:
			writeError(w, r, err)
		}
		return
	}
	limiter.recordSuccess(ip, input.Username)
	expiresAt, err := s.deps.Access.SessionExpiry(r.Context(), token)
	if err != nil {
		writeError(w, r, err)
		return
	}
	account, err := s.deps.Access.AuthenticateSession(r.Context(), token)
	if err != nil {
		writeError(w, r, err)
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
	// Cookie transport is the default: a browser that omits the field must not
	// receive the credential in a body script can read.
	if input.SessionTransport == nil || *input.SessionTransport != generated.LoginInputSessionTransportToken {
		if err := setSessionCookies(w, r, token, s.deps.SessionTTL); err != nil {
			writeError(w, r, err)
			return
		}
	} else {
		result.SessionToken = &token
	}
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// GetAuthSession reports the session the request is authenticated with. The
// browser uses it as the "am I signed in" probe instead of keeping a token in
// storage: the cookie is sent automatically, and a 401 means the gate shows the
// sign-in form.
func (s *Server) GetAuthSession(w http.ResponseWriter, r *http.Request) {
	account, ok := AccountFrom(r.Context())
	if !ok {
		writeError(w, r, unauthorized("SESSION_REQUIRED", "A session is required."))
		return
	}
	expiresAt, err := s.deps.Access.SessionExpiry(r.Context(), requestSessionToken(r))
	if err != nil {
		// The session passed authentication but its row is gone (revoked between
		// the two reads). That is a 401, not a server fault: the gate must show
		// the sign-in form rather than an unreachable error.
		if errors.Is(err, accessdomain.ErrInvalidCredentials) {
			writeError(w, r, unauthorized("SESSION_INVALID", "The session is missing, expired, or revoked."))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.AuthSession{
		Owner: generated.OwnerAccount{
			Id:       account.ID,
			Username: account.Username,
			Role:     generated.OwnerAccountRole(account.Role),
		},
		ExpiresAt: expiresAt,
	})
}

// UpdateAccount changes the signed-in owner's username and/or password. It is
// reserved for a web session: an API token cannot take over the credential
// even with the write scope. The current password is always re-checked.
func (s *Server) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	account, ok := AccountFrom(r.Context())
	if !ok || !account.WebSession {
		writeError(w, r, webSessionRequired())
		return
	}
	var input generated.AccountUpdateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if input.CurrentPassword == "" {
		writeError(w, r, unprocessable("ACCOUNT_INPUT_INVALID", "The current password is required.", nil))
		return
	}
	updated, revokedOthers, err := s.deps.Access.UpdateOwnerCredentials(
		r.Context(), account.ID, requestSessionToken(r), input.CurrentPassword, input.Username, input.NewPassword)
	if err != nil {
		switch {
		case errors.Is(err, accessdomain.ErrCurrentPasswordInvalid):
			writeError(w, r, unprocessable("CURRENT_PASSWORD_INVALID", "The current password is incorrect.", nil))
		case errors.Is(err, accessdomain.ErrOwnerInputInvalid):
			writeError(w, r, unprocessable("ACCOUNT_INPUT_INVALID", "Provide a username or a new password; the username must be 1-128 characters and the password 12-1024 characters.", nil))
		case errors.Is(err, accessdomain.ErrInvalidCredentials):
			writeError(w, r, unauthorized("SESSION_INVALID", "The session is missing, expired, or revoked."))
		default:
			writeError(w, r, err)
		}
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.AccountUpdateResult{
		Owner: generated.OwnerAccount{
			Id:       updated.ID,
			Username: updated.Username,
			Role:     generated.OwnerAccountRole(updated.Role),
		},
		OtherSessionsRevoked: revokedOthers,
	})
}

// Logout revokes the session this request authenticated with and clears the
// browser cookies, so a signed-out browser stops replaying a dead credential.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	token := requestSessionToken(r)
	if token == "" {
		writeError(w, r, unauthorized("SESSION_REQUIRED", "A session is required."))
		return
	}
	if !s.deps.SkipAuth || s.deps.Environment == "production" {
		if err := s.deps.Access.RevokeSession(r.Context(), token); err != nil {
			writeError(w, r, err)
			return
		}
	}
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// CreateApiToken issues a long-lived API token for the owner. The raw token is
// echoed in the response and encrypted at rest so it can be re-read on demand
// (ReadApiTokenRaw); its HMAC hash is the persistent auth credential.
func (s *Server) CreateApiToken(w http.ResponseWriter, r *http.Request) {
	var input generated.ApiTokenCreateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	var requested []string
	explicitEmpty := false
	if input.Scopes != nil {
		explicitEmpty = len(*input.Scopes) == 0
		for _, scope := range *input.Scopes {
			requested = append(requested, string(scope))
		}
	}
	scopes, err := accessdomain.NormalizeApiTokenScopes(requested)
	if err != nil || explicitEmpty {
		writeError(w, r, unprocessable("API_TOKEN_INVALID", "The API token input failed typed validation: scopes must be distinct values of read, write and secrets, and include read.", nil))
		return
	}
	// A bearer can never mint a token with a scope it does not hold itself.
	if err := requireBearerScopes(r, scopes); err != nil {
		writeError(w, r, err)
		return
	}
	raw, token, err := s.deps.Access.CreateApiToken(r.Context(), input.Name, scopes)
	if err != nil {
		switch {
		case errors.Is(err, accessdomain.ErrApiTokenInvalid):
			writeError(w, r, unprocessable("API_TOKEN_INVALID", "The API token input failed typed validation.", nil))
		case errors.Is(err, accessdomain.ErrSetupNotFinalized):
			writeError(w, r, conflict("BOOTSTRAP_REQUIRED", "Instance bootstrap is not complete.", nil))
		default:
			writeError(w, r, err)
		}
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, generated.ApiTokenCreateResult{
		Token:     raw,
		TokenId:   token.ID,
		Scopes:    mapApiTokenScopes(token.Scopes),
		CreatedAt: token.CreatedAt,
	})
}

// ListApiTokens returns the metadata projection of every long-lived API token.
// Raw token values are never included; the raw value is available on demand
// via ReadApiTokenRaw so the UI can display and copy a key for its lifetime.
func (s *Server) ListApiTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.deps.Access.ListApiTokens(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := make([]generated.ApiToken, 0, len(tokens))
	for _, token := range tokens {
		body = append(body, mapApiToken(token))
	}
	writeJSON(w, r.Context(), http.StatusOK, body)
}

// ReadApiTokenRaw returns the raw value of an active API token so the UI can
// keep showing the key and allow copying. The value is encrypted at rest and
// only decryptable when the deployment key is configured; otherwise 404.
func (s *Server) ReadApiTokenRaw(w http.ResponseWriter, r *http.Request, apiTokenId generated.ApiTokenID) {
	// The secrets scope is enforced by x-scope; a bearer also may not read the
	// raw value of a token that holds a scope it does not hold itself.
	if err := s.requireTargetTokenScopes(r, string(apiTokenId)); err != nil {
		writeError(w, r, err)
		return
	}
	raw, ok := s.deps.Access.ReadApiTokenRaw(r.Context(), string(apiTokenId))
	if !ok {
		writeError(w, r, notFound("API_TOKEN_RAW_UNAVAILABLE", "The API token raw value is unavailable (deployment key not configured) or the token does not exist."))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.ApiTokenRaw{Token: raw})
}

// RevokeApiToken invalidates a long-lived API token. Revoking an unknown or
// already revoked token is a no-op.
func (s *Server) RevokeApiToken(w http.ResponseWriter, r *http.Request, apiTokenId generated.ApiTokenID) {
	if err := s.deps.Access.RevokeApiToken(r.Context(), string(apiTokenId)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateApiToken atomically revokes the current token and issues a fresh one
// for the same owner. The new raw token is echoed in the response and stored
// encrypted at rest for long-term display; the old token is invalid from the
// moment the rotate commits.
func (s *Server) RotateApiToken(w http.ResponseWriter, r *http.Request, apiTokenId generated.ApiTokenID) {
	// The rotated token keeps the old scope set, so the caller must hold it.
	if err := s.requireTargetTokenScopes(r, string(apiTokenId)); err != nil {
		writeError(w, r, err)
		return
	}
	raw, token, err := s.deps.Access.RotateApiToken(r.Context(), string(apiTokenId))
	if err != nil {
		switch {
		case errors.Is(err, accessdomain.ErrApiTokenNotFound):
			writeError(w, r, notFound("API_TOKEN_NOT_FOUND", "The API token was not found or is already revoked."))
		default:
			writeError(w, r, err)
		}
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.ApiTokenCreateResult{
		Token:     raw,
		TokenId:   token.ID,
		Scopes:    mapApiTokenScopes(token.Scopes),
		CreatedAt: token.CreatedAt,
	})
}

// requireTargetTokenScopes checks that the bearer holds every scope of the
// target token. An unknown token passes here so the operation answers its own
// not-found outcome.
func (s *Server) requireTargetTokenScopes(r *http.Request, tokenID string) error {
	scopes, err := s.deps.Access.ApiTokenScopes(r.Context(), tokenID)
	if errors.Is(err, accessdomain.ErrApiTokenNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return requireBearerScopes(r, scopes)
}

func mapApiTokenScopes(scopes []string) generated.ApiTokenScopes {
	mapped := make(generated.ApiTokenScopes, 0, len(scopes))
	for _, scope := range scopes {
		mapped = append(mapped, generated.ApiTokenScope(scope))
	}
	return mapped
}

func mapApiToken(token accessdomain.ApiToken) generated.ApiToken {
	return generated.ApiToken{
		Id:         token.ID,
		Name:       token.Name,
		Scopes:     mapApiTokenScopes(token.Scopes),
		CreatedAt:  token.CreatedAt,
		LastUsedAt: token.LastUsedAt,
	}
}
