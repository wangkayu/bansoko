package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
)

const (
	rotationOldKey   = "rotation-test-old-key-0123456789abcdef"
	rotationNewKey   = "rotation-test-new-key-0123456789abcdef"
	rotationOtherKey = "rotation-test-other-key-0123456789abcdef"
)

func rotationStore(t *testing.T, key string, db *sql.DB) *DatabaseSecretStore {
	t.Helper()
	store, err := NewDatabaseSecretStore(db, []byte(key), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestRotateDeploymentKeyPostgres covers SEC-033 key rotation: every row is
// re-encrypted under the new key in one transaction (version bumped, value
// unchanged, old key no longer opens it); a wrong current key, a row the
// current key cannot open, an unchanged key and a second run change nothing;
// another connected session is refused when exclusivity is required.
func TestRotateDeploymentKeyPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM secret_values`); err != nil {
		t.Fatal(err)
	}
	old := rotationStore(t, rotationOldKey, db)
	values := map[string]string{}
	for purpose, value := range map[string]string{
		"connection:qbittorrent": "qb-password",
		"plugin-target:webhook":  "hmac-secret",
		"api-token-raw:token-1":  "pat-raw-value",
	} {
		ref, err := old.Put(ctx, purpose, value)
		if err != nil {
			t.Fatal(err)
		}
		values[ref] = value
	}
	snapshot := func() map[string]string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT ref, encode(nonce, 'hex') || ':' || encode(ciphertext, 'hex') || ':' || version FROM secret_values`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var ref, state string
			if err := rows.Scan(&ref, &state); err != nil {
				t.Fatal(err)
			}
			out[ref] = state
		}
		return out
	}
	unchanged := func(surface string, before map[string]string) {
		t.Helper()
		after := snapshot()
		if len(after) != len(before) {
			t.Fatalf("%s changed the row count", surface)
		}
		for ref, state := range before {
			if after[ref] != state {
				t.Fatalf("%s wrote row %s", surface, ref)
			}
		}
	}

	before := snapshot()
	for name, keys := range map[string][2]string{
		"unchanged key":     {rotationOldKey, rotationOldKey},
		"short new key":     {rotationOldKey, "short"},
		"missing current":   {"", rotationNewKey},
		"wrong current key": {rotationOtherKey, rotationNewKey},
	} {
		if _, err := RotateDeploymentKey(ctx, db, []byte(keys[0]), []byte(keys[1]), RotateOptions{}); err == nil {
			t.Fatalf("%s must be refused", name)
		}
		unchanged(name, before)
	}
	var decryptErr *RotationDecryptError
	if _, err := RotateDeploymentKey(ctx, db, []byte(rotationOtherKey), []byte(rotationNewKey), RotateOptions{}); !errors.As(err, &decryptErr) || len(decryptErr.Failed) != 3 || decryptErr.AlreadyNew {
		t.Fatalf("a wrong current key must report every row: %v", err)
	}

	// One row sealed under another key blocks the whole rotation.
	stray, err := rotationStore(t, rotationOtherKey, db).Put(ctx, "connection:stray", "stray-value")
	if err != nil {
		t.Fatal(err)
	}
	before = snapshot()
	if _, err := RotateDeploymentKey(ctx, db, []byte(rotationOldKey), []byte(rotationNewKey), RotateOptions{}); !errors.As(err, &decryptErr) || len(decryptErr.Failed) != 1 || decryptErr.Failed[0] != stray {
		t.Fatalf("a row the current key cannot open must abort the rotation: %v", err)
	}
	unchanged("partial-key rotation", before)
	if _, err := db.ExecContext(ctx, `DELETE FROM secret_values WHERE ref = $1`, stray); err != nil {
		t.Fatal(err)
	}

	// Exclusivity: another connected session refuses the run.
	held, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	before = snapshot()
	if _, err := RotateDeploymentKey(ctx, db, []byte(rotationOldKey), []byte(rotationNewKey), RotateOptions{RequireExclusive: true}); !errors.Is(err, ErrRotationOtherSessions) {
		t.Fatalf("another session must refuse an exclusive rotation: %v", err)
	}
	held.Close()
	unchanged("non-exclusive rotation", before)

	rotated, err := RotateDeploymentKey(ctx, db, []byte(rotationOldKey), []byte(rotationNewKey), RotateOptions{})
	if err != nil || rotated != 3 {
		t.Fatalf("rotate: %d %v", rotated, err)
	}
	newStore := rotationStore(t, rotationNewKey, db)
	for ref, want := range values {
		if got, ok := newStore.Resolve(ref); !ok || got != want {
			t.Fatalf("%s under the new key = %q %v, want %q", ref, got, ok, want)
		}
		if _, ok := old.Resolve(ref); ok {
			t.Fatalf("%s still opens with the old key", ref)
		}
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT version FROM secret_values WHERE ref = $1`, ref).Scan(&version); err != nil || version != 2 {
			t.Fatalf("%s version = %d %v, want 2", ref, version, err)
		}
	}

	// Running it again with the old key as "current" changes nothing and
	// says the rotation already happened.
	before = snapshot()
	if _, err := RotateDeploymentKey(ctx, db, []byte(rotationOldKey), []byte(rotationNewKey), RotateOptions{}); !errors.As(err, &decryptErr) || !decryptErr.AlreadyNew {
		t.Fatalf("a repeated rotation must report it already ran: %v", err)
	}
	unchanged("repeated rotation", before)

	// An empty table rotates nothing and succeeds.
	if _, err := db.ExecContext(ctx, `DELETE FROM secret_values`); err != nil {
		t.Fatal(err)
	}
	if rotated, err := RotateDeploymentKey(ctx, db, []byte(rotationNewKey), []byte(rotationOldKey), RotateOptions{}); err != nil || rotated != 0 {
		t.Fatalf("empty rotation: %d %v", rotated, err)
	}
}
