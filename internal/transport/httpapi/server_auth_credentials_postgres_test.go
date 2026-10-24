package httpapi

import (
	"net/http"
	"strconv"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

func bootstrapOwner(t *testing.T, rig *testRig, username, password string) string {
	t.Helper()
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": username, "password": password, "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("bootstrap owner: %d %s", recorder.Code, string(body))
	}
	token := decodeBody[generated.LoginResult](t, body).SessionToken
	if token == nil || *token == "" {
		t.Fatal("bootstrap owner returned no session token")
	}
	return *token
}

func loginSession(t *testing.T, rig *testRig, username, password string) string {
	t.Helper()
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password, "session_transport": "token"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login as %s failed: %d %s", username, recorder.Code, string(body))
	}
	token := decodeBody[generated.LoginResult](t, body).SessionToken
	if token == nil || *token == "" {
		t.Fatalf("login as %s returned no session token", username)
	}
	return *token
}

func TestUpdateAccountChangesUsernameAndPassword(t *testing.T) {
	rig := newTestRig(t, "development", false)
	current := bootstrapOwner(t, rig, "owner", "original password 12")
	other := loginSession(t, rig, "owner", "original password 12")
	currentHeaders := map[string]string{"Authorization": "Bearer " + current}
	otherHeaders := map[string]string{"Authorization": "Bearer " + other}

	recorder, body := rig.do(t, http.MethodPatch, "/api/v1/auth/account", map[string]any{
		"current_password": "original password 12",
		"username":         "renamed owner",
	}, currentHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", recorder.Code, string(body))
	}
	renamed := decodeBody[generated.AccountUpdateResult](t, body)
	if renamed.Owner.Username != "renamed owner" || renamed.OtherSessionsRevoked {
		t.Fatalf("unexpected rename result: %+v", renamed)
	}
	if recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, otherHeaders); recorder.Code != http.StatusOK {
		t.Fatalf("a username change must not revoke another session: %d", recorder.Code)
	}
	if recorder, body = rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "owner", "password": "original password 12", "session_transport": "token"}, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the old username must stop authenticating: %d %s", recorder.Code, string(body))
	}
	loginSession(t, rig, "renamed owner", "original password 12")

	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/auth/account", map[string]any{
		"current_password": "original password 12",
		"new_password":     "replacement password 12",
	}, currentHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("password change: %d %s", recorder.Code, string(body))
	}
	changed := decodeBody[generated.AccountUpdateResult](t, body)
	if !changed.OtherSessionsRevoked || changed.Owner.Username != "renamed owner" {
		t.Fatalf("unexpected password result: %+v", changed)
	}
	if recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, currentHeaders); recorder.Code != http.StatusOK {
		t.Fatalf("the current session must stay valid after a password change: %d", recorder.Code)
	}
	if recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, otherHeaders); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("another session must be revoked after a password change: %d", recorder.Code)
	}
	if recorder, _ = rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "renamed owner", "password": "original password 12", "session_transport": "token"}, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the old password must stop authenticating: %d", recorder.Code)
	}
	loginSession(t, rig, "renamed owner", "replacement password 12")
}

func TestUpdateAccountRejectsWrongCurrentPassword(t *testing.T) {
	rig := newTestRig(t, "development", false)
	session := bootstrapOwner(t, rig, "owner", "original password 12")
	headers := map[string]string{"Authorization": "Bearer " + session}

	recorder, body := rig.do(t, http.MethodPatch, "/api/v1/auth/account", map[string]any{
		"current_password": "wrong password 12",
		"username":         "intruder",
	}, headers)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong current password: %d %s", recorder.Code, string(body))
	}
	if code := decodeBody[generated.ErrorResponse](t, body).Code; code != "CURRENT_PASSWORD_INVALID" {
		t.Fatalf("error code = %s", code)
	}
	if recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, headers); recorder.Code != http.StatusOK {
		t.Fatalf("a wrong current password must not end the session: %d", recorder.Code)
	}
	loginSession(t, rig, "owner", "original password 12")
}

func TestUpdateAccountRequiresAWebSession(t *testing.T) {
	rig := newTestRig(t, "development", false)
	session := bootstrapOwner(t, rig, "owner", "original password 12")
	headers := map[string]string{"Authorization": "Bearer " + session}

	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens",
		map[string]any{"name": "agent", "scopes": []string{"read", "write"}}, headers)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create api token: %d %s", recorder.Code, string(body))
	}
	token := decodeBody[generated.ApiTokenCreateResult](t, body).Token
	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/auth/account", map[string]any{
		"current_password": "original password 12",
		"username":         "agent-renamed",
	}, map[string]string{"Authorization": "Bearer " + token})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("an API token must not change credentials: %d %s", recorder.Code, string(body))
	}
	if code := decodeBody[generated.ErrorResponse](t, body).Code; code != "WEB_SESSION_REQUIRED" {
		t.Fatalf("error code = %s", code)
	}
	loginSession(t, rig, "owner", "original password 12")
}

func TestLoginRateLimitBlocksFurtherAttempts(t *testing.T) {
	rig := newTestRig(t, "development", false)
	bootstrapOwner(t, rig, "owner", "correct horse battery")

	for i := 0; i < loginLimiterIdentityFailures-1; i++ {
		recorder, _ := rig.do(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "owner", "password": "wrong password 12", "session_transport": "token"}, nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, recorder.Code)
		}
	}
	// A successful sign-in clears the identity counter.
	loginSession(t, rig, "owner", "correct horse battery")

	for i := 0; i < loginLimiterIdentityFailures; i++ {
		recorder, _ := rig.do(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "owner", "password": "wrong password 12", "session_transport": "token"}, nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d after reset = %d, want 401", i+1, recorder.Code)
		}
	}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked login = %d %s, want 429", recorder.Code, string(body))
	}
	if code := decodeBody[generated.ErrorResponse](t, body).Code; code != "LOGIN_RATE_LIMITED" {
		t.Fatalf("error code = %s", code)
	}
	retryAfter, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
	if err != nil || retryAfter <= 0 || retryAfter > int(loginLimiterWindow.Seconds()) {
		t.Fatalf("Retry-After = %q", recorder.Header().Get("Retry-After"))
	}
}
