//go:build darwin

package fileidentity

import (
	"errors"
	"os"
	"syscall"

	"bansoko/internal/library/domain"
)

func platformFromFileInfo(info os.FileInfo) domain.FileIdentity {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return domain.FileIdentity{}
	}
	return domain.FileIdentity{
		Device:          uint64(stat.Dev),
		Inode:           stat.Ino,
		GenerationNanos: generationNanos(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec),
	}
}

func platformFromPath(path string) (domain.FileIdentity, error) {
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

func platformFromOpenFile(_ *os.File, fallback domain.FileIdentity) domain.FileIdentity {
	return fallback
}

// ChangeNanosFromPath returns the platform mutation marker when available.
// Darwin has no separate portable marker in this package yet.
func ChangeNanosFromPath(path string) (int64, error) {
	_, err := os.Lstat(path)
	return 0, err
}
