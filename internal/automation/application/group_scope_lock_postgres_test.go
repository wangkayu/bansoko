package application

import (
	"context"
	"testing"
	"time"
)

// Admission and authoritative refresh serialize on the subscription owner;
// selection changes cannot pass a fenced admission's scope check mid-commit.
func TestGroupScopeChangeWaitsForAdmissionOwnerLock(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	admission, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Rollback()
	var state string
	if err := admission.QueryRow(`SELECT state FROM subscriptions WHERE id='sub-1' FOR UPDATE`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	refresh, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer refresh.Rollback()
	if err := refresh.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id='sub-1' FOR UPDATE`).Scan(&state); err == nil {
		t.Fatal("scope commit passed concurrent admission owner lock")
	}
	if err := admission.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM subscriptions WHERE id='sub-1'`).Scan(&state); err != nil || state != "active" {
		t.Fatalf("owner corrupted: %s %v", state, err)
	}
}
