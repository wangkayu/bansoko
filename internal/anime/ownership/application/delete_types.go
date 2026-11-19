package application

type SubscriptionDeleteInput struct {
	ExpectedVersion  int64
	OperationBatchID *string
}
