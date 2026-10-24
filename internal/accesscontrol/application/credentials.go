package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/accesscontrol/domain"
)

// UpdateOwnerCredentials changes the signed-in owner's username and/or
// password. accountID is the owner the transport already authenticated;
// currentPassword is always re-checked: it is the only guard between a stolen
// session token and a permanent credential takeover.
//
// A username change keeps every session and API token alive. A password
// change revokes every other web session of the account in the same
// transaction but keeps the current session (the caller just proved the old
// password) and keeps API tokens, which stay bound to the account id. An
// empty current session token revokes every session of the account.
//
// There is one owner, and each request states its own intent, so the
// current-password re-check is the concurrency guard; no version CAS is
// needed and a stale write cannot silently overwrite an unseen whole row.
func (s *Service) UpdateOwnerCredentials(
	ctx context.Context,
	accountID string,
	currentSessionToken string,
	currentPassword string,
	username *string,
	newPassword *string,
) (domain.Account, bool, error) {
	if s.db == nil {
		return domain.Account{}, false, errors.New("access control database is nil")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return domain.Account{}, false, domain.ErrInvalidCredentials
	}
	var nextUsername *string
	if username != nil {
		trimmed := strings.TrimSpace(*username)
		if !validUsername(trimmed) {
			return domain.Account{}, false, fmt.Errorf("%w: username is required and must be at most 128 characters", domain.ErrOwnerInputInvalid)
		}
		nextUsername = &trimmed
	}
	if newPassword != nil && !validPassword(*newPassword) {
		return domain.Account{}, false, fmt.Errorf("%w: password must be between 12 and 1024 characters", domain.ErrOwnerInputInvalid)
	}
	if nextUsername == nil && newPassword == nil {
		return domain.Account{}, false, fmt.Errorf("%w: a username or a new password is required", domain.ErrOwnerInputInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Account{}, false, fmt.Errorf("begin credential update: %w", err)
	}
	defer tx.Rollback()

	var currentUsername, role, passwordHash string
	err = tx.QueryRowContext(ctx, `
SELECT id, username, role, password_hash
FROM local_accounts
WHERE id = $1
FOR UPDATE`, accountID).
		Scan(&accountID, &currentUsername, &role, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Account{}, false, domain.ErrInvalidCredentials
	}
	if err != nil {
		return domain.Account{}, false, fmt.Errorf("read account for credential update: %w", err)
	}
	if !verifyPassword(passwordHash, currentPassword) {
		return domain.Account{}, false, domain.ErrCurrentPasswordInvalid
	}

	updatedUsername := currentUsername
	if nextUsername != nil {
		updatedUsername = *nextUsername
	}
	updatedHash := passwordHash
	if newPassword != nil {
		hashed, hashErr := hashPassword(*newPassword)
		if hashErr != nil {
			return domain.Account{}, false, fmt.Errorf("hash owner password: %w", hashErr)
		}
		updatedHash = hashed
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE local_accounts
SET username = $2, password_hash = $3, version = version + 1, updated_at = NOW()
WHERE id = $1`, accountID, updatedUsername, updatedHash); err != nil {
		return domain.Account{}, false, fmt.Errorf("update owner credentials: %w", err)
	}

	revokedOtherSessions := false
	if newPassword != nil {
		var result sql.Result
		var err error
		if token := strings.TrimSpace(currentSessionToken); token != "" {
			result, err = tx.ExecContext(ctx, `
UPDATE auth_sessions
SET revoked_at = NOW()
WHERE account_id = $1 AND token_hash <> $2 AND revoked_at IS NULL`,
				accountID, s.tokenHash(token))
		} else {
			result, err = tx.ExecContext(ctx, `
UPDATE auth_sessions
SET revoked_at = NOW()
WHERE account_id = $1 AND revoked_at IS NULL`, accountID)
		}
		if err != nil {
			return domain.Account{}, false, fmt.Errorf("revoke other owner sessions: %w", err)
		}
		if affected, err := result.RowsAffected(); err == nil {
			revokedOtherSessions = affected > 0
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, false, fmt.Errorf("commit credential update: %w", err)
	}
	return domain.Account{ID: accountID, Username: updatedUsername, Role: role}, revokedOtherSessions, nil
}
