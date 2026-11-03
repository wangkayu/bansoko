//go:build linux || darwin

package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// removeEmptyCleanupNamespace removes the namespace directory of a settled
// delete once its object is gone. It runs only after the stage's final purge
// receipt: before that, the namespace directory is a frozen anchor of the
// invocation's start proof, so a refused or restored capture keeps it and
// leaves the empty shell to the startup sweep. Only the directory itself is
// removed: a directory that still holds an entry, or a symlink swapped into
// its place, stays untouched, and a settled action is never failed by this
// cleanup.
func removeEmptyCleanupNamespace(root, capture string) {
	directory := filepath.Dir(capture)
	if filepath.Base(capture) != "object" || !strings.HasPrefix(filepath.Base(directory), cleanupNamespacePrefix) {
		return
	}
	parent, err := openVerifiedDirectory(root, filepath.Dir(directory), false)
	if err != nil {
		return
	}
	defer parent.Close()
	_ = unix.Unlinkat(int(parent.file.Fd()), filepath.Base(directory), unix.AT_REMOVEDIR)
}

// sweepAbandonedCleanupNamespace removes an empty namespace directory whose
// object is already gone (a settled purge, or an interrupted action whose
// database owner will never return to the shell). Only the service's own 0700
// directory, older than the cutoff, is touched: anything else — a foreign
// owner, another mode, a symlink, an unexpected entry, a fresh directory that
// may belong to a running action — is left in place.
func sweepAbandonedCleanupNamespace(dir, name string, before time.Time) (bool, error) {
	if !strings.HasPrefix(name, cleanupNamespacePrefix) {
		return false, nil
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsDir() || info.Mode().Perm() != 0700 || !info.ModTime().Before(before) {
		return false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false, nil
	}
	parent, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(parent)
	// AT_REMOVEDIR unlinks only a directory and only an empty one; a name
	// swapped for another entry since the observation fails rather than
	// deleting it.
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil {
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
