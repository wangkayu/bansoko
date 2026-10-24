package application

import (
	"context"
	"testing"

	"bansoko/internal/persistence/txscope"
)

func TestDatabaseSecretStoreRequiresDatabaseAndDeploymentKey(t *testing.T) {
	if _, err := NewDatabaseSecretStore(nil, []byte("too-short"), nil); err == nil {
		t.Fatal("expected database and deployment key validation")
	}
}

func TestDatabaseSecretStoreTransactionalMethodsRejectInvalidScope(t *testing.T) {
	const want = "database secret store transaction is not configured"
	var invalidScope txscope.Tx

	stores := map[string]*DatabaseSecretStore{
		"nil receiver":        nil,
		"configured receiver": &DatabaseSecretStore{},
	}
	for name, store := range stores {
		store := store
		t.Run(name+" put", func(t *testing.T) {
			_, err := store.PutTx(context.Background(), invalidScope, "purpose", "value")
			if err == nil || err.Error() != want {
				t.Fatalf("PutTx error = %v, want %q", err, want)
			}
		})
		t.Run(name+" delete", func(t *testing.T) {
			err := store.DeleteTx(context.Background(), invalidScope, "ref")
			if err == nil || err.Error() != want {
				t.Fatalf("DeleteTx error = %v, want %q", err, want)
			}
		})
	}
}
