//go:build linux || darwin

package filesystem

import (
	"fmt"

	"bansoko/internal/library/domain"
	"golang.org/x/sys/unix"
)

// busyEffectErrnos are the answers of a rename or unlink that did not run
// because the file is held open elsewhere or the change is not permitted. On
// a Linux SMB/CIFS mount a sharing violation arrives as EBUSY and an access
// denial as EACCES; ETXTBSY, EPERM and EIO are the other answers seen for an
// open file. EROFS is a read-only mount and stays a permission failure.
var busyEffectErrnos = []unix.Errno{unix.EBUSY, unix.ETXTBSY, unix.EACCES, unix.EPERM, unix.EIO}

// busyEffectError classifies the error of one effect syscall: the rename or
// unlink itself, or the creation of the owner private namespace in the
// replaced file's directory (refused there, nothing was captured); never a
// directory check or sync around them. A busy
// answer is wrapped in domain.ErrFileBusy with its errno kept in the chain, a
// read-only filesystem (EROFS) in domain.ErrFileReadOnly; any other error is
// returned unchanged.
func busyEffectError(err error) error {
	switch {
	case err == nil:
		return nil
	case errnoIs(err, busyEffectErrnos...):
		return fmt.Errorf("%w: %w", domain.ErrFileBusy, err)
	case errnoIs(err, unix.EROFS):
		return fmt.Errorf("%w: %w", domain.ErrFileReadOnly, err)
	}
	return err
}

// busyObservationErrnos are the answers of an open that only observes a file
// someone else holds open exclusively: an SMB client opening without any
// sharing makes every other open fail with a sharing violation, which a Linux
// CIFS mount reports as EBUSY. A permission answer to an observation is not
// busy: it stays the caller's own failure.
var busyObservationErrnos = []unix.Errno{unix.EBUSY, unix.ETXTBSY}

// busyObservationError classifies the error of observing a file (no effect):
// a busy answer is wrapped in domain.ErrFileBusy, any other error is
// returned unchanged.
func busyObservationError(err error) error {
	if err == nil || !errnoIs(err, busyObservationErrnos...) {
		return err
	}
	return fmt.Errorf("%w: %w", domain.ErrFileBusy, err)
}

// cleanupEffectSyscalls are the two effect syscalls of a deletion owner's
// private-namespace capture and purge. Tests substitute errno results (a
// sharing violation, a refused unlink) without a special filesystem.
type cleanupEffectSyscalls struct {
	rename func(from, to verifiedEntry) error
	unlink func(entry verifiedEntry) error
}

var cleanupEffects = cleanupEffectSyscalls{rename: atomicRenameNoReplaceVerified, unlink: atomicUnlinkVerified}
