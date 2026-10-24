package domain

import (
	"errors"
	"testing"
	"time"
)

// Domain errors/states: the constrained bootstrap ownership model. Covers the
// sentinel errors, the bootstrap state machine, and the metadata-only
// projections (no account identity or credential data).

func TestSentinelErrorsDistinct(t *testing.T) {
	sentinels := []error{
		ErrSetupComplete, ErrOwnerExists, ErrInvalidCredentials, ErrSetupNotFinalized,
		ErrOwnerInputInvalid,
		ErrApiTokenNotFound, ErrApiTokenInvalid,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinel errors must be distinct: %v == %v", a, b)
			}
		}
		if a.Error() == "" {
			t.Fatalf("sentinel error must have a message: %d", i)
		}
	}
}

func TestBootstrapStatesAreExplicit(t *testing.T) {
	states := []BootstrapState{StateUninitialized, StateReady}
	for _, s := range states {
		if s == "" {
			t.Fatal("bootstrap state must never be empty")
		}
	}
	if StateUninitialized == StateReady {
		t.Fatal("bootstrap states must be distinct")
	}
}

func TestSetupOwnerStatusExposesOnlyOwnerCount(t *testing.T) {
	status := SetupOwnerStatus{OwnerCount: 1}
	// The projection deliberately carries no account identity or credential data.
	if status.OwnerCount != 1 {
		t.Fatalf("owner count not preserved: %+v", status)
	}
}

func TestApiTokenProjectionKeepsLifecycleMetadata(t *testing.T) {
	now := time.Now()
	token := ApiToken{ID: "tok-1", Name: "e2e-agent", CreatedAt: now}
	if token.ID != "tok-1" || token.Name != "e2e-agent" || !token.CreatedAt.Equal(now) {
		t.Fatalf("token projection not preserved: %+v", token)
	}
	if token.LastUsedAt != nil {
		t.Fatalf("unused token must have nil LastUsedAt: %+v", token)
	}
}
