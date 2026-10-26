package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

func TestAtomicHelpersRequireExpectedParentIdentity(t *testing.T) {
	root := canonicalTemp(t)
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	if err := os.WriteFile(left, []byte("left"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("right"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safeExchange(root, left, right, domain.FileIdentity{}); err == nil {
		t.Fatal("exchange accepted missing parent identity")
	}
	if err := safeRenameNoReplace(root, root, left, filepath.Join(root, "moved"), domain.FileIdentity{}, domain.FileIdentity{}); err == nil {
		t.Fatal("rename accepted missing parent identities")
	}
	if err := safeLink(root, root, left, filepath.Join(root, "linked"), domain.FileIdentity{}, domain.FileIdentity{}); err == nil {
		t.Fatal("link accepted missing parent identities")
	}
}
func TestSameMutationFileObservationHandleEvidence(t *testing.T) {
	base := domain.FileObservation{
		Fingerprint: "same",
		SizeBytes:   4,
		Identity:    domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 7},
	}
	valid := func(opaque string) *domain.HandleEvidence {
		return &domain.HandleEvidence{MountID: 1, Type: 2, Opaque: opaque}
	}
	cases := []struct {
		name  string
		left  *domain.HandleEvidence
		right *domain.HandleEvidence
		want  bool
	}{
		{name: "both nil", want: true},
		{name: "left nil", right: valid("same"), want: true},
		{name: "different valid handles", left: valid("left"), right: valid("right"), want: false},
		{name: "same valid handles", left: valid("same"), right: valid("same"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			left, right := base, base
			left.HandleEvidence, right.HandleEvidence = tc.left, tc.right
			if got := sameMutationFileObservation(left, right); got != tc.want {
				t.Fatalf("sameMutationFileObservation() = %v, want %v", got, tc.want)
			}
		})
	}
}
func TestSameMutationFileObservationFallsBackFromIncompleteHandleToIdentity(t *testing.T) {
	left := domain.FileObservation{
		Fingerprint:    "same",
		SizeBytes:      4,
		Identity:       domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 7},
		HandleEvidence: &domain.HandleEvidence{MountID: 1, Type: 2, Opaque: "left"},
	}
	right := left
	right.HandleEvidence = &domain.HandleEvidence{MountID: 0, Type: 2, Opaque: "right"}
	if !sameMutationFileObservation(left, right) {
		t.Fatal("incomplete handle evidence did not fall back to matching identity with generation")
	}
	left.Identity.GenerationNanos = 0
	right.Identity.GenerationNanos = 0
	if sameMutationFileObservation(left, right) {
		t.Fatal("matching identity without generation was not rejected fail-closed")
	}
}

func TestSameMutationFileObservationUsesChangeNanosOnlyWithoutGeneration(t *testing.T) {
	base := domain.FileObservation{
		Fingerprint: "same",
		SizeBytes:   4,
		Identity:    domain.FileIdentity{Device: 1, Inode: 2},
		ChangeNanos: 42,
	}
	cases := []struct {
		name  string
		left  domain.FileObservation
		right domain.FileObservation
		want  bool
	}{
		{name: "identity mismatch with matching change nanos", left: base, right: func() domain.FileObservation {
			right := base
			right.Identity = domain.FileIdentity{Device: 3, Inode: 4}
			return right
		}(), want: false},
		{name: "identity mismatch with different change nanos", left: base, right: func() domain.FileObservation {
			right := base
			right.Identity = domain.FileIdentity{Device: 3, Inode: 4}
			right.ChangeNanos = 43
			return right
		}(), want: false},
		{name: "missing identities with one zero change nanos", left: func() domain.FileObservation {
			left := base
			left.Identity = domain.FileIdentity{}
			return left
		}(), right: func() domain.FileObservation {
			right := base
			right.Identity = domain.FileIdentity{}
			right.ChangeNanos = 0
			return right
		}(), want: false},
		{name: "matching identity with missing generation and matching change nanos", left: base, right: base, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameMutationFileObservation(tc.left, tc.right); got != tc.want {
				t.Fatalf("sameMutationFileObservation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameFrozenFileObservationRequiresIdentity(t *testing.T) {
	root := canonicalTemp(t)
	leftPath := filepath.Join(root, "left")
	rightPath := filepath.Join(root, "right")
	content := []byte("same content")
	if err := os.WriteFile(leftPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rightPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	left, err := observeFile(leftPath)
	if err != nil {
		t.Fatal(err)
	}
	right, err := observeFile(rightPath)
	if err != nil {
		t.Fatal(err)
	}
	if left.Fingerprint != right.Fingerprint || left.SizeBytes != right.SizeBytes {
		t.Fatalf("fixtures do not have equal content observations: left=%+v right=%+v", left, right)
	}
	if left.Identity == right.Identity {
		t.Fatalf("fixtures unexpectedly share identity: left=%+v right=%+v", left, right)
	}
	if sameFrozenFileObservation(left, right) {
		t.Fatal("same-content files with different identities were accepted as the same frozen object")
	}
	withoutIdentity := left
	withoutIdentity.Identity = domain.FileIdentity{}
	if sameFrozenFileObservation(withoutIdentity, withoutIdentity) {
		t.Fatal("missing identity was accepted as a frozen object")
	}
}

// chainAnchor is the anchor every operation's directory chain is walked from:
// the filesystem root. Production freezes the same value in an overwrite
// checkpoint, and the executor refuses a recovery whose anchor differs.
const chainAnchor = string(filepath.Separator)

// canonicalTemp returns a symlink-resolved temporary directory. Every operation
// path is walked with no-follow opens from the filesystem root, so a fixture
// path must not pass through the OS's own symlinked prefixes (macOS spells the
// temporary directory /var/folders, and /var is a symlink to /private/var).
func canonicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestOSFileExecutorVersionsDifferentCollisionWithoutOverwrite(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "library", "episode.mkv")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionVersion}
	completed, err := (OSFileExecutor{}).Execute(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if completed.TargetPath != filepath.Join(resolvedRoot, "library", "episode (2).mkv") || completed.State != domain.OperationSucceeded {
		t.Fatalf("unexpected versioned operation: %+v", completed)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("existing target was modified: %q", got)
	}
}

func TestOSFileExecutorRejectsSymlinkParentAndTarget(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source.mkv")
	if err := os.WriteFile(source, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := canonicalTemp(t)
	parentLink := filepath.Join(root, "linked")
	if err := os.Symlink(outside, parentLink); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}
	operation := domain.FileOperation{Operation: domain.OperationCopy, SourcePath: source, TargetPath: filepath.Join(parentLink, "episode.mkv"), SourceFingerprint: digest("episode"), CollisionPolicy: domain.CollisionVersion}
	if _, err := (OSFileExecutor{}).Execute(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink parent was accepted: %v", err)
	}

	target := filepath.Join(root, "target.mkv")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}
	operation.TargetPath = target
	if _, err := (OSFileExecutor{}).Execute(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink target was accepted: %v", err)
	}
}

func TestOSFileExecutorRejectsStaleAuthorizedParentIdentity(t *testing.T) {
	root := canonicalTemp(t)
	canonicalRootPath, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalRootPath
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("source"), CollisionPolicy: domain.CollisionBlock}
	if err := reauthorize(root, &operation); err != nil {
		t.Fatal(err)
	}
	operation.TargetParentIdentity.Inode++
	if err := (OSFileExecutor{}).executeAbsent(context.Background(), operation, root, domain.FileObservation{Fingerprint: digest("source"), SizeBytes: 6}); err == nil {
		t.Fatal("copy accepted stale target parent identity")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale parent mutation created target: %v", err)
	}
}

func TestOSFileExecutorHardlinkFailureDoesNotFallbackToCopy(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source.mkv")
	outside := canonicalTemp(t)
	outsideTarget := filepath.Join(outside, "outside.mkv")
	if err := os.WriteFile(source, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideTarget, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "episode.mkv")
	if err := os.Symlink(outsideTarget, target); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}
	operation := domain.FileOperation{
		Operation:         domain.OperationHardlink,
		SourcePath:        source,
		TargetPath:        target,
		SourceFingerprint: digest("episode"),
		CollisionPolicy:   domain.CollisionVersion,
	}
	if _, err := (OSFileExecutor{}).Execute(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("hardlink failure was not returned exactly: %v", err)
	}
	if got, err := os.ReadFile(outsideTarget); err != nil || string(got) != "outside" {
		t.Fatalf("hardlink failure modified the symlink target: %q err=%v", got, err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("hardlink failure replaced the target instead of preserving it: info=%v err=%v", info, err)
	}
}

func TestOSFileRemoverRemovePreservesReceiverHooks(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "entry")
	if err := os.WriteFile(path, []byte("entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	remover := OSFileRemover{BeforeQuarantine: func(string) error { called = true; return errors.New("injected quarantine failure") }}
	if err := remover.Remove(context.Background(), root, path, digest("entry")); err == nil {
		t.Fatal("receiver hook was lost")
	}
	if !called {
		t.Fatal("receiver hook was not called")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("failed quarantine removed source: %v", err)
	}
}

func TestRemoveVerifiedNonRegularRetainsQuarantineOnDurableSync(t *testing.T) {
	root, err := canonicalRoot(canonicalTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "staging-entry")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := fileIdentity(info)
	if identity == (domain.FileIdentity{}) {
		t.Skip("filesystem identity unavailable")
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", path, false)
	if err != nil {
		t.Skipf("filesystem parent identity unavailable: %v", err)
	}
	if err := removeVerifiedNonRegular(root, path, displacedEntryType(info), identity, nil, parentIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-regular source remains after quarantine: %v", err)
	}
	quarantine := quarantinePath(path)
	quarantined, err := os.Lstat(quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if displacedEntryType(quarantined) != displacedEntryType(info) || fileIdentity(quarantined) != identity {
		t.Fatalf("quarantine identity changed: type=%s identity=%+v", displacedEntryType(quarantined), fileIdentity(quarantined))
	}
}

func TestRemoveVerifiedNonRegularQuarantineFailurePreservesSource(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "staging-entry")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := fileIdentity(info)
	if identity == (domain.FileIdentity{}) {
		t.Skip("filesystem identity unavailable")
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", path, false)
	if err != nil {
		t.Skipf("filesystem parent identity unavailable: %v", err)
	}
	injected := errors.New("quarantine injection")
	if err := removeVerifiedNonRegular(root, path, displacedEntryType(info), identity, func(string) error { return injected }, parentIdentity); !errors.Is(err, injected) {
		t.Fatalf("non-regular quarantine failure = %v, want injected error", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("non-regular source was not retained after quarantine failure: %v", err)
	}
	if _, err := os.Lstat(quarantinePath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed non-regular quarantine left unexpected evidence: %v", err)
	}
}

func TestOSFileExecutorRejectsChangedSourceAndNoopIsReplaySafe(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("original"), CollisionPolicy: domain.CollisionVersion}
	if _, err := (OSFileExecutor{}).Execute(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed source was accepted: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source created a target: %v", err)
	}

	if err := os.WriteFile(target, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation.SourceFingerprint = digest("changed")
	completed, err := (OSFileExecutor{}).Execute(context.Background(), operation)
	if err != nil || completed.Operation != domain.OperationNoop || completed.State != domain.OperationSkipped {
		t.Fatalf("replay was not a safe no-op: %+v, err=%v", completed, err)
	}
}

func TestOSFileExecutorReplaysMoveAfterEffectBeforeCommit(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "old-name.mkv")
	target := filepath.Join(root, "library", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("moved episode")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fingerprint := digest(string(content))
	operation := domain.FileOperation{Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: fingerprint, CollisionPolicy: domain.CollisionBlock}
	completed, err := (OSFileExecutor{}).Execute(context.Background(), operation)
	if err != nil || completed.State != domain.OperationSkipped || completed.Operation != domain.OperationNoop || completed.TargetFingerprint != fingerprint || completed.TargetSizeBytes != int64(len(content)) {
		t.Fatalf("move replay was not treated as an idempotent no-op: %+v err=%v", completed, err)
	}
}

func TestOSFileExecutorMoveReplayRemovesExistingSourceAndKeepsMatchingTarget(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "old-name.mkv")
	target := filepath.Join(root, "library", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("already moved")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fingerprint := digest(string(content))
	operation := domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: fingerprint, SourceSizeBytes: int64(len(content)), CollisionPolicy: domain.CollisionBlock,
	}
	completed, err := (OSFileExecutor{}).Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeTargetVerification {
		t.Fatalf("same-content different-inode move replay was accepted: %+v err=%v", completed, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("rejected move replay removed source: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(content) {
		t.Fatalf("rejected move replay changed target: %q err=%v", got, err)
	}
	return
}

func TestOSFileExecutorRejectsSourceSizeMismatch(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "library", "episode.mkv")
	if err := os.WriteFile(source, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{
		Operation: domain.OperationCopy, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("episode"), SourceSizeBytes: int64(len("episode")) + 1, CollisionPolicy: domain.CollisionVersion,
	}
	if _, err := (OSFileExecutor{}).Execute(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("source size mismatch was accepted: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("size-mismatched source created a target: %v", err)
	}
}

func TestOSFileExecutorTreatsExplicitZeroSizeAsKnown(t *testing.T) {
	root := canonicalTemp(t)
	nonEmpty := filepath.Join(root, "non-empty.mkv")
	if err := os.WriteFile(nonEmpty, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationCopy, SourcePath: nonEmpty, TargetPath: filepath.Join(root, "rejected.mkv"),
		SourceFingerprint: digest("episode"), SourceSizeBytes: 0, SourceSizeKnown: true, CollisionPolicy: domain.CollisionVersion,
	})
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("explicit zero size was not enforced: %v", err)
	}

	empty := filepath.Join(root, "empty.mkv")
	target := filepath.Join(root, "library", "empty.mkv")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	completed, err := (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationCopy, SourcePath: empty, TargetPath: target,
		SourceFingerprint: digest(""), SourceSizeBytes: 0, SourceSizeKnown: true, CollisionPolicy: domain.CollisionVersion,
	})
	if err != nil || completed.State != domain.OperationSucceeded || completed.TargetSizeBytes != 0 {
		t.Fatalf("explicit zero-size file was not placed: %+v err=%v", completed, err)
	}
}

func TestOSFileExecutorMoveFailurePreservesMatchingTarget(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "old-name.mkv")
	target := filepath.Join(root, "library", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("unexpected source"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetContent := []byte("expected target")
	if err := os.WriteFile(target, targetContent, 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("expected target"), SourceSizeBytes: int64(len(targetContent)), CollisionPolicy: domain.CollisionBlock,
	}
	if _, err := (OSFileExecutor{}).Execute(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("mismatched source was accepted: %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != string(targetContent) {
		t.Fatalf("matching target was removed or changed after move failure: %q err=%v", got, readErr)
	}
}

func TestOSFileObserverReturnsFingerprintAndSize(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode.mkv")
	content := []byte("episode content")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	observation, err := (OSFileObserver{}).ObserveFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Fingerprint != digest(string(content)) || observation.SizeBytes != int64(len(content)) {
		t.Fatalf("unexpected typed file observation: %+v", observation)
	}
	fingerprint, err := (OSFileObserver{}).Observe(context.Background(), path)
	if err != nil || fingerprint != observation.Fingerprint {
		t.Fatalf("legacy observer result diverged: fingerprint=%q err=%v", fingerprint, err)
	}
}

func TestOSFileRemoverRejectsSizeMismatchAndPreservesTarget(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode.mkv")
	content := []byte("episode")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	remover := OSFileRemover{}
	err := remover.RemoveFile(context.Background(), root, path, domain.FileObservation{Fingerprint: digest(string(content)), SizeBytes: int64(len(content)) + 1})
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("size-mismatched removal was accepted: %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != string(content) {
		t.Fatalf("size-mismatched removal changed target: %q err=%v", got, readErr)
	}
}

func TestOSFileRemoverRejectsReplacementRace(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode.mkv")
	content := []byte("episode")
	replacement := []byte("replacement")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	initial, err := observeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	remover := OSFileRemover{BeforeQuarantine: func(path string) error {
		for i := 0; i < 3; i++ {
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := os.WriteFile(path, replacement, 0o644); err != nil {
				return err
			}
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		return nil
	}}
	err = remover.RemoveFile(context.Background(), root, path, initial)
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("replacement was not rejected: %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != string(replacement) {
		t.Fatalf("replacement object was deleted: %q err=%v", got, readErr)
	}
	if _, statErr := os.Lstat(quarantinePath(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected replacement unexpectedly quarantined: %v", statErr)
	}
}

func TestOSFileRemoverQuarantineReplayIsIdempotent(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode.mkv")
	content := []byte("episode")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	remover := OSFileRemover{}
	if err := remover.RemoveFile(context.Background(), root, path, domain.FileObservation{Fingerprint: digest(string(content)), SizeBytes: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if err := remover.RemoveFile(context.Background(), root, path, domain.FileObservation{Fingerprint: digest(string(content)), SizeBytes: int64(len(content))}); err != nil {
		t.Fatalf("matching quarantine replay was not accepted: %v", err)
	}
}

func TestOSFileRemoverQuarantineReplayChecksIdentityAndConflict(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode.mkv")
	content := []byte("episode")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	initial, err := observeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := (OSFileRemover{}).RemoveFile(context.Background(), root, path, initial); err != nil {
		t.Fatal(err)
	}
	wrongIdentity := initial
	wrongIdentity.Identity.Inode++
	if err := (OSFileRemover{}).RemoveFile(context.Background(), root, path, wrongIdentity); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("quarantine replay accepted a different frozen identity: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyExpected := domain.FileObservation{Fingerprint: initial.Fingerprint, SizeBytes: initial.SizeBytes}
	if err := (OSFileRemover{}).RemoveFile(context.Background(), root, path, legacyExpected); err == nil || !strings.Contains(err.Error(), "both exist") {
		t.Fatalf("target and quarantine coexistence was not rejected: %v", err)
	}
}
func TestOSFileRemoverRejectsRootChildSymlink(t *testing.T) {
	root := canonicalTemp(t)
	outside := canonicalTemp(t)
	outsidePath := filepath.Join(outside, "episode.mkv")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "library")
	if err := os.Symlink(outside, child); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}
	target := filepath.Join(child, "episode.mkv")
	err := (OSFileRemover{}).RemoveFile(context.Background(), root, target, domain.FileObservation{Fingerprint: digest("outside"), SizeBytes: int64(len("outside"))})
	if err == nil {
		t.Fatal("cleanup followed a child symlink out of its root")
	}
	if got, readErr := os.ReadFile(outsidePath); readErr != nil || string(got) != "outside" {
		t.Fatalf("cleanup touched root-external file: %q err=%v", got, readErr)
	}
}
func TestOSMediaInspectorReturnsVersionedLocalSnapshot(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "Episode 01.MKV")
	if err := os.WriteFile(path, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (OSMediaInspector{}).Inspect(context.Background(), "artifact-1", path, digest("episode"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version == "" || snapshot.InspectorVersion == "" || snapshot.Values["media.file_extension"].Value != "mkv" || snapshot.SizeBytes != int64(len("episode")) || snapshot.Values["media.file_size_bytes"].Value != int64(len("episode")) {
		t.Fatalf("unexpected local media snapshot: %+v", snapshot)
	}
}

func TestMediaInfoFactsPreserveTypedTracksAndProvenance(t *testing.T) {
	values := map[string]domain.LocalMediaFact{}
	addMediaInfoFacts(values, mediaInfoDocument{Media: struct {
		Track []mediaInfoTrack `json:"track"`
	}{Track: []mediaInfoTrack{
		{Type: "General", Format: "Matroska", Duration: "90.500"},
		{Type: "Video", Format: "AVC", Width: "1920", Height: "1080", Profile: "High@L4.1", BitDepth: "10", HDRFormat: "HDR10"},
		{Type: "Audio", Format: "AAC", Channels: "2", Language: "ja"},
		{Type: "Text", Language: "zh-Hans"},
		{Type: "Text", Language: "en"},
	}}}, "artifact-media", "media-info-v2")

	checks := map[string]any{
		"media.container":          "Matroska",
		"media.duration_seconds":   90.5,
		"media.width":              int64(1920),
		"media.height":             int64(1080),
		"media.resolution":         "1080p",
		"media.video_codec":        "AVC",
		"media.video_profile":      "High@L4.1",
		"media.video_bit_depth":    "10",
		"media.hdr_format":         "HDR10",
		"media.audio_codec":        "AAC",
		"media.audio_channels":     "2",
		"media.audio_languages":    "ja",
		"media.subtitle_languages": "zh-Hans,en",
	}
	for name, expected := range checks {
		fact, ok := values[name]
		if !ok || fact.State != "value" || fact.Value != expected || fact.Source != "local_media" || fact.FactRef != "artifact-media" || fact.DefinitionVersion != "media-info-v2" {
			t.Fatalf("unexpected media fact %s: %+v (expected %v)", name, fact, expected)
		}
	}
}

func TestMediaInfoFailureProducesNullableFactsWithDiagnostics(t *testing.T) {
	values := map[string]domain.LocalMediaFact{}
	addUnavailableMediaFacts(values, "artifact-media", "media-info-v2", "MediaInfo unavailable")
	fact := values["media.video_codec"]
	if fact.State != "null" || fact.Value != nil || len(fact.Diagnostics) != 1 || fact.Source != "local_media" {
		t.Fatalf("media inspection failure was not nullable and diagnosable: %+v", fact)
	}
}

func TestOSFileScannerRecordsRegularFilesWithoutFollowingSymlinks(t *testing.T) {
	root := canonicalTemp(t)
	if err := os.MkdirAll(filepath.Join(root, "season"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "season", "episode-01.mkv"), []byte("episode-01"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := canonicalTemp(t)
	outsideFile := filepath.Join(outside, "outside.mkv")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "outside-link.mkv")); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-dir")); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}

	result, err := (OSFileScanner{Version: "scanner-test-v1"}).Scan(context.Background(), domain.ScanRequest{TreeTop: root, Scope: domain.ScanScope{Kind: domain.ScanScopeFull}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannerVersion != "scanner-test-v1" || len(result.Files) != 1 {
		t.Fatalf("unexpected scan result: %+v", result)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	file := result.Files[0]
	identity, err := fileidentity.FromPath(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Path-level: the scan records the metadata identity, never a digest.
	if file.Path != filepath.Join(resolvedRoot, "season", "episode-01.mkv") || file.Fingerprint != domain.MetadataFingerprint(int64(len("episode-01")), identity) || file.SizeBytes != int64(len("episode-01")) {
		t.Fatalf("unexpected scanned file: %+v", file)
	}
}

// TestOSFileScannerPlacementOwnedIsMetadataOnly proves the placement-owned
// scan records the path-level metadata identity (size + device/inode) and no
// content digest: a same-size rewrite in place keeps the same fingerprint.
func TestOSFileScannerPlacementOwnedIsMetadataOnly(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode-01.mkv")
	if err := os.WriteFile(path, []byte("episode-01"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := fileidentity.FromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (OSFileScanner{}).Scan(context.Background(), domain.ScanRequest{
		Scope: domain.ScanScope{Kind: domain.ScanScopePlacementOwned}, Paths: []string{path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Fingerprint != domain.MetadataFingerprint(10, identity) || result.Files[0].SizeBytes != 10 {
		t.Fatalf("unexpected placement-owned scan: %+v", result.Files)
	}
	if err := os.WriteFile(path, []byte("EPISODE-01"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := (OSFileScanner{}).Scan(context.Background(), domain.ScanRequest{
		Scope: domain.ScanScope{Kind: domain.ScanScopePlacementOwned}, Paths: []string{path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Files) != 1 || again.Files[0].Fingerprint != result.Files[0].Fingerprint {
		t.Fatalf("same-size in-place rewrite must keep the metadata identity (accepted cost): %+v vs %+v", again.Files, result.Files)
	}
}

func TestOSFileScannerTargetedMissingPathIsScopedAbsence(t *testing.T) {
	root := canonicalTemp(t)
	result, err := (OSFileScanner{}).Scan(context.Background(), domain.ScanRequest{
		TreeTop: root,
		Scope:   domain.ScanScope{Kind: domain.ScanScopeTargeted, RelativePath: "season/episode-02.mkv"},
	})
	if err != nil || len(result.Files) != 0 {
		t.Fatalf("missing targeted file should complete with no facts: %+v err=%v", result, err)
	}
	if _, err := (OSFileScanner{}).Scan(context.Background(), domain.ScanRequest{
		TreeTop: root,
		Scope:   domain.ScanScope{Kind: domain.ScanScopeTargeted, RelativePath: "../outside.mkv"},
	}); err == nil || !strings.Contains(err.Error(), "scan scope") {
		t.Fatalf("targeted path escaped the root: %v", err)
	}
}

func TestOSFileExecutorOverwriteExactOperations(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		t.Run(string(kind), func(t *testing.T) {
			root := canonicalTemp(t)
			source := filepath.Join(root, "source.mkv")
			target := filepath.Join(root, "library", "episode.mkv")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			operation := domain.FileOperation{ID: "overwrite-" + string(kind), Operation: kind, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite}
			executor := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
				return operation, nil
			})
			completed, err := executor.Execute(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			if completed.State != domain.OperationSucceeded || completed.TargetFingerprint != digest("new") || completed.OverwriteRecovery == nil || completed.OverwriteRecovery.Stage != domain.OverwriteStageCommitted {
				t.Fatalf("unexpected completion: %+v", completed)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
				t.Fatalf("unexpected target: %q err=%v", got, err)
			}
			if kind == domain.OperationMove {
				if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("move source remains: %v", err)
				}
			} else if _, err := os.Stat(source); err != nil {
				t.Fatalf("non-move consumed source: %v", err)
			}
			if kind == domain.OperationHardlink {
				sourceInfo, _ := os.Stat(source)
				targetInfo, _ := os.Stat(target)
				if !os.SameFile(sourceInfo, targetInfo) {
					t.Fatal("hardlink overwrite fell back to copy")
				}
			}
		})
	}
}

func TestOSFileExecutorAllowsSourceOutsideRootForAllPlacementPaths(t *testing.T) {
	base := canonicalTemp(t)
	root := filepath.Join(base, "library")
	downloads := filepath.Join(base, "downloads")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, collision := range []domain.CollisionPolicy{domain.CollisionBlock, domain.CollisionOverwrite} {
		for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
			t.Run(string(collision)+"/"+string(kind), func(t *testing.T) {
				source := filepath.Join(downloads, string(collision)+"-"+string(kind)+".mkv")
				target := filepath.Join(root, string(collision)+"-"+string(kind)+".mkv")
				if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
					t.Fatal(err)
				}
				if collision == domain.CollisionOverwrite {
					if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
						t.Fatal(err)
					}
				}

				operation := domain.FileOperation{
					ID: "outside-root-" + string(collision) + "-" + string(kind), Operation: kind,
					SourcePath: source, TargetPath: target,
					SourceFingerprint: digest("new"), CollisionPolicy: collision,
				}
				executor := OSFileExecutor{}
				if collision == domain.CollisionOverwrite {
					executor = executor.BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
						return operation, nil
					})
				}
				completed, err := executor.Execute(context.Background(), operation)
				if err != nil {
					t.Fatal(err)
				}
				if completed.State != domain.OperationSucceeded || completed.TargetFingerprint != digest("new") {
					t.Fatalf("unexpected completion: %+v", completed)
				}
				if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
					t.Fatalf("unexpected target: %q err=%v", got, err)
				}
				if kind == domain.OperationMove {
					if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("move source remains: %v", err)
					}
				} else {
					if _, err := os.Lstat(source); err != nil {
						t.Fatalf("source was unexpectedly consumed: %v", err)
					}
				}
				if kind == domain.OperationHardlink {
					sourceInfo, sourceErr := os.Stat(source)
					targetInfo, targetErr := os.Stat(target)
					if sourceErr != nil || targetErr != nil || !os.SameFile(sourceInfo, targetInfo) {
						t.Fatalf("hardlink placement did not preserve source identity: source=%v target=%v sourceErr=%v targetErr=%v", sourceInfo, targetInfo, sourceErr, targetErr)
					}
				}
			})
		}
	}
}

func TestOSFileExecutorCheckpointPropagatesUpdatedOperation(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	versions := make([]int64, 0)
	executor := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		operation.Version++
		versions = append(versions, operation.Version)
		return operation, nil
	})
	completed, err := executor.Execute(context.Background(), domain.FileOperation{ID: "checkpoint", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Version != int64(len(versions)) || len(versions) < 4 {
		t.Fatalf("checkpoint update was not propagated: completed=%+v versions=%v", completed, versions)
	}
}

func TestOSFileExecutorBindCheckpointCopiesAndIsolatesCallbacks(t *testing.T) {
	original := OSFileExecutor{}
	counts := [2]int{}
	first := original.BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		counts[0]++
		return operation, nil
	})
	second := original.BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		counts[1]++
		return operation, nil
	})
	if original.Checkpoint != nil {
		t.Fatal("binding mutated the original executor")
	}
	for index, executor := range []OSFileExecutor{first, second} {
		root := canonicalTemp(t)
		source := filepath.Join(root, "source")
		target := filepath.Join(root, "target")
		if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := executor.Execute(context.Background(), domain.FileOperation{ID: fmt.Sprintf("bound-%d", index), Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite})
		if err != nil {
			t.Fatal(err)
		}
		if counts[index] == 0 || counts[1-index] != 0 {
			t.Fatalf("bound callbacks were not isolated after executor %d: %v", index, counts)
		}
		counts[index] = 0
	}
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := original.BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}).Execute(context.Background(), domain.FileOperation{ID: "original", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite}); err != nil {
		t.Fatal(err)
	}
	if counts != [2]int{} {
		t.Fatalf("original executor invoked a bound callback: %v", counts)
	}
}

func TestOSFileExecutorRestoresRacedOverwriteTarget(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{BeforeExchange: func(_, target string) error { return os.WriteFile(target, []byte("raced"), 0o644) }}}
	_, err := executor.Execute(context.Background(), domain.FileOperation{ID: "race", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeTargetCollisionRace {
		t.Fatalf("race was not typed: %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "raced" {
		t.Fatalf("raced writer was not restored: %q err=%v", got, readErr)
	}
}

func TestOSFileExecutorOverwriteMoveRaceRestoresSourceAndTarget(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{BeforeExchange: func(_, target string) error {
		return os.WriteFile(target, []byte("raced"), 0o644)
	}}}
	completed, err := executor.Execute(context.Background(), domain.FileOperation{ID: "move-race", Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeTargetCollisionRace {
		t.Fatalf("move race was not typed: %+v err=%v", completed, err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "raced" {
		t.Fatalf("move race target was not restored: %q err=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "new" {
		t.Fatalf("moved source was not restored: %q err=%v", got, readErr)
	}
	if completed.OverwriteRecovery == nil || completed.OverwriteRecovery.Stage != domain.OverwriteStageAborted {
		t.Fatalf("move race did not persist aborted checkpoint: %+v", completed)
	}
	if _, statErr := os.Lstat(stagingPath(completed)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("move race left source in staging: %v", statErr)
	}
}

func TestOSFileExecutorRecoversExchangedCheckpoint(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("crash after exchange")
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		if recovery.Stage == domain.OverwriteStageExchanged {
			return operation, stop
		}
		return operation, nil
	}}
	operation := domain.FileOperation{ID: "recover", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite}
	checkpointed, err := executor.Execute(context.Background(), operation)
	if !errors.Is(err, stop) || checkpointed.OverwriteRecovery == nil {
		t.Fatalf("missing exchanged checkpoint: %+v err=%v", checkpointed, err)
	}
	completed, err := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}).Execute(context.Background(), checkpointed)
	if err != nil || completed.State != domain.OperationSucceeded {
		t.Fatalf("exchange recovery failed: %+v err=%v", completed, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("recovery changed final target: %q", got)
	}
}

func TestOSFileExecutorRecoversExchangedMismatchByRestoringDisplacedTarget(t *testing.T) {
	root := canonicalTemp(t)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(canonical, "source")
	target := filepath.Join(canonical, "target")
	staging := filepath.Join(canonical, ".bansoko-overwrite-"+hex.EncodeToString(mustOperationSum("recover-race")))
	for path, content := range map[string]string{source: "new", target: "new", staging: "raced"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldPath := filepath.Join(canonical, "old-target")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceObservation, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	stagedObservation, err := observeFile(target)
	if err != nil {
		t.Fatal(err)
	}
	targetObservation, err := observeFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
	if err != nil {
		t.Fatal(err)
	}
	recovery := domain.OverwriteRecovery{
		Stage: domain.OverwriteStageExchanged, CanonicalRoot: chainAnchor, CanonicalSource: source,
		CanonicalTarget: target, StagingPath: staging,
		SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity,
		SourceObservation: sourceObservation, TargetObservation: targetObservation,
		StagedObservation: stagedObservation,
	}
	operation := domain.FileOperation{ID: "recover-race", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), SourceSizeBytes: int64(len("new")), SourceSizeKnown: true, SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity, CollisionPolicy: domain.CollisionOverwrite, OverwriteRecovery: &recovery}
	checkpointed, err := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}).Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeTargetCollisionRace || checkpointed.OverwriteRecovery == nil || checkpointed.OverwriteRecovery.Stage != domain.OverwriteStageRestored {
		t.Fatalf("exchanged mismatch was not restored and typed: %+v err=%v", checkpointed, err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "new" {
		t.Fatalf("verified placement was not retained: %q err=%v", got, readErr)
	}
	if checkpointed.OverwriteRecovery.ForeignEvidence.Fingerprint != digest("raced") {
		t.Fatalf("foreign evidence was not frozen: %+v", checkpointed.OverwriteRecovery)
	}
}

func TestOSFileExecutorRestoredRecoveryVerifiesFrozenDisplacedTarget(t *testing.T) {
	root := canonicalTemp(t)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(canonical, "source")
	target := filepath.Join(canonical, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
	if err != nil {
		t.Fatal(err)
	}
	recovery := domain.OverwriteRecovery{
		Stage: domain.OverwriteStageRestored, CanonicalRoot: chainAnchor, CanonicalSource: source,
		CanonicalTarget: target, StagingPath: stagingPath(domain.FileOperation{ID: "restored", TargetPath: target}),
		SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity,
		SourceObservation:    domain.FileObservation{Fingerprint: digest("new"), SizeBytes: int64(len("new"))},
		DisplacedObservation: domain.FileObservation{Fingerprint: digest("raced"), SizeBytes: int64(len("raced"))},
	}
	_, err = (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}).Execute(context.Background(), domain.FileOperation{ID: "restored", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite, OverwriteRecovery: &recovery})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("restored checkpoint mismatch was not rejected safely: %v", err)
	}
}

func TestOSFileExecutorHardlinkSourceReplacementAfterObservationCleansTarget(t *testing.T) {
	replacement := "replacement"
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{Hooks: ExecutorHooks{BeforeHardlinkMutation: func(source, _ string) error {
		for i := 0; i < 3; i++ {
			if err := os.Remove(source); err != nil {
				return err
			}
			if err := os.WriteFile(source, []byte(replacement), 0o644); err != nil {
				return err
			}
		}
		if err := os.Chmod(source, 0o600); err != nil {
			return err
		}
		return nil
	}}}
	_, err := executor.Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationHardlink, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("original"), CollisionPolicy: domain.CollisionBlock,
	})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || (operationErr.Code != CodeTargetVerification && operationErr.Code != CodeRecoveryUnsafe) {
		t.Fatalf("source replacement hardlink returned unexpected error: %v", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mismatched hardlink target remained: %v", statErr)
	}
	if got, readErr := os.ReadFile(source); readErr != nil || string(got) != replacement {
		t.Fatalf("source replacement was not preserved: %q err=%v", got, readErr)
	}
}

func TestOSFileExecutorRevalidatesSourceBeforeAbsentMutation(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		t.Run(string(kind), func(t *testing.T) {
			root := canonicalTemp(t)
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			if err := os.WriteFile(source, []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			executor := OSFileExecutor{Hooks: ExecutorHooks{BeforeAbsentMutation: func(source, _ string) error {
				for i := 0; i < 3; i++ {
					if err := os.Remove(source); err != nil {
						return err
					}
					if err := os.WriteFile(source, []byte("changed"), 0o644); err != nil {
						return err
					}
				}
				return nil
			}}}
			_, err := executor.Execute(context.Background(), domain.FileOperation{Operation: kind, SourcePath: source, TargetPath: target, SourceFingerprint: digest("original"), CollisionPolicy: domain.CollisionBlock})
			var operationErr *OperationError
			if !errors.As(err, &operationErr) || operationErr.Code != CodeSourceChanged {
				t.Fatalf("source mutation was not rejected for %s: %v", kind, err)
			}
			if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("source mutation created target for %s: %v", kind, statErr)
			}
		})
	}
}

func TestOSFileExecutorMoveMismatchRestoresSourceWithoutFallback(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{Hooks: ExecutorHooks{AfterAbsentMutation: func(_, target string) error {
		return os.WriteFile(target, []byte("changed"), 0o644)
	}}}
	_, err := executor.Execute(context.Background(), domain.FileOperation{Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: digest("original"), CollisionPolicy: domain.CollisionBlock})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeSourceChanged {
		t.Fatalf("restored move mismatch was not typed as source change: %v", err)
	}
	if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "changed" {
		t.Fatalf("moved object was not restored to source: %q err=%v", got, readErr)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("raced move object remained at target: %v", statErr)
	}
}

func TestOSFileExecutorExecuteWithCheckpointUsesCallback(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	completed, err := (OSFileExecutor{}).ExecuteWithCheckpoint(context.Background(), domain.FileOperation{
		ID: "direct-checkpoint", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
	}, func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		calls++
		operation.Version++
		return operation, nil
	})
	if err != nil || calls == 0 || completed.Version != int64(calls) {
		t.Fatalf("direct checkpoint execution failed: completed=%+v calls=%d err=%v", completed, calls, err)
	}
}

func TestOSFileExecutorAtomicUnsupportedDoesNotMutate(t *testing.T) {
	for _, injected := range []error{errAtomicReplaceUnsupported, fmt.Errorf("%w: EXDEV", errAtomicReplaceUnsupported)} {
		root := canonicalTemp(t)
		source := filepath.Join(root, "source")
		target := filepath.Join(root, "target")
		if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
			return operation, nil
		}, Hooks: ExecutorHooks{AtomicExchangeCheck: func() error { return injected }}}
		_, err := executor.Execute(context.Background(), domain.FileOperation{
			ID: "unsupported", Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
			SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
		})
		var operationErr *OperationError
		if !errors.As(err, &operationErr) || operationErr.Code != CodeAtomicReplaceUnsupported {
			t.Fatalf("unsupported error was not typed: %v", err)
		}
		if got, _ := os.ReadFile(source); string(got) != "new" {
			t.Fatalf("unsupported overwrite changed source: %q", got)
		}
		if got, _ := os.ReadFile(target); string(got) != "old" {
			t.Fatalf("unsupported overwrite changed target: %q", got)
		}
		if _, statErr := os.Lstat(stagingPath(domain.FileOperation{ID: "unsupported", TargetPath: target})); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unsupported overwrite created staging: %v", statErr)
		}
	}
}

func TestOSFileExecutorOverwriteCopyStagingOpenFailureIsRecoveryUnsafe(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{ID: "copy-stage-open", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{BeforeExactCopy: func(source, _ string) error {
		return os.Remove(source)
	}}}
	_, err := executor.Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("copy staging open failure was not recovery-unsafe: %v", err)
	}
}

func TestOSFileExecutorStrictOverwriteMoveRejectsSourceReplacement(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	replacement := []byte("replacement")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := domain.FileOperation{ID: "strict-source", Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		if recovery.Stage == domain.OverwriteStagePrepared {
			for i := 0; i < 3; i++ {
				if err := os.Remove(source); err != nil {
					return operation, err
				}
				if err := os.WriteFile(source, replacement, 0o644); err != nil {
					return operation, err
				}
			}
			if err := os.Chmod(source, 0o600); err != nil {
				return operation, err
			}
		}
		return operation, nil
	}}
	_, err := executor.Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("source replacement was accepted: %v", err)
	}
}
func TestOSFileExecutorOverwriteMoveStagingFailureDoesNotFallback(t *testing.T) {
	for _, injected := range []error{os.ErrPermission, syscall.EXDEV} {
		t.Run(injected.Error(), func(t *testing.T) {
			root := canonicalTemp(t)
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
				return operation, nil
			}, Hooks: ExecutorHooks{AtomicRenameNoReplace: func(_, _ string) error { return injected }}}
			operation := domain.FileOperation{
				ID: "move-staging-" + strings.ReplaceAll(injected.Error(), " ", "-"), Operation: domain.OperationMove,
				SourcePath: source, TargetPath: target,
				SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
			}
			if _, err := executor.Execute(context.Background(), operation); !errors.Is(err, injected) {
				t.Fatalf("exact move staging error was not preserved: %v", err)
			}
			if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "new" {
				t.Fatalf("failed move staging changed source: %q err=%v", got, readErr)
			}
			if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
				t.Fatalf("failed move staging changed target: %q err=%v", got, readErr)
			}
			if _, statErr := os.Lstat(stagingPath(operation)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed move staging created a fallback artifact: %v", statErr)
			}
		})
	}
}

func TestOSFileExecutorExchangeTimeUnsupportedRestoresInitialState(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		for _, injected := range []error{
			fmt.Errorf("%w: ENOTSUP", errAtomicReplaceUnsupported),
			fmt.Errorf("%w: EXDEV", errAtomicReplaceUnsupported),
		} {
			t.Run(string(kind)+"/"+injected.Error(), func(t *testing.T) {
				root := canonicalTemp(t)
				source := filepath.Join(root, "source")
				target := filepath.Join(root, "target")
				if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
					return operation, nil
				}, Hooks: ExecutorHooks{AtomicExchange: func(_, _ string) error { return injected }}}
				operation := domain.FileOperation{
					ID: "exchange-unsupported-" + string(kind), Operation: kind, SourcePath: source, TargetPath: target,
					SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
				}
				_, err := executor.Execute(context.Background(), operation)
				var operationErr *OperationError
				if !errors.As(err, &operationErr) || operationErr.Code != CodeAtomicReplaceUnsupported {
					t.Fatalf("exchange-time unsupported error was not typed: %v", err)
				}
				if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "new" {
					t.Fatalf("unsupported exchange did not restore source: %q err=%v", got, readErr)
				}
				if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
					t.Fatalf("unsupported exchange changed target: %q err=%v", got, readErr)
				}
				if _, statErr := os.Lstat(stagingPath(operation)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("unsupported exchange left staging: %v", statErr)
				}
			})
		}
	}
}

func TestOSFileExecutorRecoveryExchangeTimeUnsupportedRestoresMoveSource(t *testing.T) {
	root := canonicalTemp(t)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(canonical, "source")
	target := filepath.Join(canonical, "target")
	operation := domain.FileOperation{
		ID: "recovery-unsupported", Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), SourceSizeBytes: 3, SourceSizeKnown: true,
		CollisionPolicy: domain.CollisionOverwrite,
	}
	staging := stagingPath(operation)
	if err := os.WriteFile(staging, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
	if err != nil {
		t.Fatal(err)
	}
	operation.SourceParentIdentity = parentIdentity
	operation.TargetParentIdentity = parentIdentity
	stagedObservation, err := observeFile(staging)
	if err != nil {
		t.Fatal(err)
	}
	targetObservation, err := observeFile(target)
	if err != nil {
		t.Fatal(err)
	}
	operation.OverwriteRecovery = &domain.OverwriteRecovery{
		Stage: domain.OverwriteStageStaged, CanonicalRoot: chainAnchor, CanonicalSource: source,
		CanonicalTarget: target, StagingPath: staging,
		SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity,
		SourceObservation: domain.FileObservation{Fingerprint: digest("new"), SizeBytes: 3, Identity: stagedObservation.Identity},
		TargetObservation: domain.FileObservation{Fingerprint: digest("old"), SizeBytes: 3, Identity: targetObservation.Identity},
		StagedObservation: domain.FileObservation{Fingerprint: digest("new"), SizeBytes: 3, Identity: stagedObservation.Identity},
	}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{AtomicExchange: func(_, _ string) error {
		return fmt.Errorf("%w: EXDEV", errAtomicReplaceUnsupported)
	}}}
	_, err = executor.Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeAtomicReplaceUnsupported {
		t.Fatalf("recovery unsupported error was not typed: %v", err)
	}
	if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "new" {
		t.Fatalf("recovery did not restore move source: %q err=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
		t.Fatalf("recovery changed target: %q err=%v", got, readErr)
	}
	if _, statErr := os.Lstat(staging); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("recovery left staging: %v", statErr)
	}
}

func TestOSFileExecutorRejectsSourceParentSymlinkSwapBeforeRestore(t *testing.T) {
	root := canonicalTemp(t)
	sourceParent := filepath.Join(root, "downloads")
	if err := os.Mkdir(sourceParent, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceParent, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := canonicalTemp(t)
	movedParent := sourceParent + ".moved"
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{
		BeforeExchange: func(_, target string) error { return os.WriteFile(target, []byte("raced"), 0o644) },
		BeforeSourceRestore: func(_, _ string) error {
			if err := os.Rename(sourceParent, movedParent); err != nil {
				return err
			}
			return os.Symlink(outside, sourceParent)
		},
	}}
	operation := domain.FileOperation{
		ID: "source-parent-swap", Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
	}
	_, err := executor.Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || (operationErr.Code != CodePathUnsafe && operationErr.Code != CodeRecoveryUnsafe) {
		t.Fatalf("source parent symlink swap was not rejected: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "source")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source escaped through swapped parent: %v", statErr)
	}
	if got, readErr := os.ReadFile(stagingPath(operation)); readErr != nil || string(got) != "new" {
		t.Fatalf("rejected restore lost staged source: %q err=%v", got, readErr)
	}
}

func TestObserveFileRejectsPathSwapBetweenLstatAndOpen(t *testing.T) {
	root := canonicalTemp(t)
	path := filepath.Join(root, "episode")
	oldPath := filepath.Join(root, "episode.old")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := observeFileWithHook(path, func(path string) error {
		if err := os.Rename(path, oldPath); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("new"), 0o644)
	})
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("path swap was not rejected: %v", err)
	}
}

func TestOSFileExecutorRejectsRecoveryWithoutParentIdentityBeforeMutation(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	recovery := domain.OverwriteRecovery{
		Stage: domain.OverwriteStagePrepared, CanonicalRoot: chainAnchor, CanonicalSource: source,
		CanonicalTarget: target, StagingPath: stagingPath(domain.FileOperation{ID: "missing-parent", TargetPath: target}),
		SourceObservation: domain.FileObservation{Fingerprint: digest("new"), SizeBytes: 3},
		TargetObservation: domain.FileObservation{Fingerprint: digest("old"), SizeBytes: 3},
	}
	operation := domain.FileOperation{ID: "missing-parent", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), SourceSizeBytes: 3, SourceSizeKnown: true, CollisionPolicy: domain.CollisionOverwrite, OverwriteRecovery: &recovery}
	_, err := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		t.Fatal("checkpoint must not be called for incomplete recovery")
		return operation, nil
	}).Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("missing parent identity was not rejected safely: %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
		t.Fatalf("incomplete recovery mutated target: %q err=%v", got, readErr)
	}
}
func TestOSFileExecutorRejectsRecoverySourceTamper(t *testing.T) {
	root := canonicalTemp(t)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(canonical, "source")
	target := filepath.Join(canonical, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
	if err != nil {
		t.Fatal(err)
	}
	recovery := domain.OverwriteRecovery{
		Stage: domain.OverwriteStagePrepared, CanonicalRoot: chainAnchor, CanonicalSource: filepath.Join(canonical, "other"),
		CanonicalTarget: target, StagingPath: stagingPath(domain.FileOperation{ID: "tampered", TargetPath: target}),
		SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity,
		SourceObservation: domain.FileObservation{Fingerprint: digest("new"), SizeBytes: 3},
		TargetObservation: domain.FileObservation{Fingerprint: digest("old"), SizeBytes: 3},
	}
	_, err = (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		ID: "tampered", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), SourceSizeBytes: 3, SourceSizeKnown: true,
		CollisionPolicy: domain.CollisionOverwrite, OverwriteRecovery: &recovery,
	})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("tampered recovery was not rejected: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("tampered recovery changed target: %q", got)
	}
}

func TestOSFileExecutorPreparedMismatchedStagingWithMissingSourceIsRecoveryUnsafe(t *testing.T) {
	root := canonicalTemp(t)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(canonical, "source")
	target := filepath.Join(canonical, "target")
	staging := stagingPath(domain.FileOperation{ID: "prepared-mismatch", TargetPath: target})
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceObservation, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	targetObservation, err := observeFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, []byte("unexpected"), 0o644); err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
	if err != nil {
		t.Fatal(err)
	}
	recovery := domain.OverwriteRecovery{
		Stage:                domain.OverwriteStagePrepared,
		CanonicalRoot:        chainAnchor,
		CanonicalSource:      source,
		CanonicalTarget:      target,
		StagingPath:          staging,
		SourceParentIdentity: parentIdentity,
		TargetParentIdentity: parentIdentity,
		SourceObservation:    sourceObservation,
		TargetObservation:    targetObservation,
	}
	operation := domain.FileOperation{
		ID: "prepared-mismatch", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), SourceSizeBytes: 3, SourceSizeKnown: true,
		CollisionPolicy: domain.CollisionOverwrite, OverwriteRecovery: &recovery,
	}
	_, err = (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}).Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("prepared mismatched staging was not recovery-unsafe: %v", err)
	}
	if got, readErr := os.ReadFile(staging); readErr != nil || string(got) != "unexpected" {
		t.Fatalf("mismatched staging evidence was changed: %q err=%v", got, readErr)
	}
}

func TestSyncRecoveryDirectoriesDoesNotReadCopySourceParent(t *testing.T) {
	root, err := canonicalRoot(canonicalTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(root, "library")
	stagingDir := filepath.Join(root, "staging")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syncRecoveryDirectories(domain.FileOperation{Operation: domain.OperationCopy}, root, domain.OverwriteRecovery{
		CanonicalSource: filepath.Join(root, "missing", "source"),
		CanonicalTarget: filepath.Join(targetDir, "target"),
		StagingPath:     filepath.Join(stagingDir, "staging"),
	}); err != nil {
		t.Fatalf("copy recovery attempted to sync missing source parent: %v", err)
	}
}

func TestOSFileExecutorMoveReplayAllowsSameInodeHardlink(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	content := []byte("same inode")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, target); err != nil {
		t.Fatal(err)
	}
	completed, err := (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest(string(content)), SourceSizeBytes: int64(len(content)), SourceSizeKnown: true,
		CollisionPolicy: domain.CollisionBlock,
	})
	if err != nil || completed.State != domain.OperationSucceeded || completed.Operation != domain.OperationMove {
		t.Fatalf("same-inode move replay was rejected: %+v err=%v", completed, err)
	}
	if _, statErr := os.Lstat(source); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("same-inode move replay left source: %v", statErr)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != string(content) {
		t.Fatalf("same-inode move replay changed target: %q err=%v", got, readErr)
	}
}

func TestOSFileExecutorCleanupReplacementPreservesUnknownStagingObject(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{
		Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
			return operation, nil
		},
		Hooks: ExecutorHooks{BeforeStagingRemove: func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("unknown"), 0o644)
		}},
	}
	_, err := executor.Execute(context.Background(), domain.FileOperation{
		ID: "cleanup-replacement", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
	})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("cleanup replacement was not recovery-unsafe: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	staging := stagingPath(domain.FileOperation{ID: "cleanup-replacement", TargetPath: filepath.Join(canonical, "target")})
	quarantine := quarantinePath(staging)
	stagingContents, stagingErr := os.ReadFile(staging)
	quarantineContents, quarantineErr := os.ReadFile(quarantine)
	if (stagingErr != nil || string(stagingContents) != "unknown") && (quarantineErr != nil || string(quarantineContents) != "unknown") {
		t.Fatalf("unknown staging replacement was not preserved in staging/quarantine: staging=%q/%v quarantine=%q/%v", stagingContents, stagingErr, quarantineContents, quarantineErr)
	}
}

func TestOSFileExecutorTemporaryCleanupReplacementPreservesObject(t *testing.T) {
	root := canonicalTemp(t)
	temporary := filepath.Join(root, "temporary")
	if err := os.WriteFile(temporary, []byte("expected"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalRootPath, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	temporary = filepath.Join(canonicalRootPath, "temporary")
	parentIdentity, err := verifiedParentIdentityWithinRoot("", temporary, false)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := openMutationEntry("", temporary, parentIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.parent.Close()
	expectedInfo, err := os.Lstat(temporary)
	if err != nil {
		t.Fatal(err)
	}
	expectedIdentity := fileIdentity(expectedInfo)
	replacement := []byte("unknown replacement")
	err = removeVerifiedEntryHandle(entry, expectedIdentity, func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.WriteFile(path, replacement, 0o644)
	})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("temporary cleanup replacement was not recovery-unsafe: %v", err)
	}
	quarantine := quarantinePath(temporary)
	preserved := false
	for _, path := range []string{temporary, quarantine} {
		if got, readErr := os.ReadFile(path); readErr == nil && string(got) == string(replacement) {
			preserved = true
			break
		}
	}
	if !preserved {
		t.Fatalf("replacement object was deleted: staging=%s quarantine=%s", temporary, quarantine)
	}
}

func TestOSFileExecutorFinalMismatchCopyPreservesRacedTarget(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{BeforeFinalRead: func(target string) error {
		if err := os.Remove(target); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("raced-copy-final"), 0o644)
	}}}
	operation := domain.FileOperation{
		ID: "final-mismatch-copy", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
	}
	completed, err := executor.Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeTargetCollisionRace {
		t.Fatalf("copy final mismatch did not converge to collision: %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
		t.Fatalf("displaced target was not restored: %q err=%v", got, readErr)
	}
	quarantine := ""
	if completed.OverwriteRecovery != nil {
		quarantine = completed.OverwriteRecovery.ForeignEvidencePath
	}
	if quarantine == "" {
		t.Fatalf("foreign evidence checkpoint was not persisted: %+v", completed.OverwriteRecovery)
	}
	if got, readErr := os.ReadFile(quarantine); readErr != nil || string(got) != "raced-copy-final" {
		t.Fatalf("raced copy target was deleted instead of preserved in quarantine: %q err=%v", got, readErr)
	}
	second, secondErr := executor.Execute(context.Background(), completed)
	var secondOperationErr *OperationError
	if !errors.As(secondErr, &secondOperationErr) || secondOperationErr.Code != CodeTargetCollisionRace {
		t.Fatalf("second recovery did not converge to a collision: %+v err=%v", second, secondErr)
	}
}

func TestOSFileExecutorFinalMismatchNeverMovesRacedTargetToSource(t *testing.T) {
	root := canonicalTemp(t)
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{Checkpoint: func(_ context.Context, operation domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		return operation, nil
	}, Hooks: ExecutorHooks{BeforeFinalRead: func(target string) error {
		return os.WriteFile(target, []byte("raced-final"), 0o644)
	}}}
	_, err := executor.Execute(context.Background(), domain.FileOperation{
		ID: "final-mismatch", Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: digest("new"), CollisionPolicy: domain.CollisionOverwrite,
	})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryBlocked {
		t.Fatalf("final mismatch move did not persist blocked recovery: %v", err)
	}
	if _, statErr := os.Lstat(source); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("raced final target was moved into source: %v", statErr)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
		t.Fatalf("displaced target was not restored: %q err=%v", got, readErr)
	}
}

func TestOSFileExecutorExchangeBackForeignRecoveryCompletesCollision(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationHardlink} {
		t.Run(string(kind), func(t *testing.T) {
			root, err := canonicalRoot(canonicalTemp(t))
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			staging := stagingPath(domain.FileOperation{ID: "exchange-back-" + string(kind), TargetPath: target})
			if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("foreign"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(staging, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
			if err != nil {
				t.Fatal(err)
			}
			sourceObservation, err := observeFile(source)
			if err != nil {
				t.Fatal(err)
			}
			displacedObservation, err := observeFile(staging)
			if err != nil {
				t.Fatal(err)
			}
			placementPath := filepath.Join(root, "placement")
			if err := os.WriteFile(placementPath, []byte("placement"), 0o644); err != nil {
				t.Fatal(err)
			}
			placementObservation, err := observeFile(placementPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(placementPath); err != nil {
				t.Fatal(err)
			}
			operation := domain.FileOperation{
				ID: "exchange-back-" + string(kind), Operation: kind, SourcePath: source, TargetPath: target,
				SourceFingerprint: sourceObservation.Fingerprint, SourceSizeBytes: sourceObservation.SizeBytes,
				SourceSizeKnown: true, CollisionPolicy: domain.CollisionOverwrite, SourceParentIdentity: parentIdentity,
				TargetParentIdentity: parentIdentity, OverwriteRecovery: &domain.OverwriteRecovery{
					Stage: domain.OverwriteStageExchanged, CanonicalRoot: chainAnchor, CanonicalSource: source, CanonicalTarget: target,
					StagingPath: staging, SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity,
					SourceObservation: sourceObservation, TargetObservation: displacedObservation, StagedObservation: placementObservation,
					DisplacedObservation: displacedObservation,
				},
			}
			checkpoints := 0
			executor := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
				checkpoints++
				operation.OverwriteRecovery = &recovery
				return operation, nil
			})
			completed, err := executor.Execute(context.Background(), operation)
			var operationErr *OperationError
			if !errors.As(err, &operationErr) || operationErr.Code != CodeTargetCollisionRace {
				t.Fatalf("exchange-back foreign recovery did not converge: %+v err=%v", completed, err)
			}
			if completed.OverwriteRecovery == nil || completed.OverwriteRecovery.Stage != domain.OverwriteStageRestored || checkpoints == 0 {
				t.Fatalf("recovery checkpoint was not finalized: %+v checkpoints=%d", completed, checkpoints)
			}
			if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
				t.Fatalf("displaced target was not restored: %q err=%v", got, readErr)
			}
			quarantine := completed.OverwriteRecovery.ForeignEvidencePath
			if got, readErr := os.ReadFile(quarantine); readErr != nil || string(got) != "foreign" {
				t.Fatalf("foreign evidence was not quarantined: %q err=%v", got, readErr)
			}
			second, secondErr := executor.Execute(context.Background(), completed)
			var secondOperationErr *OperationError
			if !errors.As(secondErr, &secondOperationErr) || secondOperationErr.Code != CodeTargetCollisionRace {
				t.Fatalf("second recovery did not remain collision: %+v err=%v", second, secondErr)
			}
		})
	}
}

func TestOSFileExecutorExchangeBackForeignMoveRecoveryRemainsBlocked(t *testing.T) {
	root, err := canonicalRoot(canonicalTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	staging := stagingPath(domain.FileOperation{ID: "exchange-back-move", TargetPath: target})
	for path, content := range map[string]string{source: "new", target: "foreign", staging: "old"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot("", target, false)
	if err != nil {
		t.Fatal(err)
	}
	sourceObservation, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	placementObservation, err := observeFile(target)
	if err != nil {
		t.Fatal(err)
	}
	displacedObservation, err := observeFile(staging)
	if err != nil {
		t.Fatal(err)
	}
	executor := (OSFileExecutor{}).BindCheckpoint(func(_ context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		operation.OverwriteRecovery = &recovery
		return operation, nil
	})
	operation := domain.FileOperation{ID: "exchange-back-move", Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: sourceObservation.Fingerprint, SourceSizeBytes: sourceObservation.SizeBytes, SourceSizeKnown: true,
		CollisionPolicy: domain.CollisionOverwrite, SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity,
		OverwriteRecovery: &domain.OverwriteRecovery{Stage: domain.OverwriteStageExchanged, CanonicalRoot: chainAnchor, CanonicalSource: source, CanonicalTarget: target,
			StagingPath: staging, SourceParentIdentity: parentIdentity, TargetParentIdentity: parentIdentity, SourceObservation: sourceObservation,
			TargetObservation: displacedObservation, StagedObservation: placementObservation, DisplacedObservation: displacedObservation}}
	completed, err := executor.Execute(context.Background(), operation)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryBlocked {
		t.Fatalf("move exchange-back recovery was not blocked: %+v err=%v", completed, err)
	}
	second, secondErr := executor.Execute(context.Background(), completed)
	var secondOperationErr *OperationError
	if !errors.As(secondErr, &secondOperationErr) || secondOperationErr.Code != CodeRecoveryBlocked {
		t.Fatalf("second move recovery did not remain blocked: %+v err=%v", second, secondErr)
	}
}
func mustOperationSum(id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return sum[:12]
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
