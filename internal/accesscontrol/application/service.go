package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/accesscontrol/domain"
	"bansoko/internal/persistence/txscope"
	"github.com/google/uuid"
)

const protocolVersion = "bootstrap-1"

type Service struct {
	db     *sql.DB
	now    func() time.Time
	secret []byte
	// rawStore encrypts raw PAT values at rest so the UI can keep showing the
	// key and allow copying for its lifetime. Nil means raw display is
	// unavailable (hash authentication still works).
	rawStore rawTokenStore
}

// rawTokenStore is the subset of the shared configuration secret store used
// by PAT raw-value persistence. It is satisfied by
// configurationapplication.DatabaseSecretStore.
type rawTokenStore interface {
	Put(ctx context.Context, purpose, value string) (string, error)
	Delete(ctx context.Context, ref string) error
	Resolve(ref string) (string, bool)
}

// transactionalRawTokenStore is the transaction-aware subset used by PAT
// mutations. PutTx participates in the caller-owned transaction; raw-value
// deletion uses rawStore.Delete only after commit or for compensation.
type transactionalRawTokenStore interface {
	PutTx(ctx context.Context, scope txscope.Tx, purpose, value string) (string, error)
}

const rawTokenSecretPurpose = "api_token_raw"

// SetRawTokenStore wires the encrypted raw-value store. Called by the
// composition root before serving; without it the UI shows no raw token.
func (s *Service) SetRawTokenStore(store rawTokenStore) {
	if s != nil {
		s.rawStore = store
	}
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func NewServiceWithSessionSecret(db *sql.DB, sessionSecret []byte) *Service {
	secret := append([]byte(nil), sessionSecret...)
	return &Service{
		db:     db,
		now:    func() time.Time { return time.Now().UTC() },
		secret: secret,
	}
}

func (s *Service) Status(ctx context.Context) (domain.BootstrapStatus, error) {
	if s.db == nil {
		return domain.BootstrapStatus{}, errors.New("access control database is nil")
	}
	var state string
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT state, version FROM instance_bootstrap WHERE singleton_id = 1`).Scan(&state, &version); err != nil {
		return domain.BootstrapStatus{}, fmt.Errorf("read bootstrap status: %w", err)
	}
	status := domain.BootstrapStatus{State: domain.BootstrapState(state), ProtocolVersion: protocolVersion, Version: version}
	switch status.State {
	case domain.StateUninitialized:
		status.AllowedActions = []string{"create_owner"}
	case domain.StateReady:
		status.AllowedActions = []string{"login"}
	}
	return status, nil
}

func (s *Service) SetupOwnerStatus(ctx context.Context) (domain.SetupOwnerStatus, error) {
	if s.db == nil {
		return domain.SetupOwnerStatus{}, errors.New("access control database is nil")
	}
	var status domain.SetupOwnerStatus
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_accounts WHERE role = 'owner'`).Scan(&status.OwnerCount); err != nil {
		return domain.SetupOwnerStatus{}, fmt.Errorf("read setup owner status: %w", err)
	}
	return status, nil
}

func (s *Service) Login(ctx context.Context, username, password string, ttl time.Duration) (string, error) {
	var id, passwordHash, state string
	if err := s.db.QueryRowContext(ctx, `
SELECT a.id, a.password_hash, b.state
FROM local_accounts a CROSS JOIN instance_bootstrap b
WHERE a.username = $1`, username).Scan(&id, &passwordHash, &state); errors.Is(err, sql.ErrNoRows) {
		// Unknown usernames pay the same bcrypt cost as known ones so the
		// response time does not reveal whether the account exists.
		_ = verifyPassword(dummyPasswordHash, password)
		return "", domain.ErrInvalidCredentials
	} else if err != nil {
		return "", fmt.Errorf("read account: %w", err)
	}
	if state != string(domain.StateReady) {
		return "", domain.ErrSetupNotFinalized
	}
	if !verifyPassword(passwordHash, password) {
		return "", domain.ErrInvalidCredentials
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	raw, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(id, account_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`, uuid.NewString(), id, s.tokenHash(raw), s.now().Add(ttl)); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return raw, nil
}

func (s *Service) AuthenticateSession(ctx context.Context, rawToken string) (domain.Account, error) {
	if s.db == nil || strings.TrimSpace(rawToken) == "" {
		return domain.Account{}, domain.ErrInvalidCredentials
	}
	var account domain.Account
	var expiresAt time.Time
	if err := s.db.QueryRowContext(ctx, `
SELECT a.id, a.username, a.role, s.expires_at
FROM auth_sessions s JOIN local_accounts a ON a.id = s.account_id
WHERE s.token_hash = $1 AND s.revoked_at IS NULL`, s.tokenHash(rawToken)).Scan(&account.ID, &account.Username, &account.Role, &expiresAt); errors.Is(err, sql.ErrNoRows) {
		// Not a session: fall through to the long-lived API token channel.
		account, err = s.authenticateApiToken(ctx, rawToken)
		if err != nil {
			return domain.Account{}, domain.ErrInvalidCredentials
		}
		return account, nil
	} else if err != nil {
		return domain.Account{}, fmt.Errorf("read session: %w", err)
	}
	if !expiresAt.After(s.now()) {
		return domain.Account{}, domain.ErrInvalidCredentials
	}
	// A web session is the owner at the keyboard: it holds every scope.
	account.Scopes = append([]string(nil), domain.AllScopes...)
	account.WebSession = true
	return account, nil
}

// authenticateApiToken resolves a long-lived API token (PAT) to its owning
// account. PATs never expire; they are only revoked. A successful lookup
// stamps last_used_at so rotation tooling can see active tokens.
func (s *Service) authenticateApiToken(ctx context.Context, rawToken string) (domain.Account, error) {
	var account domain.Account
	var scopes string
	if err := s.db.QueryRowContext(ctx, `
SELECT a.id, a.username, a.role, t.scopes
FROM api_tokens t JOIN local_accounts a ON a.id = t.account_id
	WHERE t.token_hash = $1`, s.tokenHash(rawToken)).Scan(&account.ID, &account.Username, &account.Role, &scopes); errors.Is(err, sql.ErrNoRows) {
		return domain.Account{}, domain.ErrInvalidCredentials
	} else if err != nil {
		return domain.Account{}, fmt.Errorf("read api token: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = $2 WHERE token_hash = $1`, s.tokenHash(rawToken), s.now())
	// An API token holds the scope set chosen at its creation.
	account.Scopes = domain.DecodeScopes(scopes)
	return account, nil
}

// CreateApiToken issues a long-lived bearer token for the owner account. The
// raw token is returned in the create response and, when the deployment key
// is configured, encrypted at rest so the UI can keep showing and copying it
// for the token's lifetime. The token does not expire; revoke it to invalidate.
// scopes is the requested scope set (empty selects read and write); it is
// fixed for the token's lifetime.
func (s *Service) CreateApiToken(ctx context.Context, name string, scopes []string) (string, domain.ApiToken, error) {
	if s.db == nil {
		return "", domain.ApiToken{}, errors.New("access control database is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", domain.ApiToken{}, fmt.Errorf("%w: name is required and must be at most 128 characters", domain.ErrApiTokenInvalid)
	}
	scopes, err := domain.NormalizeApiTokenScopes(scopes)
	if err != nil {
		return "", domain.ApiToken{}, err
	}
	var accountID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM local_accounts WHERE role = 'owner' LIMIT 1`).Scan(&accountID); errors.Is(err, sql.ErrNoRows) {
		return "", domain.ApiToken{}, domain.ErrSetupNotFinalized
	} else if err != nil {
		return "", domain.ApiToken{}, fmt.Errorf("read owner: %w", err)
	}
	raw, err := randomToken(32)
	if err != nil {
		return "", domain.ApiToken{}, err
	}
	token := domain.ApiToken{ID: uuid.NewString(), Name: name, Scopes: scopes, CreatedAt: s.now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", domain.ApiToken{}, fmt.Errorf("begin create api token: %w", err)
	}
	defer tx.Rollback()

	var secretValue any
	var newSecretRef string
	if s.rawStore != nil {
		store, ok := s.rawStore.(transactionalRawTokenStore)
		if !ok || store == nil {
			return "", domain.ApiToken{}, errors.New("transactional raw token store is not configured")
		}
		ref, putErr := store.PutTx(ctx, txscope.Wrap(tx), rawTokenSecretPurpose, raw)
		newSecretRef = strings.TrimSpace(ref)
		if putErr != nil {
			cleanupRawToken(ctx, s.rawStore, newSecretRef)
			return "", domain.ApiToken{}, fmt.Errorf("persist encrypted api token: %w", putErr)
		}
		if newSecretRef == "" {
			return "", domain.ApiToken{}, errors.New("persist encrypted api token: empty secret reference")
		}
		secretValue = newSecretRef
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens(id, account_id, name, token_hash, secret_ref, scopes) VALUES ($1, $2, $3, $4, $5, $6)`, token.ID, accountID, name, s.tokenHash(raw), secretValue, domain.EncodeScopes(scopes)); err != nil {
		cleanupRawToken(ctx, s.rawStore, newSecretRef)
		return "", domain.ApiToken{}, fmt.Errorf("create api token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		cleanupRawToken(ctx, s.rawStore, newSecretRef)
		return "", domain.ApiToken{}, fmt.Errorf("commit create api token: %w", err)
	}
	return raw, token, nil
}

// ReadApiTokenRaw returns the raw token value for an active token so the UI
// can display and copy it. It requires the deployment key; without it the
// value is unavailable (nil) while authentication still works.
func (s *Service) ReadApiTokenRaw(ctx context.Context, tokenID string) (string, bool) {
	if s == nil || s.db == nil || s.rawStore == nil || strings.TrimSpace(tokenID) == "" {
		return "", false
	}
	var secretRef string
	if err := s.db.QueryRowContext(ctx, `SELECT secret_ref FROM api_tokens WHERE id = $1`, tokenID).Scan(&secretRef); err != nil || strings.TrimSpace(secretRef) == "" {
		return "", false
	}
	return s.rawStore.Resolve(secretRef)
}

// ListApiTokens returns the metadata projection of the active API tokens.
// Revoked tokens are deleted, not tombstoned, so they never appear here.
// Raw token values are never included.
func (s *Service) ListApiTokens(ctx context.Context) ([]domain.ApiToken, error) {
	if s.db == nil {
		return nil, errors.New("access control database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scopes, created_at, last_used_at FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()
	tokens := make([]domain.ApiToken, 0)
	for rows.Next() {
		var token domain.ApiToken
		var scopes string
		if err := rows.Scan(&token.ID, &token.Name, &scopes, &token.CreatedAt, &token.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		token.Scopes = domain.DecodeScopes(scopes)
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api tokens: %w", err)
	}
	return tokens, nil
}

// ApiTokenScopes returns the scope set of one active token, so a caller can be
// held to "never mint or reveal more than you hold" before a rotate or a raw
// read. An unknown token is ErrApiTokenNotFound.
func (s *Service) ApiTokenScopes(ctx context.Context, tokenID string) ([]string, error) {
	if s.db == nil || strings.TrimSpace(tokenID) == "" {
		return nil, domain.ErrApiTokenNotFound
	}
	var scopes string
	if err := s.db.QueryRowContext(ctx, `SELECT scopes FROM api_tokens WHERE id = $1`, tokenID).Scan(&scopes); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrApiTokenNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read api token scopes: %w", err)
	}
	return domain.DecodeScopes(scopes), nil
}

// RevokeApiToken permanently deletes a long-lived API token, together with
// its encrypted raw value. Deleting an unknown or already-deleted token is a
// no-op so rotation stays idempotent.
func (s *Service) RevokeApiToken(ctx context.Context, tokenID string) error {
	if s.db == nil || strings.TrimSpace(tokenID) == "" {
		return domain.ErrApiTokenNotFound
	}
	var secretRef string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(secret_ref, '') FROM api_tokens WHERE id = $1`, tokenID).Scan(&secretRef); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read api token for revoke: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = $1`, tokenID); err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	if s.rawStore != nil && strings.TrimSpace(secretRef) != "" {
		_ = s.rawStore.Delete(ctx, secretRef)
	}
	return nil
}

// RotateApiToken atomically deletes the current token (with its encrypted raw
// value) and issues a fresh one for the same owner, in one transaction. The
// new raw token is returned in the response and stored encrypted at rest for
// long-term display; the old token is invalid the moment the transaction
// commits, so a rotate is safe to retry and cannot leave two active tokens.
// Rotating an unknown or already-deleted token fails with ErrApiTokenNotFound.
func (s *Service) RotateApiToken(ctx context.Context, tokenID string) (string, domain.ApiToken, error) {
	if s.db == nil || strings.TrimSpace(tokenID) == "" {
		return "", domain.ApiToken{}, domain.ErrApiTokenNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", domain.ApiToken{}, fmt.Errorf("begin rotate api token: %w", err)
	}
	defer tx.Rollback()

	var accountID, name, secretRef, scopes string
	if err := tx.QueryRowContext(ctx, `SELECT account_id, name, COALESCE(secret_ref, ''), scopes FROM api_tokens WHERE id = $1 FOR UPDATE`, tokenID).Scan(&accountID, &name, &secretRef, &scopes); errors.Is(err, sql.ErrNoRows) {
		return "", domain.ApiToken{}, domain.ErrApiTokenNotFound
	} else if err != nil {
		return "", domain.ApiToken{}, fmt.Errorf("read api token for rotate: %w", err)
	}

	now := s.now()
	raw, err := randomToken(32)
	if err != nil {
		return "", domain.ApiToken{}, err
	}
	// Rotation keeps the scope set: scopes are immutable for a token's line.
	next := domain.ApiToken{ID: uuid.NewString(), Name: name, Scopes: domain.DecodeScopes(scopes), CreatedAt: now}
	var nextSecretValue any
	var nextSecretRef string
	if s.rawStore != nil {
		store, ok := s.rawStore.(transactionalRawTokenStore)
		if !ok || store == nil {
			return "", domain.ApiToken{}, errors.New("transactional raw token store is not configured")
		}
		ref, putErr := store.PutTx(ctx, txscope.Wrap(tx), rawTokenSecretPurpose, raw)
		nextSecretRef = strings.TrimSpace(ref)
		if putErr != nil {
			cleanupRawToken(ctx, s.rawStore, nextSecretRef)
			return "", domain.ApiToken{}, fmt.Errorf("persist encrypted rotated api token: %w", putErr)
		}
		if nextSecretRef == "" {
			return "", domain.ApiToken{}, errors.New("persist encrypted rotated api token: empty secret reference")
		}
		nextSecretValue = nextSecretRef
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = $1`, tokenID); err != nil {
		cleanupRawToken(ctx, s.rawStore, nextSecretRef)
		return "", domain.ApiToken{}, fmt.Errorf("revoke old api token: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens(id, account_id, name, token_hash, secret_ref, scopes) VALUES ($1, $2, $3, $4, $5, $6)`, next.ID, accountID, name, s.tokenHash(raw), nextSecretValue, scopes); err != nil {
		cleanupRawToken(ctx, s.rawStore, nextSecretRef)
		return "", domain.ApiToken{}, fmt.Errorf("create rotated api token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		cleanupRawToken(ctx, s.rawStore, nextSecretRef)
		return "", domain.ApiToken{}, fmt.Errorf("commit rotate api token: %w", err)
	}
	if s.rawStore != nil && strings.TrimSpace(secretRef) != "" {
		// Raw-value cleanup is deliberately post-commit and nontransactional;
		// the token rotation itself is already durable and must not use a nil
		// transaction scope for cleanup.
		_ = s.rawStore.Delete(ctx, secretRef)
	}
	return raw, next, nil
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func cleanupRawToken(ctx context.Context, store rawTokenStore, ref string) {
	if store != nil && strings.TrimSpace(ref) != "" {
		_ = store.Delete(ctx, ref)
	}
}

func hashToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *Service) tokenHash(raw string) string {
	if len(s.secret) == 0 {
		return hashToken(raw)
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}
