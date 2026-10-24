//go:build !darwin && !linux

package filesystem

import (
	"errors"

	"bansoko/internal/library/domain"
)

func verifiedDirectoryChain(_, _ string, _ bool) (domain.FileIdentity, error) {
	return domain.FileIdentity{}, errors.New("root-bounded no-follow directory verification is unsupported on this platform")
}

func openVerifiedDirectory(_, _ string, _ bool) (*verifiedDirectory, error) {
	return nil, errors.New("root-bounded no-follow directory verification is unsupported on this platform")
}

func openDirectoryNoFollow(_ string) (int, error) {
	return -1, errors.New("no-follow directory opening is unsupported on this platform")
}
