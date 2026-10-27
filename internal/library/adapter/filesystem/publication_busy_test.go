//go:build linux || darwin

package filesystem

import (
	"errors"
	"os"
	"testing"

	"bansoko/internal/library/domain"
	"golang.org/x/sys/unix"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func overwriteFixture(t *testing.T) publicationFixture {
	t.Helper()
	f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionOverwrite)
	writeFile(t, f.target, "old version")
	return f
}

// An overwrite rename refused busy (an SMB sharing violation is EBUSY; an
// access denial EACCES) leaves the target as it was, removes the temp this
// invocation staged, and returns TARGET_BUSY carrying domain.ErrFileBusy; the
// publish_target receipt carries the same error.
func TestPublicationOverwriteBusyRemovesStagedTemp(t *testing.T) {
	for _, errno := range []unix.Errno{unix.EBUSY, unix.ETXTBSY, unix.EACCES, unix.EPERM, unix.EIO} {
		t.Run(errno.Error(), func(t *testing.T) {
			f := overwriteFixture(t)
			fake := &fakeSys{rename: func(verifiedEntry, verifiedEntry) error { return errno }}
			run, prep, err := runPublication(t, fake.executor(), f, "")
			if domain.PublicationFailureCode(err) != domain.CodeTargetBusy || !domain.FileBusy(err) || !errors.Is(err, errno) {
				t.Fatalf("err=%v, want busy TARGET_BUSY", err)
			}
			if readString(t, f.target) != "old version" {
				t.Fatal("refused overwrite changed the target")
			}
			if !absent(t, prep.TempPath) {
				t.Fatal("refused overwrite left its staged temp")
			}
			last := run.receipts[len(run.receipts)-1]
			if last.Step != "publish_target" || !domain.FileBusy(last.Err) {
				t.Fatalf("last receipt %s err=%v, want busy publish_target", last.Step, last.Err)
			}
			for _, step := range run.steps {
				if step == "temp_cleanup" || step == "source_cleanup" {
					t.Fatalf("step %s granted after a refused overwrite", step)
				}
			}
		})
	}
}

// EROFS is a read-only mount: a terminal permission failure, never busy. The
// staged temp is still provably ours and is removed all the same.
func TestPublicationOverwriteReadOnlyIsPermissionDenied(t *testing.T) {
	f := overwriteFixture(t)
	fake := &fakeSys{rename: func(verifiedEntry, verifiedEntry) error { return unix.EROFS }}
	_, prep, err := runPublication(t, fake.executor(), f, "")
	if domain.PublicationFailureCode(err) != domain.CodeTargetPermissionDenied || domain.FileBusy(err) {
		t.Fatalf("err=%v, want TARGET_PERMISSION_DENIED", err)
	}
	if readString(t, f.target) != "old version" || !absent(t, prep.TempPath) {
		t.Fatal("read-only overwrite changed the target or left its temp")
	}
}

// A temp that is no longer the staged object at the refused rename is not
// ours to remove: it stays in place and the busy failure is terminal (no
// ErrFileBusy, so the caller never requeues it).
func TestPublicationOverwriteBusyKeepsForeignTemp(t *testing.T) {
	f := overwriteFixture(t)
	fake := &fakeSys{}
	fake.rename = func(from, _ verifiedEntry) error {
		writeFile(t, from.canonical+".foreign", "foreign object")
		if err := unix.Rename(from.canonical+".foreign", from.canonical); err != nil {
			t.Fatal(err)
		}
		return unix.EBUSY
	}
	_, prep, err := runPublication(t, fake.executor(), f, "")
	if domain.PublicationFailureCode(err) != domain.CodeTargetBusy || domain.FileBusy(err) {
		t.Fatalf("err=%v, want terminal TARGET_BUSY", err)
	}
	if readString(t, prep.TempPath) != "foreign object" || readString(t, f.target) != "old version" {
		t.Fatal("foreign temp removed or target changed")
	}
}

// The version policy renames over its own reservation: a busy answer there
// keeps the existing mapping (no busy retry).
func TestPublicationVersionRenameBusyIsNotRetried(t *testing.T) {
	f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionVersion)
	writeFile(t, f.target, "old version")
	fake := &fakeSys{rename: func(verifiedEntry, verifiedEntry) error { return unix.EBUSY }}
	_, _, err := runPublication(t, fake.executor(), f, "")
	if err == nil || domain.FileBusy(err) || domain.PublicationFailureCode(err) == domain.CodeTargetBusy {
		t.Fatalf("err=%v, want the version mapping unchanged", err)
	}
}
