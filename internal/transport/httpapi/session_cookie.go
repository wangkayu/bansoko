package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

// The browser session travels in an HttpOnly cookie, so script never holds the
// credential and an injected script cannot lift it out of storage and replay it
// later. Two cookies are involved:
//
//   - bansoko_session carries the session token and is HttpOnly: only the HTTP
//     client sees it.
//   - bansoko_csrf is readable by script and holds a random value that the page
//     echoes in X-CSRF-Token on every unsafe request. It is a double submit
//     token, not a secret: its only job is to prove the request was issued from
//     our own origin's script rather than a form post on another site.
//
// SameSite=Strict already stops a cross-site request from carrying the session
// cookie, but it does not distinguish siblings under one registrable domain, so
// the header check stays. The check applies only to requests authenticated by
// the cookie; an Authorization header is not attached by a browser on its own,
// so bearer callers (API tokens, MCP, the CLI) are never asked for it.
const (
	sessionCookieName = "bansoko_session"
	csrfCookieName    = "bansoko_csrf"
	csrfHeaderName    = "X-CSRF-Token"
	csrfTokenBytes    = 32
)

// secureCookie reports whether the request reached us over TLS. A deployment
// puts a reverse proxy in front and the app never terminates TLS itself, so the
// proxy's X-Forwarded-Proto is the only signal available; a direct plain-HTTP
// LAN instance sends neither and gets a non-Secure cookie. Marking a cookie
// Secure on a plain-HTTP instance would make the browser drop it, which looks
// like a sign-in that silently does not stick.
func secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// newCSRFToken returns a fresh double-submit value.
func newCSRFToken() (string, error) {
	raw := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// setSessionCookies hands the browser its session and the matching CSRF value.
// ttl is the session lifetime, so both cookies expire with it.
func setSessionCookies(w http.ResponseWriter, r *http.Request, sessionToken string, ttl time.Duration) error {
	csrf, err := newCSRFToken()
	if err != nil {
		return err
	}
	secure := secureCookie(r)
	maxAge := int(ttl / time.Second)
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	// The CSRF cookie is readable by design: the page has to echo it back in a
	// header. It is not HttpOnly for that reason alone.
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrf,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// clearSessionCookies expires both cookies. A logout that only revoked the
// server row would leave a dead cookie the browser keeps sending.
func clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := secureCookie(r)
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == sessionCookieName,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

// sessionCookieToken returns the session cookie value, or "" when absent.
func sessionCookieToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

// csrfHeaderMatches reports whether the request carries a header equal to its
// own CSRF cookie. Both halves come from the same browser, so a cross-site
// request that cannot read the cookie cannot produce the header.
func csrfHeaderMatches(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	header := strings.TrimSpace(r.Header.Get(csrfHeaderName))
	if header == "" || len(header) != len(cookie.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}

// unsafeMethod reports whether the method changes state and therefore needs the
// CSRF check when the credential came from a cookie.
func unsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}
