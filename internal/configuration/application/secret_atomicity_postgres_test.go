package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/persistence/txscope"
)

type failingPutSecretStore struct {
	*DatabaseSecretStore
}

func (s failingPutSecretStore) PutTx(context.Context, txscope.Tx, string, string) (string, error) {
	return "", errors.New("injected secret write failure")
}

type secretOwnerHarness struct {
	name        string
	table       string
	id          string
	purposeLike string
	create      func(*Service, configurationdomain.SecretMutation) error
	update      func(*Service, int64, configurationdomain.SecretMutation) error
	read        func(*Service) (int64, string, error)
}

func TestConfigurationSecretOwnerAtomicityPostgres(t *testing.T) {
	if os.Getenv("BANSOKO_TEST_DATABASE_URL") == "" {
		t.Fatal("BANSOKO_TEST_DATABASE_URL is required for no-skip secret atomicity evidence")
	}
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	for _, harness := range atomicSecretOwnerHarnesses() {
		harness := harness
		t.Run(harness.name, func(t *testing.T) {
			t.Run("immutable versions survive replace and remove", func(t *testing.T) {
				cleanupAtomicSecretOwner(t, ctx, db, harness)
				store := newConnectionTestSecretStore(t, db)
				service := NewService(db, store)
				if err := harness.create(service, replaceSecret("first-secret")); err != nil {
					t.Fatal(err)
				}
				version, firstRef, err := harness.read(service)
				if err != nil {
					t.Fatal(err)
				}
				if err := harness.update(service, version, replaceSecret("second-secret")); err != nil {
					t.Fatal(err)
				}
				version, secondRef, err := harness.read(service)
				if err != nil {
					t.Fatal(err)
				}
				if firstRef == "" || secondRef == "" || firstRef == secondRef {
					t.Fatalf("replace must select a new immutable ref: first=%q second=%q", firstRef, secondRef)
				}
				assertResolvedSecret(t, store, firstRef, "first-secret")
				assertResolvedSecret(t, store, secondRef, "second-secret")
				if err := harness.update(service, version, clearSecret()); err != nil {
					t.Fatal(err)
				}
				version, currentRef, err := harness.read(service)
				if err != nil {
					t.Fatal(err)
				}
				if version != 3 || currentRef != "" {
					t.Fatalf("remove must only detach the owner ref: version=%d ref=%q", version, currentRef)
				}
				assertResolvedSecret(t, store, firstRef, "first-secret")
				assertResolvedSecret(t, store, secondRef, "second-secret")
				assertSecretCount(t, ctx, db, harness.purposeLike, 2)
			})

			t.Run("create secret write failure", func(t *testing.T) {
				cleanupAtomicSecretOwner(t, ctx, db, harness)
				store := newConnectionTestSecretStore(t, db)
				service := NewService(db, failingPutSecretStore{DatabaseSecretStore: store})
				if err := harness.create(service, replaceSecret("never-visible")); err == nil {
					t.Fatal("expected injected secret write failure")
				}
				assertOwnerAbsent(t, ctx, db, harness)
				assertSecretCount(t, ctx, db, harness.purposeLike, 0)
			})

			for _, failure := range []struct {
				name     string
				deferred bool
			}{
				{name: "owner write constraint failure"},
				{name: "commit failure", deferred: true},
			} {
				failure := failure
				t.Run("create "+failure.name, func(t *testing.T) {
					cleanupAtomicSecretOwner(t, ctx, db, harness)
					store := newConnectionTestSecretStore(t, db)
					removeFailure := installOwnerFailure(t, ctx, db, harness, failure.deferred)
					defer removeFailure()
					if err := harness.create(NewService(db, store), replaceSecret("rolled-back")); err == nil {
						t.Fatalf("expected injected %s", failure.name)
					}
					assertOwnerAbsent(t, ctx, db, harness)
					assertSecretCount(t, ctx, db, harness.purposeLike, 0)
				})
			}

			t.Run("replace secret write failure", func(t *testing.T) {
				cleanupAtomicSecretOwner(t, ctx, db, harness)
				store := newConnectionTestSecretStore(t, db)
				service := NewService(db, store)
				if err := harness.create(service, replaceSecret("original")); err != nil {
					t.Fatal(err)
				}
				failingService := NewService(db, failingPutSecretStore{DatabaseSecretStore: store})
				assertFailedMutationUnchanged(t, ctx, db, store, harness, 1, "original", func() error {
					return harness.update(failingService, 1, replaceSecret("never-visible"))
				})
			})

			for _, stale := range []struct {
				name     string
				mutation configurationdomain.SecretMutation
			}{
				{name: "replace stale CAS", mutation: replaceSecret("stale")},
				{name: "remove stale CAS", mutation: clearSecret()},
			} {
				stale := stale
				t.Run(stale.name, func(t *testing.T) {
					cleanupAtomicSecretOwner(t, ctx, db, harness)
					store := newConnectionTestSecretStore(t, db)
					service := NewService(db, store)
					if err := harness.create(service, replaceSecret("original")); err != nil {
						t.Fatal(err)
					}
					assertFailedMutationUnchanged(t, ctx, db, store, harness, 1, "original", func() error {
						return harness.update(service, 0, stale.mutation)
					})
				})
			}

			for _, action := range []struct {
				name     string
				mutation configurationdomain.SecretMutation
			}{
				{name: "replace", mutation: replaceSecret("replacement")},
				{name: "remove", mutation: clearSecret()},
			} {
				action := action
				for _, failure := range []struct {
					name     string
					deferred bool
				}{
					{name: "owner write constraint failure"},
					{name: "commit failure", deferred: true},
				} {
					failure := failure
					t.Run(action.name+" "+failure.name, func(t *testing.T) {
						cleanupAtomicSecretOwner(t, ctx, db, harness)
						store := newConnectionTestSecretStore(t, db)
						service := NewService(db, store)
						if err := harness.create(service, replaceSecret("original")); err != nil {
							t.Fatal(err)
						}
						removeFailure := installOwnerFailure(t, ctx, db, harness, failure.deferred)
						defer removeFailure()
						assertFailedMutationUnchanged(t, ctx, db, store, harness, 1, "original", func() error {
							return harness.update(service, 1, action.mutation)
						})
					})
				}
			}
		})
	}
}

func atomicSecretOwnerHarnesses() []secretOwnerHarness {
	return []secretOwnerHarness{
		{
			name: "connection", table: "connections", id: "connection-test-atomic-connection",
			purposeLike: "tmdb:connection-test-atomic-connection%",
			create: func(service *Service, secret configurationdomain.SecretMutation) error {
				_, err := service.UpsertConnection(context.Background(), configurationdomain.ConnectionInput{
					ID: "connection-test-atomic-connection", Kind: configurationdomain.ConnectionTMDB,
					Name: "connection-test-atomic-connection", Endpoint: "https://tmdb.example", Enabled: true, Secret: secret,
				})
				return err
			},
			update: func(service *Service, expectedVersion int64, secret configurationdomain.SecretMutation) error {
				_, err := service.UpsertConnection(context.Background(), configurationdomain.ConnectionInput{
					ID: "connection-test-atomic-connection", Kind: configurationdomain.ConnectionTMDB,
					Name: "connection-test-atomic-connection", Endpoint: "https://tmdb.example", Enabled: true,
					ExpectedVersion: expectedVersion, Secret: secret,
				})
				return err
			},
			read: func(service *Service) (int64, string, error) {
				owner, err := service.GetConnection(context.Background(), "connection-test-atomic-connection")
				return owner.Version, owner.SecretRef, err
			},
		},
		{
			name: "network route", table: "network_routes", id: "connection-test-atomic-route",
			purposeLike: "network-route:connection-test-atomic-route%",
			create: func(service *Service, secret configurationdomain.SecretMutation) error {
				_, err := service.UpsertNetworkRoute(context.Background(), configurationdomain.NetworkRouteInput{
					ID: "connection-test-atomic-route", Name: "connection-test-atomic-route",
					Kind: configurationdomain.NetworkRouteHTTPProxy, Endpoint: "http://proxy.example:7890", Secret: secret,
				}, 0)
				return err
			},
			update: func(service *Service, expectedVersion int64, secret configurationdomain.SecretMutation) error {
				_, err := service.UpsertNetworkRoute(context.Background(), configurationdomain.NetworkRouteInput{
					ID: "connection-test-atomic-route", Name: "connection-test-atomic-route",
					Kind: configurationdomain.NetworkRouteHTTPProxy, Endpoint: "http://proxy.example:7890", Secret: secret,
				}, expectedVersion)
				return err
			},
			read: func(service *Service) (int64, string, error) {
				owner, err := service.GetNetworkRoute(context.Background(), "connection-test-atomic-route")
				return owner.Version, owner.SecretRef, err
			},
		},
	}
}

func replaceSecret(value string) configurationdomain.SecretMutation {
	return configurationdomain.SecretMutation{Action: configurationdomain.SecretReplace, Value: value}
}

func clearSecret() configurationdomain.SecretMutation {
	return configurationdomain.SecretMutation{Action: configurationdomain.SecretClear}
}

func cleanupAtomicSecretOwner(t *testing.T, ctx context.Context, db *sql.DB, harness secretOwnerHarness) {
	t.Helper()
	if _, err := ownerTestDB(t).ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", harness.table), harness.id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM secret_values WHERE purpose LIKE $1`, harness.purposeLike); err != nil {
		t.Fatal(err)
	}
}

func installOwnerFailure(t *testing.T, ctx context.Context, db *sql.DB, harness secretOwnerHarness, deferred bool) func() {
	t.Helper()
	triggerName := "test_atomic_secret_failure"
	functionName := "test_atomic_secret_failure_fn"
	if _, err := ownerTestDB(t).ExecContext(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'injected configuration owner failure';
END;
$$;`, functionName)); err != nil {
		t.Fatal(err)
	}
	triggerKind := "TRIGGER"
	timing := "BEFORE"
	if deferred {
		triggerKind = "CONSTRAINT TRIGGER"
		timing = "AFTER"
	}
	statement := fmt.Sprintf(`CREATE %s %s %s INSERT OR UPDATE ON %s`, triggerKind, triggerName, timing, harness.table)
	if deferred {
		statement += " DEFERRABLE INITIALLY DEFERRED"
	}
	statement += fmt.Sprintf(" FOR EACH ROW WHEN (NEW.id = '%s') EXECUTE FUNCTION %s()", harness.id, functionName)
	if _, err := ownerTestDB(t).ExecContext(ctx, statement); err != nil {
		_, _ = ownerTestDB(t).ExecContext(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		t.Fatal(err)
	}
	return func() {
		if _, err := ownerTestDB(t).ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s; DROP FUNCTION IF EXISTS %s()", triggerName, harness.table, functionName)); err != nil {
			t.Errorf("remove failure injection: %v", err)
		}
	}
}

func assertFailedMutationUnchanged(t *testing.T, ctx context.Context, db *sql.DB, store *DatabaseSecretStore, harness secretOwnerHarness, wantVersion int64, wantSecret string, mutate func() error) {
	t.Helper()
	service := NewService(db, store)
	_, originalRef, err := harness.read(service)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutate(); err == nil {
		t.Fatal("expected mutation failure")
	}
	version, currentRef, err := harness.read(service)
	if err != nil {
		t.Fatal(err)
	}
	if version != wantVersion || currentRef != originalRef {
		t.Fatalf("failed mutation changed owner: version=%d ref=%q, want version=%d ref=%q", version, currentRef, wantVersion, originalRef)
	}
	assertResolvedSecret(t, store, originalRef, wantSecret)
	assertSecretCount(t, ctx, db, harness.purposeLike, 1)
}

func assertOwnerAbsent(t *testing.T, ctx context.Context, db *sql.DB, harness secretOwnerHarness) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = $1", harness.table), harness.id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("owner must be absent after rollback, got %d rows", count)
	}
}

func assertSecretCount(t *testing.T, ctx context.Context, db *sql.DB, purposeLike string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_values WHERE purpose LIKE $1`, purposeLike).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("unexpected visible secret version count: got %d want %d", count, want)
	}
}

func assertResolvedSecret(t *testing.T, store *DatabaseSecretStore, ref, want string) {
	t.Helper()
	value, ok := store.Resolve(ref)
	if !ok || value != want {
		t.Fatalf("secret %q is not resolvable: value=%q ok=%v", ref, value, ok)
	}
}
