// Package port defines the narrow cross-context boundary for encrypted configuration secrets.
package port

import (
	"context"

	"bansoko/internal/persistence/txscope"
)

// SecretRecord is an encrypted secret value owned by configuration persistence.
type SecretRecord struct {
	Ref        string
	Purpose    string
	Nonce      []byte
	Ciphertext []byte
}

// SecretRepository persists encrypted values without owning the caller's transaction.
type SecretRepository interface {
	Insert(context.Context, txscope.Tx, SecretRecord) error
	Delete(context.Context, txscope.Tx, string) error
	Get(context.Context, string) (SecretRecord, error)
}
