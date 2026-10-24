//go:build linux

package fileidentity

import (
	"errors"
	"os"

	"bansoko/internal/library/domain"
	"golang.org/x/sys/unix"
)

var nameToHandleAt = unix.NameToHandleAt

func platformHandleEvidenceFromFile(file *os.File) (*domain.HandleEvidence, error) {
	handle, mountID, err := nameToHandleAt(int(file.Fd()), "", unix.AT_EMPTY_PATH)
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
		return nil, EvidenceUnavailableError{Err: err}
	}
	if err != nil {
		return nil, err
	}
	return encodeHandleEvidence(int64(mountID), handle.Type(), handle.Bytes()), nil
}
