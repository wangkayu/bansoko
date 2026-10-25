package application

import (
	"context"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
)

// sourceGroupDisplayName is the single place an admission decides what the
// placeholder source and every naming render call the seed's group. It must
// answer a display name and never the provider's group identity.
func TestSourceGroupDisplayNameNeverFallsBackToTheIdentity(t *testing.T) {
	customDraft := func(groupName string) animedomain.AnimeDraft {
		return animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginCustom, RssSources: []animedomain.RSSSourceDraft{{
			SourceKind: animedomain.SourceKindCustomRss, Locator: "https://feeds.example/custom", GroupName: groupName, Title: "Show",
		}}}
	}
	customSeed := SelectedSeed{Provider: animedomain.ProviderKindCustom, ProviderEntryRef: "custom-source:" + strings.Repeat("a", 64), ProviderGroupRef: "custom-group:" + strings.Repeat("b", 64)}
	providerSeed := SelectedSeed{Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "entry-1", ProviderGroupRef: "612"}

	assertUnavailable := func(label string, draft animedomain.AnimeDraft, seed SelectedSeed) {
		t.Helper()
		name, err := sourceGroupDisplayName(context.Background(), nil, draft, seed)
		business, ok := err.(*apperrors.Business)
		if !ok || business.Code != "SOURCE_GROUP_UNAVAILABLE" || business.Status != 422 {
			t.Fatalf("%s: want the typed SOURCE_GROUP_UNAVAILABLE refusal, got name %q err %v", label, name, err)
		}
		if name != "" {
			t.Fatalf("%s: refused admission still produced the name %q", label, name)
		}
	}

	// A custom seed's identity is a digest of the draft's own group name, so the
	// name itself is the display name.
	name, err := sourceGroupDisplayName(context.Background(), nil, customDraft("  LoliHouse  "), customSeed)
	if err != nil || name != "LoliHouse" {
		t.Fatalf("custom seed display name = %q err %v, want the draft's trimmed group name", name, err)
	}

	// The identity is never substituted for a missing name: neither a custom
	// seed's custom-group digest nor a provider seed's group id becomes one.
	assertUnavailable("custom draft without a group name", customDraft("   "), customSeed)
	assertUnavailable("custom draft without its source", animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginCustom}, customSeed)
	assertUnavailable("provider seed without a recorded observation", animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginMikan}, providerSeed)

	// The target plan takes the admission's resolved name, never the seed's
	// provider group identity, so a one-shot names its files by the display name.
	request := oneShotTargetRequest("release-1", oneShotTargetDraft(), "LoliHouse", 0, "regex", oneShotTargetEffective(t), oneShotTargetItem(), oneShotTargetPreview(), nil, "skip")
	if request.GroupName != "LoliHouse" {
		t.Fatalf("target request group name = %q, want the admission's display name", request.GroupName)
	}
	if request.GroupName == providerSeed.ProviderGroupRef {
		t.Fatal("target request group name is the provider group identity")
	}
}
