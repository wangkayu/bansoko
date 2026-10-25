// Package executor defines the filesystem-effect ports used by
// FileMaintenance owners. Implementations belong to library adapters.
package executor

import (
	"context"

	"bansoko/internal/library/domain"
)

// CodeTargetOccupied is the adapter-neutral code for a verified occupied target.
const CodeTargetOccupied = domain.FileErrorCodeTargetOccupied

// ErrorCoder is implemented by typed adapter errors that expose a stable code.
type ErrorCoder interface{ ErrorCode() string }

// FileExecutor performs one verified alignment effect.
type FileExecutor interface {
	Execute(context.Context, domain.FileOperation) (domain.FileOperation, error)
}

// FileObserver reads back a path after an external effect.
type FileObserver interface {
	Observe(context.Context, string) (string, error)
}

// MetadataFileObserver reads a path's path-level metadata fingerprint
// (domain.MetadataFingerprint: size plus device/inode) without reading its
// content. Owners whose frozen fingerprint is a metadata fingerprint (a
// scan-derived alignment source) compare with it; see ObserveLike.
type MetadataFileObserver interface {
	ObserveMetadata(context.Context, string) (string, error)
}

// ObserveLike observes path in the fingerprint mode of reference: a metadata
// reference is matched by the observer's metadata observation (never a
// content read), anything else by Observe. An observer without a metadata
// observation falls back to Observe, whose digest never matches a metadata
// reference, so the caller fails closed.
func ObserveLike(ctx context.Context, observer FileObserver, path, reference string) (string, error) {
	if domain.IsMetadataFingerprint(reference) {
		if metadata, ok := observer.(MetadataFileObserver); ok {
			return metadata.ObserveMetadata(ctx, path)
		}
	}
	return observer.Observe(ctx, path)
}

// CleanupMetadataFileObserver is the no-content-read observation mode used
// when a cleanup froze a placement's metadata fingerprint envelope. Executors
// without this port cannot perform that mode; callers fail closed rather than
// silently falling back to a content hash.
type CleanupMetadataFileObserver interface {
	ObserveMetadataFile(context.Context, string) (domain.FileObservation, error)
}

// CleanupFileExecutor performs verified quarantine, purge, and restore effects.
type CleanupFileExecutor interface {
	Observe(context.Context, string) (domain.FileObservation, error)
	MoveToQuarantine(context.Context, string, string, domain.FileObservation) (string, error)
	RemoveQuarantined(context.Context, string, domain.FileObservation) error
	RestoreFromQuarantine(context.Context, string, string, domain.FileObservation) error
}
