package domain

import (
	"errors"
	"time"
)

var (
	ErrSetupComplete          = errors.New("instance setup is already complete")
	ErrOwnerExists            = errors.New("instance owner already exists")
	ErrInvalidCredentials     = errors.New("invalid credentials")
	ErrSetupNotFinalized      = errors.New("instance setup is not finalized")
	ErrOwnerInputInvalid      = errors.New("owner input is invalid")
	ErrCurrentPasswordInvalid = errors.New("current password is invalid")
	ErrApiTokenNotFound       = errors.New("api token not found")
	ErrApiTokenInvalid        = errors.New("api token input is invalid")
)

type BootstrapState string

const (
	StateUninitialized BootstrapState = "uninitialized"
	StateReady         BootstrapState = "ready"
)

type BootstrapStatus struct {
	State           BootstrapState `json:"state"`
	ProtocolVersion string         `json:"protocol_version"`
	AllowedActions  []string       `json:"allowed_actions"`
	Version         int64          `json:"version"`
}

type Account struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Scopes is what this bearer may do (SEC-033). A web session holds every
	// scope; an API token holds the set chosen at its creation. An account
	// value without scopes may do nothing scope-gated (fail closed).
	Scopes []string `json:"scopes,omitempty"`
	// WebSession is true only for a signed-in web session (the owner at the
	// keyboard), never for an API token. Operations that must stay out of
	// reach of agents and scripts (database restore) require it.
	WebSession bool `json:"-"`
}

// HasScope reports whether the bearer holds scope.
func (a Account) HasScope(scope string) bool {
	for _, held := range a.Scopes {
		if held == scope {
			return true
		}
	}
	return false
}

// HasScopes reports whether the bearer holds every scope in scopes.
func (a Account) HasScopes(scopes []string) bool {
	for _, scope := range scopes {
		if !a.HasScope(scope) {
			return false
		}
	}
	return true
}

// SetupOwnerStatus exposes only the ownership fact needed by the constrained
// setup read model. It deliberately contains no account identity or credential
// data.
type SetupOwnerStatus struct {
	OwnerCount int
}

// ApiToken is the metadata projection of an active long-lived API token. The
// raw token value is stored encrypted at rest and returned on demand; only its
// HMAC hash authenticates. Revocation deletes the row, so only active tokens
// are ever listed.
type ApiToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}
