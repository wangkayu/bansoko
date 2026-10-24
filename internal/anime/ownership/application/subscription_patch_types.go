package application

import subscriptiondomain "bansoko/internal/subscription/domain"

type SubscriptionPatchInput struct {
	ExpectedVersion int64
	State           subscriptiondomain.SubscriptionState
}
