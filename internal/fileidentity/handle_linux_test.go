//go:build linux

package fileidentity

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPlatformHandleEvidenceFromFileReportsUnavailableErrors(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "handle")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	original := nameToHandleAt
	t.Cleanup(func() { nameToHandleAt = original })
	for _, unavailable := range []error{unix.EPERM, unix.EOPNOTSUPP, unix.ENOSYS} {
		nameToHandleAt = func(int, string, int) (unix.FileHandle, int, error) {
			return unix.FileHandle{}, 0, unavailable
		}
		got, err := platformHandleEvidenceFromFile(file)
		if got != nil || !errors.Is(err, ErrEvidenceUnavailable) || !errors.Is(err, unavailable) {
			t.Fatalf("error %v: got evidence=%v err=%v, want typed unavailable error", unavailable, got, err)
		}
	}
}

func TestPlatformHandleEvidenceFromFilePreservesUnexpectedErrors(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "handle")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	original := nameToHandleAt
	t.Cleanup(func() { nameToHandleAt = original })
	unexpected := errors.New("unexpected handle failure")
	nameToHandleAt = func(int, string, int) (unix.FileHandle, int, error) {
		return unix.FileHandle{}, 0, unexpected
	}
	got, err := platformHandleEvidenceFromFile(file)
	if got != nil || !errors.Is(err, unexpected) {
		t.Fatalf("got evidence=%v err=%v, want unexpected error", got, err)
	}
}
