package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeRequestID(t *testing.T) {
	valid := []string{"client-req-1", "abc.DEF_123:trace"}
	for _, value := range valid {
		if got := normalizeRequestID(value); got != value {
			t.Fatalf("normalizeRequestID(%q) = %q, want unchanged", value, got)
		}
	}
	for _, value := range []string{"", "https://example.test/id", "token value", "token/part", "token\npart", strings.Repeat("a", 129)} {
		got := normalizeRequestID(value)
		if got == value || uuid.Validate(got) != nil {
			t.Fatalf("normalizeRequestID(%q) = %q, want a generated UUID", value, got)
		}
	}
}

func TestPanicRecoveryReturnsGenericJSONWithExactRequestID(t *testing.T) {
	const requestID = "panic-request-1"
	const panicValue = "postgres://user:password@db.example/private"
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	server := &Server{deps: Dependencies{
		Environment:        "development",
		SkipAuth:           true,
		RuntimeLogger:      logger,
		DiagnosticRecorder: diagnostic,
	}}
	handler := server.withCommonMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(panicValue)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if response.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("response request ID = %q, want %q", response.Header().Get("X-Request-ID"), requestID)
	}
	if !strings.Contains(response.Body.String(), `"code":"INTERNAL"`) || !strings.Contains(response.Body.String(), `"request_id":"`+requestID+`"`) {
		t.Fatalf("response = %q, want generic error with request ID", response.Body.String())
	}
	if strings.Contains(response.Body.String(), panicValue) || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("panic value leaked into response: %q", response.Body.String())
	}
	if len(diagnostic.failures) != 1 || diagnostic.failures[0].LastError != "<redacted>" {
		t.Fatalf("panic diagnostic = %+v, want one redacted failure", diagnostic.failures)
	}
	if len(logger.events) != 1 || logger.events[0].RequestID != requestID || logger.events[0].ReasonCode != "unknown_error" {
		t.Fatalf("panic runtime events = %+v", logger.events)
	}
	encoded, err := json.Marshal(logger.events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "postgres://") {
		t.Fatalf("panic value leaked into runtime event: %s", encoded)
	}
}

func TestPanicRecoveryUsesNoopLoggerWhenServerLoggerIsNil(t *testing.T) {
	server := &Server{deps: Dependencies{Environment: "development", SkipAuth: true}}
	handler := server.withCommonMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("panic without configured logger")
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"code":"INTERNAL"`) {
		t.Fatalf("response = %q, want generic error", response.Body.String())
	}
}

func TestPanicRecoveryDiscardsPartialAPIResponse(t *testing.T) {
	const requestID = "panic-partial-request"
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	server := &Server{deps: Dependencies{
		Environment:        "development",
		SkipAuth:           true,
		RuntimeLogger:      logger,
		DiagnosticRecorder: diagnostic,
	}}
	handler := server.withCommonMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Partial-Handler", "must-not-survive")
		w.Header().Set("X-Request-ID", "handler-overwrite-must-not-survive")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial body"))
		panic("partial panic value")
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 after partial write", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"code":"INTERNAL"`) || strings.Contains(response.Body.String(), "partial body") {
		t.Fatalf("response = %q, want only generic JSON", response.Body.String())
	}
	if response.Header().Get("X-Partial-Handler") != "" {
		t.Fatalf("partial handler header survived panic: %q", response.Header().Get("X-Partial-Handler"))
	}
	if response.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("response request ID = %q, want %q", response.Header().Get("X-Request-ID"), requestID)
	}
	if !strings.Contains(response.Body.String(), `"request_id":"`+requestID+`"`) || len(diagnostic.failures) != 1 {
		t.Fatalf("response/diagnostic mismatch: response=%q diagnostics=%+v", response.Body.String(), diagnostic.failures)
	}
}

func TestPanicRecoveryDoesNotBufferSSEResponse(t *testing.T) {
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	server := &Server{}
	request := requestWithDependencies("sse-panic-request", logger, diagnostic)
	request.URL.Path = eventsStreamPath
	response := newFlushRecorder()

	server.withPanicRecovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("SSE recovery writer must preserve http.Flusher")
		}
		_, _ = w.Write([]byte("streamed\n"))
		flusher.Flush()
		panic("stream panic value")
	})).ServeHTTP(response, request)

	if response.String() != "streamed\n" {
		t.Fatalf("SSE body = %q, want streamed bytes without JSON replacement", response.String())
	}
	if response.Header().Get("X-Request-ID") != "sse-panic-request" {
		t.Fatalf("SSE request ID = %q, want exact request ID", response.Header().Get("X-Request-ID"))
	}
	if len(diagnostic.failures) != 1 || diagnostic.failures[0].LastError != "<redacted>" {
		t.Fatalf("SSE panic diagnostic = %+v", diagnostic.failures)
	}
}

func TestPanicRecoveryUsesUnbufferedMCPBeforeAndAfterWrite(t *testing.T) {
	t.Run("before write", func(t *testing.T) {
		logger := &captureRuntimeLogger{}
		diagnostic := &captureDiagnosticRecorder{}
		server := &Server{deps: Dependencies{Environment: "development", SkipAuth: true, RuntimeLogger: logger, DiagnosticRecorder: diagnostic}}
		handler := server.withCommonMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Fatal("MCP recovery writer must preserve Flusher")
			}
			panic("MCP panic before write")
		}))
		request := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader("{}"))
		request.Header.Set("X-Request-ID", "mcp-before-request")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL"`) {
			t.Fatalf("MCP before-write response = %d %q, want generic 500", response.Code, response.Body.String())
		}
		if response.Header().Get("X-Request-ID") != "mcp-before-request" {
			t.Fatalf("MCP before-write request ID = %q", response.Header().Get("X-Request-ID"))
		}
		if len(diagnostic.failures) != 1 {
			t.Fatalf("MCP before-write diagnostics = %+v, want one failure", diagnostic.failures)
		}
	})

	t.Run("after write", func(t *testing.T) {
		logger := &captureRuntimeLogger{}
		diagnostic := &captureDiagnosticRecorder{}
		server := &Server{deps: Dependencies{Environment: "development", SkipAuth: true, RuntimeLogger: logger, DiagnosticRecorder: diagnostic}}
		handler := server.withCommonMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-ID", "handler-overwrite")
			_, _ = w.Write([]byte("mcp response"))
			panic("MCP panic after write")
		}))
		request := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader("{}"))
		request.Header.Set("X-Request-ID", "mcp-after-request")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK || response.Body.String() != "mcp response" {
			t.Fatalf("MCP after-write response = %d %q, want established body only", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), `"code":"INTERNAL"`) || response.Header().Get("X-Request-ID") != "mcp-after-request" {
			t.Fatalf("MCP after-write response was corrupted: headers=%v body=%q", response.Header(), response.Body.String())
		}
		if len(diagnostic.failures) != 1 || len(logger.events) != 1 {
			t.Fatalf("MCP after-write diagnostics/logger = %+v/%+v", diagnostic.failures, logger.events)
		}
	})
}

func TestPanicRecoveryDoesNotBufferPosterResponse(t *testing.T) {
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	request := requestWithDependencies("poster-panic-request", logger, diagnostic)
	request.URL.Path = imagePosterPathPrefix + "bgm-1"
	response := httptest.NewRecorder()

	(&Server{}).withPanicRecovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("poster recovery writer must preserve http.Flusher")
		}
		_, _ = w.Write([]byte("poster bytes"))
		panic("poster panic value")
	})).ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "poster bytes" {
		t.Fatalf("poster response = %d %q, want established body only", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") != "poster-panic-request" || len(diagnostic.failures) != 1 {
		t.Fatalf("poster response/diagnostic mismatch: headers=%v diagnostics=%+v", response.Header(), diagnostic.failures)
	}
}

func TestCoverResponsesAreNotBuffered(t *testing.T) {
	if !isUnbufferedPath(imageCoverPathPrefix+"abc") || !isUnbufferedPath(imagePosterPathPrefix+"1") {
		t.Fatal("image proxy responses must stream")
	}
}

func TestBufferedResponseWriterHEAD204AndTrailerCompatibility(t *testing.T) {
	t.Run("HEAD suppresses body and preserves length", func(t *testing.T) {
		buffered := &bufferedResponseWriter{header: make(http.Header), method: http.MethodHead, requestID: "head-request"}
		n, err := buffered.Write([]byte("head body"))
		if n != 9 || err != nil {
			t.Fatalf("HEAD Write = (%d, %v), want (9, nil)", n, err)
		}
		response := httptest.NewRecorder()
		buffered.commit(response)

		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("HEAD response = %d %q, want 200 with no body", response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Length") != "9" || response.Header().Get("X-Request-ID") != "head-request" {
			t.Fatalf("HEAD headers = %v, want length and request ID", response.Header())
		}
	})

	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(http.StatusText(status)+" rejects body", func(t *testing.T) {
			buffered := &bufferedResponseWriter{header: make(http.Header), method: http.MethodGet}
			buffered.Header().Set("Content-Length", "15")
			buffered.WriteHeader(status)
			n, err := buffered.Write([]byte("must not survive"))
			if n != 0 || err != http.ErrBodyNotAllowed {
				t.Fatalf("%d Write = (%d, %v), want (0, http.ErrBodyNotAllowed)", status, n, err)
			}
			response := httptest.NewRecorder()
			buffered.commit(response)

			if response.Code != status || response.Body.Len() != 0 {
				t.Fatalf("%d response = %d %q, want empty body", status, response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Length") != "" {
				t.Fatalf("%d response has content length: %q", status, response.Header().Get("Content-Length"))
			}
		})
	}

	t.Run("declared and dynamic trailers survive", func(t *testing.T) {
		buffered := &bufferedResponseWriter{header: make(http.Header), method: http.MethodGet}
		buffered.Header().Set("Trailer", "Digest")
		buffered.WriteHeader(http.StatusOK)
		buffered.Header().Set("Digest", "declared-digest")
		buffered.Header()[http.TrailerPrefix+"Checksum"] = []string{"dynamic-checksum"}
		_, _ = buffered.Write([]byte("body"))
		response := httptest.NewRecorder()
		buffered.commit(response)

		result := response.Result()
		if result.Body == nil || response.Body.String() != "body" {
			t.Fatalf("trailer response body = %q, want body", response.Body.String())
		}
		if result.Trailer.Get("Digest") != "declared-digest" || result.Trailer.Get("Checksum") != "dynamic-checksum" {
			t.Fatalf("response trailers = %v, want declared and dynamic values", result.Trailer)
		}
	})
}

func TestForwardingResponseWritersUnwrapUnderlyingWriter(t *testing.T) {
	underlying := httptest.NewRecorder()
	streaming := &streamingResponseWriter{ResponseWriter: underlying, flusher: underlying, requestID: "stream-request"}
	tracking := &trackingResponseWriter{ResponseWriter: underlying, requestID: "tracking-request"}
	if streaming.Unwrap() != underlying || tracking.Unwrap() != underlying {
		t.Fatal("forwarding response writers must unwrap to the original writer")
	}
}

// frontendDir builds a minimal built-frontend layout: an index document and one
// hashed asset, matching what the Vite build emits.
func frontendDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Bansoko</title>"), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o700); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "index-abc123.js"), []byte("export default 1"), 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	return dir
}

func get(t *testing.T, handler http.Handler, target string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder.Code, recorder.Body.String()
}

// The frontend loads from "/" and routes on the fragment, so the document and
// the hashed assets are the whole contract this handler owes it.
func TestFrontendHandlerServesTheBuiltFrontend(t *testing.T) {
	handler := frontendHandler(frontendDir(t))

	if status, body := get(t, handler, "/"); status != http.StatusOK || body != "<!doctype html><title>Bansoko</title>" {
		t.Fatalf("root = %d %q, want 200 and index.html", status, body)
	}
	if status, body := get(t, handler, "/assets/index-abc123.js"); status != http.StatusOK || body != "export default 1" {
		t.Fatalf("asset = %d %q, want 200 and the asset contents", status, body)
	}
}

// Pins the absence of a history fallback. A catch-all rewrite would answer 200
// HTML for these, hiding the 404 from anything that is not the hash-routed
// frontend — including API paths outside /api/v1.
func TestFrontendHandlerDoesNotFallBackToIndex(t *testing.T) {
	handler := frontendHandler(frontendDir(t))

	for _, target := range []string{"/settings", "/settings/connections", "/api/v3/anime", "/assets/index-gone.js"} {
		if status, _ := get(t, handler, target); status != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", target, status)
		}
	}
}

func TestFrontendHandlerRejectsNonReadMethods(t *testing.T) {
	recorder := httptest.NewRecorder()
	frontendHandler(frontendDir(t)).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST status = %d, want 404", recorder.Code)
	}
}

func TestFrontendHandlerDoesNotEscapeRoot(t *testing.T) {
	dir := frontendDir(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret"), []byte("do-not-serve"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	handler := frontendHandler(dir)

	for _, target := range []string{"/../secret", "/..%2fsecret", "/assets/../../secret"} {
		if _, body := get(t, handler, target); body == "do-not-serve" {
			t.Fatalf("%s leaked a file outside the frontend directory", target)
		}
	}
}

func TestFrontendHandlerWithoutDirectoryIs404(t *testing.T) {
	if status, _ := get(t, frontendHandler("  "), "/"); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no frontend directory is configured", status)
	}
}

// SEC-033: the document carries the strict page CSP; every response carries
// the baseline headers and names no third-party origin; script assets (the
// service worker included) are served without the page CSP.
func TestSecurityHeaders(t *testing.T) {
	handler := frontendHandler(frontendDir(t))
	baseline := func(surface string, header http.Header) {
		t.Helper()
		for name, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
			"Permissions-Policy":     PermissionsPolicy,
		} {
			if got := header.Get(name); got != want {
				t.Fatalf("%s %s = %q, want %q", surface, name, got, want)
			}
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	baseline("document", recorder.Header())
	csp := recorder.Header().Get("Content-Security-Policy")
	if csp != DocumentContentSecurityPolicy {
		t.Fatalf("document CSP = %q", csp)
	}
	for _, directive := range []string{"default-src 'self'", "script-src 'self';", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("document CSP lacks %q: %s", directive, csp)
		}
	}
	for _, want := range []string{"img-src 'self' data: blob:;", "font-src 'self';", "style-src 'self' 'unsafe-inline';"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("document CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "http:") || strings.Contains(csp, "https:") || strings.Contains(csp, "//") || strings.Contains(csp, "*") {
		t.Fatalf("document CSP must name no third-party origin or scheme source: %s", csp)
	}
	scriptSrc := csp[strings.Index(csp, "script-src"):]
	scriptSrc = scriptSrc[:strings.Index(scriptSrc, ";")]
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "http", "*", "data:"} {
		if strings.Contains(scriptSrc, forbidden) {
			t.Fatalf("script-src must be 'self' only: %s", scriptSrc)
		}
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	baseline("asset", recorder.Header())
	if got := recorder.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("a script asset (incl. the service worker) must not carry the page CSP: %q", got)
	}

	api := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder = httptest.NewRecorder()
	api.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	baseline("api", recorder.Header())
	if got := recorder.Header().Get("Content-Security-Policy"); got != APIContentSecurityPolicy {
		t.Fatalf("api CSP = %q", got)
	}
}
