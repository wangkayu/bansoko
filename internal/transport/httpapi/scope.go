package httpapi

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/transport/apiscope"
)

// Bearer scopes (SEC-033). Every OpenAPI operation declares the scope it needs
// in x-scope; this middleware step enforces it for every authenticated /api/v1
// request. MCP tool calls replay through the same chain with the caller's own
// bearer, so a tool can never do more than the token's scopes allow.
//
// A request whose path is not an OpenAPI operation (the MCP transport itself,
// or an unknown path the router answers with 404/405) carries no product
// effect here and passes; the replayed tool calls are checked one by one.

// scopeTable returns the parsed contract scope model.
func scopeTable() (*apiscope.Table, error) { return apiscope.Load() }

// authorizeScope returns the handler to run for an authenticated request, or
// the 403 to answer. For a bearer without the secrets scope, an operation that
// can return an x-secret field is wrapped so every such value becomes the fixed
// placeholder.
func (s *Server) authorizeScope(r *http.Request, account accessdomain.Account, next http.Handler) (http.Handler, error) {
	table, err := scopeTable()
	if err != nil {
		// The contract is embedded and validated by the contract gate; an
		// unreadable table fails closed.
		return nil, err
	}
	route, ok := table.Match(r.Method, r.URL.EscapedPath())
	if !ok {
		return next, nil
	}
	switch route.Scope {
	case apiscope.ScopeRead, apiscope.ScopeWrite, apiscope.ScopeSecrets:
		if !account.HasScope(route.Scope) {
			return nil, scopeInsufficient(route.Scope)
		}
	}
	if route.RevealsSecrets() && !account.HasScope(accessdomain.ScopeSecrets) {
		return secretMasking(table, route, next), nil
	}
	return next, nil
}

// requireBearerScopes holds the caller to "never mint or reveal more than you
// hold": every scope in scopes must be held by the authenticated bearer.
func requireBearerScopes(r *http.Request, scopes []string) error {
	account, ok := AccountFrom(r.Context())
	if !ok {
		return scopeInsufficient(accessdomain.ScopeRead)
	}
	for _, scope := range scopes {
		if !account.HasScope(scope) {
			return scopeInsufficient(scope)
		}
	}
	return nil
}

// secretMasking captures the response of an operation that can reveal x-secret
// fields and rewrites every such value to the placeholder before it leaves the
// process. A 2xx JSON body that cannot be decoded is never passed through.
func secretMasking(table *apiscope.Table, route *apiscope.Route, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture := &captureWriter{header: http.Header{}}
		next.ServeHTTP(capture, r)
		status := capture.status
		if status == 0 {
			status = http.StatusOK
		}
		body := capture.body.Bytes()
		if status >= 200 && status < 300 && strings.HasPrefix(capture.header.Get("Content-Type"), "application/json") {
			masked, changed, err := table.MaskSecrets(route, status, body)
			if err != nil {
				writeError(w, r, err)
				return
			}
			if changed {
				body = masked
				capture.header.Del("Content-Length")
				capture.header.Set("Content-Length", strconv.Itoa(len(body)))
			}
		}
		for key, values := range capture.header {
			w.Header()[key] = values
		}
		w.WriteHeader(status)
		if len(body) > 0 && r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

type captureWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}
