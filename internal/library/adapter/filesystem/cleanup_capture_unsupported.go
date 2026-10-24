//go:build !linux && !darwin

package filesystem

import (
	"bansoko/internal/library/domain"
	"errors"
)

type cleanupMutationProbe func(string) error

func cleanupCaptureDirectory(string, string, bool, ...cleanupMutationProbe) (*verifiedDirectory, error) {
	return nil, errors.New("private cleanup capture unsupported")
}
func captureCleanupObject(string, string, string, domain.FileObservation, func() error, ...cleanupMutationProbe) (string, error) {
	return "", errors.New("private cleanup capture unsupported")
}
func purgeCleanupObject(string, string, domain.FileObservation, ...cleanupMutationProbe) error {
	return errors.New("private cleanup purge unsupported")
}
