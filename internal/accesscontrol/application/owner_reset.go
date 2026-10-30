package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"bansoko/internal/accesscontrol/domain"
)

// ResetOwnerCredentials is the operator password reset behind
// `/app/bansoko-server bootstrap reset-password`. The command runs inside the container, so
// the authority is physical access to the deployment; no network credential
// gates it. One transaction:
//
//   - replaces the single owner credential (username unchanged when empty);
//   - revokes every outstanding user session so a lost-credential incident
//     cannot keep stale logins alive (session authentication is a per-request
//     database lookup, so revocation is visible immediately);
//   - keeps long-lived API tokens: they are scoped and machine-held, bound to
//     the surviving owner account id.
//
// The instance must already be bootstrapped (state ready); a fresh
// uninitialized instance creates its owner through the first-visit flow
// instead. No session is opened: the operator logs in normally afterwards.
func (s *Service) ResetOwnerCredentials(ctx context.Context, username, password string) (domain.Account, int, error) {
	if s.db == nil {
		return domain.Account{}, 0, errors.New("access control database is nil")
	}
	username = strings.TrimSpace(username)
	if username != "" && !validUsername(username) {
		return domain.Account{}, 0, fmt.Errorf("%w: username must be at most 128 characters", domain.ErrOwnerInputInvalid)
	}
	if !validPassword(password) {
		return domain.Account{}, 0, fmt.Errorf("%w: password must be between 12 and 1024 characters", domain.ErrOwnerInputInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Account{}, 0, fmt.Errorf("begin owner reset transaction: %w", err)
	}
	defer tx.Rollback()

	var state string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM instance_bootstrap WHERE singleton_id = 1 FOR UPDATE`).Scan(&state); err != nil {
		return domain.Account{}, 0, fmt.Errorf("lock bootstrap state: %w", err)
	}
	if state != string(domain.StateReady) {
		return domain.Account{}, 0, domain.ErrSetupNotFinalized
	}

	hash, err := hashPassword(password)
	if err != nil {
		return domain.Account{}, 0, fmt.Errorf("hash owner password: %w", err)
	}
	var account domain.Account
	err = tx.QueryRowContext(ctx,
		`SELECT id, username FROM local_accounts WHERE role = 'owner' ORDER BY created_at LIMIT 1`).Scan(&account.ID, &account.Username)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A ready instance without an owner row is inconsistent; the reset
		// repairs it by recreating the owner so the operator is never locked
		// out of a ready instance.
		if username == "" {
			return domain.Account{}, 0, fmt.Errorf("%w: username is required when no owner account exists", domain.ErrOwnerInputInvalid)
		}
		account = domain.Account{ID: uuid.NewString(), Username: username, Role: "owner"}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO local_accounts(id, username, password_hash, role) VALUES ($1, $2, $3, 'owner')`,
			account.ID, account.Username, string(hash)); err != nil {
			return domain.Account{}, 0, fmt.Errorf("create owner: %w", err)
		}
	case err != nil:
		return domain.Account{}, 0, fmt.Errorf("read owner: %w", err)
	default:
		account.Role = "owner"
		if username != "" {
			account.Username = username
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE local_accounts SET username = $2, password_hash = $3, version = version + 1, updated_at = NOW() WHERE id = $1`,
			account.ID, account.Username, string(hash)); err != nil {
			return domain.Account{}, 0, fmt.Errorf("replace owner credentials: %w", err)
		}
	}
	// Credential loss is an ownership incident: every live user session dies
	// with the reset. Long-lived API tokens stay bound to the same account id
	// and are deliberately preserved so agent tooling survives the reset.
	revoked, err := tx.ExecContext(ctx,
		`UPDATE auth_sessions SET revoked_at = NOW() WHERE revoked_at IS NULL`)
	if err != nil {
		return domain.Account{}, 0, fmt.Errorf("revoke outstanding sessions: %w", err)
	}
	revokedCount, err := revoked.RowsAffected()
	if err != nil {
		return domain.Account{}, 0, fmt.Errorf("count revoked sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, 0, fmt.Errorf("commit owner reset: %w", err)
	}
	return account, int(revokedCount), nil
}
