//go:build !darwin && !linux

package fileidentity

import (
	"errors"
	"os"

	"bansoko/internal/library/domain"
)

func platformFromFileInfo(os.FileInfo) domain.FileIdentity {
	return domain.FileIdentity{}
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

func ChangeNanosFromPath(path string) (int64, error) {
	_, err := os.Lstat(path)
	return 0, err
}
