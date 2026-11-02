package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/transport/httpapi/generated"
	updateapplication "bansoko/internal/update/application"
)

type admitUpdateRecorder struct{ called bool }

func (f *admitUpdateRecorder) AdmitUpdate(context.Context, updateapplication.AdmitInput, string) (updateapplication.Status, bool, error) {
	f.called = true
	return updateapplication.Status{OperationID: "update-1"}, true, nil
}

func (f *admitUpdateRecorder) CheckUpdate(context.Context) (updateapplication.Check, error) {
	return updateapplication.Check{}, nil
}

func (f *admitUpdateRecorder) GetUpdateStatus(context.Context) (updateapplication.Status, bool, error) {
	return updateapplication.Status{}, false, nil
}

func TestAdmitUpdateIsOnlyForTheWebSession(t *testing.T) {
	service := &admitUpdateRecorder{}
	server := &Server{deps: Dependencies{EmbeddedUpdate: service}}
	account := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...)}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/update/admit", strings.NewReader("{}"))
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response := httptest.NewRecorder()
	server.AdmitUpdate(response, request, generated.AdmitUpdateParams{})
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "WEB_SESSION_REQUIRED") {
		t.Fatalf("token admit = %d %s, want 403 WEB_SESSION_REQUIRED", response.Code, response.Body.String())
	}
	if service.called {
		t.Fatal("a token reached the update admission service")
	}

	account.WebSession = true
	request = httptest.NewRequest(http.MethodPost, "/api/v1/update/admit", strings.NewReader("{}"))
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response = httptest.NewRecorder()
	server.AdmitUpdate(response, request, generated.AdmitUpdateParams{})
	if response.Code != http.StatusCreated || !service.called {
		t.Fatalf("web session admit = %d %s, called = %v", response.Code, response.Body.String(), service.called)
	}
}
