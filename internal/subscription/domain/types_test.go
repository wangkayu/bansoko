package domain

import "testing"

func TestRSSLocatorRejectsCredentialAndPrivateTargets(t *testing.T) {
	for _, value := range []string{"http://user:pass@example.com/feed", "http://127.0.0.1/feed", "https://localhost/feed"} {
		if _, err := (RSSLocator{URL: value}).Normalize(); err == nil {
			t.Fatalf("unsafe RSS locator accepted: %s", value)
		}
	}
}

func TestRSSLocatorPreservesExplicitConnectionAndRouteReferences(t *testing.T) {
	normalized, err := (RSSLocator{URL: "HTTPS://Example.com/feed.xml", HTTPFeedConnection: " feed-1 ", NetworkRoute: " route-1 "}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.URL != "https://example.com/feed.xml" || normalized.HTTPFeedConnection != "feed-1" || normalized.NetworkRoute != "route-1" {
		t.Fatalf("RSS locator references were not normalized: %+v", normalized)
	}
}

func TestEvaluateSourceRefreshEligibility(t *testing.T) {
	tests := []struct {
		name  string
		facts SourceRefreshEligibilityFacts
		want  SourceRefreshIneligibility
	}{
		{name: "active custom", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionActive}, want: SourceRefreshEligible},
		{name: "stopped custom", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionStopped}, want: SourceRefreshSubscriptionStopped},
		{name: "pending custom", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionPending}, want: SourceRefreshSubscriptionPending},
		{name: "pending selected provider", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionPending, ProviderSource: true, ProviderSelectionExists: true, ProviderSelected: true}, want: SourceRefreshSubscriptionPending},
		{name: "unknown state fails closed", facts: SourceRefreshEligibilityFacts{SubscriptionState: "paused"}, want: SourceRefreshSubscriptionPending},
		{name: "provider missing selection", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionActive, ProviderSource: true}, want: SourceRefreshPolicyNotSelected},
		{name: "provider explicit false", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionActive, ProviderSource: true, ProviderSelectionExists: true}, want: SourceRefreshPolicyNotSelected},
		{name: "provider explicit true", facts: SourceRefreshEligibilityFacts{SubscriptionState: SubscriptionActive, ProviderSource: true, ProviderSelectionExists: true, ProviderSelected: true}, want: SourceRefreshEligible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EvaluateSourceRefreshEligibility(test.facts); got != test.want {
				t.Fatalf("eligibility = %q, want %q", got, test.want)
			}
		})
	}
}
