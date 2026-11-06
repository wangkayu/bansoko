package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/parser/titleparser"
	"bansoko/internal/transport/httpapi/generated"
)

type fakeParserComponent struct {
	status       titleparser.Status
	statusErr    error
	checkErr     error
	installErr   error
	installCalls int
}

func (f *fakeParserComponent) Status(ctx context.Context) (titleparser.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeParserComponent) Check(ctx context.Context) (titleparser.Status, error) {
	return f.status, f.checkErr
}

func (f *fakeParserComponent) Install(ctx context.Context) (titleparser.Status, error) {
	f.installCalls++
	return f.status, f.installErr
}

func newTitleParserServer(t *testing.T, skipAuth bool, service ParserComponentService) *httptest.Server {
	t.Helper()
	srv, err := NewServer(Dependencies{
		DB:          &sql.DB{},
		Access:      accessapplication.NewService(nil),
		Environment: "development",
		SkipAuth:    skipAuth,
		TitleParser: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return server
}

func TestTitleParserRequiresAuthentication(t *testing.T) {
	server := newTitleParserServer(t, false, &fakeParserComponent{})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/v1/title-parser"
		if method == http.MethodPost {
			path = "/api/v1/title-parser/check"
		}
		request, err := http.NewRequest(method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", method, path, response.StatusCode)
		}
	}
}

func TestGetTitleParserReportsTheStatusShape(t *testing.T) {
	checkedAt := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC)
	service := &fakeParserComponent{status: titleparser.Status{
		Source:          titleparser.SourceBuiltin,
		Version:         "reltitle@v0.0.4",
		Line:            "0.0",
		ConfigDigest:    "lexicon:sha256:abc",
		BinaryPath:      "/config/rt/reltitle",
		Note:            "startup note",
		ExternalFailure: "",
		Lane:            titleparser.LaneStable,
		Message:         "",
		Channel: &titleparser.ChannelStatus{
			CheckedAt:  checkedAt,
			Version:    "0.0.5",
			Notes:      titleparser.Notes{ZhCN: "- 新版本", EN: "- new build"},
			ReleaseURL: "https://example.test/reltitle/v0.0.5",
			Newer:      true,
			Error:      "",
		},
	}}
	server := newTitleParserServer(t, true, service)

	response, err := http.Get(server.URL + "/api/v1/title-parser")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body generated.TitleParserStatus
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Source != generated.TitleParserStatusSourceBuiltin || body.Version != "reltitle@v0.0.4" || body.Line != "0.0" {
		t.Fatalf("body = %+v", body)
	}
	if body.BinaryPath != "/config/rt/reltitle" || body.Lane != generated.TitleParserStatusLaneStable {
		t.Fatalf("path/lane = %+v", body)
	}
	if body.Channel == nil || body.Channel.Version != "0.0.5" || !body.Channel.Newer {
		t.Fatalf("channel = %+v", body.Channel)
	}
	if body.Channel.Notes.ZhCn != "- 新版本" || body.Channel.Notes.En != "- new build" {
		t.Fatalf("notes = %+v", body.Channel.Notes)
	}
	if !body.Channel.CheckedAt.Equal(checkedAt) {
		t.Fatalf("checked_at = %v", body.Channel.CheckedAt)
	}
}

func TestCheckTitleParserMapsAChannelFailureTo502(t *testing.T) {
	service := &fakeParserComponent{checkErr: fmt.Errorf("%w: unreachable", titleparser.ErrChannel)}
	server := newTitleParserServer(t, true, service)

	response, err := http.Post(server.URL+"/api/v1/title-parser/check", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
	var body generated.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "TITLE_PARSER_CHANNEL_UNAVAILABLE" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestUpdateTitleParserMapsAnAdoptionFailureTo502(t *testing.T) {
	service := &fakeParserComponent{installErr: fmt.Errorf("%w: bad binary", titleparser.ErrUpdate)}
	server := newTitleParserServer(t, true, service)

	response, err := http.Post(server.URL+"/api/v1/title-parser/update", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
	var body generated.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "TITLE_PARSER_UPDATE_FAILED" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestUpdateTitleParserIsOnlyForTheWebSession(t *testing.T) {
	service := &fakeParserComponent{}
	server := &Server{deps: Dependencies{TitleParser: service}}
	account := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...)}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/title-parser/update", nil)
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response := httptest.NewRecorder()
	server.UpdateTitleParser(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "WEB_SESSION_REQUIRED") {
		t.Fatalf("token update = %d %s, want 403 WEB_SESSION_REQUIRED", response.Code, response.Body.String())
	}
	if service.installCalls != 0 {
		t.Fatal("a token reached the parser install service")
	}

	account.WebSession = true
	request = httptest.NewRequest(http.MethodPost, "/api/v1/title-parser/update", nil)
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response = httptest.NewRecorder()
	server.UpdateTitleParser(response, request)
	if response.Code != http.StatusOK || service.installCalls != 1 {
		t.Fatalf("web session update = %d %s, calls = %d", response.Code, response.Body.String(), service.installCalls)
	}
}

func TestUpdateTitleParserReturnsTheNewStatus(t *testing.T) {
	service := &fakeParserComponent{status: titleparser.Status{
		Source:  titleparser.SourceChannel,
		Version: "reltitle@0.0.4",
		Line:    "0.0",
		Lane:    titleparser.LaneStable,
		Message: "installed reltitle 0.0.4",
		Channel: &titleparser.ChannelStatus{Version: "0.0.4"},
	}}
	server := newTitleParserServer(t, true, service)

	response, err := http.Post(server.URL+"/api/v1/title-parser/update", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var body generated.TitleParserStatus
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Source != generated.TitleParserStatusSourceChannel || body.Version != "reltitle@0.0.4" {
		t.Fatalf("body = %+v", body)
	}
	if body.Message != "installed reltitle 0.0.4" {
		t.Fatalf("message = %q", body.Message)
	}
}

func TestTitleParserEndpointsAnswer503WithoutAService(t *testing.T) {
	server := newTitleParserServer(t, true, nil)
	response, err := http.Get(server.URL + "/api/v1/title-parser")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}
	var body generated.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "SERVICE_NOT_CONFIGURED" {
		t.Fatalf("code = %q", body.Code)
	}
}
