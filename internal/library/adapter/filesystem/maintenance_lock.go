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

// discardRefusedMaintenance removes the start marker and the lock of one
// invocation whose effect provably did not apply (its rename or unlink was
// refused), so a stage retried every few minutes leaves no markers behind.
// The owner retires the invocation's identity (the retry runs under a new
// one), so nothing reads or locks it again. A final receipt is never
// discarded. An invocation that wrote nothing (its namespace was refused)
// has nothing to remove.
func discardRefusedMaintenance(i maintenance.MaintenanceInvocation) error {
	token, err := i.Token()
	if err != nil {
		return err
	}
	lockPath, err := PublicationMarkerPath(i.Target, token+"-lock")
	if err != nil {
		return err
	}
	if _, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlock, err := lockMaintenanceInvocation(i)
	if err != nil {
		return err
	}
	defer unlock()
	if _, _, err := ReadPublicationMarker(i.Target, token+"-final"); err == nil {
		return errors.New("a final maintenance receipt is never discarded")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent, err := openVerifiedDirectory("", filepath.Dir(lockPath), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	for _, suffix := range []string{"-started", "-lock"} {
		p, err := PublicationMarkerPath(i.Target, token+suffix)
		if err != nil {
			return err
		}
		if err := unix.Unlinkat(int(parent.file.Fd()), filepath.Base(p), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return parent.Sync()
}
