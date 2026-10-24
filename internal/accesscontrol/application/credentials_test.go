package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"bansoko/internal/accesscontrol/domain"
	"bansoko/internal/testutil/disposabledb"
)

const testSessionSecret = "test-session-secret-0123456789abcdef"

// newOwnerService builds the access service on a disposable schema with the
// deployment session secret wired, as the serving composition root does.
func newOwnerService(t *testing.T) *Service {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return NewServiceWithSessionSecret(db, []byte(testSessionSecret))
}

func newCredentialService(t *testing.T) (*Service, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	service := newOwnerService(t)
	account, _, _, err := service.CompleteBootstrapOwner(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}
	return service, ctx, account.ID
}

func TestUpdateOwnerCredentialsChangesUsernameWithoutEndingSessions(t *testing.T) {
	service, ctx, accountID := newCredentialService(t)
	currentSession, err := service.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := service.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatal(err)
	}

	renamed := "  renamed owner  "
	account, revoked, err := service.UpdateOwnerCredentials(ctx, accountID, currentSession, "original password 12", &renamed, nil)
	if err != nil {
		t.Fatalf("change username: %v", err)
	}
	if account.Username != "renamed owner" || account.Role != "owner" || revoked {
		t.Fatalf("unexpected result: %+v revoked=%v", account, revoked)
	}
	for _, token := range []string{currentSession, otherSession} {
		got, err := service.AuthenticateSession(ctx, token)
		if err != nil {
			t.Fatalf("session must survive a username change: %v", err)
		}
		if got.Username != "renamed owner" {
			t.Fatalf("session reports %q, want the new username", got.Username)
		}
	}
	if _, err := service.Login(ctx, "owner", "original password 12", 0); err == nil {
		t.Fatal("the old username must stop authenticating")
	}
	if _, err := service.Login(ctx, "renamed owner", "original password 12", 0); err != nil {
		t.Fatalf("the new username must authenticate: %v", err)
	}
}

func TestUpdateOwnerCredentialsPasswordRevokesOtherSessionsOnly(t *testing.T) {
	service, ctx, accountID := newCredentialService(t)
	currentSession, err := service.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := service.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, _, err := service.CreateApiToken(ctx, "agent", nil)
	if err != nil {
		t.Fatal(err)
	}

	newPassword := strings.Repeat("long replacement ", 15) // past bcrypt's raw 72-byte limit
	_, revoked, err := service.UpdateOwnerCredentials(ctx, accountID, currentSession, "original password 12", nil, &newPassword)
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if !revoked {
		t.Fatal("the other web session must be reported as revoked")
	}
	if _, err := service.AuthenticateSession(ctx, currentSession); err != nil {
		t.Fatalf("the current session must stay valid: %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, otherSession); err == nil {
		t.Fatal("another web session must be revoked by a password change")
	}
	if _, err := service.AuthenticateSession(ctx, apiToken); err != nil {
		t.Fatalf("API tokens must survive a password change: %v", err)
	}
	if _, err := service.Login(ctx, "owner", "original password 12", 0); err == nil {
		t.Fatal("the old password must stop authenticating")
	}
	if _, err := service.Login(ctx, "owner", newPassword, 0); err != nil {
		t.Fatalf("the new password must authenticate: %v", err)
	}
}

func TestUpdateOwnerCredentialsRejectsWrongCurrentPassword(t *testing.T) {
	service, ctx, accountID := newCredentialService(t)
	session, err := service.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatal(err)
	}
	renamed := "intruder"
	if _, _, err := service.UpdateOwnerCredentials(ctx, accountID, session, "wrong password 12", &renamed, nil); !errors.Is(err, domain.ErrCurrentPasswordInvalid) {
		t.Fatalf("wrong current password = %v, want ErrCurrentPasswordInvalid", err)
	}
	account, err := service.AuthenticateSession(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if account.Username != "owner" {
		t.Fatalf("a refused change must leave the username intact, got %q", account.Username)
	}
	if _, err := service.Login(ctx, "owner", "original password 12", 0); err != nil {
		t.Fatalf("the original credential must keep working: %v", err)
	}
}

func TestUpdateOwnerCredentialsValidatesInput(t *testing.T) {
	service, ctx, accountID := newCredentialService(t)
	session, err := service.Login(ctx, "owner", "original password 12", 0)
	if err != nil {
		t.Fatal(err)
	}
	tooLong := strings.Repeat("a", usernameMaxRunes+1)
	tooShort := strings.Repeat("a", passwordMinRunes-1)
	blank := "   "
	cases := []struct {
		name        string
		username    *string
		newPassword *string
	}{
		{name: "no change", username: nil, newPassword: nil},
		{name: "blank username", username: &blank, newPassword: nil},
		{name: "long username", username: &tooLong, newPassword: nil},
		{name: "short password", username: nil, newPassword: &tooShort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := service.UpdateOwnerCredentials(ctx, accountID, session, "original password 12", tc.username, tc.newPassword); !errors.Is(err, domain.ErrOwnerInputInvalid) {
				t.Fatalf("err = %v, want ErrOwnerInputInvalid", err)
			}
		})
	}
}

func TestUpdateOwnerCredentialsRequiresAnAccount(t *testing.T) {
	service, ctx, _ := newCredentialService(t)
	renamed := "renamed"
	_, _, err := service.UpdateOwnerCredentials(ctx, "not-an-account", "not-a-session", "original password 12", &renamed, nil)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}
