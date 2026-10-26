package httpapi

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/presetdraft"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/metadata/matcher"
	"bansoko/internal/namingvars"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	providerdomain "bansoko/internal/provider/domain"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

type fakeFetcher struct {
	result animeapplication.FeedResult
	err    error
	calls  []string
}

func (f *fakeFetcher) Fetch(_ context.Context, locatorURL string) (animeapplication.FeedResult, error) {
	f.calls = append(f.calls, locatorURL)
	return f.result, f.err
}

type fakeHTTPMetadataMatcher struct {
	result *matcher.MatchResult
}

func (f *fakeHTTPMetadataMatcher) Match(context.Context, string, string, string, bool, bool) (*matcher.MatchResult, error) {
	return f.result, nil
}

type testRig struct {
	db        *sql.DB
	handler   http.Handler
	access    *accessapplication.Service
	fetcher   *fakeFetcher
	anime     *animeapplication.Service
	ownership *ownershipapplication.Service
	catalog   *catalogapplication.Service
	readmodel *readmodelapplication.Service
	// deps is the dependency set the handler was built from, so a test can
	// add optional collaborators and rebuild the server with them.
	deps Dependencies
}

// rigOption mutates the transport dependencies before the server builds;
// tests that need optional composition wiring (e.g. the out-of-band owner
// recovery window) use it instead of a second rig constructor.
type rigOption func(*Dependencies)

func newTestRig(t *testing.T, environment string, skipAuth bool, options ...rigOption) *testRig {
	t.Helper()
	ensureTestNamingVars(t)
	db := disposabledb.ResetDisposableSchema(t)
	access := accessapplication.NewServiceWithSessionSecret(db, []byte("test-session-secret-0123456789abcdef"))
	workStore, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	// The deployment secret key is configured in this rig, so PAT raw values
	// are encrypted at rest and readable back (TestApiTokenRawRead). Tests that
	// pin the degraded mode disable the store instead.
	access.SetRawTokenStore(secrets)
	configService := configurationapplication.NewHTTPService(configurationapplication.NewService(db, secrets))
	fetcher := &fakeFetcher{result: animeapplication.FeedResult{
		Items: []acquisitiondomain.FeedItem{
			{GUID: "item-1", Title: "[TestGroup] API Show - 01 [1080p]", Enclosure: "https://feeds.example.com/01.torrent"},
		},
		Parse: acquisitiondomain.FeedParseResult{Format: acquisitiondomain.FeedFormatRSS},
	}}
	animeService, err := animeapplication.NewService(db, workStore, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: workStore})
	if err != nil {
		t.Fatal(err)
	}
	// The per-instance parser policy owner is wired exactly like the server
	// composition root; draft/candidate operations require it rather than
	// falling back to the bundled regex parser.
	draftService.SetFrozenParserPolicy(parserpolicy.NewService(db))
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	draftService.SetActivityWriter(observability)
	ownershipService, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: workStore})
	if err != nil {
		t.Fatal(err)
	}
	ownershipService.SetOneShotMarkerRepository(animepersistence.NewRepository(db))
	ownershipService.SetActivityWriter(observability)
	ownershipService.SetOffsetMigrationAdmission(filemaintenance.NewOffsetMigrationAdmissionService())
	retryService, err := animeapplication.NewManualRetryService(db, workStore)
	if err != nil {
		t.Fatal(err)
	}
	importRetry, err := animeapplication.NewImportRetryService(db)
	if err != nil {
		t.Fatal(err)
	}
	rssService, err := rssapplication.NewService(db, workStore, emptyRSSFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalogapplication.NewService(db, nil)
	readmodelService, err := readmodelapplication.NewService(db, readmodelapplication.Dependencies{
		Config: animeService, Owners: ownershipService, Revisions: draftService,
		SeedPreviews: animeapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload),
		MediaFacts:   facts.NewStoreWithRepository(mediapersistence.NewRepository(db)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Keep this test composition aligned with cmd/bansoko-server/provideReadmodelService;
	// update it there too whenever the production collaborator set changes.
	deps := Dependencies{
		DB: db, Access: access, Draft: draftService, Ownership: ownershipService, Readmodel: readmodelService, ReleaseRetry: rssService, Connections: configService, ManualRetry: retryService, ImportRetry: importRetry,
		Environment: environment, SkipAuth: skipAuth,
	}
	for _, apply := range options {
		apply(&deps)
	}
	server, err := NewServer(deps)
	if err != nil {
		t.Fatal(err)
	}
	return &testRig{db: db, handler: server.Handler(), access: access, fetcher: fetcher, anime: animeService, ownership: ownershipService, catalog: catalogService, readmodel: readmodelService, deps: deps}
}

func (r *testRig) do(t *testing.T, method, path string, body any, headers map[string]string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r.seedBusinessBody(t, body)
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	r.handler.ServeHTTP(recorder, request)
	return recorder, recorder.Body.Bytes()
}

func decodeBody[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode response %s: %v", string(raw), err)
	}
	return value
}

func assertSafeRSSSourceJSON(t *testing.T, raw []byte) {
	t.Helper()
	for _, forbidden := range [][]byte{
		[]byte("https://user:secret@example.test/feed?token=rawsecret"),
		[]byte("rawsecret"),
		[]byte(`"locator"`),
	} {
		if bytes.Contains(raw, forbidden) {
			t.Fatalf("ordinary response leaked protected RSS locator %q: %s", forbidden, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"locator_display"`)) {
		t.Fatalf("ordinary RSS source response omitted locator_display: %s", raw)
	}
}

// defaultRulesPayload is the rules value of a draft created from a never-saved
// Global: every section carries its complete code-default copy.
func defaultRulesPayload() map[string]any {
	rules, err := presetdraft.DefaultRulesValue()
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		panic(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		panic(err)
	}
	return value
}

func draftPayload() map[string]any {
	return map[string]any{
		"draft": map[string]any{
			"origin":         "custom",
			"instance_label": "api-instance",
			"title":          "API Show",
			"metadata": map[string]any{
				"bgm":  map[string]any{"work_id": "bgm-1"},
				"tmdb": map[string]any{"work_id": "tmdb-1", "media_kind": "tv", "season": 1},
			},
			"subgroups": map[string]any{"max_rss": 100, "entries": []any{}},
			"rules":     defaultRulesPayload(),
			"rss_sources": []any{
				map[string]any{"source_kind": "custom_rss", "locator": "https://user:secret@example.test/feed?token=rawsecret", "group_name": "TestGroup", "title": "API Show"},
			},
		},
	}
}

// withStart returns the payload for POST /anime/subscribe that starts the new
// subscription at once; candidate saves take the bare draftPayload.
func withStart(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		out[key] = value
	}
	out["start"] = true
	return out
}

// ensureTestNamingVars installs an explicitly loaded, empty snapshot for HTTP
// tests whose naming rules do not reference vars. Tests of populated vars must
// install their own snapshot; an uninitialized registry must remain an error.
func ensureTestNamingVars(t *testing.T) {
	t.Helper()
	vars := namingvars.Vars{}
	fingerprint, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatalf("fingerprint naming vars: %v", err)
	}
	if err := namingvars.SetActive(vars, fingerprint); err != nil {
		t.Fatalf("activate naming vars: %v", err)
	}
}

func TestBootstrapLoginAndSessionLifecycle(t *testing.T) {
	rig := newTestRig(t, "development", false)

	_, statusBody := rig.do(t, http.MethodGet, "/api/v1/bootstrap/status", nil, nil)
	status := decodeBody[generated.BootstrapStatus](t, statusBody)
	if status.State != "bootstrap_required" {
		t.Fatalf("fresh instance must require bootstrap: %+v", status)
	}

	// The first visit directly creates the owner account; no grant or setup
	// session is involved. The endpoint is anonymous.
	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	if ownerResult.Owner.Username != "owner" || ownerResult.SessionToken == nil || *ownerResult.SessionToken == "" {
		t.Fatalf("owner session must be returned: %+v", ownerResult)
	}

	// The instance now reports owner_exists and rejects a second owner creation.
	_, statusBody = rig.do(t, http.MethodGet, "/api/v1/bootstrap/status", nil, nil)
	status = decodeBody[generated.BootstrapStatus](t, statusBody)
	if status.State != "owner_exists" {
		t.Fatalf("instance must report owner_exists: %+v", status)
	}
	recorder, _ = rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "other", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("second owner creation must be rejected with 409, got %d", recorder.Code)
	}

	// Login returns a session; the session authenticates requests.
	recorder, loginBody := rig.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", recorder.Code, string(loginBody))
	}
	login := decodeBody[generated.LoginResult](t, loginBody)
	authHeaders := map[string]string{"Authorization": "Bearer " + *login.SessionToken}
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, authHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated list failed: %d", recorder.Code)
	}

	// Logout revokes the session.
	recorder, _ = rig.do(t, http.MethodPost, "/api/v1/auth/logout", nil, authHeaders)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("logout failed: %d", recorder.Code)
	}
	recorder, body := rig.do(t, http.MethodGet, "/api/v1/anime", nil, authHeaders)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session must be rejected: %d %s", recorder.Code, string(body))
	}
}

// TestApiTokenLifecycle covers the long-lived PAT channel: create echoes the raw
// token once, the token authenticates requests, list shows metadata, and revoke
// invalidates it. Rotation (revoke old + create new) is the same two operations.
func TestApiTokenLifecycle(t *testing.T) {
	rig := newTestRig(t, "development", false)
	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	authHeaders := map[string]string{"Authorization": "Bearer " + *ownerResult.SessionToken}

	// Create: the raw token is echoed exactly once.
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens",
		map[string]string{"name": "agent-bot"}, authHeaders)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create api token failed: %d %s", recorder.Code, string(body))
	}
	created := decodeBody[generated.ApiTokenCreateResult](t, body)
	if len(created.Token) < 32 || created.TokenId == "" {
		t.Fatalf("raw token must be echoed once: %+v", created)
	}

	// The PAT authenticates requests on its own (no session header).
	patHeaders := map[string]string{"Authorization": "Bearer " + created.Token}
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime", nil, patHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PAT-authenticated list failed: %d %s", recorder.Code, string(body))
	}

	// List shows metadata only; the raw token value never appears.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens", nil, authHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list api tokens failed: %d %s", recorder.Code, string(body))
	}
	tokens := decodeBody[[]generated.ApiToken](t, body)
	if len(tokens) != 1 || tokens[0].Name != "agent-bot" || tokens[0].Id != created.TokenId {
		t.Fatalf("unexpected api token list: %+v", tokens)
	}
	if bytes.Contains(body, []byte(created.Token)) {
		t.Fatalf("api token list leaked the raw token value")
	}

	// Revoke invalidates the token and deletes the row (no tombstone).
	recorder, body = rig.do(t, http.MethodDelete, "/api/v1/api-tokens/"+created.TokenId, nil, authHeaders)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("revoke api token failed: %d %s", recorder.Code, string(body))
	}
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime", nil, patHeaders)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked PAT must be rejected: %d %s", recorder.Code, string(body))
	}
	// The revoked token no longer appears in the list (hard delete).
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens", nil, authHeaders)
	tokens = decodeBody[[]generated.ApiToken](t, body)
	if len(tokens) != 0 {
		t.Fatalf("revoked token must be deleted from the list, got %+v", tokens)
	}
	// Revoke again stays a no-op (idempotent deletion).
	recorder, _ = rig.do(t, http.MethodDelete, "/api/v1/api-tokens/"+created.TokenId, nil, authHeaders)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("repeat revoke must be idempotent 204, got %d", recorder.Code)
	}
}

// TestApiTokenRequiresAuth asserts PAT management itself requires an authenticated
// request and that a valid name is required.
func TestApiTokenRequiresAuth(t *testing.T) {
	rig := newTestRig(t, "development", false)
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens", map[string]string{"name": "x"}, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous api-token create must be 401, got %d %s", recorder.Code, string(body))
	}
}

// TestApiTokenRotate covers the atomic rotate endpoint: the new raw token is
// echoed once, the old token is immediately invalid (401), the new token
// authenticates, and the list shows exactly one active token (rotation cannot
// leave two active).
func TestApiTokenRotate(t *testing.T) {
	rig := newTestRig(t, "development", false)
	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	authHeaders := map[string]string{"Authorization": "Bearer " + *ownerResult.SessionToken}

	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens",
		map[string]string{"name": "agent-bot"}, authHeaders)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create api token failed: %d %s", recorder.Code, string(body))
	}
	created := decodeBody[generated.ApiTokenCreateResult](t, body)
	oldToken := created.Token
	oldHeaders := map[string]string{"Authorization": "Bearer " + oldToken}

	// The old token authenticates before rotation.
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, oldHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pre-rotate PAT must work: %d", recorder.Code)
	}

	// Rotate: new raw echoed once, old token invalid immediately.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/api-tokens/"+created.TokenId, nil, authHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotate api token failed: %d %s", recorder.Code, string(body))
	}
	rotated := decodeBody[generated.ApiTokenCreateResult](t, body)
	if rotated.Token == oldToken || len(rotated.Token) < 32 {
		t.Fatalf("rotate must return a fresh raw token: %+v", rotated)
	}
	newHeaders := map[string]string{"Authorization": "Bearer " + rotated.Token}

	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, oldHeaders)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("rotated-away PAT must be rejected: %d", recorder.Code)
	}
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, newHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotated PAT must authenticate: %d", recorder.Code)
	}

	// Exactly one active token remains (the rotated one), and the old row is
	// deleted (no tombstone).
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens", nil, authHeaders)
	tokens := decodeBody[[]generated.ApiToken](t, body)
	if len(tokens) != 1 || tokens[0].Id != rotated.TokenId {
		t.Fatalf("rotation must leave exactly the new token row (old row hard-deleted), got %+v", tokens)
	}

	// Rotating the already-rotated (now old) token id is a no-op 404.
	recorder, _ = rig.do(t, http.MethodPost, "/api/v1/api-tokens/"+created.TokenId, nil, authHeaders)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("rotating an already-rotated token must be 404, got %d", recorder.Code)
	}
}

// TestApiTokenRawRead covers the long-term raw display channel: the raw value
// encrypted at rest can be re-read on demand for the token's lifetime, the
// create response matches the raw endpoint, rotation serves the new value and
// drops the old one, and revoke makes the raw read 404 together with the row.
func TestApiTokenRawRead(t *testing.T) {
	rig := newTestRig(t, "development", false)
	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	authHeaders := map[string]string{"Authorization": "Bearer " + *ownerResult.SessionToken}

	// Create then read the raw value back on demand (not just once at create).
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens",
		map[string]string{"name": "long-lived"}, authHeaders)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create api token failed: %d %s", recorder.Code, string(body))
	}
	created := decodeBody[generated.ApiTokenCreateResult](t, body)

	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+created.TokenId+"/raw", nil, authHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read raw api token failed: %d %s", recorder.Code, string(body))
	}
	raw := decodeBody[generated.ApiTokenRaw](t, body)
	if raw.Token != created.Token {
		t.Fatalf("raw read must match the create echo: got %q want %q", raw.Token, created.Token)
	}

	// The raw endpoint is authenticated: anonymous reads are rejected.
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+created.TokenId+"/raw", nil, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous raw read must be 401, got %d", recorder.Code)
	}

	// Rotate: the new id serves the new raw value; the old id is gone (404).
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/api-tokens/"+created.TokenId, nil, authHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotate api token failed: %d %s", recorder.Code, string(body))
	}
	rotated := decodeBody[generated.ApiTokenCreateResult](t, body)
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+rotated.TokenId+"/raw", nil, authHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read rotated raw failed: %d %s", recorder.Code, string(body))
	}
	raw = decodeBody[generated.ApiTokenRaw](t, body)
	if raw.Token != rotated.Token {
		t.Fatalf("rotated raw read must match: got %q want %q", raw.Token, rotated.Token)
	}
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+created.TokenId+"/raw", nil, authHeaders)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("old token raw read after rotate must be 404, got %d", recorder.Code)
	}

	// Revoke: the raw read 404s together with the deleted row.
	recorder, _ = rig.do(t, http.MethodDelete, "/api/v1/api-tokens/"+rotated.TokenId, nil, authHeaders)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("revoke after rotate failed: %d", recorder.Code)
	}
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+rotated.TokenId+"/raw", nil, authHeaders)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("raw read after revoke must be 404, got %d", recorder.Code)
	}

	// Unknown ids are 404, never 500.
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/api-tokens/no-such-token/raw", nil, authHeaders)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown token raw read must be 404, got %d", recorder.Code)
	}
}

// TestApiTokenRawUnavailable pins the degraded mode: without the deployment
// secret key the server stores no raw value, so the raw endpoint returns 404
// while list/create/authentication all still work.
func TestApiTokenRawUnavailable(t *testing.T) {
	rig := newTestRig(t, "development", false)
	// Simulate an instance without BANSOKO_SECRET_ENCRYPTION_KEY: no raw store, so
	// raw values are neither stored nor readable; auth keeps working.
	rig.access.SetRawTokenStore(nil)
	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	authHeaders := map[string]string{"Authorization": "Bearer " + *ownerResult.SessionToken}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/api-tokens",
		map[string]string{"name": "no-key"}, authHeaders)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create api token without key failed: %d %s", recorder.Code, string(body))
	}
	created := decodeBody[generated.ApiTokenCreateResult](t, body)
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/api-tokens/"+created.TokenId+"/raw", nil, authHeaders)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("raw read without deployment key must be 404, got %d %s", recorder.Code, string(body))
	}
	// Authentication still works in degraded mode.
	patHeaders := map[string]string{"Authorization": "Bearer " + created.Token}
	recorder, _ = rig.do(t, http.MethodGet, "/api/v1/anime", nil, patHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PAT must still authenticate without deployment key: %d", recorder.Code)
	}
}

// TestMCPServerMountRequiresAuth pins the MCP endpoint behind the same bearer
// middleware as every other /api/v1 path: an anonymous initialize handshake is
// rejected, while the same handshake with a bearer token succeeds. The MCP
// server is intentionally not in publicAPIPaths.
func TestMCPServerMountRequiresAuth(t *testing.T) {
	rig := newTestRig(t, "development", false)
	handshake := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-test","version":"1.0.0"}}}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader([]byte(handshake)))
	req.Header.Set("Content-Type", "application/json")
	anonRecorder := httptest.NewRecorder()
	rig.handler.ServeHTTP(anonRecorder, req)
	if anonRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous MCP handshake must be 401, got %d %s", anonRecorder.Code, anonRecorder.Body.String())
	}

	// A valid session lets the handshake through; the server responds with the
	// MCP initialize result rather than an auth error.
	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	authReq := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader([]byte(handshake)))
	authReq.Header.Set("Content-Type", "application/json")
	authReq.Header.Set("Authorization", "Bearer "+*ownerResult.SessionToken)
	authRecorder := httptest.NewRecorder()
	rig.handler.ServeHTTP(authRecorder, authReq)
	if authRecorder.Code != http.StatusOK {
		t.Fatalf("authenticated MCP handshake must be 200, got %d %s", authRecorder.Code, authRecorder.Body.String())
	}
	if !bytes.Contains(authRecorder.Body.Bytes(), []byte(`"result"`)) {
		t.Fatalf("MCP handshake must return an initialize result: %s", authRecorder.Body.String())
	}
}

func TestAuthenticationRequiredAndErrorShape(t *testing.T) {
	rig := newTestRig(t, "development", false)
	recorder, body := rig.do(t, http.MethodGet, "/api/v1/anime", nil, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing session must be 401, got %d", recorder.Code)
	}
	failure := decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "SESSION_REQUIRED" || failure.RequestId == nil || *failure.RequestId == "" {
		t.Fatalf("typed error with request id required: %+v", failure)
	}
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("request id header must be set")
	}
	// A client-supplied request ID is echoed.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime", nil, map[string]string{"X-Request-ID": "client-req-1"})
	if recorder.Header().Get("X-Request-ID") != "client-req-1" {
		t.Fatalf("request id must be echoed, got %s", recorder.Header().Get("X-Request-ID"))
	}
	failure = decodeBody[generated.ErrorResponse](t, body)
	if failure.RequestId == nil || *failure.RequestId != "client-req-1" {
		t.Fatalf("error must carry the client request id: %+v", failure)
	}
}

func TestSubscribePreviewWorkspaceOverHTTP(t *testing.T) {
	ensureTestAttrRules(t)
	rig := newTestRig(t, "development", true)

	// Missing idempotency key is rejected at the generated parameter boundary.
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key must be 400, got %d %s", recorder.Code, string(body))
	}
	failure := decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "REQUEST_INVALID" || failure.RequestId == nil {
		t.Fatalf("missing parameter must be a typed 400 with request id: %+v", failure)
	}
	// A too-short key is rejected by the application boundary.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), map[string]string{"Idempotency-Key": "short"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("short idempotency key must be 400, got %d %s", recorder.Code, string(body))
	}
	failure = decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("unexpected error code: %+v", failure)
	}

	// Subscribe creates the owner set and replays idempotently.
	headers := map[string]string{"Idempotency-Key": "http-subscribe-0001"}
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), headers)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("subscribe failed: %d %s", recorder.Code, string(body))
	}
	assertSafeRSSSourceJSON(t, body)
	subscribed := decodeBody[generated.SubscribeResult](t, body)
	if subscribed.ManagedAnime.Id == "" || len(subscribed.RssSources) != 1 || subscribed.RssSources[0].LocatorDisplay == "" {
		t.Fatalf("unexpected subscribe result: %+v", subscribed)
	}
	recorder, _ = rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), headers)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("replay must succeed: %d", recorder.Code)
	}
	changed := draftPayload()
	changed["draft"].(map[string]any)["title"] = "Other Show"
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(changed), headers)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("key reuse with a different body must be 409, got %d %s", recorder.Code, string(body))
	}
	failure = decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("unexpected conflict code: %+v", failure)
	}

	if _, err := rig.db.ExecContext(context.Background(), `
INSERT INTO subscription_log_entries(
    id, subscription_id, managed_anime_id, kind, outcome, occurred_at,
    title, summary, rss_source_id, dedupe_key, retention_until
)
VALUES ('http-log-1', $1, $2, 'subscription_created', 'info', $3::timestamptz,
        'Subscription created', 'Created from the custom RSS draft', $4,
        'http-log-dedupe-1', $3::timestamptz + INTERVAL '30 days')`,
		subscribed.Subscription.Id, subscribed.ManagedAnime.Id, time.Now().UTC(), subscribed.RssSources[0].Id); err != nil {
		t.Fatalf("insert subscription log: %v", err)
	}
	logCountBefore := countRows(t, rig.db, "subscription_log_entries")

	// The workspace read model returns the owner chain and bounded business log.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/workspace", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("workspace failed: %d %s", recorder.Code, string(body))
	}
	assertSafeRSSSourceJSON(t, body)
	workspace := decodeBody[generated.AnimeWorkspace](t, body)
	if workspace.ManagedAnime.Id != subscribed.ManagedAnime.Id || len(workspace.RssSources) != 1 || workspace.Rules.Effective.Naming.Tv.VideoFilenameTemplate == "" {
		t.Fatalf("unexpected workspace: %+v", workspace)
	}
	if len(workspace.Logs) != 2 || workspace.Logs[0].Id != "http-log-1" || workspace.Logs[0].Kind != generated.SubscriptionLogKindSubscriptionCreated || workspace.Logs[1].Kind != generated.SubscriptionLogKindSubscriptionCreated {
		t.Fatalf("workspace must include both typed subscription logs: %+v", workspace.Logs)
	}
	if logCountAfter := countRows(t, rig.db, "subscription_log_entries"); logCountAfter != logCountBefore {
		t.Fatalf("workspace GET must not write subscription logs: before=%d after=%d", logCountBefore, logCountAfter)
	}

	// Metadata PATCH with CAS.
	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/metadata",
		map[string]any{"expected_version": 99, "bgm": nil, "tmdb": nil}, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale CAS must be 409, got %d %s", recorder.Code, string(body))
	}
	failure = decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "OWNER_VERSION_CONFLICT" || failure.CurrentVersion == nil || *failure.CurrentVersion != 1 {
		t.Fatalf("conflict must carry the current version: %+v", failure)
	}
	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/metadata",
		map[string]any{"expected_version": 1, "bgm": map[string]any{"work_id": "bgm-2"}, "tmdb": map[string]any{"work_id": "tmdb-2", "media_kind": "tv", "season": 2}}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata patch failed: %d %s", recorder.Code, string(body))
	}
	metadata := decodeBody[generated.AnimeMetadata](t, body)
	if metadata.Version != 2 || metadata.Bgm == nil || metadata.Tmdb == nil {
		t.Fatalf("metadata must be replaced: %+v", metadata)
	}

	// The one-click action accepts only expected_version; caller-supplied IDs are rejected.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/metadata/match",
		map[string]any{"expected_version": 2, "bgm": map[string]any{"work_id": "caller-id"}}, map[string]string{"Idempotency-Key": "http-match-reject-0001"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("metadata match must reject caller-supplied IDs, got %d %s", recorder.Code, string(body))
	}

	bgmID := "matched-bgm"
	tmdbID := "matched-tmdb"
	kind := "tv"
	season := 3
	rig.ownership.SetMetadataMatcher(&fakeHTTPMetadataMatcher{result: &matcher.MatchResult{
		BgmID: &bgmID, TmdbWorkID: &tmdbID, TmdbKind: &kind, TmdbSeason: &season,
	}})
	// Clear the existing owner through the ordinary CAS endpoint, then match it.
	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/metadata",
		map[string]any{"expected_version": 2, "bgm": nil, "tmdb": nil}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata clear failed: %d %s", recorder.Code, string(body))
	}
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/metadata/match",
		map[string]any{"expected_version": 3}, map[string]string{"Idempotency-Key": "http-match-clear-0001"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata match failed: %d %s", recorder.Code, string(body))
	}
	matched := decodeBody[generated.MetadataMatchResult](t, body)
	if matched.Outcome != generated.MetadataMatchOutcomeComplete || matched.Metadata.Version != 4 || matched.Metadata.Bgm == nil || matched.Metadata.Bgm.WorkId != bgmID {
		t.Fatalf("unexpected metadata match response: %+v", matched)
	}
}

func TestReevaluateAnimeReleasesOverHTTPIsIdempotentAndSkipsSubmitted(t *testing.T) {
	rig := newTestRig(t, "development", true)
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), map[string]string{"Idempotency-Key": "http-reevaluate-0001"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("subscribe failed: %d %s", recorder.Code, string(body))
	}
	subscribed := decodeBody[generated.SubscribeResult](t, body)
	sourceID := subscribed.RssSources[0].Id
	ctx := context.Background()
	if _, err := rig.db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES
    ('http-reevaluate-pending', $1, 'http-reevaluate-pending', '{}', '{}', 'discovered', 'discovered'),
    ('http-reevaluate-submitted', $1, 'http-reevaluate-submitted', '{}', '{}', 'discovered', 'discovered')`, sourceID); err != nil {
		t.Fatalf("seed releases: %v", err)
	}
	acquisitionfixture.SeedObservedDownload(t, disposabledb.Owner(t), "http-reevaluate-root", "http-reevaluate-task", "http-reevaluate-submitted", "http-reevaluate-task-key", `{}`, `{}`, "queued", `null`)
	if _, err := rig.db.ExecContext(ctx, `
UPDATE releases
SET download_task_id = 'http-reevaluate-task', verdict = 'submitted', reason_code = 'submitted'
WHERE id = 'http-reevaluate-submitted'`); err != nil {
		t.Fatalf("mark submitted release: %v", err)
	}

	path := "/api/v1/anime/" + subscribed.ManagedAnime.Id + "/releases/reevaluate"
	recorder, body = rig.do(t, http.MethodPost, path, nil, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("reevaluate failed: %d %s", recorder.Code, string(body))
	}
	result := decodeBody[generated.ReleaseReevaluationResult](t, body)
	if result.ManagedAnimeId != subscribed.ManagedAnime.Id || result.Accepted != generated.ReleaseReevaluationResultAccepted(true) || result.ScheduledCount != 1 {
		t.Fatalf("unexpected reevaluation result: %+v", result)
	}

	var pendingCount, submittedCount int
	if err := rig.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'http-reevaluate-pending' AND kind = 'release_evaluate' AND idempotency_key = 'release_evaluate:http-reevaluate-pending'`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'http-reevaluate-submitted' AND kind = 'release_evaluate'`).Scan(&submittedCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 1 || submittedCount != 0 {
		t.Fatalf("reevaluation work items pending=%d submitted=%d, want 1/0", pendingCount, submittedCount)
	}

	recorder, body = rig.do(t, http.MethodPost, path, nil, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("repeat reevaluate failed: %d %s", recorder.Code, string(body))
	}
	result = decodeBody[generated.ReleaseReevaluationResult](t, body)
	if result.ScheduledCount != 0 {
		t.Fatalf("repeat reevaluation must merge the live work item: %+v", result)
	}
	if err := rig.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'http-reevaluate-pending' AND kind = 'release_evaluate'`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 1 {
		t.Fatalf("repeat reevaluation created duplicate work, got %d", pendingCount)
	}
}

func TestMalformedOpaqueLocatorNonDisclosureOverHTTP(t *testing.T) {
	rig := newTestRig(t, "development", true)
	const (
		rawLocator   = "opaque://feed/%zz/DO_NOT_DISCLOSE_SECRET_MARKER"
		secretMarker = "DO_NOT_DISCLOSE_SECRET_MARKER"
		safeDisplay  = "invalid locator"
	)

	payload := draftPayload()
	sources := payload["draft"].(map[string]any)["rss_sources"].([]any)
	sources[0].(map[string]any)["locator"] = rawLocator
	assertSafeResponse := func(surface string, body []byte) {
		t.Helper()
		if bytes.Contains(body, []byte(rawLocator)) || bytes.Contains(body, []byte(secretMarker)) || bytes.Contains(body, []byte(`"locator"`)) {
			t.Fatalf("%s leaked malformed protected locator: %s", surface, body)
		}
		if !bytes.Contains(body, []byte(`"locator_display":"`+safeDisplay+`"`)) {
			t.Fatalf("%s omitted fixed safe locator display: %s", surface, body)
		}
	}
	assertSafeError := func(surface string, body []byte) generated.ErrorResponse {
		t.Helper()
		if bytes.Contains(body, []byte(rawLocator)) || bytes.Contains(body, []byte(secretMarker)) {
			t.Fatalf("%s leaked malformed protected locator: %s", surface, body)
		}
		return decodeBody[generated.ErrorResponse](t, body)
	}

	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(payload),
		map[string]string{"Idempotency-Key": "malformed-direct-0001"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("direct subscribe failed: %d %s", recorder.Code, body)
	}
	assertSafeResponse("direct SubscribeResult", body)
	direct := decodeBody[generated.SubscribeResult](t, body)

	var storedLocator, storedDisplay string
	if err := rig.db.QueryRowContext(context.Background(), `
SELECT locator, locator_display FROM rss_sources WHERE id = $1`, direct.RssSources[0].Id).Scan(&storedLocator, &storedDisplay); err != nil {
		t.Fatal(err)
	}
	if storedLocator != rawLocator || storedDisplay != safeDisplay {
		t.Fatalf("direct subscribe storage mismatch: locator=%q display=%q", storedLocator, storedDisplay)
	}

	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime/"+direct.ManagedAnime.Id+"/workspace", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("workspace GET failed: %d %s", recorder.Code, body)
	}
	assertSafeResponse("workspace GET", body)

	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/anime/"+direct.ManagedAnime.Id+"/subgroups",
		map[string]any{
			"expected_version": 1,
			"subgroups":        map[string]any{"max_rss": 50, "entries": []any{}},
			"group_actions":    []any{},
		}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("subgroups PATCH failed: %d %s", recorder.Code, body)
	}
	assertSafeResponse("subgroups PATCH mutation response", body)

	recorder, body = rig.do(t, http.MethodPatch,
		"/api/v1/anime/"+direct.ManagedAnime.Id+"/rss-sources/"+direct.RssSources[0].Id+"/offset",
		map[string]any{"expected_version": 1, "episode_offset": 2}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("offset PATCH failed: %d %s", recorder.Code, body)
	}
	assertSafeResponse("offset PATCH mutation response", body)

	recorder, body = rig.do(t, http.MethodPatch,
		"/api/v1/anime/"+direct.ManagedAnime.Id+"/rss-sources/"+direct.RssSources[0].Id+"/offset",
		map[string]any{"expected_version": 1, "episode_offset": 3}, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale offset PATCH must be 409: %d %s", recorder.Code, body)
	}
	failure := assertSafeError("stale offset PATCH error", body)
	if failure.Code != "OWNER_VERSION_CONFLICT" || failure.CurrentVersion == nil || *failure.CurrentVersion != 2 {
		t.Fatalf("unexpected stale offset error: %+v", failure)
	}

	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", payload, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("candidate save failed: %d %s", recorder.Code, body)
	}
	if !bytes.Contains(body, []byte(rawLocator)) || !bytes.Contains(body, []byte(secretMarker)) {
		t.Fatalf("candidate save must preserve the opaque draft locator: %s", body)
	}
	candidate := decodeBody[generated.AnimeConfigCandidate](t, body)
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no immutable revision")
	}
	activationPayload := map[string]any{"revision": *candidate.CurrentRevision}

	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime-config-candidates/"+candidate.Id, nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("candidate GET failed: %d %s", recorder.Code, body)
	}
	if !bytes.Contains(body, []byte(rawLocator)) || !bytes.Contains(body, []byte(secretMarker)) {
		t.Fatalf("candidate GET must preserve the opaque draft locator: %s", body)
	}

	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", activationPayload,
		map[string]string{"Idempotency-Key": "malformed-candidate-0001", "If-Match": "1"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("candidate activation failed: %d %s", recorder.Code, body)
	}
	assertSafeResponse("candidate activation SubscribeResult", body)
	activated := decodeBody[generated.SubscribeResult](t, body)
	if err := rig.db.QueryRowContext(context.Background(), `
SELECT locator, locator_display FROM rss_sources WHERE id = $1`, activated.RssSources[0].Id).Scan(&storedLocator, &storedDisplay); err != nil {
		t.Fatal(err)
	}
	if storedLocator != rawLocator || storedDisplay != safeDisplay {
		t.Fatalf("candidate activation storage mismatch: locator=%q display=%q", storedLocator, storedDisplay)
	}

	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", activationPayload,
		map[string]string{"Idempotency-Key": "malformed-candidate-0001", "If-Match": "1"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("candidate activation replay failed: %d %s", recorder.Code, body)
	}
	assertSafeResponse("candidate activation replay", body)

	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", activationPayload,
		map[string]string{"Idempotency-Key": "malformed-candidate-new-key", "If-Match": "1"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("new key on activated candidate must be 409: %d %s", recorder.Code, body)
	}
	failure = assertSafeError("already activated candidate error", body)
	if failure.Code != "CANDIDATE_ALREADY_ACTIVATED" {
		t.Fatalf("unexpected already activated error: %+v", failure)
	}

	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", payload, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("second candidate save failed: %d %s", recorder.Code, body)
	}
	secondCandidate := decodeBody[generated.AnimeConfigCandidate](t, body)
	if secondCandidate.CurrentRevision == nil {
		t.Fatal("second saved candidate has no immutable revision")
	}
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates/"+secondCandidate.Id+"/subscribe", map[string]any{"revision": *secondCandidate.CurrentRevision},
		map[string]string{"Idempotency-Key": "malformed-candidate-0001", "If-Match": "1"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("same key on another candidate must be 409: %d %s", recorder.Code, body)
	}
	failure = assertSafeError("candidate idempotency reuse error", body)
	if failure.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("unexpected candidate key reuse error: %+v", failure)
	}
}

func TestLegacyLocatorDisplayIsSanitizedAtHTTPOutputBoundaries(t *testing.T) {
	rig := newTestRig(t, "development", true)
	ctx := context.Background()
	const (
		legacyDisplay = "https://user:pass@example.test/private-passkey?foo=secret#fragment"
		safeDisplay   = "https://example.test"
	)

	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()),
		map[string]string{"Idempotency-Key": "legacy-display-http"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("subscribe failed: %d %s", recorder.Code, body)
	}
	subscribed := decodeBody[generated.SubscribeResult](t, body)
	if _, err := rig.db.ExecContext(ctx, `UPDATE rss_sources SET locator_display = $2 WHERE id = $1`, subscribed.RssSources[0].Id, legacyDisplay); err != nil {
		t.Fatal(err)
	}

	assertSanitized := func(surface string, body []byte) {
		t.Helper()
		for _, forbidden := range [][]byte{[]byte("user:pass"), []byte("private-passkey"), []byte("foo=secret"), []byte("fragment")} {
			if bytes.Contains(body, forbidden) {
				t.Fatalf("%s leaked legacy display component %q: %s", surface, forbidden, body)
			}
		}
		if !bytes.Contains(body, []byte(`"locator_display":"`+safeDisplay+`"`)) {
			t.Fatalf("%s omitted sanitized display: %s", surface, body)
		}
	}

	recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/workspace", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("workspace failed: %d %s", recorder.Code, body)
	}
	assertSanitized("workspace GET", body)

	recorder, body = rig.do(t, http.MethodPatch, "/api/v1/anime/"+subscribed.ManagedAnime.Id+"/subgroups",
		map[string]any{"expected_version": 1, "subgroups": map[string]any{"max_rss": 50, "entries": []any{}}, "group_actions": []any{}}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("subgroups PATCH failed: %d %s", recorder.Code, body)
	}
	assertSanitized("subgroups PATCH", body)

	recorder, body = rig.do(t, http.MethodPatch,
		"/api/v1/anime/"+subscribed.ManagedAnime.Id+"/rss-sources/"+subscribed.RssSources[0].Id+"/offset",
		map[string]any{"expected_version": 1, "episode_offset": 2}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("offset PATCH failed: %d %s", recorder.Code, body)
	}
	assertSanitized("offset PATCH", body)
}

func TestSkipAuthNeverAllowedInProduction(t *testing.T) {
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BANSOKO_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	access := accessapplication.NewServiceWithSessionSecret(db, []byte("test-session-secret-0123456789abcdef"))
	if _, err := NewServer(Dependencies{DB: db, Access: access, Environment: "production", SkipAuth: true}); err == nil {
		t.Fatal("production skip-auth must be rejected at startup")
	}
}

func TestManualRetryEndpointCreatesNewWorkItem(t *testing.T) {
	rig := newTestRig(t, "development", true)
	headers := map[string]string{"Idempotency-Key": "http-subscribe-0002"}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), headers)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("subscribe failed: %d %s", recorder.Code, string(body))
	}
	subscribed := decodeBody[generated.SubscribeResult](t, body)
	sourceID := subscribed.RssSources[0].Id

	// Force the initial refresh item into a terminal failed state.
	var workItemID string
	fixtureDB := disposabledb.Owner(t)
	if err := fixtureDB.QueryRowContext(context.Background(), `
SELECT id FROM work_items WHERE owner_id = $1`, sourceID).Scan(&workItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureDB.ExecContext(context.Background(), `SELECT public.work_item_close($1, 'failed')`, workItemID); err != nil {
		t.Fatal(err)
	}
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/rss-source-runtimes/"+sourceID+"/retry",
		map[string]string{"failed_work_item_id": workItemID, "reason": "operator recovery"},
		map[string]string{"Idempotency-Key": "retry-key-00001"})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("manual retry failed: %d %s", recorder.Code, string(body))
	}
	result := decodeBody[generated.ManualRetryResult](t, body)
	if result.OwnerType != "rss_source_runtime" || result.OwnerId != sourceID || !bool(result.Accepted) {
		t.Fatalf("unexpected retry result: %+v", result)
	}
	var live int
	if err := rig.db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_items WHERE owner_id = $1 AND state = 'available' AND attempt = 0`, sourceID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("manual retry must create one fresh live item, got %d", live)
	}
	// The replayed request returns the same accepted result without a new row.
	recorder, _ = rig.do(t, http.MethodPost, "/api/v1/rss-source-runtimes/"+sourceID+"/retry",
		map[string]string{"failed_work_item_id": workItemID, "reason": "operator recovery"},
		map[string]string{"Idempotency-Key": "retry-key-00001"})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("manual retry replay failed: %d", recorder.Code)
	}
	if err := rig.db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_items WHERE owner_id = $1 AND state = 'available'`, sourceID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("replayed manual retry must not create another row, got %d", live)
	}
}

func TestPutAndListConnectionsOverHTTP(t *testing.T) {
	rig := newTestRig(t, "development", true)
	payload := map[string]any{
		"kind": "qbittorrent", "name": "qb", "enabled": true,
		"base_url": "http://qb.local:8080", "username": "admin", "download_root": "/downloads",
		"secret": map[string]any{"mode": "replace", "value": "secret-password"},
	}
	recorder, body := rig.do(t, http.MethodPut, "/api/v1/config/connections/qb-1", payload, map[string]string{"If-Match": "0"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("put connection failed: %d %s", recorder.Code, string(body))
	}
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/config/connections", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list connections failed: %d", recorder.Code)
	}
	var connections []map[string]any
	if err := json.Unmarshal(body, &connections); err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 || connections[0]["credential_configured"] != true {
		t.Fatalf("connection must be redacted with credential flag: %s", string(body))
	}
	if _, leaked := connections[0]["password"]; leaked {
		t.Fatal("connection response must never include the password")
	}
	// Stale If-Match conflicts with the current version.
	recorder, body = rig.do(t, http.MethodPut, "/api/v1/config/connections/qb-1", payload, map[string]string{"If-Match": "9"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale If-Match must conflict: %d %s", recorder.Code, string(body))
	}
	// Legacy flat-password bodies no longer decode into the tri-state secret
	// contract and must be rejected as a client error, never an unmapped 500.
	legacy := map[string]any{
		"kind": "qbittorrent", "name": "qb", "enabled": true,
		"base_url": "http://qb.local:8080", "username": "admin", "password": "secret-password",
	}
	recorder, body = rig.do(t, http.MethodPut, "/api/v1/config/connections/qb-1", legacy, map[string]string{"If-Match": "2"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("legacy secret shape must be a 400: %d %s", recorder.Code, string(body))
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// providerDraftPayload builds a provider-origin draft whose subgroups selection
// names one group, so a saved revision has a well-defined render set.
func providerDraftPayload(entryRef, groupRef string) map[string]any {
	return map[string]any{
		"draft": map[string]any{
			"origin":           "mikan",
			"origin_entry_ref": entryRef,
			"title":            "API Provider Show",
			"metadata":         map[string]any{},
			"subgroups": map[string]any{
				"max_rss": 3,
				"entries": []any{map[string]any{
					"provider": "mikan", "group_ref": groupRef, "display_name": "Group " + groupRef,
					"selection_mode": "auto", "score": 50,
					"effective_selected": true,
				}},
			},
			"rules":       defaultRulesPayload(),
			"rss_sources": []any{},
		},
	}
}

type stubCatalogFeedFetcher struct {
	items []acquisitiondomain.FeedItem
}

func (f *stubCatalogFeedFetcher) FetchProviderFeed(context.Context, providerdomain.FeedLocator) ([]acquisitiondomain.FeedItem, error) {
	return f.items, nil
}

// TestRSSListViewReadOverHTTPUsesTheAdvisoryFeedIdentity covers the surviving
// read route end to end against a real persisted provider snapshot: the
// saved-revision read accepts the advisory identity the client supplies, echoes
// it on the group it matched, renders the persisted seed's verdict, reports
// snapshot_required (never a live fetch) when the identity is absent, and
// persists nothing. The retired flat preview pinned the same transport facts on
// the retired flat preview route with a 409 failure mode; the read model reports the gap
// as state instead, so the assertion is the reported state, not an error.
func TestRSSListViewReadOverHTTPUsesTheAdvisoryFeedIdentity(t *testing.T) {
	rig := newTestRig(t, "development", true)
	ctx := context.Background()
	const entryRef = "80801"
	const groupRef = "678"

	snapshotFetcher := &stubCatalogFeedFetcher{items: []acquisitiondomain.FeedItem{{
		GUID: "seed-http-01", Title: "[Group] API Provider Show - 01 [1080p]",
		Enclosure: "https://torrent.example.test/http-01.torrent",
		Published: time.Now().UTC().Truncate(time.Microsecond),
	}}}
	catalogService := rig.catalog
	catalogService.SetProviderFeedFetcher(snapshotFetcher)
	catalogService.SetFeedSnapshotStore(catalogpersistence.NewRepository(rig.db))
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", providerDraftPayload(entryRef, groupRef), nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("save provider candidate failed: %d %s", recorder.Code, string(body))
	}
	candidate := decodeBody[generated.AnimeConfigCandidate](t, body)
	if candidate.CurrentRevision == nil {
		t.Fatal("provider candidate has no revision")
	}
	snapshot, err := catalogService.RefreshProviderEntryRSS(ctx, "mikan", entryRef, groupRef)
	if err != nil {
		t.Fatalf("refresh provider snapshot: %v", err)
	}

	// No identity: the selected group is reported as needing one, and the read
	// succeeds instead of failing.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/rss-list-view", map[string]any{
		"input_source": "candidate_revision", "candidate_id": candidate.Id, "revision": *candidate.CurrentRevision,
	}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read without feed identity = %d %s, want 200", recorder.Code, string(body))
	}
	withoutIdentity := decodeBody[generated.RSSListView](t, body)
	if len(withoutIdentity.Groups) != 1 || withoutIdentity.Groups[0].Feed.State != generated.RSSListFeedStatusStateSnapshotRequired {
		t.Fatalf("groups = %+v, want one snapshot_required group", withoutIdentity.Groups)
	}
	if len(withoutIdentity.Groups[0].Items) != 0 {
		t.Fatalf("a snapshot_required group must not fabricate items: %+v", withoutIdentity.Groups[0].Items)
	}

	// Exact identity: the group carries the persisted snapshot and the seed
	// renders with its server verdict.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/anime/rss-list-view", map[string]any{
		"input_source": "candidate_revision", "candidate_id": candidate.Id, "revision": *candidate.CurrentRevision,
		"feed": []any{map[string]any{
			"provider": "mikan", "provider_entry_ref": entryRef, "provider_group_ref": groupRef,
			"snapshot_id": snapshot.SnapshotID, "snapshot_version": snapshot.SnapshotVersion,
			"content_hash": snapshot.ContentHash,
		}},
	}, nil)
	if recorder.Code != http.StatusOK {
		_, underlyingErr := rig.readmodel.ReadAnimeRSSListView(ctx, readmodelapplication.ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: string(candidate.Id), Revision: int64(*candidate.CurrentRevision), Feed: []readmodelapplication.PreviewFeedSnapshotRef{{Provider: animedomain.ProviderKind("mikan"), ProviderEntryRef: entryRef, ProviderGroupRef: groupRef, SnapshotID: snapshot.SnapshotID, SnapshotVersion: snapshot.SnapshotVersion, ContentHash: snapshot.ContentHash}}})
		t.Logf("exact-identity production readmodel error: %T: %v", underlyingErr, underlyingErr)
		t.Fatalf("read with the exact feed identity = %d %s", recorder.Code, string(body))
	}
	view := decodeBody[generated.RSSListView](t, body)
	if len(view.Groups) != 1 || view.Groups[0].Feed.State != generated.RSSListFeedStatusStateFresh {
		t.Fatalf("groups = %+v, want one fresh group", view.Groups)
	}
	if view.Groups[0].Feed.SnapshotId == nil || string(*view.Groups[0].Feed.SnapshotId) != snapshot.SnapshotID || view.Groups[0].Feed.SnapshotVersion == nil || int64(*view.Groups[0].Feed.SnapshotVersion) != snapshot.SnapshotVersion {
		t.Fatalf("group did not echo the exact feed identity: %+v", view.Groups[0].Feed)
	}
	if len(view.UnusedFeedIdentities) != 0 {
		t.Fatalf("a matched identity must not be reported as unused: %+v", view.UnusedFeedIdentities)
	}
	if len(view.Groups[0].Items) != 1 {
		t.Fatalf("items = %+v, want the persisted seed", view.Groups[0].Items)
	}
	item := view.Groups[0].Items[0]
	if item.Seed.Title != "[Group] API Provider Show - 01 [1080p]" {
		t.Fatalf("seed title = %q", item.Seed.Title)
	}
	// The persisted seed was never expanded, so the row this read projects is
	// pending on both axes: no verdict was computed and there is no structure to
	// list. The read must not invent one either way.
	if item.Torrent.State != generated.RSSListTorrentStateStatePending || item.Torrent.Manifest != nil {
		t.Fatalf("torrent = %+v, want the unexpanded pending state", item.Torrent)
	}
	if item.Evaluation.State != generated.RSSListEvaluationStatePending || len(item.Files) != 0 {
		t.Fatalf("evaluation = %+v, want an unexpanded pending row with no files", item.Evaluation)
	}
	if !view.MetadataHold.Held {
		t.Fatalf("the same draft must report its metadata hold: %+v", view.MetadataHold)
	}
	// The read is pure: no owner row appears because a read ran.
	if count := countRows(t, rig.db, "managed_anime"); count != 0 {
		t.Fatalf("the read persisted anime rows: %d", count)
	}
}

// emptyRSSFetcher serves the release retry service, which never fetches a feed.
type emptyRSSFetcher struct{}

func (emptyRSSFetcher) FetchSource(context.Context, rssapplication.SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	return nil, nil
}
