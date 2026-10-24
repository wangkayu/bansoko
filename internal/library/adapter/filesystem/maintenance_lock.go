//go:build linux || darwin

package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// The lock inode is never removed. All invocations for this durable owner
// checkpoint hold it through OS effect, observation and final receipt fsync.
// Kernel releases flock on process exit; a timeout/lease expiry does not.
func lockMaintenanceInvocation(i maintenance.MaintenanceInvocation) (func(), error) {
	token, err := i.Token()
	if err != nil {
		return nil, err
	}
	path, err := PublicationMarkerPath(i.Target, token+"-lock")
	if err != nil {
		return nil, err
	}
	parent, err := openVerifiedDirectory("", filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	entry := verifiedEntry{parent: parent, name: filepath.Base(path), canonical: path}
	f, err := atomicCreateNoReplaceVerified(entry, entry.name)
	if errors.Is(err, os.ErrExist) {
		f, err = openEntryNoFollow(entry)
	}
	if err != nil {
		parent.Close()
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		parent.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close(); _ = parent.Close() }, nil
}
