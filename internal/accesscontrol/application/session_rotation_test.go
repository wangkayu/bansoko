package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bansoko/internal/accesscontrol/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

// memoryRawTokens is the test stand-in for the encrypted raw-value store: it
// keeps each raw token in a map under its reference. Token creation only accepts
// a store that participates in the caller-owned transaction, so PutTx is the arm
// the service calls; Put stays for the non-transactional callers.
type memoryRawTokens struct {
	values map[string]string
	next   int
}

func (m *memoryRawTokens) put(value string) (string, error) {
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.next++
	ref := "mem:v1:" + string(rune('a'+m.next))
	m.values[ref] = value
	return ref, nil
}

func (m *memoryRawTokens) Put(_ context.Context, _ string, value string) (string, error) {
	return m.put(value)
}

func (m *memoryRawTokens) PutTx(_ context.Context, _ txscope.Tx, _ string, value string) (string, error) {
	return m.put(value)
}

func (m *memoryRawTokens) Delete(_ context.Context, ref string) error {
	delete(m.values, ref)
	return nil
}

func (m *memoryRawTokens) Resolve(ref string) (string, bool) {
	value, ok := m.values[ref]
	return value, ok
}

// TestRotateSessionSecretPostgres covers the session-secret rotation: every web
// session ends, API tokens keep authenticating under the new secret because
// their hash is recomputed from the stored raw value, and a token whose raw
// value is not stored is dropped instead of left dead.
func TestRotateSessionSecretPostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	oldSecret := []byte("session-rotation-old-secret-0123456789")
	oldService := NewServiceWithSessionSecret(db, oldSecret)
	store := &memoryRawTokens{}
	oldService.SetRawTokenStore(store)
	_, bootstrapSession, _, err := oldService.CompleteBootstrapOwner(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}
	session, err := oldService.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	keptRaw, _, err := oldService.CreateApiToken(ctx, "kept", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	droppedRaw, _, err := oldService.CreateApiToken(ctx, "dropped", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	// The second token loses its raw value, as when the DB predates raw storage.
	if _, err := db.ExecContext(ctx, `UPDATE api_tokens SET secret_ref = NULL WHERE name = 'dropped'`); err != nil {
		t.Fatalf("clear secret ref: %v", err)
	}

	newSecret := []byte("session-rotation-new-secret-0123456789")
	result, err := RotateSessionSecret(ctx, db, newSecret, store.Resolve, false)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// Two live web sessions exist: the one bootstrap issued and the one login
	// issued. Rotation ends every session, so both are revoked.
	if result.SessionsRevoked != 2 || result.TokensRehashed != 1 || len(result.TokensDropped) != 1 || result.TokensDropped[0] != "dropped" {
		t.Fatalf("unexpected result: %+v", result)
	}

	rotated := NewServiceWithSessionSecret(db, newSecret)
	for _, stale := range []string{bootstrapSession, session} {
		if _, err := rotated.AuthenticateSession(ctx, stale); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("the old web session still authenticates: %v", err)
		}
	}
	account, err := rotated.AuthenticateSession(ctx, keptRaw)
	if err != nil {
		t.Fatalf("the kept API token no longer authenticates: %v", err)
	}
	if account.Username != "owner" || account.WebSession {
		t.Fatalf("unexpected account: %+v", account)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("dropped token still rows: %d (%v)", remaining, err)
	}
	if _, err := rotated.AuthenticateSession(ctx, droppedRaw); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("the dropped token still authenticates: %v", err)
	}

	// A fresh login works: the new secret protects new sessions.
	fresh, err := rotated.Login(ctx, "owner", "original password 12", time.Hour)
	if err != nil {
		t.Fatalf("login after rotation: %v", err)
	}
	if _, err := rotated.AuthenticateSession(ctx, fresh); err != nil {
		t.Fatalf("the new session does not authenticate: %v", err)
	}
}

func TestRotateSessionSecretRejectsShortSecret(t *testing.T) {
	db := &sql.DB{}
	if _, err := RotateSessionSecret(context.Background(), db, []byte("short"), nil, false); !errors.Is(err, ErrSessionRotationSecretInvalid) {
		t.Fatalf("expected the secret rejection, got %v", err)
	}
}
