package application

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/configuration/domain"
	configurationport "bansoko/internal/configuration/port"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/events"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/jackett"
	"bansoko/internal/persistence/txscope"

	"github.com/google/uuid"
	"golang.org/x/net/proxy"
)

type SecretStore interface {
	Put(context.Context, string, string) (string, error)
}

// SecretRecord and SecretRepository are aliases for the configuration port so
// application services expose no adapter or database types.
type SecretRecord = configurationport.SecretRecord
type SecretRepository = configurationport.SecretRepository

// TransactionalSecretStore lets an owner commit secret-version changes in the
// same SQL transaction as its typed configuration row.
type TransactionalSecretStore interface {
	SecretStore
	PutTx(context.Context, txscope.Tx, string, string) (string, error)
}

// MemorySecretStore is intentionally an adapter for local development and
// tests. Production composition must provide an external secret store; the
// configuration tables persist only opaque references.
type MemorySecretStore struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewMemorySecretStore() *MemorySecretStore {
	return &MemorySecretStore{values: make(map[string]string)}
}

func (s *MemorySecretStore) Put(_ context.Context, purpose, value string) (string, error) {
	if strings.TrimSpace(purpose) == "" || strings.TrimSpace(value) == "" {
		return "", errors.New("secret purpose and value are required")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate secret reference: %w", err)
	}
	ref := "mem:" + hex.EncodeToString(raw)
	s.mu.Lock()
	s.values[ref] = value
	s.mu.Unlock()
	return ref, nil
}

func (s *MemorySecretStore) Delete(_ context.Context, ref string) error {
	if ref == "" {
		return nil
	}
	s.mu.Lock()
	delete(s.values, ref)
	s.mu.Unlock()
	return nil
}

func (s *MemorySecretStore) Resolve(ref string) (string, bool) {
	s.mu.RLock()
	value, ok := s.values[ref]
	s.mu.RUnlock()
	return value, ok
}

// PutTx persists a secret inside the caller's transaction. MemorySecretStore is
// not transactional; the tx is accepted to satisfy TransactionalSecretStore.
func (s *MemorySecretStore) PutTx(ctx context.Context, _ txscope.Tx, purpose, value string) (string, error) {
	return s.Put(ctx, purpose, value)
}

// DeleteTx removes a secret inside the caller's transaction.
func (s *MemorySecretStore) DeleteTx(_ context.Context, _ txscope.Tx, ref string) error {
	return s.Delete(context.Background(), ref)
}

type Service struct {
	db       *sql.DB
	secrets  SecretStore
	now      func() time.Time
	eventHub *events.Hub
}

// QBittorrentRuntimeConfig is the provider-facing projection of the single
// typed qBittorrent connection selected by configuration. The password is
// resolved only for an external side effect; it is never part of a read model.
type QBittorrentRuntimeConfig struct {
	// ConnectionID is the internal owner reference of the selected connection.
	// It is never part of a public DTO or of provider credentials; the provider
	// effect evidence only records it as an opaque, reconcilable anchor.
	ConnectionID string
	// ConnectionVersion is that connection's row version. Reconfiguring a
	// connection names a new provider namespace rather than silently reusing
	// the old one, so the version travels with the reference.
	ConnectionVersion int64
	Endpoint          string
	Username          string
	Password          string
	Category          string
	UserTags          []string
	SaveRoot          string
	ApplicationRoot   string
}

type networkRouteRecord struct {
	Kind      string
	Endpoint  string
	SecretRef string
	State     string
}

type ConnectionTester interface {
	Test(context.Context, domain.Connection) domain.HealthSnapshot
}

// ConnectionHTTPClientResolver is the narrow network boundary used by
// connection-test commands. Provider/catalog/feed adapters use the same
// resolver, so a test exercises the exact configured direct/proxy route that
// a subsequent provider operation will use.
type ConnectionHTTPClientResolver interface {
	ClientForConnection(context.Context, domain.Connection) (*http.Client, error)
}

// SecretResolver is deliberately narrower than SecretStore. Connection test
// commands may read a secret reference, while ordinary reads never expose it.
type SecretResolver interface {
	Resolve(string) (string, bool)
}

type HTTPConnectionTester struct{ Client *http.Client }

func (t HTTPConnectionTester) Test(ctx context.Context, connection domain.Connection) domain.HealthSnapshot {
	snapshot := domain.HealthSnapshot{ConnectionID: connection.ID, Kind: connection.Kind, State: domain.ConnectionDegraded, Code: "unavailable", Message: "connection test failed", CheckedAt: time.Now().UTC()}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, connection.Endpoint, nil)
	if err != nil {
		snapshot.Code = "invalid_endpoint"
		snapshot.Message = "connection endpoint is invalid"
		return snapshot
	}
	response, err := client.Do(request)
	if err != nil {
		snapshot.Code = "network_error"
		snapshot.Message = "provider connection failed"
		return snapshot
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		snapshot.State = domain.ConnectionConfigured
		snapshot.Code = "ok"
		snapshot.Message = "provider connection is reachable"
	} else {
		snapshot.Code = fmt.Sprintf("http_%d", response.StatusCode)
		snapshot.Message = "provider connection returned an error"
	}
	return snapshot
}

// ProviderConnectionTester keeps provider-specific authentication at the
// connection-test command boundary. Read endpoints remain side-effect free.
type ProviderConnectionTester struct {
	Client         *http.Client
	ClientResolver ConnectionHTTPClientResolver
	ResolveSecret  SecretResolver
}

func (t ProviderConnectionTester) Test(ctx context.Context, connection domain.Connection) domain.HealthSnapshot {
	switch connection.Kind {
	case domain.ConnectionQBittorrent:
		return t.testQBittorrent(ctx, connection)
	case domain.ConnectionTMDB:
		return t.testTMDB(ctx, connection)
	case domain.ConnectionJackett:
		return t.testJackett(ctx, connection)
	default:
		client, err := t.clientForConnection(ctx, connection)
		if err != nil {
			return connectionHealthFailure(connection, "network_route_invalid", "provider network route is not usable")
		}
		return (HTTPConnectionTester{Client: client}).Test(ctx, connection)
	}
}

func (t ProviderConnectionTester) clientForConnection(ctx context.Context, connection domain.Connection) (*http.Client, error) {
	needsResolvedClient := strings.TrimSpace(connection.NetworkRouteID) != "" ||
		(connection.Kind == domain.ConnectionHTTPFeed && httpFeedNeedsClient(connection))
	if needsResolvedClient {
		if t.ClientResolver == nil {
			return nil, errors.New("provider network route client is not configured")
		}
		client, err := t.ClientResolver.ClientForConnection(ctx, connection)
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, errors.New("provider network route resolver returned nil client")
		}
		return client, nil
	}
	if t.Client != nil {
		return t.Client, nil
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: hardeningtransport.DirectTransport()}, nil
}

func connectionHealthFailure(connection domain.Connection, code, message string) domain.HealthSnapshot {
	return domain.HealthSnapshot{
		ConnectionID: connection.ID,
		Kind:         connection.Kind,
		State:        domain.ConnectionDegraded,
		Code:         code,
		Message:      message,
		CheckedAt:    time.Now().UTC(),
	}
}

func (t ProviderConnectionTester) testQBittorrent(ctx context.Context, connection domain.Connection) domain.HealthSnapshot {
	snapshot := domain.HealthSnapshot{ConnectionID: connection.ID, Kind: connection.Kind, State: domain.ConnectionDegraded, Code: "unavailable", Message: "qBittorrent connection test failed", CheckedAt: time.Now().UTC()}
	password := ""
	if t.ResolveSecret != nil && connection.SecretRef != "" {
		password, _ = t.ResolveSecret.Resolve(connection.SecretRef)
	}
	if connection.QBittorrentConfig == nil || strings.TrimSpace(connection.QBittorrentConfig.Username) == "" || password == "" {
		snapshot.Code = "missing_credentials"
		snapshot.Message = "qBittorrent username or secret is not configured"
		return snapshot
	}
	client, err := t.clientForConnection(ctx, connection)
	if err != nil {
		snapshot.Code = "network_route_invalid"
		snapshot.Message = "qBittorrent network route is not usable"
		return snapshot
	}
	if strings.TrimSpace(connection.NetworkRouteID) == "" && t.Client == nil {
		// Without an explicit route the test dials the downloader the same way
		// the runtime client does: directly, bypassing the env egress proxy.
		client = &http.Client{Timeout: 10 * time.Second, Transport: qbittorrent.DirectTransport()}
	}
	form := url.Values{"username": {connection.QBittorrentConfig.Username}, "password": {password}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(connection.Endpoint, "/")+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		snapshot.Code = "invalid_endpoint"
		snapshot.Message = "qBittorrent endpoint is invalid"
		return snapshot
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		snapshot.Code = "network_error"
		snapshot.Message = "qBittorrent provider is unreachable"
		return snapshot
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64))
	if response.StatusCode >= 200 && response.StatusCode < 300 && strings.TrimSpace(string(body)) == "Ok." {
		snapshot.State = domain.ConnectionConfigured
		snapshot.Code = "ok"
		snapshot.Message = "qBittorrent authentication succeeded"
		return snapshot
	}
	snapshot.Code = fmt.Sprintf("http_%d", response.StatusCode)
	snapshot.Message = "qBittorrent authentication failed"
	return snapshot
}

func (t ProviderConnectionTester) testTMDB(ctx context.Context, connection domain.Connection) domain.HealthSnapshot {
	snapshot := domain.HealthSnapshot{ConnectionID: connection.ID, Kind: connection.Kind, State: domain.ConnectionDegraded, Code: "unavailable", Message: "TMDB connection test failed", CheckedAt: time.Now().UTC()}
	apiKey := ""
	if t.ResolveSecret != nil && connection.SecretRef != "" {
		apiKey, _ = t.ResolveSecret.Resolve(connection.SecretRef)
	}
	if strings.TrimSpace(apiKey) == "" {
		snapshot.Code = "missing_credentials"
		snapshot.Message = "TMDB API key is not configured"
		return snapshot
	}
	endpoint, err := url.Parse(connection.Endpoint)
	if err != nil {
		snapshot.Code = "invalid_endpoint"
		snapshot.Message = "TMDB endpoint is invalid"
		return snapshot
	}
	query := endpoint.Query()
	query.Set("api_key", apiKey)
	endpoint.RawQuery = query.Encode()
	client, err := t.clientForConnection(ctx, connection)
	if err != nil {
		snapshot.Code = "network_route_invalid"
		snapshot.Message = "TMDB network route is not usable"
		return snapshot
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		snapshot.Code = "invalid_endpoint"
		snapshot.Message = "TMDB endpoint is invalid"
		return snapshot
	}
	response, err := client.Do(request)
	if err != nil {
		snapshot.Code = "network_error"
		snapshot.Message = "TMDB provider is unreachable"
		return snapshot
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		snapshot.State = domain.ConnectionConfigured
		snapshot.Code = "ok"
		snapshot.Message = "TMDB API key is accepted"
		return snapshot
	}
	snapshot.Code = fmt.Sprintf("http_%d", response.StatusCode)
	snapshot.Message = "TMDB provider returned an error"
	return snapshot
}

func (t ProviderConnectionTester) testJackett(ctx context.Context, connection domain.Connection) domain.HealthSnapshot {
	snapshot := domain.HealthSnapshot{ConnectionID: connection.ID, Kind: connection.Kind, State: domain.ConnectionDegraded, Code: "unavailable", Message: "Jackett connection test failed", CheckedAt: time.Now().UTC()}
	apiKey := ""
	if t.ResolveSecret != nil && connection.SecretRef != "" {
		apiKey, _ = t.ResolveSecret.Resolve(connection.SecretRef)
	}
	endpoint, err := jackett.AggregateURL(connection.Endpoint, apiKey, url.Values{"t": {"caps"}})
	if err != nil {
		snapshot.Code = "missing_credentials"
		if strings.TrimSpace(apiKey) != "" {
			snapshot.Code = "invalid_endpoint"
		}
		snapshot.Message = "Jackett endpoint or API key is not configured correctly"
		return snapshot
	}
	client, err := t.clientForConnection(ctx, connection)
	if err != nil {
		snapshot.Code = "network_route_invalid"
		snapshot.Message = "Jackett network route is not usable"
		return snapshot
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		snapshot.Code = "invalid_endpoint"
		snapshot.Message = "Jackett endpoint is invalid"
		return snapshot
	}
	request.Header.Set("Accept", "application/xml")
	clientCopy := *client
	clientCopy.CheckRedirect = jackett.ValidateRedirect
	response, err := clientCopy.Do(request)
	if err != nil {
		snapshot.Code = "network_error"
		snapshot.Message = "Jackett is unreachable"
		return snapshot
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snapshot.Code = fmt.Sprintf("http_%d", response.StatusCode)
		snapshot.Message = "Jackett rejected the connection request"
		return snapshot
	}
	decoder := xml.NewDecoder(io.LimitReader(response.Body, 64<<10))
	for {
		token, decodeErr := decoder.Token()
		if decodeErr != nil {
			snapshot.Code = "invalid_response"
			snapshot.Message = "Jackett returned an unusable response"
			return snapshot
		}
		if root, ok := token.(xml.StartElement); ok {
			if root.Name.Local == "caps" {
				snapshot.State = domain.ConnectionConfigured
				snapshot.Code = "ok"
				snapshot.Message = "Jackett API key is accepted"
				return snapshot
			}
			snapshot.Code = "authentication_failed"
			if root.Name.Local != "error" {
				snapshot.Code = "invalid_response"
			}
			snapshot.Message = "Jackett rejected the API key or returned an invalid response"
			return snapshot
		}
	}
}

func NewService(db *sql.DB, secrets SecretStore) *Service {
	return &Service{db: db, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

// UpsertNetworkRoute owns the user-facing egress route resource. Provider
// adapters only receive a resolved HTTP client and never read this table.
func (s *Service) UpsertNetworkRoute(ctx context.Context, input domain.NetworkRouteInput, expectedVersion int64) (domain.NetworkRoute, error) {
	if s == nil || s.db == nil {
		return domain.NetworkRoute{}, errors.New("configuration database is nil")
	}
	normalized, err := input.Normalize()
	if err != nil {
		return domain.NetworkRoute{}, err
	}
	secretMutation := normalized.Secret
	if secretMutation.Action == "" {
		secretMutation.Action = domain.SecretKeep
	}
	if err := secretMutation.Validate(); err != nil {
		return domain.NetworkRoute{}, err
	}

	id := normalized.ID
	if id == "" {
		id = uuid.NewString()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NetworkRoute{}, fmt.Errorf("begin upsert network route: %w", err)
	}
	defer tx.Rollback()
	var currentVersion int64
	var currentSecretRef string
	versionErr := tx.QueryRowContext(ctx, `
SELECT version, COALESCE(secret_ref, '')
FROM network_routes WHERE id = $1 FOR UPDATE`, id).Scan(&currentVersion, &currentSecretRef)
	if errors.Is(versionErr, sql.ErrNoRows) {
		if expectedVersion != 0 {
			return domain.NetworkRoute{}, &apperrors.VersionConflict{Owner: "network_route", CurrentVersion: 0}
		}
	} else if versionErr != nil {
		return domain.NetworkRoute{}, fmt.Errorf("read network route version: %w", versionErr)
	} else if currentVersion != expectedVersion {
		return domain.NetworkRoute{}, &apperrors.VersionConflict{Owner: "network_route", CurrentVersion: currentVersion}
	}
	secretRef, err := s.mutateSecretTx(ctx, tx, secretMutation, "network-route:"+normalized.Name, currentSecretRef)
	if err != nil {
		return domain.NetworkRoute{}, fmt.Errorf("store network route secret: %w", err)
	}
	if normalized.Kind == domain.NetworkRouteGitHub {
		// One GitHub route at most: it is the update channel's single egress.
		// The advisory lock serializes concurrent creates; an update of that same
		// row is always allowed.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('network_routes:github'))`); err != nil {
			return domain.NetworkRoute{}, fmt.Errorf("lock github network route: %w", err)
		}
		var otherID string
		otherErr := tx.QueryRowContext(ctx, `SELECT id FROM network_routes WHERE kind = 'github' AND id <> $1 LIMIT 1`, id).Scan(&otherID)
		if otherErr == nil {
			return domain.NetworkRoute{}, &apperrors.Business{Status: http.StatusConflict, Code: "GITHUB_ROUTE_ALREADY_CONFIGURED", Message: "A GitHub route is already configured; edit the existing one."}
		}
		if !errors.Is(otherErr, sql.ErrNoRows) {
			return domain.NetworkRoute{}, fmt.Errorf("check existing github network route: %w", otherErr)
		}
	}
	state := domain.ConnectionConfigured
	if !normalized.Enabled {
		state = domain.ConnectionDisabled
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO network_routes(id, name, kind, endpoint, secret_ref, state)
VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6)
ON CONFLICT (id) DO UPDATE SET
  name = EXCLUDED.name, kind = EXCLUDED.kind, endpoint = EXCLUDED.endpoint,
  secret_ref = EXCLUDED.secret_ref, state = EXCLUDED.state,
  version = network_routes.version + 1, updated_at = NOW()`,
		id, normalized.Name, string(normalized.Kind), normalized.Endpoint, secretRef, string(state))
	if err != nil {
		return domain.NetworkRoute{}, fmt.Errorf("upsert network route: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.NetworkRoute{}, fmt.Errorf("commit upsert network route: %w", err)
	}
	return s.GetNetworkRoute(ctx, id)
}

func (s *Service) GetNetworkRoute(ctx context.Context, id string) (domain.NetworkRoute, error) {
	if s == nil || s.db == nil {
		return domain.NetworkRoute{}, errors.New("configuration database is nil")
	}
	if strings.TrimSpace(id) == "" {
		return domain.NetworkRoute{}, errors.New("network route ID is required")
	}
	var result domain.NetworkRoute
	var kind, endpoint, secretRef, state sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT id, name, kind, COALESCE(endpoint, ''), COALESCE(secret_ref, ''), state, version, updated_at
FROM network_routes WHERE id = $1`, id).Scan(&result.ID, &result.Name, &kind, &endpoint, &secretRef,
		&state, &result.Version, &result.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NetworkRoute{}, fmt.Errorf("network route %q not found", id)
		}
		return domain.NetworkRoute{}, fmt.Errorf("read network route: %w", err)
	}
	result.Kind = domain.NetworkRouteKind(kind.String)
	result.Endpoint = endpoint.String
	result.SecretRef = secretRef.String
	result.State = domain.ConnectionState(state.String)
	return result, nil
}

func (s *Service) ListNetworkRoutes(ctx context.Context) ([]domain.NetworkRoute, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("configuration database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, kind, COALESCE(endpoint, ''), COALESCE(secret_ref, ''), state, version, updated_at
FROM network_routes ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list network routes: %w", err)
	}
	defer rows.Close()
	result := make([]domain.NetworkRoute, 0)
	for rows.Next() {
		var item domain.NetworkRoute
		var kind, endpoint, secretRef, state string
		if err := rows.Scan(&item.ID, &item.Name, &kind, &endpoint, &secretRef, &state, &item.Version, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan network route: %w", err)
		}
		item.Kind = domain.NetworkRouteKind(kind)
		item.Endpoint = endpoint
		item.SecretRef = secretRef
		item.State = domain.ConnectionState(state)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate network routes: %w", err)
	}
	return result, nil
}

func (s *Service) UpsertConnection(ctx context.Context, input domain.ConnectionInput) (domain.Connection, error) {
	if s.db == nil {
		return domain.Connection{}, errors.New("configuration database is nil")
	}
	if err := validateConnectionInput(input); err != nil {
		return domain.Connection{}, err
	}
	if routeID := strings.TrimSpace(input.NetworkRouteID); routeID != "" {
		var routeKind, routeState string
		if err := s.db.QueryRowContext(ctx, `SELECT kind, state FROM network_routes WHERE id = $1`, routeID).Scan(&routeKind, &routeState); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Connection{}, fmt.Errorf("network route %q is not configured", routeID)
			}
			return domain.Connection{}, fmt.Errorf("read network route %q: %w", routeID, err)
		}
		if domain.NetworkRouteKind(routeKind) == domain.NetworkRouteGitHub {
			// The GitHub route belongs to the update channel alone; a provider
			// connection never sends its traffic through it.
			return domain.Connection{}, &apperrors.BadRequest{Code: "ROUTE_NOT_BINDABLE", Message: "The GitHub update route cannot be bound to a connection."}
		}
		if routeState != string(domain.ConnectionConfigured) {
			return domain.Connection{}, fmt.Errorf("network route %q is not configured", routeID)
		}
	}
	var normalizedQBConfig *domain.QBittorrentConnectionConfig
	if input.QBittorrentConfig != nil {
		normalized, err := input.QBittorrentConfig.Normalize()
		if err != nil {
			return domain.Connection{}, err
		}
		normalizedQBConfig = &normalized
	}
	var normalizedHTTPFeedConfig *domain.HTTPFeedConnectionConfig
	if input.HTTPFeedConfig != nil {
		normalized, err := input.HTTPFeedConfig.Normalize()
		if err != nil {
			return domain.Connection{}, err
		}
		normalizedHTTPFeedConfig = &normalized
	}
	id := input.ID
	if id == "" {
		id = uuid.NewString()
	}
	secretMutation := input.Secret
	if secretMutation.Action == "" {
		secretMutation.Action = domain.SecretKeep
	}

	state := domain.ConnectionConfigured
	if !input.Enabled || strings.TrimSpace(input.Endpoint) == "" {
		state = domain.ConnectionDisabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Connection{}, fmt.Errorf("begin upsert connection: %w", err)
	}
	defer tx.Rollback()
	var existingKind string
	var existingSecretRef string
	var existingVersion int64
	var existingConfig []byte
	existingErr := tx.QueryRowContext(ctx, `
SELECT kind, COALESCE(secret_ref, ''), version, config
FROM connections WHERE id = $1 FOR UPDATE`, id).Scan(&existingKind, &existingSecretRef, &existingVersion, &existingConfig)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return domain.Connection{}, fmt.Errorf("read existing connection kind: %w", existingErr)
	}
	if errors.Is(existingErr, sql.ErrNoRows) && input.ExpectedVersion != 0 {
		return domain.Connection{}, &apperrors.VersionConflict{Owner: "connection", CurrentVersion: 0}
	}
	if existingErr == nil && existingVersion != input.ExpectedVersion {
		return domain.Connection{}, &apperrors.VersionConflict{Owner: "connection", CurrentVersion: existingVersion}
	}
	if input.Kind == domain.ConnectionQBittorrent {
		// The runtime drives exactly one qBittorrent, so a second row is refused. The advisory lock
		// serializes concurrent creates; an update of the existing row (same id) is always allowed.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('connections:qbittorrent'))`); err != nil {
			return domain.Connection{}, fmt.Errorf("lock qBittorrent connection: %w", err)
		}
		var otherID string
		otherErr := tx.QueryRowContext(ctx, `SELECT id FROM connections WHERE kind = 'qbittorrent' AND id <> $1 LIMIT 1`, id).Scan(&otherID)
		if otherErr == nil {
			return domain.Connection{}, &apperrors.Business{Status: http.StatusConflict, Code: "DOWNLOADER_ALREADY_CONFIGURED", Message: "A download client is already configured; edit the existing one."}
		}
		if !errors.Is(otherErr, sql.ErrNoRows) {
			return domain.Connection{}, fmt.Errorf("read existing qBittorrent connection: %w", otherErr)
		}
	}
	secretRef, err := s.mutateSecretTx(ctx, tx, secretMutation, string(input.Kind)+":"+input.Name, existingSecretRef)
	if err != nil {
		return domain.Connection{}, fmt.Errorf("store connection secret: %w", err)
	}
	if input.Kind == domain.ConnectionJackett && secretRef == "" {
		return domain.Connection{}, &apperrors.BadRequest{Code: "CONNECTION_SECRET_REQUIRED", Message: "A Jackett API key is required."}
	}
	var effectiveQBConfig *domain.QBittorrentConnectionConfig
	if input.Kind == domain.ConnectionQBittorrent {
		effectiveQBConfig = normalizedQBConfig
		if effectiveQBConfig == nil && !errors.Is(existingErr, sql.ErrNoRows) && existingKind == string(domain.ConnectionQBittorrent) {
			loaded, loadErr := parseQBittorrentConfig(existingConfig)
			if loadErr != nil {
				return domain.Connection{}, loadErr
			}
			effectiveQBConfig = loaded
		}
		if effectiveQBConfig == nil {
			return domain.Connection{}, errors.New("qBittorrent connection config is required when creating a qBittorrent connection")
		}
	}
	var effectiveHTTPFeedConfig *domain.HTTPFeedConnectionConfig
	if input.Kind == domain.ConnectionHTTPFeed {
		effectiveHTTPFeedConfig = normalizedHTTPFeedConfig
		if effectiveHTTPFeedConfig == nil && !errors.Is(existingErr, sql.ErrNoRows) && existingKind == string(domain.ConnectionHTTPFeed) {
			loaded, loadErr := parseHTTPFeedConfig(existingConfig)
			if loadErr != nil {
				return domain.Connection{}, loadErr
			}
			effectiveHTTPFeedConfig = loaded
		}
		if effectiveHTTPFeedConfig == nil {
			defaultConfig := domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthNone}
			effectiveHTTPFeedConfig = &defaultConfig
		}
		if effectiveHTTPFeedConfig.AuthMode == domain.HTTPFeedAuthNone && secretRef != "" {
			err = errors.New("HTTP feed secret requires an explicit authentication mode")
			return domain.Connection{}, err
		}
		if effectiveHTTPFeedConfig.AuthMode != domain.HTTPFeedAuthNone && secretRef == "" {
			err = errors.New("HTTP feed authentication requires a configured secret")
			return domain.Connection{}, err
		}
	}
	configJSON, err := connectionConfigJSON(input.Kind, effectiveQBConfig, effectiveHTTPFeedConfig)
	if err != nil {
		return domain.Connection{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, network_route_id, state, config)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8)
ON CONFLICT (id) DO UPDATE SET
  kind = EXCLUDED.kind, name = EXCLUDED.name, endpoint = EXCLUDED.endpoint,
  secret_ref = EXCLUDED.secret_ref, network_route_id = EXCLUDED.network_route_id, state = EXCLUDED.state,
  config = EXCLUDED.config,
  version = connections.version + 1, updated_at = NOW()`, id, string(input.Kind),
		input.Name, input.Endpoint, secretRef, input.NetworkRouteID, string(state), configJSON)
	if err != nil {
		return domain.Connection{}, fmt.Errorf("upsert connection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Connection{}, fmt.Errorf("commit upsert connection: %w", err)
	}
	return s.GetConnection(ctx, id)
}

// SeedDefaultConnections ensures the built-in provider/metadata connections
// (mikan, anibt, bgm, tmdb) exist on a fresh instance. Each is seeded with
// its official endpoint, a direct route (empty network_route_id) and an
// enabled state. The operation is idempotent: a kind that already has any
// connection row is left untouched so a user's edits and disables survive a
// restart. It never overwrites, deletes, or changes state; it only inserts
// missing rows. qBittorrent and rss_http are not seeded because they require
// explicit user configuration (credentials, save root, auth mode).
func (s *Service) SeedDefaultConnections(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("configuration database is nil")
	}
	for _, seed := range domain.DefaultConnectionSeeds() {
		if err := s.seedDefaultConnection(ctx, seed); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) seedDefaultConnection(ctx context.Context, seed domain.DefaultConnectionSeed) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin seed %s connection: %w", seed.Kind, err)
	}
	defer tx.Rollback()
	var existing string
	// A kind is considered already configured when any row exists for it,
	// regardless of state. This preserves a user's disabled or edited row
	// across restarts; seeding only fills a completely empty kind.
	err = tx.QueryRowContext(ctx, `SELECT id FROM connections WHERE kind = $1 LIMIT 1`, string(seed.Kind)).Scan(&existing)
	if err == nil {
		return tx.Rollback()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read existing %s connection: %w", seed.Kind, err)
	}
	configJSON, err := connectionConfigJSON(seed.Kind, nil, nil)
	if err != nil {
		return fmt.Errorf("seed %s connection config: %w", seed.Kind, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, network_route_id, state, config)
VALUES ($1, $2, $3, $4, NULL, NULL, $5, $6)`,
		seed.ID, string(seed.Kind), seed.Name, seed.Endpoint, string(domain.ConnectionConfigured), configJSON); err != nil {
		return fmt.Errorf("seed %s connection: %w", seed.Kind, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seed %s connection: %w", seed.Kind, err)
	}
	return nil
}

// ErrConnectionNotFound reports that no connection row exists for the given
// ID. Callers that need a typed 404 at their own boundary may errors.Is
// against this sentinel without affecting other shared consumers of
// GetConnection.
var ErrConnectionNotFound = errors.New("connection not found")

func (s *Service) GetConnection(ctx context.Context, id string) (domain.Connection, error) {
	if s.db == nil {
		return domain.Connection{}, errors.New("configuration database is nil")
	}
	if strings.TrimSpace(id) == "" {
		return domain.Connection{}, errors.New("connection ID is required")
	}
	var result domain.Connection
	var kind, state, secretRef, route sql.NullString
	var configRaw []byte
	if err := s.db.QueryRowContext(ctx, `
	SELECT id, kind, name, endpoint, secret_ref, network_route_id, state,
       health_version, version, updated_at, config
FROM connections WHERE id = $1`, id).Scan(&result.ID, &kind, &result.Name,
		&result.Endpoint, &secretRef, &route, &state, &result.HealthVersion,
		&result.Version, &result.UpdatedAt, &configRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Connection{}, fmt.Errorf("connection %q not found: %w", id, ErrConnectionNotFound)
		}
		return domain.Connection{}, fmt.Errorf("read connection: %w", err)
	}
	result.Kind = domain.ConnectionKind(kind.String)
	result.State = domain.ConnectionState(state.String)
	result.SecretRef = secretRef.String
	result.NetworkRouteID = route.String
	if result.Kind == domain.ConnectionQBittorrent {
		config, err := parseQBittorrentConfig(configRaw)
		if err != nil {
			return domain.Connection{}, err
		}
		result.QBittorrentConfig = config
	}
	if result.Kind == domain.ConnectionHTTPFeed {
		config, err := parseHTTPFeedConfig(configRaw)
		if err != nil {
			return domain.Connection{}, err
		}
		result.HTTPFeedConfig = config
	}
	return result, nil
}

// GetQBittorrentSaveRoot resolves the single active qBittorrent connection's
// typed save root. A future per-profile connection selector must be explicit;
// silently choosing among multiple named connections would change the target
// of an already frozen download intent.
func (s *Service) GetQBittorrentSaveRoot(ctx context.Context) (string, error) {
	if s.db == nil {
		return "", errors.New("configuration database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id
FROM connections c
WHERE c.kind = 'qbittorrent' AND c.state = 'configured'
ORDER BY c.name, c.id`)
	if err != nil {
		return "", fmt.Errorf("read qBittorrent save roots: %w", err)
	}
	defer rows.Close()
	var roots []string
	for rows.Next() {
		var connectionID string
		if err := rows.Scan(&connectionID); err != nil {
			return "", fmt.Errorf("scan qBittorrent save root: %w", err)
		}
		connection, err := s.GetConnection(ctx, connectionID)
		if err != nil {
			return "", err
		}
		if connection.QBittorrentConfig != nil && strings.TrimSpace(connection.QBittorrentConfig.SaveRoot) != "" {
			// Same-path convention: the configured save root is the path the
			// app reads, with no translation.
			roots = append(roots, path.Clean(strings.TrimSpace(connection.QBittorrentConfig.SaveRoot)))
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate qBittorrent save roots: %w", err)
	}
	if len(roots) == 0 {
		return "", errors.New("no configured qBittorrent save root is available")
	}
	if len(roots) > 1 {
		return "", errors.New("multiple qBittorrent save roots require an explicit download profile")
	}
	return roots[0], nil
}

// ClientForConnection resolves the connection's saved network route into an
// HTTP client. Provider/catalog/feed adapters call this through their narrow
// resolver port; they never read network_routes or build proxy clients
// themselves. A direct route intentionally returns nil so callers use their
// normal transport and SSRF policy.
func (s *Service) ClientForConnection(ctx context.Context, connection domain.Connection) (*http.Client, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("configuration database is nil")
	}
	routeID := strings.TrimSpace(connection.NetworkRouteID)
	var client *http.Client
	var err error
	if routeID == "" {
		if connection.Kind != domain.ConnectionHTTPFeed || !httpFeedNeedsClient(connection) {
			return nil, nil
		}
		client = &http.Client{Timeout: 30 * time.Second, Transport: hardeningtransport.DirectTransport()}
	} else {
		var route networkRouteRecord
		if err := s.db.QueryRowContext(ctx, `
SELECT kind, COALESCE(endpoint, ''), COALESCE(secret_ref, ''), state
	FROM network_routes WHERE id = $1`, routeID).Scan(&route.Kind, &route.Endpoint, &route.SecretRef, &route.State); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("network route %q is not configured", routeID)
			}
			return nil, fmt.Errorf("read network route %q: %w", routeID, err)
		}
		if route.State != string(domain.ConnectionConfigured) {
			return nil, fmt.Errorf("network route %q is not configured", routeID)
		}
		if domain.NetworkRouteKind(route.Kind) == domain.NetworkRouteGitHub {
			return nil, fmt.Errorf("network route %q is not usable by a connection", routeID)
		}
		client, err = s.buildNetworkRouteClient(route)
		if err != nil {
			return nil, err
		}
	}
	if connection.Kind == domain.ConnectionHTTPFeed {
		return s.applyHTTPFeedAuth(ctx, connection, client)
	}
	return client, nil
}

func httpFeedNeedsClient(connection domain.Connection) bool {
	if connection.SecretRef != "" {
		return true
	}
	return connection.HTTPFeedConfig != nil && connection.HTTPFeedConfig.AuthMode != domain.HTTPFeedAuthNone
}

func (s *Service) applyHTTPFeedAuth(_ context.Context, connection domain.Connection, client *http.Client) (*http.Client, error) {
	config := connection.HTTPFeedConfig
	if config == nil {
		if connection.SecretRef != "" {
			return nil, errors.New("HTTP feed secret has no explicit authentication configuration")
		}
		return client, nil
	}
	normalized, err := config.Normalize()
	if err != nil {
		return nil, err
	}
	if normalized.AuthMode == domain.HTTPFeedAuthNone {
		if connection.SecretRef != "" {
			return nil, errors.New("HTTP feed secret requires an explicit authentication mode")
		}
		return client, nil
	}
	if connection.SecretRef == "" {
		return nil, errors.New("HTTP feed authentication requires a configured secret")
	}
	resolver, ok := s.secrets.(SecretResolver)
	if !ok || resolver == nil {
		return nil, errors.New("HTTP feed secret resolver is not configured")
	}
	secret, found := resolver.Resolve(connection.SecretRef)
	if !found || strings.TrimSpace(secret) == "" {
		return nil, errors.New("HTTP feed credentials are unavailable")
	}
	if normalized.AuthMode != domain.HTTPFeedAuthBasic && strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("HTTP feed credentials contain invalid header characters")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	copy := *client
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	origin := ""
	if parsed, err := url.Parse(connection.Endpoint); err == nil {
		origin = normalizedHTTPOrigin(parsed)
	}
	copy.Transport = httpFeedAuthTransport{base: base, mode: normalized.AuthMode, username: normalized.Username, secret: secret, origin: origin}
	return &copy, nil
}

type httpFeedAuthTransport struct {
	base     http.RoundTripper
	mode     domain.HTTPFeedAuthMode
	username string
	secret   string
	origin   string
}

// RSSBaseTransport and WithRSSRequestOrigin let the RSS execution boundary
// insert its SSRF-safe transport beneath authentication. Credentials are then
// applied only to requests whose normalized origin matches the configured URL.
func (t httpFeedAuthTransport) RSSBaseTransport() http.RoundTripper {
	return t.base
}

func (t httpFeedAuthTransport) WithRSSRequestOrigin(base http.RoundTripper, origin string) http.RoundTripper {
	t.base = base
	t.origin = origin
	return t
}

func (t httpFeedAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Del("Authorization")
	clone.Header.Del("Cookie")
	if t.origin == "" || normalizedHTTPOrigin(clone.URL) != t.origin {
		return t.base.RoundTrip(clone)
	}
	switch t.mode {
	case domain.HTTPFeedAuthBasic:
		clone.SetBasicAuth(t.username, t.secret)
	case domain.HTTPFeedAuthBearer:
		clone.Header.Set("Authorization", "Bearer "+strings.TrimSpace(t.secret))
	case domain.HTTPFeedAuthCookie:
		clone.Header.Set("Cookie", strings.TrimSpace(t.secret))
	default:
		return nil, fmt.Errorf("unsupported HTTP feed authentication mode %q", t.mode)
	}
	return t.base.RoundTrip(clone)
}

func normalizedHTTPOrigin(target *url.URL) string {
	if target == nil {
		return ""
	}
	scheme := strings.ToLower(target.Scheme)
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	if host == "" || (scheme != "http" && scheme != "https") {
		return ""
	}
	port := target.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// updateRouteTimeout is the update channel's own bound: an update download may
// legitimately outlive a provider request, so it never inherits the 30 second
// route timeout.
const updateRouteTimeout = 5 * time.Minute

// UpdateRouteClient returns the HTTP client of the single enabled GitHub update
// route, or nil when no GitHub route is configured or it is disabled. A saved
// route that cannot be turned into a client is an error: the update channel
// fails closed rather than quietly going direct. The route is read on every
// call, so saving a route affects the next update operation without a restart.
func (s *Service) UpdateRouteClient(ctx context.Context) (*http.Client, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("configuration database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT kind, COALESCE(endpoint, ''), COALESCE(secret_ref, ''), state
FROM network_routes WHERE kind = 'github' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read github update route: %w", err)
	}
	defer rows.Close()
	var routes []networkRouteRecord
	for rows.Next() {
		var route networkRouteRecord
		if err := rows.Scan(&route.Kind, &route.Endpoint, &route.SecretRef, &route.State); err != nil {
			return nil, fmt.Errorf("scan github update route: %w", err)
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate github update routes: %w", err)
	}
	return s.updateRouteClient(routes)
}

// updateRouteClient is the selection rule for the stored GitHub routes: none or
// a disabled one means the deployment default, more than one is a corrupt store
// and fails closed, and a route that cannot build a client also fails closed.
func (s *Service) updateRouteClient(routes []networkRouteRecord) (*http.Client, error) {
	if len(routes) == 0 {
		return nil, nil
	}
	if len(routes) > 1 {
		return nil, errors.New("more than one GitHub update route is stored")
	}
	if routes[0].State != string(domain.ConnectionConfigured) {
		return nil, nil
	}
	client, err := s.buildNetworkRouteClient(routes[0])
	if err != nil {
		return nil, fmt.Errorf("build github update route client: %w", err)
	}
	client.Timeout = updateRouteTimeout
	return client, nil
}

func (s *Service) buildNetworkRouteClient(route networkRouteRecord) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = nil
	const timeout = 30 * time.Second

	switch strings.ToLower(strings.TrimSpace(route.Kind)) {
	case "direct":
		return &http.Client{Transport: transport, Timeout: timeout}, nil
	case "http_proxy", "github":
		proxyURL, err := s.parseNetworkRouteURL(route, "http", "https")
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		return &http.Client{Transport: transport, Timeout: timeout}, nil
	case "socks5":
		socksURL, err := s.parseNetworkRouteURL(route, "socks5", "socks5h")
		if err != nil {
			return nil, err
		}
		auth := (*proxy.Auth)(nil)
		if socksURL.User != nil {
			password, _ := socksURL.User.Password()
			auth = &proxy.Auth{User: socksURL.User.Username(), Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", socksURL.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("configure SOCKS5 network route: %w", err)
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
				return contextDialer.DialContext(ctx, network, address)
			}
			type result struct {
				conn net.Conn
				err  error
			}
			ch := make(chan result, 1)
			go func() {
				conn, err := dialer.Dial(network, address)
				ch <- result{conn: conn, err: err}
			}()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case outcome := <-ch:
				return outcome.conn, outcome.err
			}
		}
		return &http.Client{Transport: transport, Timeout: timeout}, nil
	default:
		return nil, fmt.Errorf("unsupported network route kind %q", route.Kind)
	}
}

func (s *Service) parseNetworkRouteURL(route networkRouteRecord, schemes ...string) (*url.URL, error) {
	raw := strings.TrimSpace(route.Endpoint)
	if raw == "" {
		return nil, fmt.Errorf("network route %q requires an endpoint", route.Kind)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("network route %q endpoint is invalid", route.Kind)
	}
	allowed := false
	for _, scheme := range schemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("network route %q endpoint must use %s", route.Kind, strings.Join(schemes, " or "))
	}
	if route.SecretRef != "" {
		resolver, ok := s.secrets.(SecretResolver)
		if !ok || resolver == nil {
			return nil, errors.New("network route secret resolver is not configured")
		}
		secret, found := resolver.Resolve(route.SecretRef)
		if !found || strings.TrimSpace(secret) == "" {
			return nil, errors.New("network route credentials are unavailable")
		}
		if parsed.User == nil {
			username, password, hasPassword := strings.Cut(secret, ":")
			if strings.TrimSpace(username) == "" {
				return nil, errors.New("network route credentials are invalid")
			}
			if hasPassword {
				parsed.User = url.UserPassword(username, password)
			} else {
				parsed.User = url.User(username)
			}
		}
	}
	return parsed, nil
}

// GetQBittorrentRuntimeConfig resolves the same configured connection used by
// GetQBittorrentSaveRoot. A provider operation must not combine an endpoint
// or credential from another configuration source.
func (s *Service) GetQBittorrentRuntimeConfig(ctx context.Context) (QBittorrentRuntimeConfig, error) {
	if s.db == nil {
		return QBittorrentRuntimeConfig{}, errors.New("configuration database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id
FROM connections c
WHERE c.kind = 'qbittorrent' AND c.state = 'configured'
ORDER BY c.name, c.id`)
	if err != nil {
		return QBittorrentRuntimeConfig{}, fmt.Errorf("read configured qBittorrent connections: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return QBittorrentRuntimeConfig{}, fmt.Errorf("scan configured qBittorrent connection: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return QBittorrentRuntimeConfig{}, fmt.Errorf("iterate configured qBittorrent connections: %w", err)
	}
	if len(ids) == 0 {
		return QBittorrentRuntimeConfig{}, errors.New("no configured qBittorrent connection is available")
	}
	if len(ids) > 1 {
		return QBittorrentRuntimeConfig{}, errors.New("multiple qBittorrent connections require an explicit download profile")
	}
	connection, err := s.GetConnection(ctx, ids[0])
	if err != nil {
		return QBittorrentRuntimeConfig{}, err
	}
	if connection.QBittorrentConfig == nil {
		return QBittorrentRuntimeConfig{}, errors.New("configured qBittorrent connection has no typed config")
	}
	resolver, ok := s.secrets.(SecretResolver)
	if !ok || resolver == nil || strings.TrimSpace(connection.SecretRef) == "" {
		return QBittorrentRuntimeConfig{}, errors.New("qBittorrent secret resolver is not configured")
	}
	password, ok := resolver.Resolve(connection.SecretRef)
	if !ok || strings.TrimSpace(password) == "" {
		return QBittorrentRuntimeConfig{}, errors.New("qBittorrent credentials are unavailable")
	}
	return QBittorrentRuntimeConfig{
		ConnectionID:      connection.ID,
		ConnectionVersion: connection.Version,
		Endpoint:          connection.Endpoint,
		Username:          connection.QBittorrentConfig.Username,
		Password:          password,
		Category:          connection.QBittorrentConfig.Category,
		UserTags:          append([]string(nil), connection.QBittorrentConfig.UserTags...),
		SaveRoot:          connection.QBittorrentConfig.SaveRoot,
		ApplicationRoot:   connection.QBittorrentConfig.ApplicationRoot,
	}, nil
}

func (s *Service) ListConnections(ctx context.Context) ([]domain.Connection, error) {
	if s.db == nil {
		return nil, errors.New("configuration database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, kind, name, endpoint, secret_ref, network_route_id, state,
       health_version, version, updated_at, config
FROM connections ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	var result []domain.Connection
	for rows.Next() {
		var item domain.Connection
		var kind, state, secretRef, route sql.NullString
		var configRaw []byte
		if err := rows.Scan(&item.ID, &kind, &item.Name, &item.Endpoint, &secretRef,
			&route, &state, &item.HealthVersion, &item.Version, &item.UpdatedAt, &configRaw); err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		item.Kind = domain.ConnectionKind(kind.String)
		item.State = domain.ConnectionState(state.String)
		item.SecretRef = secretRef.String
		item.NetworkRouteID = route.String
		if item.Kind == domain.ConnectionQBittorrent {
			config, err := parseQBittorrentConfig(configRaw)
			if err != nil {
				return nil, err
			}
			item.QBittorrentConfig = config
		}
		if item.Kind == domain.ConnectionHTTPFeed {
			config, err := parseHTTPFeedConfig(configRaw)
			if err != nil {
				return nil, err
			}
			item.HTTPFeedConfig = config
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connections: %w", err)
	}
	return result, nil
}

// RecordHealth persists the connection state projection of a health probe.
// The retired wide-V2 connection_health history table is gone: health
// state lives on the connection row itself (state + health_version).
// It bumps only health_version, never the optimistic-locking owner version,
// so a TestConnection probe must not invalidate pending If-Match saves.
func (s *Service) RecordHealth(ctx context.Context, snapshot domain.HealthSnapshot) error {
	if s.db == nil {
		return errors.New("configuration database is nil")
	}
	if snapshot.ConnectionID == "" || snapshot.Kind == "" || snapshot.Code == "" {
		return errors.New("health snapshot identity is required")
	}
	if snapshot.State != domain.ConnectionConfigured && snapshot.State != domain.ConnectionDegraded && snapshot.State != domain.ConnectionDisabled {
		return fmt.Errorf("unsupported connection health state %q", snapshot.State)
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE connections SET state = $2, health_version = health_version + 1,
  updated_at = NOW() WHERE id = $1`, snapshot.ConnectionID, string(snapshot.State))
	if err != nil {
		return fmt.Errorf("update connection health state: %w", err)
	}
	return nil
}

func (s *Service) TestConnection(ctx context.Context, id string, tester ConnectionTester) (domain.HealthSnapshot, error) {
	if tester == nil {
		return domain.HealthSnapshot{}, errors.New("connection tester is not configured")
	}
	connection, err := s.GetConnection(ctx, id)
	if err != nil {
		return domain.HealthSnapshot{}, err
	}
	snapshot := tester.Test(ctx, connection)
	if snapshot.ConnectionID == "" {
		snapshot.ConnectionID = connection.ID
	}
	if snapshot.Kind == "" {
		snapshot.Kind = connection.Kind
	}
	if snapshot.CheckedAt.IsZero() {
		snapshot.CheckedAt = s.now()
	}
	if err := s.RecordHealth(ctx, snapshot); err != nil {
		return domain.HealthSnapshot{}, err
	}
	return snapshot, nil
}

// connectionConfigJSON encodes the per-kind typed config stored on the
// connection row. Kinds without typed config persist an empty object.
func connectionConfigJSON(kind domain.ConnectionKind, qbConfig *domain.QBittorrentConnectionConfig, httpFeedConfig *domain.HTTPFeedConnectionConfig) ([]byte, error) {
	switch kind {
	case domain.ConnectionQBittorrent:
		if qbConfig == nil {
			return nil, errors.New("qBittorrent connection config is required when creating a qBittorrent connection")
		}
		// A nil tag slice marshals to JSON null, but the stored shape requires
		// an array; normalize to the empty array before encoding.
		config := *qbConfig
		if config.UserTags == nil {
			config.UserTags = []string{}
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return nil, fmt.Errorf("encode qBittorrent typed config: %w", err)
		}
		return encoded, nil
	case domain.ConnectionHTTPFeed:
		if httpFeedConfig == nil {
			return nil, errors.New("HTTP feed connection config is required")
		}
		encoded, err := json.Marshal(httpFeedConfig)
		if err != nil {
			return nil, fmt.Errorf("encode HTTP feed typed config: %w", err)
		}
		return encoded, nil
	default:
		return []byte(`{}`), nil
	}
}

func parseQBittorrentConfig(raw []byte) (*domain.QBittorrentConnectionConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var config domain.QBittorrentConnectionConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("decode qBittorrent typed config: %w", err)
	}
	// The subtype table read back zero tags as a nil slice; keep that shape.
	if len(config.UserTags) == 0 {
		config.UserTags = nil
	}
	return &config, nil
}

func parseHTTPFeedConfig(raw []byte) (*domain.HTTPFeedConnectionConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var config domain.HTTPFeedConnectionConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("decode HTTP feed typed config: %w", err)
	}
	normalized, err := config.Normalize()
	if err != nil {
		return nil, fmt.Errorf("normalize HTTP feed typed config: %w", err)
	}
	return &normalized, nil
}

func (s *Service) mutateSecretTx(ctx context.Context, tx *sql.Tx, mutation domain.SecretMutation, purpose, currentRef string) (string, error) {
	switch mutation.Action {
	case domain.SecretKeep:
		return currentRef, nil
	case domain.SecretClear:
		// Clearing detaches the owner reference. Immutable versions remain until a
		// retention-aware GC proves no frozen owner or delivery still references them.
		return "", nil
	case domain.SecretReplace:
		store, ok := s.secrets.(TransactionalSecretStore)
		if !ok || store == nil {
			return "", errors.New("transactional secret store is not configured")
		}
		return store.PutTx(ctx, txscope.Wrap(tx), purpose, mutation.Value)
	default:
		return "", fmt.Errorf("unsupported secret action %q", mutation.Action)
	}
}

func validateConnectionInput(input domain.ConnectionInput) error {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Endpoint) == "" {
		return errors.New("connection name and endpoint are required")
	}
	if err := validateConnectionEndpointCredentialFree(input.Endpoint); err != nil {
		return err
	}
	switch input.Kind {
	case domain.ConnectionAniBT, domain.ConnectionMikan, domain.ConnectionNyaa, domain.ConnectionJackett, domain.ConnectionBGM, domain.ConnectionQBittorrent, domain.ConnectionTMDB, domain.ConnectionHTTPFeed:
	default:
		return fmt.Errorf("unsupported connection kind %q", input.Kind)
	}
	if input.Kind == domain.ConnectionQBittorrent {
		if input.QBittorrentConfig == nil {
			if input.ID == "" {
				return errors.New("qBittorrent connection config is required")
			}
		} else {
			normalized, err := input.QBittorrentConfig.Normalize()
			if err != nil {
				return err
			}
			if normalized.SaveRoot == "" {
				return &apperrors.BadRequest{Code: "VALIDATION_FAILED", Message: "download_root is required for a qBittorrent connection"}
			}
		}
	} else if input.QBittorrentConfig != nil {
		return errors.New("qBittorrent config is only valid for qBittorrent connections")
	}
	if input.Kind == domain.ConnectionHTTPFeed {
		if input.HTTPFeedConfig != nil {
			config, err := input.HTTPFeedConfig.Normalize()
			if err != nil {
				return err
			}
			if input.Secret.Action == domain.SecretReplace && config.AuthMode == domain.HTTPFeedAuthNone {
				return errors.New("HTTP feed secret requires an explicit authentication mode")
			}
		}
	} else if input.HTTPFeedConfig != nil {
		return errors.New("HTTP feed config is only valid for HTTP feed connections")
	}
	if input.Secret.Action == "" {
		return nil
	}
	return input.Secret.Validate()
}

// validateConnectionEndpointCredentialFree refuses a connection base URL that
// carries userinfo, a query or a fragment (SEC-033). Credentials belong in the
// connection's write-only secret, so the base URL every read returns can never
// hold one.
func validateConnectionEndpointCredentialFree(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return &apperrors.Business{
			Status:  http.StatusUnprocessableEntity,
			Code:    "CONNECTION_ENDPOINT_INVALID",
			Message: "The connection address must not contain a user name, password, query or fragment; put credentials in the connection secret.",
		}
	}
	return nil
}
