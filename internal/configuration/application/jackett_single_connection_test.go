package application

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

func putJackettConnectionForTest(t *testing.T, ctx context.Context, svc generatedHTTPService, id string, expected int64, name string) error {
	t.Helper()
	var input generated.ConnectionInput
	if err := input.FromJackettConnectionInput(generated.JackettConnectionInput{
		Kind: "jackett", Name: name, Enabled: true, BaseUrl: "http://jackett.local:9117",
		Secret: replaceConnectionSecret(t, "jackett-api-key"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.PutConnection(ctx, id, expected, input)
	return err
}

// The runtime resolves exactly one Jackett relay: a second row is refused with 409
// JACKETT_ALREADY_CONFIGURED, while editing the existing row stays allowed.
func TestSecondJackettConnectionIsRefusedPostgres(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	cleanupConnectionTestRows(t, ctx, db)
	t.Cleanup(func() { cleanupConnectionTestRows(t, ctx, db) })

	svc := newGeneratedHTTPService(NewHTTPService(NewService(db, newConnectionTestSecretStore(t, db))))
	if err := putJackettConnectionForTest(t, ctx, svc, "connection-test-jackett-one", 0, "Jackett"); err != nil {
		t.Fatalf("first Jackett connection must save: %v", err)
	}
	err := putJackettConnectionForTest(t, ctx, svc, "connection-test-jackett-two", 0, "Jackett 2")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != http.StatusConflict || business.Code != "JACKETT_ALREADY_CONFIGURED" {
		t.Fatalf("second Jackett connection must be refused with 409 JACKETT_ALREADY_CONFIGURED, got %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM connections WHERE kind = 'jackett'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exactly one Jackett row must remain, count=%d err=%v", count, err)
	}
	if err := putJackettConnectionForTest(t, ctx, svc, "connection-test-jackett-one", 1, "Jackett renamed"); err != nil {
		t.Fatalf("editing the existing Jackett connection must stay allowed: %v", err)
	}
}
