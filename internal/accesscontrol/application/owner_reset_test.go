package application

import (
	"context"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"bansoko/internal/accesscontrol/domain"
)

func TestResetOwnerCredentialsRejectsUninitializedInstance(t *testing.T) {
	service := newOwnerService(t)
	if _, _, err := service.ResetOwnerCredentials(context.Background(), "owner", "correct horse battery"); err != domain.ErrSetupNotFinalized {
		t.Fatalf("uninitialized instance must refuse the reset, got %v", err)
	}
}

func TestResetOwnerCredentialsValidatesInput(t *testing.T) {
	ctx := context.Background()
	service := newOwnerService(t)
	if _, _, _, err := service.CompleteBootstrapOwner(ctx, "owner", "original password 12", 0); err != nil {
		t.Fatalf("bootstrap completion failed: %v", err)
	}
	if _, _, err := service.ResetOwnerCredentials(ctx, "owner", "short"); err == nil {
		t.Fatal("a password below the minimum length must be refused")
	}
	tooLongUsername := string(make([]rune, 129))
	if _, _, err := service.ResetOwnerCredentials(ctx, tooLongUsername, "correct horse battery"); err == nil {
		t.Fatal("an overlong username must be refused")
	}
}

func TestResetOwnerCredentialsReplacesCredentialsAndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	service := newOwnerService(t)
	if _, _, _, err := service.CompleteBootstrapOwner(ctx, "first-owner", "original password 12", 0); err != nil {
		t.Fatalf("bootstrap completion failed: %v", err)
	}
	oldSession, err := service.Login(ctx, "first-owner", "original password 12", 0)
	if err != nil {
		t.Fatalf("pre-reset login failed: %v", err)
	}
	// Sanity: the old credential is live before the reset.
	if _, err := service.AuthenticateSession(ctx, oldSession); err != nil {
		t.Fatalf("old session must authenticate before the reset: %v", err)
	}

	account, revoked, err := service.ResetOwnerCredentials(ctx, "reset-owner", "replacement password 12")
	if err != nil {
		t.Fatalf("owner reset failed: %v", err)
	}
	if account.Username != "reset-owner" {
		t.Fatalf("reset must apply the new username: %+v", account)
	}
	if revoked < 2 {
		t.Fatalf("the bootstrap and login sessions must be revoked, got %d", revoked)
	}

	// The outstanding old session dies with the credential reset.
	if _, err := service.AuthenticateSession(ctx, oldSession); err == nil {
		t.Fatal("old user session must be revoked by the reset transaction")
	}
	// The new credentials log in; the old ones do not.
	if _, err := service.Login(ctx, "first-owner", "original password 12", 0); err == nil {
		t.Fatal("old credentials must not authenticate after the reset")
	}
	if _, err := service.Login(ctx, "reset-owner", "replacement password 12", 0); err != nil {
		t.Fatalf("new credentials must log in after the reset: %v", err)
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatalf("status read failed: %v", err)
	}
	if status.State != domain.StateReady {
		t.Fatalf("bootstrap must stay ready after the reset, got %s", status.State)
	}
}

func TestResetOwnerCredentialsKeepsUsernameAndApiTokens(t *testing.T) {
	ctx := context.Background()
	service := newOwnerService(t)
	if _, _, _, err := service.CompleteBootstrapOwner(ctx, "owner", "original password 12", 0); err != nil {
		t.Fatalf("bootstrap completion failed: %v", err)
	}
	rawToken, token, err := service.CreateApiToken(ctx, "agent", nil)
	if err != nil {
		t.Fatalf("api token creation failed: %v", err)
	}

	account, _, err := service.ResetOwnerCredentials(ctx, "", "replacement password 12")
	if err != nil {
		t.Fatalf("owner reset failed: %v", err)
	}
	if account.Username != "owner" {
		t.Fatalf("an empty username must keep the current one: %+v", account)
	}

	// API tokens are scoped and machine-held: they survive the credential
	// reset unchanged.
	if _, err := service.AuthenticateSession(ctx, rawToken); err != nil {
		t.Fatalf("api token must keep authenticating after the reset: %v", err)
	}
	tokens, err := service.ListApiTokens(ctx)
	if err != nil {
		t.Fatalf("token list failed: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != token.ID {
		t.Fatalf("api token must survive the reset: %+v", tokens)
	}
	if _, err := service.Login(ctx, "owner", "replacement password 12", 0); err != nil {
		t.Fatalf("new password must log in with the unchanged username: %v", err)
	}
}
