//go:build !darwin && !linux

package filesystem

import (
	"errors"
	"os"
)

func atomicExchangeAvailable() bool { return false }

func atomicExchangeVerified(_, _ verifiedEntry) error {
	return errAtomicReplaceUnsupported
}

func atomicRenameNoReplaceVerified(_, _ verifiedEntry) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}

func atomicRenameReplaceVerified(_, _ verifiedEntry) error {
	return errors.New("atomic rename is unsupported on this platform")
}

func atomicLinkVerified(_, _ verifiedEntry) error {
	return errors.New("hardlink placement is unsupported on this platform")
}

func atomicCreateNoReplaceVerified(_ verifiedEntry, _ string) (*os.File, error) {
	return nil, errors.New("identity-bound temporary file creation is unsupported on this platform")
}

func atomicUnlinkVerified(_ verifiedEntry) error {
	return errors.New("identity-bound removal is unsupported on this platform")
}

func openEntryNoFollow(_ verifiedEntry) (*os.File, error) {
	return nil, errors.New("identity-bound entry opening is unsupported on this platform")
}
