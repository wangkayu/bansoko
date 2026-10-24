//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func atomicExchangeAvailable() bool { return true }

func atomicExchangeVerified(left, right verifiedEntry) error {
	if err := left.valid(); err != nil {
		return err
	}
	if err := right.valid(); err != nil {
		return err
	}
	err := unix.Renameat2(int(left.parent.file.Fd()), left.name, int(right.parent.file.Fd()), right.name, unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EXDEV) {
		return fmt.Errorf("%w: %v", errAtomicReplaceUnsupported, err)
	}
	return err
}

func atomicRenameNoReplaceVerified(source, target verifiedEntry) error {
	if err := source.valid(); err != nil {
		return err
	}
	if err := target.valid(); err != nil {
		return err
	}
	sourceFD, targetFD := int(source.parent.file.Fd()), int(target.parent.file.Fd())
	err := unix.Renameat2(sourceFD, source.name, targetFD, target.name, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	// NFS and some FUSE/SMB mounts refuse RENAME_NOREPLACE. link(2) never
	// replaces an existing name (EEXIST), so link-then-unlink keeps the
	// no-replace guarantee and the inode.
	if err := unix.Linkat(sourceFD, source.name, targetFD, target.name, 0); err != nil {
		return err
	}
	if err := unix.Unlinkat(sourceFD, source.name, 0); err != nil {
		_ = unix.Unlinkat(targetFD, target.name, 0)
		return err
	}
	return nil
}

// atomicRenameReplaceVerified is plain rename(2): it atomically replaces an
// existing target on POSIX filesystems, NFS and SMB.
func atomicRenameReplaceVerified(source, target verifiedEntry) error {
	if err := source.valid(); err != nil {
		return err
	}
	if err := target.valid(); err != nil {
		return err
	}
	return unix.Renameat(int(source.parent.file.Fd()), source.name, int(target.parent.file.Fd()), target.name)
}

func atomicLinkVerified(source, target verifiedEntry) error {
	if err := source.valid(); err != nil {
		return err
	}
	if err := target.valid(); err != nil {
		return err
	}
	return unix.Linkat(int(source.parent.file.Fd()), source.name, int(target.parent.file.Fd()), target.name, 0)
}

func atomicCreateNoReplaceVerified(parent verifiedEntry, name string) (*os.File, error) {
	if err := parent.valid(); err != nil {
		return nil, err
	}
	if name == "" || name == "." || name == ".." {
		return nil, errors.New("temporary basename is invalid")
	}
	fd, err := unix.Openat(int(parent.parent.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.parent.canonical, name)), nil
}

func atomicUnlinkVerified(entry verifiedEntry) error {
	if err := entry.valid(); err != nil {
		return err
	}
	return unix.Unlinkat(int(entry.parent.file.Fd()), entry.name, 0)
}

func openEntryNoFollow(parent verifiedEntry) (*os.File, error) {
	if err := parent.valid(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.parent.file.Fd()), parent.name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), parent.name), nil
}
