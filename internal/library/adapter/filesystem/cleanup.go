package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

// OSCleanupExecutor is the anchored filesystem adapter for the deletion owners
// (a replacement loser's deletion and a file-manager delete). A file is never
// unlinked by its public name: it is first captured into the owner's private
// namespace, verified there, and only that exact object is purged.
type OSCleanupExecutor struct {
	anchor string
	// Hooks are the instance-bound test and fault hooks; zero in production.
	Hooks CleanupHooks
}

// CleanupHooks lets a test answer the effect syscalls of a deletion the way a
// busy or read-only file would, with the real receipts written around them.
type CleanupHooks struct {
	// Effect, when set, runs just before the capture rename (stage
	// "capture") and the purge unlink (stage "purge"); a non-nil result is
	// that syscall's answer and the syscall does not run.
	Effect func(stage string) error
}

// effectProbe turns Hooks.Effect into the probe of the capture or purge
// point; nil when unset.
func (executor OSCleanupExecutor) effectProbe() []cleanupMutationProbe {
	effect := executor.Hooks.Effect
	if effect == nil {
		return nil
	}
	return []cleanupMutationProbe{func(point string) error {
		switch point {
		case "before_object_capture":
			return effect("capture")
		case "before_object_purge":
			return effect("purge")
		}
		return nil
	}}
}

// NewOSCleanupExecutor binds cleanup to the anchor its composition root
// injected: the directory a cleanup effect may touch, which the file manager's
// own user operations supply as the deployment's filesystem root. A missing or
// non-absolute anchor fails closed rather than widening the effect.
func NewOSCleanupExecutor(anchor string) OSCleanupExecutor {
	return OSCleanupExecutor{anchor: anchor}
}

// Observe returns a regular, non-symlink observation under the configured root.
// Parent traversal is verified with the same no-follow descriptor chain used by
// the placement filesystem adapter.
func (executor OSCleanupExecutor) Observe(ctx context.Context, path string) (domain.FileObservation, error) {
	if err := ctx.Err(); err != nil {
		return domain.FileObservation{}, err
	}
	root, canonical, err := executor.rootAndPath(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	parent, err := verifiedParentIdentityWithinRoot(root, canonical, false)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("verify cleanup parent: %w", err)
	}
	entry, err := openMutationEntry(root, canonical, parent)
	if err != nil {
		return domain.FileObservation{}, busyObservationError(fmt.Errorf("verify cleanup entry: %w", err))
	}
	defer entry.parent.Close()
	observed, err := observeFile(canonical)
	return observed, busyObservationError(err)
}

// ObserveMetadataFile is the no-content-read counterpart to Observe for a
// cleanup whose frozen placement identity is metadata. It keeps the same
// anchored, no-follow parent and final-entry checks as the hashing observer.
func (executor OSCleanupExecutor) ObserveMetadataFile(ctx context.Context, path string) (domain.FileObservation, error) {
	if err := ctx.Err(); err != nil {
		return domain.FileObservation{}, err
	}
	root, canonical, err := executor.rootAndPath(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	parent, err := verifiedParentIdentityWithinRoot(root, canonical, false)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("verify cleanup parent: %w", err)
	}
	entry, err := openMutationEntry(root, canonical, parent)
	if err != nil {
		return domain.FileObservation{}, busyObservationError(fmt.Errorf("verify cleanup entry: %w", err))
	}
	defer entry.parent.Close()
	observed, err := (OSFileObserver{}).ObserveMetadataFile(ctx, canonical)
	return observed, busyObservationError(err)
}

// CaptureForDelete validates the frozen evidence and the no-follow rooted
// paths, then captures the source into the owner's private namespace and
// verifies the captured object against the frozen observation.
func (executor OSCleanupExecutor) CaptureForDelete(ctx context.Context, source, quarantine string, expected domain.FileObservation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := requireCleanupObservation(expected); err != nil {
		return "", err
	}
	root, sourcePath, err := executor.rootAndPath(source)
	if err != nil {
		return "", err
	}
	_, quarantinePath, err := executor.rootAndPath(quarantine)
	if err != nil {
		return "", err
	}
	if filepath.Clean(sourcePath) == filepath.Clean(quarantinePath) {
		return "", errors.New("cleanup source and capture paths must differ")
	}
	return captureCleanupObject(root, sourcePath, quarantinePath, expected, nil, executor.effectProbe()...)
}

// PurgeCaptured verifies the exact captured object but never unlinks by its
// public basename. A missing parent or entry is recovery-unsafe, never a
// successful destructive effect.
func (executor OSCleanupExecutor) PurgeCaptured(ctx context.Context, quarantine string, expected domain.FileObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireCleanupObservation(expected); err != nil {
		return err
	}
	root, quarantinePath, err := executor.rootAndPath(quarantine)
	if err != nil {
		return err
	}
	parent, err := verifiedParentIdentityWithinRoot(root, quarantinePath, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cleanup quarantine parent or entry is missing; purge effect is unproven")}
		}
		return fmt.Errorf("verify cleanup quarantine parent: %w", err)
	}
	entry, err := openMutationEntry(root, quarantinePath, parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cleanup quarantine entry is missing; purge effect is unproven")}
		}
		return fmt.Errorf("verify cleanup quarantine entry: %w", err)
	}
	defer entry.parent.Close()
	observed, err := observeFileAs(quarantinePath, expected.Fingerprint)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cleanup quarantine entry is missing; purge effect is unproven")}
		}
		return fmt.Errorf("observe cleanup quarantine: %w", err)
	}
	if !sameMutationFileObservation(observed, expected) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cleanup quarantine identity or fingerprint changed")}
	}
	return purgeCleanupObject(root, quarantinePath, expected, executor.effectProbe()...)
}

// RestoreCaptured moves the exact captured object of a deletion back to the
// public path it was captured from, never replacing anything there. A
// deletion that gives up after capturing uses it so the file is left where
// it was rather than hidden in the private namespace.
func (executor OSCleanupExecutor) RestoreCaptured(ctx context.Context, capture, source string, expected domain.FileObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireCleanupObservation(expected); err != nil {
		return err
	}
	root, capturePath, err := executor.rootAndPath(capture)
	if err != nil {
		return err
	}
	_, sourcePath, err := executor.rootAndPath(source)
	if err != nil {
		return err
	}
	if filepath.Clean(sourcePath) == filepath.Clean(capturePath) {
		return errors.New("cleanup source and capture paths must differ")
	}
	return restoreCleanupObject(root, capturePath, sourcePath, expected)
}

func (executor OSCleanupExecutor) rootAndPath(path string) (string, string, error) {
	anchor := strings.TrimSpace(executor.anchor)
	root, err := canonicalRoot(anchor)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", "", errors.New("cleanup path must be absolute")
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", "", errors.New("cleanup path must be canonical")
	}
	// canonicalRoot resolves platform aliases such as /var -> /private/var.
	// Rebase an entry supplied through the anchor's original spelling before the
	// lexical containment check; otherwise a safe fixture/deployment path can
	// be rejected as escaping its own anchor on macOS.
	rebased, err := rebaseTarget(filepath.Clean(anchor), root, clean)
	if err != nil {
		return "", "", err
	}
	if err := targetUnderRoot(root, rebased); err != nil {
		return "", "", err
	}
	return root, rebased, nil
}

func requireCleanupObservation(observation domain.FileObservation) error {
	if observation.Fingerprint == "" || observation.SizeBytes < 0 || !fileidentity.IsPresent(observation.Identity) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cleanup requires frozen fingerprint, size, and file identity")}
	}
	return nil
}
