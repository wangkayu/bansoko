package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/accesscontrol/domain"
)

// CompleteBootstrapOwner is the bootstrap completion: the first
// visit directly creates the first owner account, marks the instance ready,
// and opens a normal owner session in one transaction. There is no bootstrap
// grant, setup session, or setup token; the deployer is responsible for
// network-level trust (the instance is expected to be unreachable from the
// public internet until the owner exists). There is no separate
// finalize step, setup finalization rows, or lifecycle evidence chain; the
// owner account and the bootstrap singleton are the product truth.
func (s *Service) CompleteBootstrapOwner(ctx context.Context, username, password string, ttl time.Duration) (domain.Account, string, time.Time, error) {
	if s.db == nil {
		return domain.Account{}, "", time.Time{}, errors.New("access control database is nil")
	}
	username = strings.TrimSpace(username)
	if !validUsername(username) {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("%w: username is required and must be at most 128 characters", domain.ErrOwnerInputInvalid)
	}
	if !validPassword(password) {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("%w: password must be between 12 and 1024 characters", domain.ErrOwnerInputInvalid)
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("begin bootstrap owner transaction: %w", err)
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM instance_bootstrap WHERE singleton_id = 1 FOR UPDATE`).Scan(&state); err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("lock bootstrap state: %w", err)
	}
	if state == string(domain.StateReady) {
		return domain.Account{}, "", time.Time{}, domain.ErrSetupComplete
	}
	// Bootstrap completes only from the fresh "uninitialized" state and
	// transitions directly to "ready".
	if state != string(domain.StateUninitialized) {
		return domain.Account{}, "", time.Time{}, domain.ErrSetupNotFinalized
	}
	var ownerCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_accounts`).Scan(&ownerCount); err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("count owners: %w", err)
	}
	if ownerCount > 0 {
		return domain.Account{}, "", time.Time{}, domain.ErrOwnerExists
	}
	hash, err := hashPassword(password)
	if err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("hash owner password: %w", err)
	}
	account := domain.Account{ID: uuid.NewString(), Username: username, Role: "owner"}
	if _, err := tx.ExecContext(ctx, `INSERT INTO local_accounts(id, username, password_hash, role) VALUES ($1, $2, $3, 'owner')`, account.ID, account.Username, string(hash)); err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("create owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE instance_bootstrap
SET state = 'ready', completed_at = NOW(), version = version + 1, updated_at = NOW()
WHERE singleton_id = 1 AND state = 'uninitialized'`); err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("mark bootstrap ready: %w", err)
	}
	raw, err := randomToken(32)
	if err != nil {
		return domain.Account{}, "", time.Time{}, err
	}
	sessionExpiresAt := s.now().Add(ttl)
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_sessions(id, account_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`, uuid.NewString(), account.ID, s.tokenHash(raw), sessionExpiresAt); err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("create owner session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, "", time.Time{}, fmt.Errorf("commit bootstrap owner: %w", err)
	}
	return account, raw, sessionExpiresAt, nil
}

// RevokeSession invalidates the presented user session. Revoking an unknown
// or already revoked session is a no-op so logout stays idempotent.
func (s *Service) RevokeSession(ctx context.Context, rawToken string) error {
	if s.db == nil || strings.TrimSpace(rawToken) == "" {
		return domain.ErrInvalidCredentials
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE token_hash = $1 AND revoked_at IS NULL`, s.tokenHash(rawToken)); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// SessionExpiry reports when the presented user session expires.
func (s *Service) SessionExpiry(ctx context.Context, rawToken string) (time.Time, error) {
	if s.db == nil || strings.TrimSpace(rawToken) == "" {
		return time.Time{}, domain.ErrInvalidCredentials
	}
	var expiresAt time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM auth_sessions WHERE token_hash = $1 AND revoked_at IS NULL`, s.tokenHash(rawToken)).Scan(&expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, domain.ErrInvalidCredentials
		}
		return time.Time{}, fmt.Errorf("read session expiry: %w", err)
	}
	return expiresAt, nil
}
