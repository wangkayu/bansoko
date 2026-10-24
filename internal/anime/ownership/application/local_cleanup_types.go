package application

// LocalDataCleanupInput is the explicit, separately authorized physical local
// cleanup request for one retired ManagedAnime. It is never implied by
// retirement and never touches qBittorrent data or media-library files.
type LocalDataCleanupInput struct {
	// ExpectedLifecycleEpoch is the retired lifecycle fence_epoch the caller
	// observed. A mismatch is a typed 409 so a stale view can never delete the
	// wrong owner generation.
	ExpectedLifecycleEpoch int64
	// Confirmation is the fixed acknowledgement string from the contract. It
	// makes the destructive command impossible to trigger accidentally.
	Confirmation string
}

// LocalDataCleanupConfirmation is the exact acknowledgement every local
// cleanup request must carry.
const LocalDataCleanupConfirmation = "delete-local-data"
