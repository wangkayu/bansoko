package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bansoko/internal/attrrules"

	templatecatalogapplication "bansoko/internal/templatecatalog/application"
)

// Direct-surface contract evidence for the preview matrix: the parse endpoints,
// the naming template render and the media-recognition batch are exercised
// through their real handlers with the real services behind them, so the
// documented success and refusal payloads are proven at the HTTP boundary
// rather than assumed from unit-level behaviour.

func postJSON(t *testing.T, handler func(http.ResponseWriter, *http.Request), path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// The parser surface answers a structured release-title parse and refuses an
// unusable title with a typed 422 instead of a 200 carrying nulls.
func TestParseReleaseTitleContract(t *testing.T) {
	// The pure parse surface reads the process attribute rules, so the test
	// loads the shipped defaults instead of inventing a shape.
	rulesPath := filepath.Join("..", "..", "..", "deployment", "defaults", "attr-rules.yaml")
	rulesData, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("read shipped attribute rules: %v", err)
	}
	rules, err := attrrules.Parse(rulesData, rulesPath)
	if err != nil {
		t.Fatalf("parse shipped attribute rules: %v", err)
	}
	fingerprint, err := attrrules.Canonical(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := attrrules.SetActive(rules, string(fingerprint)); err != nil {
		t.Fatalf("activate attribute rules: %v", err)
	}

	server := &Server{}
	ok := postJSON(t, server.ParseReleaseTitle, "/api/v1/parse/release-title",
		`{"title":"[Group] Show - 01 [1080p] [CHS]"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("parse status = %d body = %s", ok.Code, ok.Body.String())
	}
	parsed := decodeBody[map[string]any](t, ok.Body.Bytes())
	if _, found := parsed["facts"]; !found {
		t.Fatalf("parse payload has no facts: %s", ok.Body.String())
	}
	if parsed["parser_version"] == "" {
		t.Fatalf("parse payload carries no parser version: %s", ok.Body.String())
	}

	empty := postJSON(t, server.ParseReleaseTitle, "/api/v1/parse/release-title", `{"title":""}`)
	if empty.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty title status = %d body = %s", empty.Code, empty.Body.String())
	}
	if refusal := decodeBody[map[string]any](t, empty.Body.Bytes()); refusal["code"] != "TITLE_PARSE_FAILED" {
		t.Fatalf("empty-title refusal = %v", refusal)
	}

	malformed := postJSON(t, server.ParseReleaseTitle, "/api/v1/parse/release-title", `{"title":`)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d body = %s", malformed.Code, malformed.Body.String())
	}
}

// The naming render answers the documented preview for a valid template and an
// explicit invalid result for a template the engine refuses: it never returns a
// half-rendered target.
func TestNamingTemplatePreviewContract(t *testing.T) {
	server := &Server{deps: Dependencies{TemplateCatalog: templatecatalogapplication.NewService()}}

	valid := postJSON(t, server.PreviewNamingTemplate, "/api/v1/template-previews/naming",
		`{"template_kind":"directory","template_source":"/library/${sanitize(anime.title)}"}`)
	if valid.Code != http.StatusOK {
		t.Fatalf("naming preview status = %d body = %s", valid.Code, valid.Body.String())
	}
	preview := decodeBody[map[string]any](t, valid.Body.Bytes())
	if preview["valid"] != true {
		t.Fatalf("valid template reported valid=%v body=%s", preview["valid"], valid.Body.String())
	}
	rendered, _ := preview["rendered"].(string)
	if strings.TrimSpace(rendered) == "" {
		t.Fatalf("valid template rendered nothing: %s", valid.Body.String())
	}

	// A value wrapped in its own backticks is the old full-literal form; it is
	// refused like any other template the engine cannot compile.
	wrapped := postJSON(t, server.PreviewNamingTemplate, "/api/v1/template-previews/naming",
		`{"template_kind":"directory","template_source":"`+"`"+`/library/${sanitize(anime.title)}`+"`"+`"}`)
	if wrapped.Code != http.StatusOK {
		t.Fatalf("wrapped preview status = %d body = %s", wrapped.Code, wrapped.Body.String())
	}
	if wrappedPreview := decodeBody[map[string]any](t, wrapped.Body.Bytes()); wrappedPreview["valid"] != false {
		t.Fatalf("wrapped template reported valid=%v body=%s", wrappedPreview["valid"], wrapped.Body.String())
	}

	invalid := postJSON(t, server.PreviewNamingTemplate, "/api/v1/template-previews/naming",
		`{"template_kind":"directory","template_source":"${DefinitelyNotAFunction(anime.title)}"}`)
	if invalid.Code != http.StatusOK {
		t.Fatalf("invalid template status = %d, want a 200 with valid=false: %s", invalid.Code, invalid.Body.String())
	}
	invalidPreview := decodeBody[map[string]any](t, invalid.Body.Bytes())
	if invalidPreview["valid"] != false {
		t.Fatalf("invalid template reported valid=%v body=%s", invalidPreview["valid"], invalid.Body.String())
	}
	errors, _ := invalidPreview["errors"].([]any)
	if len(errors) == 0 {
		t.Fatalf("invalid template reported no error: %s", invalid.Body.String())
	}

	unknownKind := postJSON(t, server.PreviewNamingTemplate, "/api/v1/template-previews/naming",
		`{"template_kind":"not_a_kind","template_source":"x"}`)
	if unknownKind.Code == http.StatusOK {
		t.Fatalf("unknown template kind was accepted: %s", unknownKind.Body.String())
	}
}

// The media-recognition batch is bounded at the boundary: an empty or oversized
// batch is a typed 422 and a single item reaches the real parser.
func TestMediaRecognitionBatchIsBounded(t *testing.T) {
	server := &Server{deps: Dependencies{MediaRecognition: NewMediaRecognitionService(nil)}}

	over := make([]string, 257)
	for index := range over {
		over[index] = `{"client_ref":"ref-` + strconv.Itoa(index) + `","name":"show - 01 [1080p].mkv"}`
	}
	oversized := postJSON(t, server.PreviewMediaRecognition, "/api/v1/media-recognition/previews",
		`{"context":{"work_kind":"tv"},"parser":{"id":"regex"},"items":[`+strings.Join(over, ",")+`]}`)
	if oversized.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized batch status = %d body = %s", oversized.Code, oversized.Body.String())
	}
	if refusal := decodeBody[map[string]any](t, oversized.Body.Bytes()); refusal["code"] != "MEDIA_RECOGNITION_INVALID" {
		t.Fatalf("oversized batch refusal = %v", refusal)
	}

	empty := postJSON(t, server.PreviewMediaRecognition, "/api/v1/media-recognition/previews",
		`{"context":{"work_kind":"tv"},"parser":{"id":"regex"},"items":[]}`)
	if empty.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty batch status = %d body = %s", empty.Code, empty.Body.String())
	}

	single := postJSON(t, server.PreviewMediaRecognition, "/api/v1/media-recognition/previews",
		`{"context":{"work_kind":"tv"},"parser":{"id":"regex"},"items":[{"client_ref":"ref-1","name":"[Group] Show - 01 [1080p].mkv"}]}`)
	if single.Code != http.StatusOK {
		t.Fatalf("single item status = %d body = %s", single.Code, single.Body.String())
	}
}

// The generated router wires these three POSTs, so the contract above is
// reachable at the paths the specification documents.
func TestDirectSurfacesAreRouted(t *testing.T) {
	server := &Server{deps: Dependencies{
		TemplateCatalog:  templatecatalogapplication.NewService(),
		MediaRecognition: NewMediaRecognitionService(nil),
	}}
	router := server.Handler()
	for _, path := range []string{"/api/v1/parse/release-title", "/api/v1/media-recognition/previews", "/api/v1/template-previews/naming"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound || recorder.Code == http.StatusMethodNotAllowed {
			t.Fatalf("%s is not routed: %d", path, recorder.Code)
		}
	}
}
