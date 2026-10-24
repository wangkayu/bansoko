package domain

import "context"

// PublicationCallbacks bridge the durable owner protocol to actual filesystem
// publication. Begin must persist permission; Finish records real exit evidence.
type PublicationCallbacks struct {
	Begin  func(context.Context, FileOperation, FileObservation) (string, error)
	Finish func(context.Context, string, FileOperation, FileObservation, error) error
}
