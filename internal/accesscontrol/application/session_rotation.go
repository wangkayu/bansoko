package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrSessionRotationSecretInvalid reports a new session secret that is too short
// to key the session and API-token hashes.
var ErrSessionRotationSecretInvalid = errors.New("the new session secret must be at least 32 characters")

// ErrSessionRotationOtherSessions reports other open connections to the
// database while requireExclusive is set (the app is still running).
var ErrSessionRotationOtherSessions = errors.New("other sessions are connected to the database; stop the app before rotating the session secret")

// SessionRotationResult reports what one rotation changed. Tokens whose
// raw value could not be recovered are dropped: after the rotation their stored
// hash matches nothing, so keeping the row would show a token that cannot
// authenticate.
type SessionRotationResult struct {
	SessionsRevoked int64
	TokensRehashed  int
	TokensDropped   []string
}

// RotateSessionSecret re-keys every stored credential hash for a new session
// secret (SEC-033). The secret is never stored in PostgreSQL: session tokens are
// HMAC'd with it, so a new secret ends every web session, and the API tokens are
// re-hashed from their encrypted raw values so existing integrations keep
// working. Every unrecognizable token is dropped rather than left dead.
//
// Sessions are revoked and tokens re-hashed in one transaction. resolve opens an
// encrypted raw token value; without it no token survives the rotation.
// requireExclusive refuses to run while another client is connected to the same
// database, which catches a still-running app before the secret changes.
func RotateSessionSecret(ctx context.Context, db *sql.DB, newSecret []byte, resolve func(string) (string, bool), requireExclusive bool) (SessionRotationResult, error) {
	if db == nil {
		return SessionRotationResult{}, errors.New("session rotation database is nil")
	}
	if len(strings.TrimSpace(string(newSecret))) < 32 {
		return SessionRotationResult{}, ErrSessionRotationSecretInvalid
	}
	mac := func(raw string) string {
		h := hmac.New(sha256.New, newSecret)
		_, _ = h.Write([]byte(raw))
		return hex.EncodeToString(h.Sum(nil))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRotationResult{}, fmt.Errorf("begin session rotation: %w", err)
	}
	defer tx.Rollback()
	if requireExclusive {
		var others int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pg_stat_activity
WHERE datname = current_database() AND pid <> pg_backend_pid() AND backend_type = 'client backend'`).Scan(&others); err != nil {
			return SessionRotationResult{}, fmt.Errorf("check other sessions: %w", err)
		}
		if others > 0 {
			return SessionRotationResult{}, fmt.Errorf("%w (%d connected)", ErrSessionRotationOtherSessions, others)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, secret_ref FROM api_tokens ORDER BY id`)
	if err != nil {
		return SessionRotationResult{}, fmt.Errorf("read api tokens: %w", err)
	}
	type tokenRow struct {
		id, name  string
		secretRef sql.NullString
	}
	var tokens []tokenRow
	for rows.Next() {
		var token tokenRow
		if err := rows.Scan(&token.id, &token.name, &token.secretRef); err != nil {
			rows.Close()
			return SessionRotationResult{}, fmt.Errorf("scan api token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SessionRotationResult{}, fmt.Errorf("read api tokens: %w", err)
	}
	rows.Close()

	result := SessionRotationResult{}
	revoked, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE revoked_at IS NULL`)
	if err != nil {
		return SessionRotationResult{}, fmt.Errorf("revoke sessions: %w", err)
	}
	if affected, err := revoked.RowsAffected(); err == nil {
		result.SessionsRevoked = affected
	}

	for _, token := range tokens {
		raw, ok := "", false
		if resolve != nil && token.secretRef.Valid && strings.TrimSpace(token.secretRef.String) != "" {
			raw, ok = resolve(token.secretRef.String)
		}
		if !ok || strings.TrimSpace(raw) == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = $1`, token.id); err != nil {
				return SessionRotationResult{}, fmt.Errorf("drop api token %s: %w", token.id, err)
			}
			result.TokensDropped = append(result.TokensDropped, token.name)
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET token_hash = $1 WHERE id = $2`, mac(raw), token.id); err != nil {
			return SessionRotationResult{}, fmt.Errorf("rehash api token %s: %w", token.id, err)
		}
		result.TokensRehashed++
	}
	if err := tx.Commit(); err != nil {
		return SessionRotationResult{}, fmt.Errorf("commit session rotation: %w", err)
	}
	return result, nil
}
