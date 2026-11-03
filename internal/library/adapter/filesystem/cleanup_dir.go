//go:build linux || darwin

package filesystem

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// purgeDirectoryMaxDepth bounds the recursion of a captured directory's
// removal; a deeper tree is refused rather than walked without a limit.
const purgeDirectoryMaxDepth = 64

// purgeDirectoryObject removes the captured directory name and everything
// below it, depth-first, inside the verified private namespace. Nothing is
// ever followed: each level is opened with O_NOFOLLOW|O_DIRECTORY, a symlink
// is unlinked rather than descended, and each removal is typed — unlinkat
// with AT_REMOVEDIR only removes a directory, plain unlinkat never removes
// one — so a type swap mid-walk fails the syscall instead of deleting the
// wrong kind. The walk stops at the first error; a partial tree stays inside
// the private namespace, which the next purge attempt re-enters.
func purgeDirectoryObject(private *verifiedDirectory, name string) error {
	if private == nil || private.file == nil {
		return errors.New("verified directory is closed")
	}
	if err := purgeDirectoryContents(int(private.file.Fd()), name, 0); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(private.file.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove captured directory: %w", err)
	}
	return nil
}

// purgeDirectoryContents empties the directory name below parentFD. The
// directory itself is left for the caller's typed AT_REMOVEDIR unlink.
func purgeDirectoryContents(parentFD int, name string, depth int) error {
	if depth >= purgeDirectoryMaxDepth {
		return errors.New("captured directory is deeper than the purge limit")
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open captured directory: %w", err)
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	for {
		names, readErr := dir.Readdirnames(128)
		for _, child := range names {
			if err := purgeDirectoryChild(fd, child, depth); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("list captured directory: %w", readErr)
		}
	}
}

func purgeDirectoryChild(parentFD int, name string, depth int) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat captured entry: %w", err)
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err := purgeDirectoryContents(parentFD, name, depth+1); err != nil {
			return err
		}
		if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove captured directory: %w", err)
		}
		return nil
	}
	if err := unix.Unlinkat(parentFD, name, 0); err != nil {
		return fmt.Errorf("remove captured entry: %w", err)
	}
	return nil
}
