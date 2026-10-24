package application

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Deployment key rotation (SEC-033).
//
// Every encrypted value lives in secret_values (connection secrets, plugin
// target secrets, API token raw values). RotateDeploymentKey re-encrypts all of
// them from the old BANSOKO_SECRET_ENCRYPTION_KEY to a new one in one transaction:
// the table is locked, every row must first decrypt with the old key (one
// failure aborts with nothing written), each row is sealed again under the new
// key with a fresh nonce, and every row is read back and opened with the new
// key before commit. The app must be stopped: a running process would keep
// sealing new rows with the old key.

// ErrRotationKeyInvalid reports a missing, short or unchanged key.
var ErrRotationKeyInvalid = errors.New("secret key rotation needs the current key and a different new key of at least 32 characters")

// ErrRotationOtherSessions reports other open connections to the database
// while RotateOptions.RequireExclusive is set (the app is still running).
var ErrRotationOtherSessions = errors.New("other sessions are connected to the database; stop the app before rotating the secret key")

// RotationDecryptError lists the rows the given key cannot open. Refs are
// opaque identifiers, never secret values.
type RotationDecryptError struct {
	Total  int
	Failed []string
	// AlreadyNew is true when every row already opens with the new key: the
	// rotation has run before and the "current" key given is the old one.
	AlreadyNew bool
}

func (e *RotationDecryptError) Error() string {
	if e.AlreadyNew {
		return fmt.Sprintf("all %d secret values already decrypt with the new key; nothing to rotate (set BANSOKO_SECRET_ENCRYPTION_KEY to the new key)", e.Total)
	}
	shown := e.Failed
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return fmt.Sprintf("%d of %d secret values do not decrypt with the current key (first: %s); nothing was changed", len(e.Failed), e.Total, strings.Join(shown, ", "))
}

// RotateOptions controls the safety checks of RotateDeploymentKey.
type RotateOptions struct {
	// RequireExclusive refuses to run while any other client session is
	// connected to the same database.
	RequireExclusive bool
}

// RotateDeploymentKey re-encrypts every stored secret value and returns how
// many rows it rotated. On any error nothing is written.
func RotateDeploymentKey(ctx context.Context, db *sql.DB, currentKey, newKey []byte, options RotateOptions) (int, error) {
	if db == nil {
		return 0, errors.New("secret key rotation database is nil")
	}
	if len(strings.TrimSpace(string(currentKey))) < 32 || len(strings.TrimSpace(string(newKey))) < 32 {
		return 0, ErrRotationKeyInvalid
	}
	currentAEAD, err := deploymentAEAD(currentKey)
	if err != nil {
		return 0, err
	}
	newAEAD, err := deploymentAEAD(newKey)
	if err != nil {
		return 0, err
	}
	if digest := sha256.Sum256(currentKey); digest == sha256.Sum256(newKey) {
		return 0, ErrRotationKeyInvalid
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin key rotation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE secret_values IN ACCESS EXCLUSIVE MODE`); err != nil {
		return 0, fmt.Errorf("lock secret values: %w", err)
	}
	if options.RequireExclusive {
		var others int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pg_stat_activity
WHERE datname = current_database() AND pid <> pg_backend_pid() AND backend_type = 'client backend'`).Scan(&others); err != nil {
			return 0, fmt.Errorf("check other sessions: %w", err)
		}
		if others > 0 {
			return 0, fmt.Errorf("%w (%d connected)", ErrRotationOtherSessions, others)
		}
	}

	type row struct {
		ref, purpose string
		plaintext    []byte
	}
	rows, err := tx.QueryContext(ctx, `SELECT ref, purpose, nonce, ciphertext FROM secret_values ORDER BY ref`)
	if err != nil {
		return 0, fmt.Errorf("read secret values: %w", err)
	}
	var decrypted []row
	failure := &RotationDecryptError{}
	openedWithNew := 0
	for rows.Next() {
		var ref, purpose string
		var nonce, ciphertext []byte
		if err := rows.Scan(&ref, &purpose, &nonce, &ciphertext); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan secret value: %w", err)
		}
		failure.Total++
		plaintext, ok := openSecret(currentAEAD, nonce, ciphertext, purpose)
		if !ok {
			failure.Failed = append(failure.Failed, ref)
			if _, ok := openSecret(newAEAD, nonce, ciphertext, purpose); ok {
				openedWithNew++
			}
			continue
		}
		decrypted = append(decrypted, row{ref: ref, purpose: purpose, plaintext: plaintext})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("read secret values: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read secret values: %w", err)
	}
	if len(failure.Failed) > 0 {
		failure.AlreadyNew = openedWithNew == failure.Total
		return 0, failure
	}

	for _, value := range decrypted {
		nonce := make([]byte, newAEAD.NonceSize())
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return 0, fmt.Errorf("generate secret nonce: %w", err)
		}
		sealed := newAEAD.Seal(nil, nonce, value.plaintext, []byte(value.purpose))
		result, err := tx.ExecContext(ctx, `
UPDATE secret_values
SET nonce = $2, ciphertext = $3, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE ref = $1`, value.ref, nonce, sealed)
		if err != nil {
			return 0, fmt.Errorf("re-encrypt secret value %s: %w", value.ref, err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			return 0, fmt.Errorf("re-encrypt secret value %s: %d rows affected (%v)", value.ref, affected, err)
		}
	}

	// Read every row back and open it with the new key before commit.
	for _, value := range decrypted {
		var nonce, ciphertext []byte
		if err := tx.QueryRowContext(ctx, `SELECT nonce, ciphertext FROM secret_values WHERE ref = $1`, value.ref).Scan(&nonce, &ciphertext); err != nil {
			return 0, fmt.Errorf("verify secret value %s: %w", value.ref, err)
		}
		plaintext, ok := openSecret(newAEAD, nonce, ciphertext, value.purpose)
		if !ok || !bytes.Equal(plaintext, value.plaintext) {
			return 0, fmt.Errorf("verify secret value %s: does not decrypt with the new key; nothing was changed", value.ref)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit key rotation: %w", err)
	}
	return len(decrypted), nil
}

// deploymentAEAD derives the AES-256-GCM AEAD exactly as DatabaseSecretStore
// does (SHA-256 of the deployment key).
func deploymentAEAD(deploymentKey []byte) (cipher.AEAD, error) {
	digest := sha256.Sum256(deploymentKey)
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, fmt.Errorf("create secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create secret AEAD: %w", err)
	}
	return aead, nil
}

func openSecret(aead cipher.AEAD, nonce, ciphertext []byte, purpose string) ([]byte, bool) {
	if len(nonce) != aead.NonceSize() {
		return nil, false
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return nil, false
	}
	return plaintext, true
}
