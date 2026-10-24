package application

// preparedOperation is a fully computed operation plus its captured file facts.
// PersistPreparedOperationsTx never renders, observes or canonicalizes paths.
// Binding decisions must be persisted by the batch caller in this transaction.
type preparedOperation struct {
	plan     operationPlan
	artifact artifactContext
	file     artifactFileContext
}
