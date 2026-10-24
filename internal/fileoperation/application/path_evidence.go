package application

import (
	"strconv"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
)

// Path-level evidence (PathObjectV1): a regular file at a location with a
// size and, for diagnostics and same-invocation hardlink checks, a
// device/inode. There is no content proof, no change marker and no verifier
// version; the downloader's piece verification is the content proof.

func frozenUnavailable(reason string) map[string]any {
	return map[string]any{"state": "unavailable", "value": nil, "reason_code": reason}
}

func frozenKnown(value any) map[string]any {
	return map[string]any{"state": "known", "value": value, "reason_code": nil}
}

// pathIdentity is PathIdentityV1.
func pathIdentity(i domain.FileIdentity) map[string]any {
	return map[string]any{"kind": "local_fs", "device": strconv.FormatUint(i.Device, 10), "inode": strconv.FormatUint(i.Inode, 10)}
}

// pathObject is PathObjectV1 for one observation at a location.
func pathObject(location any, o domain.PathObservation) map[string]any {
	return map[string]any{"location": location, "file_type": "regular", "size": o.SizeBytes, "identity": pathIdentity(o.Identity), "observed_at": observationTime(o.ObservedAt)}
}

func observationTime(t time.Time) string { return protocol.FormatTime(t) }
