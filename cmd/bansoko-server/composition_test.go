package main

import (
	"context"
	"testing"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// stubHandler satisfies worker.Handler with no behaviour: these tests are about
// the registration set, not about what a handler does.
type stubHandler struct{}

func (stubHandler) Handle(context.Context, workitems.Claim) (worker.Outcome, error) {
	return worker.Outcome{}, nil
}

// The count-only guard this replaced could not see a same-sized divergence: the
// exact case it was supposed to catch (one kind dropped, another duplicated)
// kept both lengths equal, and the map build then silently kept the duplicate's
// last handler while the dropped kind had none. Every branch below is a way a
// shipped binary could have promised a kind it never wired.
func TestWorkerRegistrationValidatorRequiresAnExactBijection(t *testing.T) {
	handler := stubHandler{}
	declared := []string{"a", "b", "c"}
	registration := func(kind string) workerRegistration { return workerRegistration{kind: kind, handler: handler} }

	cases := []struct {
		name          string
		registrations []workerRegistration
		declared      []string
		wantErr       string
	}{
		{
			name:          "exact set passes",
			registrations: []workerRegistration{registration("a"), registration("b"), registration("c")},
			declared:      declared,
		},
		{
			// The old guard's blind spot: 3 == 3, but a handler is lost and another
			// is silently replaced.
			name:          "same-size divergence fails",
			registrations: []workerRegistration{registration("a"), registration("b"), registration("b")},
			declared:      declared,
			wantErr:       `work item kind "b" is registered twice`,
		},
		{
			name:          "a missing declared kind fails",
			registrations: []workerRegistration{registration("a"), registration("b")},
			declared:      declared,
			wantErr:       `declared work item kind "c" has no handler registration`,
		},
		{
			name:          "an undeclared registration fails",
			registrations: []workerRegistration{registration("a"), registration("b"), registration("z")},
			declared:      declared,
			wantErr:       `work item kind "z" is registered but not declared by the server`,
		},
		{
			name:          "a nil handler fails",
			registrations: []workerRegistration{{kind: "a"}, registration("b"), registration("c")},
			declared:      declared,
			wantErr:       `work item kind "a" is registered with a nil handler`,
		},
		{
			name:          "a duplicated declaration fails",
			registrations: []workerRegistration{registration("a"), registration("b"), registration("c")},
			declared:      []string{"a", "b", "b"},
			wantErr:       `server work item kind "b" is declared twice`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkerRegistrations(tc.registrations, tc.declared)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestWorkItemKindsCoverPoliciesAndAllowlist pins the three kind inventories
// of the composition root together:
//
//   - every DefaultPolicies() kind must be wired in the composition root
//     (serverWorkItemKinds), otherwise a scheduled item is never handled;
//   - every wired kind must be claimable by the worker allowlist
//     (worker.AllowedKinds), otherwise the lazy worker build fails at
//     startup — the rc.5 rollout failure (#4): the reconciliation closure
//     added three kinds to the policy registry and main.go but not to the
//     allowlist, and no test exercised the startup path.
//
// main() additionally asserts len(registrations) == len(serverWorkItemKinds()).
func TestWorkItemKindsCoverPoliciesAndAllowlist(t *testing.T) {
	wired := serverWorkItemKinds()
	wiredSet := make(map[string]bool, len(wired))
	for _, kind := range wired {
		if wiredSet[kind] {
			t.Fatalf("duplicate wired kind %q", kind)
		}
		wiredSet[kind] = true
	}

	allowlist := make(map[string]bool)
	for _, kind := range worker.AllowedKinds() {
		allowlist[kind] = true
	}

	for _, policy := range worker.DefaultPolicies() {
		if !wiredSet[policy.Kind] {
			t.Errorf("policy kind %q is not wired in the composition root", policy.Kind)
		}
		if !allowlist[policy.Kind] {
			t.Errorf("policy kind %q is missing from the worker allowlist", policy.Kind)
		}
	}
	for _, kind := range wired {
		if !allowlist[kind] {
			t.Errorf("wired kind %q is missing from the worker allowlist", kind)
		}
	}
}
