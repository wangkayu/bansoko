package httpapi

import (
	"testing"

	animeapplication "bansoko/internal/anime/application"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapSubscriptionPatchAndResult(t *testing.T) {
	input := mapSubscriptionPatch(generated.SubscriptionPatch{
		ExpectedVersion: 7,
		State:           generated.SubscriptionPatchStateStopped,
	})
	if input.ExpectedVersion != 7 || input.State != subscriptiondomain.SubscriptionStopped {
		t.Fatalf("mapped patch = %+v", input)
	}

	originEntryRef := "entry-1"
	instanceLabel := "label-1"
	value := animeapplication.Subscription{
		ID:             "sub-1",
		ManagedAnimeID: "anime-1",
		Origin:         "mikan",
		OriginEntryRef: &originEntryRef,
		InstanceLabel:  &instanceLabel,
		State:          subscriptiondomain.SubscriptionActive,
		Version:        8,
	}
	mapped := mapSubscription(value)
	if mapped.Id != "sub-1" || mapped.ManagedAnimeId != "anime-1" || mapped.State != generated.SubscriptionStateActive || mapped.Version != 8 {
		t.Fatalf("mapped subscription = %+v", mapped)
	}
	if mapped.OriginEntryRef == value.OriginEntryRef || mapped.InstanceLabel == value.InstanceLabel {
		t.Fatal("mapped subscription aliases application pointers")
	}
	*value.OriginEntryRef = "changed"
	*value.InstanceLabel = "changed"
	if *mapped.OriginEntryRef != "entry-1" || *mapped.InstanceLabel != "label-1" {
		t.Fatal("mapped subscription changed with application input")
	}
}
