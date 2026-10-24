package persistence

import (
	"context"
	"testing"

	"bansoko/internal/persistence/txscope"
)

func TestRepositoryRejectsInvalidTransactionalScope(t *testing.T) {
	repository := NewRepository(nil)
	var scope txscope.Tx
	record := SecretRecord{Ref: "db:v1:test", Purpose: "test", Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext")}

	if err := repository.Insert(context.Background(), scope, record); err == nil {
		t.Fatal("expected insert to reject invalid transaction scope")
	}
	if err := repository.Delete(context.Background(), scope, record.Ref); err == nil {
		t.Fatal("expected delete to reject invalid transaction scope")
	}
}

func TestRepositoryGetRejectsMissingDatabase(t *testing.T) {
	if _, err := NewRepository(nil).Get(context.Background(), "db:v1:test"); err == nil {
		t.Fatal("expected get to reject missing database")
	}
}
