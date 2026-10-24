package httpapi

import (
	animeapplication "bansoko/internal/anime/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func mapSubscriptionPatch(value generated.SubscriptionPatch) ownershipapplication.SubscriptionPatchInput {
	return ownershipapplication.SubscriptionPatchInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		State:           subscriptiondomain.SubscriptionState(value.State),
	}
}

func mapLocalDataCleanupInput(value generated.LocalDataCleanup) ownershipapplication.LocalDataCleanupInput {
	return ownershipapplication.LocalDataCleanupInput{
		ExpectedLifecycleEpoch: int64(value.ExpectedLifecycleEpoch),
		Confirmation:           string(value.Confirmation),
	}
}

func mapSubscription(value animeapplication.Subscription) generated.Subscription {
	result := generated.Subscription{
		Id:              generated.ID(value.ID),
		ManagedAnimeId:  generated.ID(value.ManagedAnimeID),
		Origin:          generated.SubscriptionOrigin(value.Origin),
		State:           generated.SubscriptionState(value.State),
		LifecycleState:  generated.SubscriptionLifecycleState(value.LifecycleState),
		LifecycleEpoch:  value.LifecycleEpoch,
		RetiredAt:       value.RetiredAt,
		Version:         generated.Version(value.Version),
		NewReleasesOnly: value.NewReleasesOnly,
		StartBaselineAt: value.StartBaselineAt,
	}
	if value.OriginEntryRef != nil {
		originEntryRef := *value.OriginEntryRef
		result.OriginEntryRef = &originEntryRef
	}
	if value.InstanceLabel != nil {
		instanceLabel := *value.InstanceLabel
		result.InstanceLabel = &instanceLabel
	}
	return result
}
