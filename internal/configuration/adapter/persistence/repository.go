// Package persistence implements the PostgreSQL adapter for encrypted configuration secrets.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	generated "bansoko/internal/configuration/adapter/persistence/generated"
	configurationport "bansoko/internal/configuration/port"
	"bansoko/internal/persistence/txscope"
)

// SecretRecord is the encrypted value exchanged between the application and adapter.
type SecretRecord = configurationport.SecretRecord

// Repository owns secret SQL and never controls a caller-owned transaction.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Insert(ctx context.Context, scope txscope.Tx, record SecretRecord) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("secret repository transaction is nil")
	}
	if err := generated.New(tx).InsertSecret(ctx, generated.InsertSecretParams{
		Ref: record.Ref, Purpose: record.Purpose, Nonce: record.Nonce, Ciphertext: record.Ciphertext,
	}); err != nil {
		return fmt.Errorf("insert encrypted secret: %w", err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, scope txscope.Tx, ref string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("secret repository transaction is nil")
	}
	if err := generated.New(tx).DeleteSecret(ctx, ref); err != nil {
		return fmt.Errorf("delete encrypted secret: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, ref string) (SecretRecord, error) {
	if r == nil || r.db == nil {
		return SecretRecord{}, errors.New("secret repository database is nil")
	}
	row, err := generated.New(r.db).GetSecret(ctx, ref)
	if err != nil {
		return SecretRecord{}, err
	}
	return SecretRecord{Ref: row.Ref, Purpose: row.Purpose, Nonce: row.Nonce, Ciphertext: row.Ciphertext}, nil
}

var _ interface {
	Insert(context.Context, txscope.Tx, SecretRecord) error
	Delete(context.Context, txscope.Tx, string) error
	Get(context.Context, string) (SecretRecord, error)
} = (*Repository)(nil)
