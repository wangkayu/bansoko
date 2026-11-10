package port

import (
	"context"
	"time"
)

// Snapshot is the single reusable bangumi-data reference payload.
type Snapshot struct {
	UpstreamVersion string
	Payload         []byte
	LastModified    string
	FetchedAt       time.Time
	CheckedAt       time.Time
}

type SnapshotStore interface {
	Load(context.Context) (Snapshot, bool, error)
	Save(context.Context, Snapshot) error
	Touch(context.Context, time.Time) error
}
