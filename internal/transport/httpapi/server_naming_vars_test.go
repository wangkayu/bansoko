package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
	"bansoko/internal/namingvars/editor"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

type fakeNamingVars struct {
	saveErr    error
	expected   int64
	importCall int
	dryTargets []editor.DryRunTarget
}

func version(revision int64, active bool) namingvars.Version {
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	return namingvars.Version{
		VersionInfo: namingvars.VersionInfo{Revision: revision, Digest: "sha256:abc", Origin: namingvars.OriginEditor, Note: "n", SourceBytes: 4, CreatedAt: at, ActivatedAt: &at, Active: active},
		Source:      "x=1;",
	}
}

func (f *fakeNamingVars) GetState(context.Context) (editor.State, error) {
	return editor.State{
		Active: version(2, true), Versions: []namingvars.VersionInfo{version(2, true).VersionInfo, version(1, false).VersionInfo},
		Facts:   []editor.InputFact{{Path: "anime.title", Type: "string", Nullable: true, LabelKey: "facts.anime.title.label", DescriptionKey: "facts.anime.title.description"}},
		Samples: []editor.SampleInfo{{Ref: "tv_full", ReleaseTitle: "T", FileCount: 1}}, Limits: editor.Limits{MaxSourceBytes: 65536, TimeoutMillis: 500},
	}, nil
}
func (f *fakeNamingVars) GetVersion(context.Context, int64) (namingvars.Version, error) {
	return version(1, false), nil
}
func (f *fakeNamingVars) ListReleases(context.Context) ([]editor.ReleaseOption, error) {
	return []editor.ReleaseOption{{ID: "r1", Title: "T", AnimeTitle: "A"}}, nil
}
func (f *fakeNamingVars) DryRun(_ context.Context, source string, targets []editor.DryRunTarget) (editor.DryRunResult, error) {
	f.dryTargets = targets
	return editor.DryRunResult{Digest: "sha256:abc", Targets: []editor.TargetResult{{
		Kind: "sample", Ref: "tv_full", Title: "T", FileCount: 1, Status: editor.StatusError,
		Diagnostic: &namingvars.Diagnostic{Code: namingvars.DiagOutputMissing, Message: "missing", Start: 3, End: 9},
	}}}, nil
}
func (f *fakeNamingVars) Save(_ context.Context, _, _ string, expected int64) (namingvars.Version, error) {
	f.expected = expected
	if f.saveErr != nil {
		return namingvars.Version{}, f.saveErr
	}
	return version(3, true), nil
}
func (f *fakeNamingVars) Activate(_ context.Context, revision, expected int64) (namingvars.Version, error) {
	f.expected = expected
	return version(revision, true), nil
}
func (f *fakeNamingVars) Export(context.Context) (editor.Document, error) {
	return editor.Document{Format: editor.DocumentFormat, FormatVersion: 1, Runtime: namingvars.ProfileBase, Digest: "sha256:abc", Revision: 2, ExportedAt: time.Now().UTC(), Source: "x=1;"}, nil
}
func (f *fakeNamingVars) Import(context.Context, editor.Document, int64) (namingvars.Version, error) {
	f.importCall++
	return version(4, true), nil
}

func serveNamingVars(t *testing.T, service *fakeNamingVars, method, target string, body any, header http.Header, account *accessdomain.Account) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	server := &Server{deps: Dependencies{NamingVars: service, SkipAuth: true, RuntimeLogger: observabilityport.NopRuntimeLogger{}}}
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, target, reader)
	for key, values := range header {
		request.Header[key] = values
	}
	recorder := httptest.NewRecorder()
	handler := server.Handler()
	if account != nil {
		// Reach the generated route with a non-owner account on the context.
		inner := generated.HandlerWithOptions(server, generated.StdHTTPServerOptions{BaseURL: "/api/v1"})
		request = request.WithContext(context.WithValue(request.Context(), accountKey, *account))
		inner.ServeHTTP(recorder, request)
	} else {
		handler.ServeHTTP(recorder, request)
	}
	var parsed map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &parsed)
	return recorder, parsed
}

func TestNamingVarsStateShape(t *testing.T) {
	recorder, body := serveNamingVars(t, &fakeNamingVars{}, http.MethodGet, "/api/v1/naming-vars", nil, nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	active := body["active"].(map[string]any)
	if active["source"] != "x=1;" || active["version"].(map[string]any)["revision"].(float64) != 2 {
		t.Fatalf("active = %v", active)
	}
	if len(body["versions"].([]any)) != 2 || len(body["input_facts"].([]any)) != 1 || len(body["samples"].([]any)) != 1 {
		t.Fatalf("state = %v", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("state must not be cached: a stale revision is a false CAS token")
	}
}

func TestNamingVarsSaveMapsCASAndSpans(t *testing.T) {
	service := &fakeNamingVars{}
	recorder, body := serveNamingVars(t, service, http.MethodPost, "/api/v1/naming-vars/versions", map[string]any{"source": "x=1;"}, http.Header{"If-Match": {"2"}}, nil)
	if recorder.Code != http.StatusCreated || service.expected != 2 || body["version"].(map[string]any)["revision"].(float64) != 3 {
		t.Fatalf("save = %d %v (expected %d)", recorder.Code, body, service.expected)
	}

	conflict := &fakeNamingVars{saveErr: &apperrors.VersionConflict{Owner: "naming_vars", CurrentVersion: 5}}
	recorder, body = serveNamingVars(t, conflict, http.MethodPost, "/api/v1/naming-vars/versions", map[string]any{"source": "x=1;"}, http.Header{"If-Match": {"2"}}, nil)
	if recorder.Code != http.StatusConflict || body["current_version"].(float64) != 5 {
		t.Fatalf("conflict = %d %v", recorder.Code, body)
	}

	start, end := 7, 12
	invalid := &fakeNamingVars{saveErr: &apperrors.Validation{Code: "NAMING_VARS_INVALID", Details: []apperrors.FieldViolation{{Field: strPtr("source"), Code: "syntax_error", Message: "Unexpected token", Start: &start, End: &end}}}}
	recorder, body = serveNamingVars(t, invalid, http.MethodPost, "/api/v1/naming-vars/versions", map[string]any{"source": "x"}, http.Header{"If-Match": {"2"}}, nil)
	details := body["details"].([]any)
	if recorder.Code != http.StatusUnprocessableEntity || body["code"] != "NAMING_VARS_INVALID" || details[0].(map[string]any)["start"].(float64) != 7 || details[0].(map[string]any)["end"].(float64) != 12 {
		t.Fatalf("invalid = %d %v", recorder.Code, body)
	}
}

func TestNamingVarsDryRunPassesTargetsAndShapesDiagnostics(t *testing.T) {
	service := &fakeNamingVars{}
	recorder, body := serveNamingVars(t, service, http.MethodPost, "/api/v1/naming-vars/dry-runs", map[string]any{
		"source": "x=1;",
		"targets": []any{
			map[string]any{"kind": "sample", "sample": "tv_full"},
			map[string]any{"kind": "custom", "title": "T", "files": []any{map[string]any{"path": "a/b.mkv", "size": 5}}},
		},
	}, nil, nil)
	if recorder.Code != http.StatusOK || len(service.dryTargets) != 2 || service.dryTargets[1].Files[0].Path != "a/b.mkv" || service.dryTargets[1].Files[0].Size != 5 {
		t.Fatalf("dry run = %d %s targets %+v", recorder.Code, recorder.Body.String(), service.dryTargets)
	}
	target := body["targets"].([]any)[0].(map[string]any)
	diagnostic := target["diagnostics"].([]any)[0].(map[string]any)
	if diagnostic["code"] != "output_missing" || diagnostic["start"].(float64) != 3 || diagnostic["end"].(float64) != 9 {
		t.Fatalf("diagnostic = %v", diagnostic)
	}
	if _, ok := target["output"].(map[string]any); !ok {
		t.Fatalf("output must always be an object: %v", target)
	}
}

func TestNamingVarsExportAndImportAreOwnerOnly(t *testing.T) {
	member := &accessdomain.Account{ID: "x", Username: "x", Role: "member"}
	service := &fakeNamingVars{}
	recorder, body := serveNamingVars(t, service, http.MethodGet, "/api/v1/naming-vars/export", nil, nil, member)
	if recorder.Code != http.StatusForbidden || body["code"] != "OWNER_REQUIRED" {
		t.Fatalf("member export = %d %v", recorder.Code, body)
	}
	document := map[string]any{"format": "bansoko-naming-vars", "format_version": 1, "runtime": "naming-vars-v1", "digest": "sha256:abc", "revision": 2, "note": "", "exported_at": "2026-10-12T08:00:00Z", "source": "x=1;"}
	recorder, _ = serveNamingVars(t, service, http.MethodPost, "/api/v1/naming-vars/imports", document, http.Header{"If-Match": {"2"}}, member)
	if recorder.Code != http.StatusForbidden || service.importCall != 0 {
		t.Fatalf("member import = %d (service called %d times)", recorder.Code, service.importCall)
	}
	owner := &accessdomain.Account{ID: "o", Username: "o", Role: "owner"}
	recorder, body = serveNamingVars(t, service, http.MethodGet, "/api/v1/naming-vars/export", nil, nil, owner)
	if recorder.Code != http.StatusOK || body["format"] != "bansoko-naming-vars" || body["source"] != "x=1;" {
		t.Fatalf("owner export = %d %v", recorder.Code, body)
	}
	recorder, _ = serveNamingVars(t, service, http.MethodPost, "/api/v1/naming-vars/imports", document, http.Header{"If-Match": {"2"}}, owner)
	if recorder.Code != http.StatusCreated || service.importCall != 1 {
		t.Fatalf("owner import = %d", recorder.Code)
	}
}

func strPtr(value string) *string { return &value }
