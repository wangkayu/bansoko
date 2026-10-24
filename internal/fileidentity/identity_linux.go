//go:build linux

package fileidentity

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"bansoko/internal/library/domain"
)

func platformFromFileInfo(info os.FileInfo) domain.FileIdentity {
	if info == nil {
		return domain.FileIdentity{}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return domain.FileIdentity{}
	}
	return domain.FileIdentity{
		Device:          stat.Dev,
		Inode:           stat.Ino,
		GenerationNanos: 0,
	}
}

func platformFromPath(path string) (domain.FileIdentity, error) {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &stat); err == nil {
		if identity := statxIdentity(stat, domain.FileIdentity{}); IsPresent(identity) {
			return identity, nil
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	identity := platformFromFileInfo(info)
	if !IsPresent(identity) {
		return domain.FileIdentity{}, errors.New("file identity is unavailable")
	}
	return identity, nil
}

func platformFromOpenFile(file *os.File, fallback domain.FileIdentity) domain.FileIdentity {
	var stat unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &stat); err != nil {
		return fallback
	}
	return statxIdentity(stat, fallback)
}

func statxIdentity(stat unix.Statx_t, fallback domain.FileIdentity) domain.FileIdentity {
	identity := domain.FileIdentity{
		Device: uint64(unix.Mkdev(stat.Dev_major, stat.Dev_minor)),
		Inode:  stat.Ino,
	}
	if !IsPresent(identity) {
		return fallback
	}
	if stat.Mask&unix.STATX_BTIME != 0 {
		identity.GenerationNanos = generationNanos(stat.Btime.Sec, int64(stat.Btime.Nsec))
	}
	return identity
}

// ChangeNanosFromPath returns the platform mutation marker for an entry.
// On Linux this is ctime; it is deliberately separate from object identity.
func ChangeNanosFromPath(path string) (int64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_CTIME, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_CTIME == 0 {
		return 0, nil
	}
	return generationNanos(stat.Ctime.Sec, int64(stat.Ctime.Nsec)), nil
}
