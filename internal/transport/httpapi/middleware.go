package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	accessdomain "bansoko/internal/accesscontrol/domain"
	observabilityport "bansoko/internal/observability/port"
)

type contextKey string

const (
	requestIDKey          contextKey = "httpapi.request_id"
	accountKey            contextKey = "httpapi.account"
	sessionTokenKey       contextKey = "httpapi.session_token"
	runtimeLoggerKey      contextKey = "httpapi.runtime_logger"
	diagnosticRecorderKey contextKey = "httpapi.diagnostic_recorder"
)

// requestSessionToken returns the credential this request authenticated with,
// whichever channel supplied it. Handlers that act on "the current session"
// (logout, account update, the session probe) use this instead of re-reading
// the Authorization header, which a cookie-authenticated request never sends.
func requestSessionToken(r *http.Request) string {
	value, _ := r.Context().Value(sessionTokenKey).(string)
	return value
}

// RequestIDFrom returns the synchronous request identifier. It is never an
// authorization input or an asynchronous trace identity.
func RequestIDFrom(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

// AccountFrom returns the authenticated owner for this request.
func AccountFrom(ctx context.Context) (accessdomain.Account, bool) {
	account, ok := ctx.Value(accountKey).(accessdomain.Account)
	return account, ok
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// publicAPIPaths are reachable without an established session. Every other
// /api/v1 path requires the bearer session declared in the OpenAPI contract.
var publicAPIPaths = map[string]bool{
	"GET /api/v1/livez":            true,
	"GET /api/v1/readyz":           true,
	"GET /api/v1/system/health":    true,
	"GET /api/v1/bootstrap/status": true,
	"POST /api/v1/bootstrap/owner": true,
	// Out-of-band owner recovery: anonymous by contract, but gated at the
	// handler to the boot-time consumed-signal window and blocked like any
	// product write while the maintenance gate rejects writes.
	"POST /api/v1/bootstrap/owner/recover": true,
	"POST /api/v1/auth/login":              true,
	// The single-use download ticket is the credential (see server_backup.go).
	"GET /api/v1/backup/download": true,
}

// readOnlyWritePaths are POST operations that produce no persistent effect and
// stay available during maintenance (previews, prechecks, connectivity tests,
// parse helpers, no-op reconciliation scan). Entries are regexes matched
// against the concrete request path; new side-effect-free POSTs must be added
// here with an explicit comment; any other POST is treated as a write.
var readOnlyWritePathPatterns = []struct {
	pattern string
	comment string
}{
	{`^/api/v1/anime/rss-list-view$`, "RSSListView: side-effect free read model"},
	{`^/api/v1/anime/subgroups-selection-preview$`, "group selection preview: decides and reads only"},
	{`^/api/v1/config/business-preset/load$`, "pure draft transformation, nothing stored"},
	{`^/api/v1/config/connections/[^/]+/test$`, "connectivity probe"},
	{`^/api/v1/config/network-routes/[^/]+/test$`, "connectivity probe"},
	{`^/api/v1/plugin-targets/[^/]+/test$`, "delivery probe"},
	{`^/api/v1/parse/release-title$`, "pure parse"},
	{`^/api/v1/parse/torrent-files$`, "pure parse"},
	{`^/api/v1/media-recognition/previews$`, "pure media recognition"},
	{`^/api/v1/template-previews/notification$`, "pure render"},
	{`^/api/v1/template-previews/naming$`, "pure render"},
	{`^/api/v1/naming-vars/dry-runs$`, "pure evaluation, nothing stored"},
	{`^/api/v1/file-manager/entry/match/preview$`, "file match preview: renders and reads only"},
}

// maintenanceRecoveryWritePaths stay available during maintenance because
// authentication recovery requires them. Login is how an operator proves
// identity to run recovery diagnostics; everything else product-write is
// rejected.
var maintenanceRecoveryWritePaths = map[string]bool{
	"POST /api/v1/auth/login": true,
}

// mcpPath is always allowed through the HTTP maintenance gate: the MCP
// transport itself carries no product effect. Every tool call is replayed as an
// ordinary in-process API request through this same chain, so the per-path
// rules here gate it: read-only tools stay available for recovery diagnostics
// while write tools fail closed with WRITES_REJECTED.
const mcpPath = "/api/v1/mcp"

func isReadOnlyPath(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	if path == mcpPath {
		return true
	}
	for _, entry := range readOnlyWritePathPatterns {
		if regexp.MustCompile(entry.pattern).MatchString(path) {
			return true
		}
	}
	if maintenanceRecoveryWritePaths[method+" "+path] {
		return true
	}
	return false
}

func normalizeRequestID(value string) string {
	if observabilityport.IsSafeRequestID(value) {
		return value
	}
	return uuid.NewString()
}

func (s *Server) withCommonMiddleware(next http.Handler) http.Handler {
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeKey := r.Method + " " + r.URL.Path
		if publicAPIPaths[routeKey] {
			// Public paths: login must stay available during maintenance for
			// auth recovery; everything else public is handled below.
			if isMaintenanceWriteBlocked(s, r) {
				writeError(w, r, maintenanceRejected())
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if s.deps.SkipAuth && s.deps.Environment != "production" {
			account := accessdomain.Account{ID: "development-skip-auth", Username: "development", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...), WebSession: true}
			if isMaintenanceWriteBlocked(s, r) {
				writeError(w, r, maintenanceRejected())
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountKey, account)))
			return
		}
		// The bearer header wins: API tokens, MCP and the CLI never depend on
		// a cookie, and a browser request carrying one is treated as a bearer
		// request (so no CSRF header is expected from script that set it).
		token := bearerToken(r)
		cookieAuthenticated := false
		if token == "" {
			token = sessionCookieToken(r)
			cookieAuthenticated = token != ""
		}
		if token == "" {
			writeError(w, r, unauthorized("SESSION_REQUIRED", "A session is required. Sign in, or send an API token as Authorization: Bearer."))
			return
		}
		// A credential the browser attaches by itself can be attached by
		// another site's page too, so unsafe methods must echo the CSRF value.
		if cookieAuthenticated && unsafeMethod(r.Method) && !csrfHeaderMatches(r) {
			writeError(w, r, forbiddenCSRF())
			return
		}
		account, err := s.deps.Access.AuthenticateSession(r.Context(), token)
		if err != nil {
			writeError(w, r, unauthorized("SESSION_INVALID", "The session is missing, expired, or revoked."))
			return
		}
		handler, scopeErr := s.authorizeScope(r, account, next)
		if scopeErr != nil {
			writeError(w, r, scopeErr)
			return
		}
		if isMaintenanceWriteBlocked(s, r) {
			writeError(w, r, maintenanceRejected())
			return
		}
		ctx := context.WithValue(r.Context(), accountKey, account)
		ctx = context.WithValue(ctx, sessionTokenKey, token)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	recovered := s.withPanicRecovery(authenticated)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := normalizeRequestID(r.Header.Get("X-Request-ID"))
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		logger := s.deps.RuntimeLogger
		if logger == nil {
			logger = defaultRuntimeLogger
		}
		ctx = context.WithValue(ctx, runtimeLoggerKey, logger)
		if s.deps.DiagnosticRecorder != nil {
			ctx = context.WithValue(ctx, diagnosticRecorderKey, s.deps.DiagnosticRecorder)
		}
		r = r.WithContext(ctx)
		recovered.ServeHTTP(w, r)
	})
}

const eventsStreamPath = "/api/v1/events/stream"
const imagePosterPathPrefix = "/api/v1/images/poster/"
const imageCoverPathPrefix = "/api/v1/images/cover/"

// Backup export streams a file of any size and restore reads one; neither may
// be held in the response buffer, and both extend their deadlines through the
// ResponseController, which needs the unwrapping writer.
const backupExportPath = "/api/v1/backup/export"
const backupRestorePath = "/api/v1/backup/restore"
const backupTicketPath = "/api/v1/backup/download-ticket"
const backupDownloadPath = "/api/v1/backup/download"

func isUnbufferedPath(path string) bool {
	return path == eventsStreamPath || path == mcpPath || path == backupExportPath || path == backupRestorePath ||
		path == backupTicketPath || path == backupDownloadPath ||
		strings.HasPrefix(path, imagePosterPathPrefix) || strings.HasPrefix(path, imageCoverPathPrefix)
}

// withPanicRecovery keeps ordinary generated API responses transactional until
// the handler returns. A panic therefore discards both a partial body and its
// headers before emitting the same generic unknown-500 response as writeError.
// Streamable HTTP, SSE, and poster responses are deliberately not buffered: the
// forwarding writer preserves Flush/ResponseController behavior and tracks
// whether a response has started, so a pre-write panic can still answer JSON
// while a post-write panic cannot corrupt an established response.
func (s *Server) withPanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnbufferedPath(r.URL.Path) {
			s.withStreamingPanicRecovery(next).ServeHTTP(w, r)
			return
		}

		buffered := &bufferedResponseWriter{
			header:    make(http.Header),
			method:    r.Method,
			requestID: RequestIDFrom(r.Context()),
		}
		defer func() {
			if recover() != nil {
				buffered.resetForError()
				writeError(buffered, r, errRequestPanicRecovered)
			}
			buffered.commit(w)
		}()
		next.ServeHTTP(buffered, r)
	})
}

func (s *Server) withStreamingPanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			tracking := &trackingResponseWriter{ResponseWriter: w, requestID: RequestIDFrom(r.Context())}
			defer func() {
				if recover() != nil {
					if !tracking.wroteHeader {
						writeError(tracking, r, errRequestPanicRecovered)
						return
					}
					recordUnknownRequestFailure(r, errRequestPanicRecovered)
				}
			}()
			next.ServeHTTP(tracking, r)
			return
		}
		streaming := &streamingResponseWriter{ResponseWriter: w, flusher: flusher, requestID: RequestIDFrom(r.Context())}
		defer func() {
			if recover() != nil {
				if !streaming.wroteHeader {
					writeError(streaming, r, errRequestPanicRecovered)
					return
				}
				// Once an SSE response has started, replacing it with JSON would
				// corrupt the stream. The failure still uses the same diagnostic
				// classification as an ordinary unknown HTTP error.
				recordUnknownRequestFailure(r, errRequestPanicRecovered)
			}
		}()
		next.ServeHTTP(streaming, r)
	})
}

type bufferedResponseWriter struct {
	header      http.Header
	body        bytes.Buffer
	bodyLen     int64
	status      int
	method      string
	requestID   string
	wroteHeader bool
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		return
	}
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if len(data) == 0 {
		return 0, nil
	}
	if responseStatusDisallowsBody(w.status) {
		return 0, http.ErrBodyNotAllowed
	}
	if w.method == http.MethodHead {
		w.bodyLen += int64(len(data))
		return len(data), nil
	}
	w.bodyLen += int64(len(data))
	return w.body.Write(data)
}

func (w *bufferedResponseWriter) resetForError() {
	w.header = make(http.Header)
	w.body.Reset()
	w.bodyLen = 0
	w.status = 0
	w.wroteHeader = false
}

func (w *bufferedResponseWriter) commit(dst http.ResponseWriter) {
	if !w.wroteHeader && len(w.header) == 0 && w.body.Len() == 0 && w.bodyLen == 0 {
		return
	}
	if w.requestID != "" {
		dst.Header().Set("X-Request-ID", w.requestID)
	}

	declaredTrailers := trailerNames(w.header)
	trailers := make(http.Header)
	for key, values := range w.header {
		// The outer request-ID middleware owns this header. A generated or
		// handwritten handler must not be able to replace the exact ID returned
		// to the caller and attached to the Runtime Log/diagnostic episode.
		if strings.EqualFold(key, "X-Request-ID") {
			continue
		}
		if strings.EqualFold(key, "Trailer") {
			continue
		}
		if trailerKey, ok := trailerPrefixName(key); ok {
			if trailerKey != "" {
				canonical := http.CanonicalHeaderKey(trailerKey)
				declaredTrailers[canonical] = struct{}{}
				trailers[canonical] = append([]string(nil), values...)
			}
			continue
		}
		canonical := http.CanonicalHeaderKey(key)
		if _, ok := declaredTrailers[canonical]; ok {
			trailers[canonical] = append([]string(nil), values...)
			continue
		}
		dst.Header()[key] = append([]string(nil), values...)
	}

	trailerKeys := make([]string, 0, len(declaredTrailers))
	for key := range declaredTrailers {
		if key != "" {
			trailerKeys = append(trailerKeys, key)
		}
	}
	sort.Strings(trailerKeys)
	if len(trailerKeys) > 0 {
		dst.Header().Set("Trailer", strings.Join(trailerKeys, ", "))
	}

	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	if w.method == http.MethodHead && w.bodyLen > 0 && dst.Header().Get("Content-Length") == "" && !responseStatusDisallowsBody(status) {
		dst.Header().Set("Content-Length", strconv.FormatInt(w.bodyLen, 10))
	}
	if responseStatusDisallowsBody(status) {
		dst.Header().Del("Content-Length")
	}
	dst.WriteHeader(status)
	for _, key := range trailerKeys {
		if values, ok := trailers[key]; ok {
			dst.Header()[key] = values
		}
	}
	if w.method != http.MethodHead && !responseStatusDisallowsBody(status) && w.body.Len() > 0 {
		_, _ = dst.Write(w.body.Bytes())
	}
}

func responseStatusDisallowsBody(status int) bool {
	return (status >= 100 && status < 200) || status == http.StatusNoContent || status == http.StatusNotModified
}

func trailerNames(header http.Header) map[string]struct{} {
	result := make(map[string]struct{})
	for _, line := range header.Values("Trailer") {
		for _, name := range strings.Split(line, ",") {
			name = http.CanonicalHeaderKey(strings.TrimSpace(name))
			if name != "" {
				result[name] = struct{}{}
			}
		}
	}
	return result
}

func trailerPrefixName(key string) (string, bool) {
	if len(key) < len(http.TrailerPrefix) || !strings.EqualFold(key[:len(http.TrailerPrefix)], http.TrailerPrefix) {
		return "", false
	}
	return key[len(http.TrailerPrefix):], true
}

func isInformationalStatus(status int) bool {
	return status >= 100 && status < 200 && status != http.StatusSwitchingProtocols
}

type streamingResponseWriter struct {
	http.ResponseWriter
	flusher     http.Flusher
	requestID   string
	wroteHeader bool
}

func (w *streamingResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if w.requestID != "" {
		w.ResponseWriter.Header().Set("X-Request-ID", w.requestID)
	}
	w.ResponseWriter.WriteHeader(status)
	if !isInformationalStatus(status) {
		w.wroteHeader = true
	}
}

func (w *streamingResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *streamingResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.flusher.Flush()
}

func (w *streamingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type trackingResponseWriter struct {
	http.ResponseWriter
	requestID   string
	wroteHeader bool
}

func (w *trackingResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if w.requestID != "" {
		w.ResponseWriter.Header().Set("X-Request-ID", w.requestID)
	}
	w.ResponseWriter.WriteHeader(status)
	if !isInformationalStatus(status) {
		w.wroteHeader = true
	}
}

func (w *trackingResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *trackingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// isMaintenanceWriteBlocked reports whether this request is a product write
// that must fail closed while the maintenance gate rejects writes. Read-only
// GET/HEAD, side-effect-free POSTs (previews, prechecks, tests, parses, scans)
// and the auth-recovery login stay available.
func isMaintenanceWriteBlocked(s *Server, r *http.Request) bool {
	if s.deps.MaintenanceGate == nil {
		return false
	}
	if !s.deps.MaintenanceGate.RejectWrites() {
		return false
	}
	return !isReadOnlyPath(r.Method, r.URL.Path)
}

func maintenanceRejected() error {
	return unavailable("WRITES_REJECTED", "The instance is in maintenance; product writes are paused.")
}

// frontendHandler serves the built frontend as static files. It owns no
// product behavior; the API is the only product boundary.
//
// There is deliberately no SPA history fallback. The frontend routes on the URL
// fragment (HashRouter), so every client route is served from index.html at "/"
// and the server never sees a path like /settings/connections. Adding a
// catch-all rewrite here would buy the frontend nothing and would turn every
// mistyped path — including an API path outside /api/v1 — into a 200 HTML
// response, hiding the 404 that callers need.
func frontendHandler(dir string) http.Handler {
	if strings.TrimSpace(dir) == "" {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		// index.html must revalidate on every load: it references bundles named by
		// content hash, and a heuristically cached stale HTML keeps the browser
		// loading old code (the classic "new feature seems missing" cause).
		// Hashed asset names are their own version, so their default caching is
		// fine.
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		setBaselineHeaders(w.Header())
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Content-Security-Policy", DocumentContentSecurityPolicy)
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Response security headers (SEC-033).
//
// DocumentContentSecurityPolicy governs the SPA document. No inline or
// third-party script runs (the theme bootstrap is /theme-init.js); the page
// cannot be framed, cannot change its base URL and loads no plugins. Styles
// allow 'unsafe-inline' because the UI libraries inject <style> elements
// (toasts, the code editor); styles cannot run script. Nothing is loaded from
// another origin: the web fonts ship with the bundle (font-src 'self'), and
// every cover or poster is a same-origin API path the page fetches with its
// session and shows through a blob: URL (img-src 'self' data: blob:), so the
// instance address never reaches a provider image host and the CSP names no
// third party. data: stays for small inline images only. The service worker
// script is a script asset and does not carry this header; it caches no
// third-party image (the runtime rules for provider image hosts are gone).
//
// APIContentSecurityPolicy is for JSON responses: nothing in them may load or
// be framed if a browser ever renders one as a document.
const (
	DocumentContentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"font-src 'self'; img-src 'self' data: blob:; " +
		"connect-src 'self'; worker-src 'self'; manifest-src 'self'; object-src 'none'; " +
		"base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
	APIContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	// PermissionsPolicy turns off the powerful browser features the UI never
	// uses. Clipboard stays at the browser default (copying a token is a
	// user-initiated write).
	PermissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), " +
		"serial=(), bluetooth=(), hid=(), midi=(), display-capture=()"
)

// setBaselineHeaders applies the headers every response carries: no MIME
// sniffing, no Referer sent to any other site (poster hosts and outbound links
// learn nothing about the instance address), no framing, and no powerful
// browser features.
func setBaselineHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Permissions-Policy", PermissionsPolicy)
}

// securityHeaders applies the baseline response headers for API responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setBaselineHeaders(w.Header())
		w.Header().Set("Content-Security-Policy", APIContentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}
