package application

import (
	"testing"

	subscriptiondomain "bansoko/internal/subscription/domain"
)

// The pending bench is one model for every origin: a pending custom owner is
// served by the bench exactly like a provider origin, and nothing else is.
func TestPendingBenchAppliesToEveryPendingOrigin(t *testing.T) {
	entry := "70032"
	blank := " "
	for _, tc := range []struct {
		name   string
		origin string
		entry  *string
		state  subscriptiondomain.SubscriptionState
		want   bool
	}{
		{name: "pending mikan", origin: "mikan", entry: &entry, state: subscriptiondomain.SubscriptionPending, want: true},
		{name: "pending anibt", origin: "anibt", entry: &entry, state: subscriptiondomain.SubscriptionPending, want: true},
		{name: "pending custom", origin: "custom", state: subscriptiondomain.SubscriptionPending, want: true},
		{name: "provider without an entry", origin: "mikan", entry: &blank, state: subscriptiondomain.SubscriptionPending, want: false},
		{name: "active custom", origin: "custom", state: subscriptiondomain.SubscriptionState("active"), want: false},
		{name: "active provider", origin: "mikan", entry: &entry, state: subscriptiondomain.SubscriptionState("active"), want: false},
		{name: "pending oneshot mount", origin: "oneshot", state: subscriptiondomain.SubscriptionPending, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var workspace AnimeWorkspace
			workspace.Subscription.Origin = tc.origin
			workspace.Subscription.OriginEntryRef = tc.entry
			workspace.Subscription.State = tc.state
			if got := pendingBenchApplies(workspace); got != tc.want {
				t.Fatalf("pendingBenchApplies = %v, want %v", got, tc.want)
			}
		})
	}
}
