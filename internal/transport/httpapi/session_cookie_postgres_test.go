package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

// cookieSession logs in through the browser transport and returns a client
// that replays the two cookies the way a browser would.
type cookieSession struct {
	session string
	csrf    string
}

// loginCookies performs a cookie-transport login and captures the Set-Cookie
// pairs the response handed the browser.
func loginCookies(t *testing.T, rig *testRig, username, password string) cookieSession {
	t.Helper()
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password, "session_transport": "cookie"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login: %d %s", recorder.Code, body)
	}
	return cookiesFrom(t, recorder)
}

func cookiesFrom(t *testing.T, recorder *httptest.ResponseRecorder) cookieSession {
	t.Helper()
	var session cookieSession
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			session.session = cookie.Value
			// The credential must never be reachable from script.
			if !cookie.HttpOnly {
				t.Fatal("the session cookie must be HttpOnly")
			}
			if cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie SameSite = %v, want Strict", cookie.SameSite)
			}
		case csrfCookieName:
			session.csrf = cookie.Value
			if cookie.HttpOnly {
				t.Fatal("the CSRF cookie must be readable by the page")
			}
		}
	}
	if session.session == "" || session.csrf == "" {
		t.Fatalf("login set session=%q csrf=%q, want both", session.session, session.csrf)
	}
	return session
}

// asBrowser replays the cookies plus the CSRF echo an unsafe request needs.
func (c cookieSession) headers(method string) map[string]string {
	header := map[string]string{"Cookie": sessionCookieName + "=" + c.session + "; " + csrfCookieName + "=" + c.csrf}
	if unsafeMethod(method) {
		header[csrfHeaderName] = c.csrf
	}
	return header
}

func TestCookieTransportIsTheDefaultAndReturnsNoToken(t *testing.T) {
	rig := newTestRig(t, "development", false)
	bootstrapOwner(t, rig, "owner", "correct horse battery")

	// No session_transport at all: the browser client asks for nothing and
	// still must not receive a script-readable credential.
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "owner", "password": "correct horse battery"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login: %d %s", recorder.Code, body)
	}
	result := decodeBody[generated.LoginResult](t, body)
	if result.SessionToken != nil {
		t.Fatalf("the default transport returned a body token: %q", *result.SessionToken)
	}
	cookiesFrom(t, recorder)

	// An explicit token request still answers in the body and sets no session
	// cookie, which is what the CLI and the scripts rely on.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("token login: %d %s", recorder.Code, body)
	}
	if decodeBody[generated.LoginResult](t, body).SessionToken == nil {
		t.Fatal("session_transport=token returned no token")
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			t.Fatal("a token-transport login must not also set a session cookie")
		}
	}
}

// TestCookieSessionRequiresTheCSRFEcho is the double-submit contract: a
// cookie-authenticated unsafe request without the matching header is refused
// before the handler runs, and the same request with it succeeds.
func TestCookieSessionRequiresTheCSRFEcho(t *testing.T) {
	rig := newTestRig(t, "development", false)
	bootstrapOwner(t, rig, "owner", "correct horse battery")
	session := loginCookies(t, rig, "owner", "correct horse battery")

	// Reads need no echo: a GET changes nothing.
	if recorder, body := rig.do(t, http.MethodGet, "/api/v1/anime", nil, session.headers(http.MethodGet)); recorder.Code != http.StatusOK {
		t.Fatalf("cookie GET: %d %s", recorder.Code, body)
	}

	// The cookie alone authenticates but is not enough to write.
	withoutEcho := map[string]string{"Cookie": sessionCookieName + "=" + session.session + "; " + csrfCookieName + "=" + session.csrf}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", map[string]any{"name": "csrf-probe"}, withoutEcho)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("write without the CSRF echo: %d %s, want 403", recorder.Code, body)
	}
	if code := decodeBody[generated.ErrorResponse](t, body).Code; code != "CSRF_TOKEN_INVALID" {
		t.Fatalf("refusal code = %s, want CSRF_TOKEN_INVALID", code)
	}
	var minted int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM api_tokens`).Scan(&minted); err != nil {
		t.Fatal(err)
	}
	if minted != 0 {
		t.Fatalf("the refused request minted %d tokens", minted)
	}

	// A header that does not match the cookie is refused the same way.
	mismatched := map[string]string{
		"Cookie":       sessionCookieName + "=" + session.session + "; " + csrfCookieName + "=" + session.csrf,
		csrfHeaderName: "not-the-cookie-value",
	}
	if recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", map[string]any{"name": "csrf-probe"}, mismatched); recorder.Code != http.StatusForbidden {
		t.Fatalf("mismatched CSRF echo: %d %s, want 403", recorder.Code, body)
	}

	if recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", map[string]any{"name": "csrf-probe"}, session.headers(http.MethodPost)); recorder.Code != http.StatusCreated {
		t.Fatalf("write with the CSRF echo: %d %s", recorder.Code, body)
	}
}

// TestBearerCallersNeedNoCSRF pins the exemption: a browser never attaches an
// Authorization header by itself, so a bearer request cannot be forged by
// another origin's page and must not be asked for the double-submit value.
func TestBearerCallersNeedNoCSRF(t *testing.T) {
	rig := newTestRig(t, "development", false)
	token := bootstrapOwner(t, rig, "owner", "correct horse battery")
	bearer := map[string]string{"Authorization": "Bearer " + token}

	if recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", map[string]any{"name": "agent"}, bearer); recorder.Code != http.StatusCreated {
		t.Fatalf("bearer write without CSRF: %d %s", recorder.Code, body)
	}
}

// TestAuthSessionProbeReportsTheCookieSession covers the gate's only way of
// learning it is signed in, and its fail-closed answer when it is not.
func TestAuthSessionProbeReportsTheCookieSession(t *testing.T) {
	rig := newTestRig(t, "development", false)
	bootstrapOwner(t, rig, "owner", "correct horse battery")

	// No credential: a browser with no session must get a clear 401 rather
	// than an empty 200 that would open the shell.
	if recorder, body := rig.do(t, http.MethodGet, "/api/v1/auth/session", nil, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous probe: %d %s, want 401", recorder.Code, body)
	}

	session := loginCookies(t, rig, "owner", "correct horse battery")
	recorder, body := rig.do(t, http.MethodGet, "/api/v1/auth/session", nil, session.headers(http.MethodGet))
	if recorder.Code != http.StatusOK {
		t.Fatalf("probe: %d %s", recorder.Code, body)
	}
	reported := decodeBody[generated.AuthSession](t, body)
	if reported.Owner.Username != "owner" || reported.ExpiresAt.IsZero() {
		t.Fatalf("probe reported %+v", reported)
	}
}

// TestLogoutClearsBothCookies: a logout that only revoked the row would leave
// a dead cookie the browser keeps replaying, so both must be expired.
func TestLogoutClearsBothCookies(t *testing.T) {
	rig := newTestRig(t, "development", false)
	bootstrapOwner(t, rig, "owner", "correct horse battery")
	session := loginCookies(t, rig, "owner", "correct horse battery")

	recorder, body := rig.do(t, http.MethodPost, "/api/v1/auth/logout", nil, session.headers(http.MethodPost))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", recorder.Code, body)
	}
	cleared := map[string]bool{}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	if !cleared[sessionCookieName] || !cleared[csrfCookieName] {
		t.Fatalf("logout cleared %v, want both cookies expired", cleared)
	}

	// The replayed credential is dead: revocation, not just the cookie drop.
	if recorder, _ := rig.do(t, http.MethodGet, "/api/v1/auth/session", nil, session.headers(http.MethodGet)); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("probe after logout: %d, want 401", recorder.Code)
	}
}

// TestSessionCookieSecureFlagFollowsTheProxy pins the reverse-proxy rule: the
// app never terminates TLS itself, so a Secure cookie is only correct when the
// proxy said the request arrived over HTTPS. Marking it Secure on a plain-HTTP
// LAN instance makes the browser drop it, which reads as an unstickable login.
func TestSessionCookieSecureFlagFollowsTheProxy(t *testing.T) {
	rig := newTestRig(t, "development", false)
	bootstrapOwner(t, rig, "owner", "correct horse battery")

	secureFor := func(headers map[string]string) bool {
		t.Helper()
		body := map[string]string{"username": "owner", "password": "correct horse battery"}
		recorder, payload := rig.do(t, http.MethodPost, "/api/v1/auth/login", body, headers)
		if recorder.Code != http.StatusOK {
			t.Fatalf("login: %d %s", recorder.Code, payload)
		}
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == sessionCookieName {
				return cookie.Secure
			}
		}
		t.Fatal("login set no session cookie")
		return false
	}
	if secureFor(nil) {
		t.Fatal("a direct plain-HTTP login must not mark the cookie Secure")
	}
	if !secureFor(map[string]string{"X-Forwarded-Proto": "https"}) {
		t.Fatal("a proxied HTTPS login must mark the cookie Secure")
	}
}
