package application

import (
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

	"bansoko/internal/persistence/txscope"
	"github.com/google/uuid"
)

// DatabaseSecretStore keeps user-provided credentials encrypted at rest. The
// deployment key is supplied by the runtime secret manager and is never
// persisted in PostgreSQL. The database stores only an opaque reference and
// an authenticated ciphertext.
type DatabaseSecretStore struct {
	db         *sql.DB
	key        []byte
	repository SecretRepository
}

func NewDatabaseSecretStore(db *sql.DB, deploymentKey []byte, repository SecretRepository) (*DatabaseSecretStore, error) {
	if db == nil {
		return nil, errors.New("database secret store database is nil")
	}
	if repository == nil {
		return nil, errors.New("database secret store repository is nil")
	}
	if len(strings.TrimSpace(string(deploymentKey))) < 32 {
		return nil, errors.New("database secret store deployment key must be at least 32 bytes")
	}
	digest := sha256.Sum256(deploymentKey)
	return &DatabaseSecretStore{db: db, key: append([]byte(nil), digest[:]...), repository: repository}, nil
}

func (s *DatabaseSecretStore) Put(ctx context.Context, purpose, value string) (string, error) {
	if s == nil || s.db == nil || s.repository == nil {
		return "", errors.New("database secret store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin secret transaction: %w", err)
	}
	defer tx.Rollback()
	ref, err := s.PutTx(ctx, txscope.Wrap(tx), purpose, value)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit secret transaction: %w", err)
	}
	return ref, nil
}

// PutTx persists a new immutable secret version inside the caller's transaction.
// The caller owns commit/rollback together with its configuration owner.
func (s *DatabaseSecretStore) PutTx(ctx context.Context, scope txscope.Tx, purpose, value string) (string, error) {
	if s == nil || scope.SQLTx() == nil || s.repository == nil {
		return "", errors.New("database secret store transaction is not configured")
	}
	purpose = strings.TrimSpace(purpose)
	if purpose == "" || strings.TrimSpace(value) == "" {
		return "", errors.New("secret purpose and value are required")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("create secret cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create secret AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate secret nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), []byte(purpose))
	ref := newSecretReference()
	if err := s.repository.Insert(ctx, scope, SecretRecord{Ref: ref, Purpose: purpose, Nonce: nonce, Ciphertext: ciphertext}); err != nil {
		return "", fmt.Errorf("persist encrypted secret: %w", err)
	}
	return ref, nil
}

func (s *DatabaseSecretStore) Delete(ctx context.Context, ref string) error {
	if s == nil || s.db == nil || s.repository == nil {
		return errors.New("database secret store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin secret deletion transaction: %w", err)
	}
	defer tx.Rollback()
	if err := s.DeleteTx(ctx, txscope.Wrap(tx), ref); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit secret deletion transaction: %w", err)
	}
	return nil
}

// DeleteTx physically deletes an encrypted secret inside the caller's transaction.
// Callers must first prove that no owner or frozen evidence still references it.
func (s *DatabaseSecretStore) DeleteTx(ctx context.Context, scope txscope.Tx, ref string) error {
	if s == nil || scope.SQLTx() == nil || s.repository == nil {
		return errors.New("database secret store transaction is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if err := s.repository.Delete(ctx, scope, ref); err != nil {
		return fmt.Errorf("delete encrypted secret: %w", err)
	}
	return nil
}

func newSecretReference() string {
	return "db:v1:" + uuid.NewString()
}

// Resolve implements SecretResolver. The resolver interface intentionally has
// no context, so a lookup uses a background database operation and returns
// false for unavailable, malformed, or tampered values.
func (s *DatabaseSecretStore) Resolve(ref string) (string, bool) {
	if s == nil || s.repository == nil || !strings.HasPrefix(ref, "db:v1:") {
		return "", false
	}
	record, err := s.repository.Get(context.Background(), ref)
	if err != nil {
		return "", false
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(record.Nonce) != gcm.NonceSize() {
		return "", false
	}
	plaintext, err := gcm.Open(nil, record.Nonce, record.Ciphertext, []byte(record.Purpose))
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}
