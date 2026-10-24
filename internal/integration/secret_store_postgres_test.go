package integration

import (
	"context"
	"strings"
	"testing"

	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configuration "bansoko/internal/configuration/application"
	"bansoko/internal/testutil/disposabledb"
)

func TestDatabaseSecretStorePersistsOnlyAuthenticatedCiphertextPostgres(t *testing.T) {
	// Reset through the disposable guard: the test must not depend on an
	// earlier test having migrated the shared schema.
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store, err := configuration.NewDatabaseSecretStore(db, []byte("test-deployment-key-that-is-at-least-32-bytes"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	value := "s2a-secret-value"
	ref, err := store.Put(ctx, "integration:test", value)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(ctx, ref) })
	if !strings.HasPrefix(ref, "db:v1:") {
		t.Fatalf("unexpected secret ref %q", ref)
	}
	var purpose string
	var nonce, ciphertext []byte
	if err := db.QueryRowContext(ctx, `SELECT purpose, nonce, ciphertext FROM secret_values WHERE ref = $1`, ref).Scan(&purpose, &nonce, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if purpose != "integration:test" || len(nonce) == 0 || len(ciphertext) == 0 || strings.Contains(string(ciphertext), value) {
		t.Fatalf("secret was not stored as opaque ciphertext: purpose=%q nonce=%d ciphertext=%d", purpose, len(nonce), len(ciphertext))
	}
	resolved, ok := store.Resolve(ref)
	if !ok || resolved != value {
		t.Fatalf("secret did not survive database-backed resolve: value=%q ok=%v", resolved, ok)
	}
	restarted, err := configuration.NewDatabaseSecretStore(db, []byte("test-deployment-key-that-is-at-least-32-bytes"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	if resolved, ok := restarted.Resolve(ref); !ok || resolved != value {
		t.Fatalf("secret did not survive a fresh store instance: value=%q ok=%v", resolved, ok)
	}
	wrongKey, err := configuration.NewDatabaseSecretStore(db, []byte("wrong-deployment-key-that-is-at-least-32-bytes"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wrongKey.Resolve(ref); ok {
		t.Fatal("a different deployment key must not decrypt the secret")
	}
}
