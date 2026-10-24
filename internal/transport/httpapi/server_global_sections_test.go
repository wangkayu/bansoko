package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bansoko/internal/anime/animeconfig"
	configurationdomain "bansoko/internal/configuration/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

// recordingGlobalSections is the transport-boundary stub for the global
// section owner: it records what the generated route handed to the handler so a
// test can prove the path parameter and the value object survive the HTTP
// boundary.
type recordingGlobalSections struct {
	views       []configurationdomain.GlobalSectionView
	putResult   configurationdomain.GlobalSectionView
	lastName    string
	lastVersion int64
	lastInput   configurationdomain.GlobalSectionInput

	library        configurationdomain.NamingTemplateLibraryView
	libraryVersion int64
	libraryInput   configurationdomain.NamingTemplateLibraryInput
}

func (r *recordingGlobalSections) GetNamingTemplateLibrary(context.Context) (configurationdomain.NamingTemplateLibraryView, error) {
	return r.library, nil
}

func (r *recordingGlobalSections) PutNamingTemplateLibrary(_ context.Context, expected int64, input configurationdomain.NamingTemplateLibraryInput) (configurationdomain.NamingTemplateLibraryView, error) {
	r.libraryVersion, r.libraryInput = expected, input
	return r.library, nil
}

func (r *recordingGlobalSections) ReadBusinessPreset(context.Context) (animeconfig.BusinessSnapshot, error) {
	return animeconfig.BusinessSnapshot{}, nil
}

func (r *recordingGlobalSections) ReadGlobalTagFilters(context.Context) (json.RawMessage, error) {
	return nil, nil
}

func (r *recordingGlobalSections) ListGlobalSections(context.Context) ([]configurationdomain.GlobalSectionView, error) {
	return r.views, nil
}

func (r *recordingGlobalSections) PutGlobalSection(_ context.Context, name string, expected int64, input configurationdomain.GlobalSectionInput) (configurationdomain.GlobalSectionView, error) {
	r.lastName, r.lastVersion, r.lastInput = name, expected, input
	return r.putResult, nil
}

func newGlobalSectionTestServer(t *testing.T, sections *recordingGlobalSections) *Server {
	t.Helper()
	return &Server{deps: Dependencies{
		GlobalSections: sections,
		SkipAuth:       true,
		RuntimeLogger:  observabilityport.NopRuntimeLogger{},
	}}
}

// An owned section lists through the generic surface with the object value
// shape the contract names, so the UI can read every section from the same
// endpoint.
func TestListGlobalSectionsCarriesAnObjectValue(t *testing.T) {
	sections := &recordingGlobalSections{views: []configurationdomain.GlobalSectionView{{
		SectionName:   "file_effect",
		SchemaVersion: "1",
		Value:         map[string]any{"operation": "copy", "collision_policy": "fail"},
		Version:       4,
		Saved:         true,
	}}}
	recorder := httptest.NewRecorder()
	newGlobalSectionTestServer(t, sections).Handler().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/config/global-sections", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var decoded []generated.GlobalSectionView
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode list: %v; body=%s", err, recorder.Body.String())
	}
	if len(decoded) != 1 || decoded[0].SectionName != generated.GlobalSectionNameFileEffect {
		t.Fatalf("file_effect missing from the wire view: %+v", decoded)
	}
	if decoded[0].Value["collision_policy"] != "fail" || decoded[0].Version != 4 || !decoded[0].Saved {
		t.Fatalf("file_effect wire view drifted: %+v", decoded[0])
	}
}

// The section name is part of the path enum and reaches the owner with its
// If-Match version and value object intact; the generated constant itself is
// the proof that the contract, not a transport special case, admits the name.
func TestPutGlobalSectionBindsSectionName(t *testing.T) {
	sections := &recordingGlobalSections{putResult: configurationdomain.GlobalSectionView{
		SectionName:   "file_effect",
		SchemaVersion: "1",
		Value:         map[string]any{"operation": "copy", "collision_policy": "fail"},
		Version:       5,
		Saved:         true,
	}}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/config/global-sections/"+string(generated.GlobalSectionNameFileEffect),
		bytes.NewBufferString(`{"schema_version":"1","value":{"operation":"copy","collision_policy":"fail"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", "4")
	recorder := httptest.NewRecorder()
	newGlobalSectionTestServer(t, sections).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if sections.lastName != "file_effect" || sections.lastVersion != 4 {
		t.Fatalf("handler saw name=%q version=%d", sections.lastName, sections.lastVersion)
	}
	if sections.lastInput.SchemaVersion != "1" || sections.lastInput.Value["collision_policy"] != "fail" {
		t.Fatalf("handler saw input=%+v", sections.lastInput)
	}
	var decoded generated.GlobalSectionView
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode put: %v; body=%s", err, recorder.Body.String())
	}
	if decoded.SectionName != generated.GlobalSectionNameFileEffect || decoded.Version != 5 {
		t.Fatalf("put response drifted: %+v", decoded)
	}
}

// The naming template library crosses the boundary with its typed rules
// bodies: the owner sees each body as naming-rules JSON and the If-Match
// version, and the view decodes back into typed rules.
func TestNamingTemplateLibraryRoundTripsTypedRules(t *testing.T) {
	rules := `{"language":"js-naming-1","tv":{"directory_template":"tv-dir","video_filename_template":"tv-file"},"movie":{"directory_template":"m-dir","video_filename_template":"m-file"},"unknown":{"directory_template":"u-dir","video_filename_template":"u-file"},"subtitle_suffix_template":"sfx"}`
	sections := &recordingGlobalSections{library: configurationdomain.NamingTemplateLibraryView{
		DefaultKey: "default",
		Version:    3,
		Templates:  []configurationdomain.NamingTemplate{{Key: "default", Name: "Default", Rules: json.RawMessage(rules)}},
	}}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/config/naming-templates",
		bytes.NewBufferString(`{"default_key":"default","templates":[{"key":"default","name":"Default","rules":`+rules+`}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", "2")
	recorder := httptest.NewRecorder()
	newGlobalSectionTestServer(t, sections).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if sections.libraryVersion != 2 || sections.libraryInput.DefaultKey != "default" || len(sections.libraryInput.Templates) != 1 {
		t.Fatalf("owner saw version=%d input=%+v", sections.libraryVersion, sections.libraryInput)
	}
	var sent map[string]any
	if err := json.Unmarshal(sections.libraryInput.Templates[0].Rules, &sent); err != nil || sent["subtitle_suffix_template"] != "sfx" {
		t.Fatalf("owner rules = %s (%v)", sections.libraryInput.Templates[0].Rules, err)
	}
	var decoded generated.NamingTemplateLibraryView
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode put: %v; body=%s", err, recorder.Body.String())
	}
	if decoded.Version != 3 || decoded.Templates[0].Rules.Tv.VideoFilenameTemplate != "tv-file" {
		t.Fatalf("library view drifted: %+v", decoded)
	}
}
