package domain

import (
	"fmt"
	"strings"
)

// Bearer scopes (SEC-033). Every OpenAPI operation declares the scope it needs
// (`x-scope`); fields marked `x-secret` are returned as stored only to bearers
// holding ScopeSecrets.
const (
	ScopeRead    = "read"
	ScopeWrite   = "write"
	ScopeSecrets = "secrets"
)

// AllScopes is the canonical order of every scope; a web session holds all.
var AllScopes = []string{ScopeRead, ScopeWrite, ScopeSecrets}

// DefaultApiTokenScopes is the set a new API token gets when none is chosen:
// read and write, no secrets.
var DefaultApiTokenScopes = []string{ScopeRead, ScopeWrite}

// NormalizeApiTokenScopes validates a requested API token scope set and returns
// it in canonical order. An empty request selects DefaultApiTokenScopes. Every
// token holds read; an unknown or duplicated scope is invalid.
func NormalizeApiTokenScopes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), DefaultApiTokenScopes...), nil
	}
	seen := map[string]bool{}
	for _, raw := range requested {
		scope := strings.TrimSpace(raw)
		known := false
		for _, candidate := range AllScopes {
			if scope == candidate {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("%w: unknown scope %q", ErrApiTokenInvalid, raw)
		}
		if seen[scope] {
			return nil, fmt.Errorf("%w: scope %q is listed twice", ErrApiTokenInvalid, scope)
		}
		seen[scope] = true
	}
	if !seen[ScopeRead] {
		return nil, fmt.Errorf("%w: every token includes the read scope", ErrApiTokenInvalid)
	}
	canonical := make([]string, 0, len(seen))
	for _, scope := range AllScopes {
		if seen[scope] {
			canonical = append(canonical, scope)
		}
	}
	return canonical, nil
}

// EncodeScopes is the stored form (comma-joined canonical order).
func EncodeScopes(scopes []string) string { return strings.Join(scopes, ",") }

// DecodeScopes parses the stored form. A value that is not one of the
// canonical sets yields no scopes, so a corrupted row can authenticate but do
// nothing scope-gated (fail closed).
func DecodeScopes(stored string) []string {
	if stored == "" {
		return nil
	}
	scopes, err := NormalizeApiTokenScopes(strings.Split(stored, ","))
	if err != nil || EncodeScopes(scopes) != stored {
		return nil
	}
	return scopes
}
