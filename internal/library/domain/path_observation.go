package domain

import (
	"context"
	"errors"
	"time"
)

// PathObservation is one stat of the regular file at a path: the final
// component is opened without following a symlink and fstat-ed, so network
// filesystems return fresh attributes under close-to-open semantics. It is
// the only evidence path-level verification records: path, size and, for
// diagnostics and same-invocation hardlink checks, device and inode. Device
// and inode are never compared across invocations; NFS can change st_dev on a
// remount and SMB noserverino mounts synthesize inode numbers.
type PathObservation struct {
	SizeBytes  int64
	Identity   FileIdentity
	ObservedAt time.Time
}

// PathObserver stats a regular file at an absolute path. An absent final
// component returns an error that wraps os.ErrNotExist.
type PathObserver interface {
	ObservePath(context.Context, string) (PathObservation, error)
}

// DeviceObserver reports whether a source file's directory and the nearest
// existing ancestor of a target's directory are on the same device. Equal
// devices do not prove a hardlink is possible (two bind mounts of one disk
// still return EXDEV); different devices prove it is not.
type DeviceObserver interface {
	SameDevice(ctx context.Context, source, target string) (bool, error)
}

// Publication failure codes. Each one is a terminal FileOperation failure
// shown in the records UI; none is retried automatically and none switches to
// another operation kind: a hardlink never falls back to copy or move.
const (
	CodeHardlinkCrossDevice    = "HARDLINK_CROSS_DEVICE"
	CodeHardlinkUnsupported    = "HARDLINK_UNSUPPORTED"
	CodeHardlinkNotPermitted   = "HARDLINK_NOT_PERMITTED"
	CodeHardlinkLimit          = "HARDLINK_LIMIT"
	CodeTargetNameInvalid      = "TARGET_NAME_INVALID"
	CodeTargetNameTooLong      = "TARGET_NAME_TOO_LONG"
	CodeTargetPermissionDenied = "TARGET_PERMISSION_DENIED"
	CodeSourcePermissionDenied = "SOURCE_PERMISSION_DENIED"
	CodeTargetSizeMismatch     = "TARGET_SIZE_MISMATCH"
	CodeTargetUnreadable       = "TARGET_UNREADABLE"
	CodeSourceChanged          = "SOURCE_CHANGED"
)

// PublicationError carries one of the publication failure codes above.
type PublicationError struct {
	Code string
	Err  error
}

func (e *PublicationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *PublicationError) Unwrap() error { return e.Err }

// ErrorCode exposes the code without coupling callers to the adapter.
func (e *PublicationError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// PublicationFailureCode returns the publication code an error carries, or "".
func PublicationFailureCode(err error) string {
	var typed *PublicationError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// TerminalPublicationCode reports whether code is one of the terminal
// per-file publication failures.
func TerminalPublicationCode(code string) bool {
	switch code {
	case CodeHardlinkCrossDevice, CodeHardlinkUnsupported, CodeHardlinkNotPermitted, CodeHardlinkLimit,
		CodeTargetNameInvalid, CodeTargetNameTooLong, CodeTargetPermissionDenied, CodeSourcePermissionDenied,
		CodeTargetSizeMismatch, CodeTargetUnreadable, CodeSourceChanged, FileErrorCodeTargetOccupied:
		return true
	}
	return false
}
