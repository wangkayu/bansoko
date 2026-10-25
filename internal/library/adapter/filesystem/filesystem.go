package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

const defaultInspectorVersion = "local-media-facts-v1"
const defaultScannerVersion = "filesystem-scan-v1"

var errAtomicReplaceUnsupported = errors.New("atomic replace is unsupported")

type ErrorCode string

const CodeTargetOccupied ErrorCode = domain.FileErrorCodeTargetOccupied
const (
	CodeAtomicReplaceUnsupported           ErrorCode = "OVERWRITE_ATOMIC_REPLACE_UNSUPPORTED"
	CodeTargetCollisionRace                ErrorCode = "TARGET_COLLISION_RACE"
	CodeRecoveryUnsafe                     ErrorCode = "OVERWRITE_RECOVERY_UNSAFE"
	CodeRecoveryBlocked                    ErrorCode = "OVERWRITE_RECOVERY_BLOCKED"
	CodeCleanupIdentityMutationUnsupported ErrorCode = "CLEANUP_IDENTITY_MUTATION_UNSUPPORTED"
	CodePathUnsafe                         ErrorCode = "FILE_PATH_UNSAFE"
	CodeSourceChanged                      ErrorCode = "SOURCE_CHANGED"
	CodeSourceCleanupFailed                ErrorCode = "SOURCE_CLEANUP_FAILED"
	CodeTargetVerification                 ErrorCode = "TARGET_VERIFICATION_FAILED"
)

type OperationError struct {
	Code ErrorCode
	Err  error
}

func (e *OperationError) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Err.Error()
}

func (e *OperationError) Unwrap() error { return e.Err }

// ErrorCode exposes the adapter's fail-closed classification to the application
// layer without coupling it to this filesystem package.
func (e *OperationError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return string(e.Code)
}

func (e *OperationError) RecoveryUncertain() bool {
	return e != nil && e.Code == CodeRecoveryUnsafe
}

func (e *OperationError) RecoveryBlocked() bool {
	return e != nil && e.Code == CodeRecoveryBlocked
}

func (e *OperationError) TargetCollisionRace() bool {
	return e != nil && e.Code == CodeTargetCollisionRace
}

// Temporary marks recovery-unsafe evidence as retryable at the application
// boundary. A retry lets the checkpointed owner reconcile the filesystem
// instead of terminalizing an effect whose durable state is incomplete.
func (e *OperationError) Temporary() bool {
	return e != nil && e.Code == CodeRecoveryUnsafe
}

// ExecutorHooks are deterministic, instance-bound test/fault hooks. Production
// zero-value executors have no hooks and no package-global mutable behavior.
type ExecutorHooks struct {
	BeforeExchange          func(string, string) error
	AfterExchange           func(string, string) error
	BeforeFinalRead         func(string) error
	BeforeStagingRemove     func(string) error
	AfterStagingRemove      func(string) error
	BeforeExactCopy         func(string, string) error
	BeforeTemporaryRemove   func(string) error
	BeforeAbsentMutation    func(string, string) error
	BeforePublish           func(string, string) error
	BeforeHardlinkMutation  func(string, string) error
	AfterAbsentMutation     func(string, string) error
	BeforeSourceRestore     func(string, string) error
	AtomicExchangeCheck     func() error
	AtomicExchange          func(string, string) error
	AtomicRenameNoReplace   func(string, string) error
	PublicationCleanupPoint func(string) error
}

// OSFileExecutor is the filesystem adapter for the library application port.
type OSFileExecutor struct {
	Checkpoint domain.FileOperationCheckpoint
	Hooks      ExecutorHooks
	// publicationSys replaces publication syscalls in unit tests (errno
	// seam). Nil uses the real descriptor-relative syscalls.
	publicationSys *publicationSyscalls
}

// ParentIdentities freezes the verified parent directories of one overwrite's
// source and target. Every operation path is absolute, so both chains are
// walked with no-follow opens from the anchor the executor derived them under.
func (executor OSFileExecutor) ParentIdentities(root, source, target string) (domain.FileIdentity, domain.FileIdentity, error) {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return domain.FileIdentity{}, domain.FileIdentity{}, err
	}
	sourceParent, err := verifiedParentIdentityWithinRoot(canonical, source, false)
	if err != nil {
		return domain.FileIdentity{}, domain.FileIdentity{}, err
	}
	targetParent, err := verifiedParentIdentityWithinRoot(canonical, target, false)
	if err != nil {
		return domain.FileIdentity{}, domain.FileIdentity{}, err
	}
	return sourceParent, targetParent, nil
}

// ExecuteWithCheckpoint binds one durable callback to this execution. This
// keeps the filesystem adapter independent from the application package while
// satisfying its optional checkpoint executor port.
func (executor OSFileExecutor) ExecuteWithCheckpoint(ctx context.Context, operation domain.FileOperation, callback domain.FileOperationCheckpoint) (domain.FileOperation, error) {
	executor.Checkpoint = callback
	return executor.Execute(ctx, operation)
}

// BindCheckpoint remains useful for adapter-focused fault tests.
func (executor OSFileExecutor) BindCheckpoint(callback domain.FileOperationCheckpoint) OSFileExecutor {
	executor.Checkpoint = callback
	return executor
}

type targetLockEntry struct {
	mutex sync.Mutex
	refs  int
}

var processTargetLocks = struct {
	sync.Mutex
	entries map[string]*targetLockEntry
}{entries: make(map[string]*targetLockEntry)}

func lockCanonicalTarget(path string) func() {
	processTargetLocks.Lock()
	entry := processTargetLocks.entries[path]
	if entry == nil {
		entry = &targetLockEntry{}
		processTargetLocks.entries[path] = entry
	}
	entry.refs++
	processTargetLocks.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		processTargetLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(processTargetLocks.entries, path)
		}
		processTargetLocks.Unlock()
	}
}

func (executor OSFileExecutor) Execute(ctx context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	if err := ctx.Err(); err != nil {
		return operation, err
	}
	if operation.SourcePath == "" || operation.TargetPath == "" || operation.SourceFingerprint == "" {
		return operation, errors.New("file operation paths and source fingerprint are required")
	}
	// The target is the operation's own absolute path. There is no configured
	// root to resolve it against and none to authorize it under: the
	// deployment owns the read/write boundary, and this executor only asserts
	// path hygiene and then walks the directory chain with no-follow opens, so
	// no intermediate symlink is ever followed. The chain anchor is therefore
	// the filesystem root, which is also what the frozen overwrite checkpoint
	// records, so its anchor still has to match.
	root := string(filepath.Separator)
	operation.TargetPath = filepath.Clean(operation.TargetPath)
	if !filepath.IsAbs(operation.TargetPath) {
		return operation, pathError(errors.New("target path must be absolute"))
	}
	if err := placementTargetHygiene(operation.TargetPath); err != nil {
		return operation, pathError(err)
	}
	var err error
	unlock := lockCanonicalTarget(operation.TargetPath)
	defer unlock()
	if operation.CollisionPolicy == domain.CollisionOverwrite && executor.Checkpoint == nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite requires a durable checkpoint callback")}
	}
	if operation.CollisionPolicy == domain.CollisionOverwrite && operation.OverwriteRecovery != nil {
		if recovered, done, recoveryErr := executor.recoverOverwrite(ctx, operation, root); done || recoveryErr != nil {
			return recovered, recoveryErr
		}
	}
	operation.SourcePath, err = canonicalSource(operation.SourcePath)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && operation.Operation == domain.OperationMove) {
		return operation, pathError(err)
	}

	source, sourceErr := observeFileAs(operation.SourcePath, operation.SourceFingerprint)
	if errors.Is(sourceErr, os.ErrNotExist) && operation.Operation == domain.OperationMove {
		target, targetErr := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
		if targetErr == nil && fileObservationMatches(target, operation.SourceFingerprint, expectedSourceSize(operation)) {
			return completeOperation(operation, target, true), nil
		}
		if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify replay target: %w", targetErr)}
		}
	}
	if sourceErr != nil {
		return operation, fmt.Errorf("verify source before placement: %w", sourceErr)
	}
	if !fileObservationMatches(source, operation.SourceFingerprint, expectedSourceSize(operation)) {
		return operation, &OperationError{Code: CodeSourceChanged, Err: errors.New("source fingerprint or size changed before placement")}
	}
	legacyHashOnlyMove := operation.Operation == domain.OperationMove && !sourceSizeKnown(operation)
	operation.SourceSizeBytes = source.SizeBytes
	operation.SourceSizeKnown = true
	if operation.Operation == domain.OperationMove && filepath.Clean(operation.SourcePath) == filepath.Clean(operation.TargetPath) {
		return completeOperation(operation, source, true), nil
	}
	sourceForMutation := source
	if legacyHashOnlyMove && !fileidentity.IsPresent(source.Identity) {
		// Legacy persisted operations may only have hash/size evidence when the
		// platform cannot provide a usable live identity. Keep the compatibility
		// fallback strictly limited to that case.
		sourceForMutation.Identity = domain.FileIdentity{}
	}

	target, noop, targetObservation, err := chooseTarget(operation)
	if err != nil {
		return operation, err
	}
	operation.TargetPath = target
	if err := reauthorize(root, &operation); err != nil {
		return operation, err
	}
	if noop {
		if operation.Operation == domain.OperationMove || operation.Operation == domain.OperationHardlink {
			currentTarget, err := observeFileAs(target, operation.SourceFingerprint)
			matches := err == nil && sameMutationFileObservation(currentTarget, sourceForMutation)
			if fileidentity.IsPresent(sourceForMutation.Identity) {
				matches = err == nil && sameFrozenFileObservation(currentTarget, sourceForMutation)
			}
			if !matches {
				return operation, &OperationError{Code: CodeTargetVerification, Err: errors.New("existing placement target changed or has a different identity")}
			}
			if operation.Operation == domain.OperationHardlink {
				if !sameFrozenFileObservation(currentTarget, sourceForMutation) {
					return operation, &OperationError{Code: CodeTargetVerification, Err: errors.New("existing hardlink target does not match the frozen source inode")}
				}
				return completeOperation(operation, currentTarget, true), nil
			}
			if err := authorizeExistingSourceMutation(operation.SourcePath); err != nil {
				return operation, pathError(err)
			}
			if err := removeVerifiedSource("", "", operation.SourcePath, sourceForMutation, operation.SourceParentIdentity); err != nil {
				return operation, &OperationError{Code: CodeSourceCleanupFailed, Err: err}
			}
			return completeOperation(operation, currentTarget, false), nil
		}
		return completeOperation(operation, targetObservation, true), nil
	}
	if operation.CollisionPolicy == domain.CollisionOverwrite && targetObservation.Fingerprint != "" {
		return executor.executeOverwrite(ctx, operation, root, source, targetObservation)
	}
	if err := executor.executeAbsent(ctx, operation, root, source); err != nil {
		return operation, err
	}
	final, err := observeFileAs(target, operation.SourceFingerprint)
	matchesFinal := err == nil && sameFileObservation(final, source)
	if operation.Operation == domain.OperationHardlink {
		matchesFinal = err == nil && sameFrozenFileObservation(final, source)
	}
	if !matchesFinal {
		if operation.Operation == domain.OperationHardlink && err == nil {
			if cleanupErr := removeIdentityBound(root, target, operation.TargetParentIdentity, final.Identity); cleanupErr != nil {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("remove mismatched hardlink target: %w", cleanupErr)}
			}
			if _, cleanupErr := os.Lstat(target); cleanupErr == nil {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("mismatched hardlink target remained after identity-bound cleanup")}
			} else if !errors.Is(cleanupErr, os.ErrNotExist) {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify mismatched hardlink target cleanup: %w", cleanupErr)}
			}
		} else if operation.Operation == domain.OperationHardlink && !errors.Is(err, os.ErrNotExist) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe mismatched hardlink target: %w", err)}
		}
		return operation, &OperationError{Code: CodeTargetVerification, Err: errors.New("target fingerprint, size, or identity differs after file operation")}
	}
	return completeOperation(operation, final, false), nil
}

func expectedSourceSize(operation domain.FileOperation) int64 {
	if sourceSizeKnown(operation) {
		return operation.SourceSizeBytes
	}
	return -1
}

func completeOperation(operation domain.FileOperation, observation domain.FileObservation, skipped bool) domain.FileOperation {
	operation.State = domain.OperationSucceeded
	if skipped {
		operation.Operation = domain.OperationNoop
		operation.State = domain.OperationSkipped
	}
	operation.TargetFingerprint = observation.Fingerprint
	operation.TargetSizeBytes = observation.SizeBytes
	if !operation.SourceSizeKnown {
		operation.SourceSizeBytes = observation.SizeBytes
		operation.SourceSizeKnown = true
	}
	return operation
}

func recoveryPlacementObservation(operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileObservation, bool) {
	if operation.Operation == domain.OperationCopy {
		if !fileidentity.IsPresent(recovery.StagedObservation.Identity) {
			return domain.FileObservation{}, false
		}
		return recovery.StagedObservation, true
	}
	if !fileidentity.IsPresent(recovery.SourceObservation.Identity) {
		return domain.FileObservation{}, false
	}
	return recovery.SourceObservation, true
}

func (executor OSFileExecutor) verifyDisplacedStaging(recovery domain.OverwriteRecovery) (bool, error) {
	info, err := os.Lstat(recovery.StagingPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if recovery.DisplacedNonRegular {
		return true, func() error {
			if !sameDisplacedIdentity(recovery.StagingPath, info, recovery) {
				return errors.New("staging entry does not match frozen non-regular displaced identity")
			}
			return nil
		}()
	}
	observation, err := observeFileAs(recovery.StagingPath, recovery.DisplacedObservation.Fingerprint)
	if err != nil {
		return false, err
	}
	if !sameFrozenFileObservation(observation, recovery.DisplacedObservation) {
		return false, errors.New("staging entry does not match frozen displaced observation")
	}
	return true, nil
}

func verifyStagedObservation(path string, expected domain.FileObservation) error {
	staged, err := observeFileAs(path, expected.Fingerprint)
	if err != nil {
		return err
	}
	if !sameFrozenFileObservation(staged, expected) {
		return errors.New("staging identity changed before overwrite exchange")
	}
	return nil
}

func (executor OSFileExecutor) removeVerifiedDisplacedStaging(root string, operation domain.FileOperation, recovery domain.OverwriteRecovery) error {
	present, err := executor.verifyDisplacedStaging(recovery)
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify displaced staging before removal: %w", err)}
	}
	if present {
		if recovery.DisplacedNonRegular {
			err = removeVerifiedNonRegular(root, recovery.StagingPath, recovery.DisplacedType, recovery.DisplacedIdentity, executor.Hooks.BeforeStagingRemove, operation.TargetParentIdentity)
		} else {
			err = removeVerifiedEntry(root, recovery.StagingPath, recovery.DisplacedObservation, executor.Hooks.BeforeStagingRemove, operation.TargetParentIdentity)
		}
		if err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("remove verified displaced staging: %w", err)}
		}
	}
	if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staging entry reappeared after removal")}
		}
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify staging removal: %w", err)}
	}
	if executor.Hooks.AfterStagingRemove != nil {
		if err := executor.Hooks.AfterStagingRemove(recovery.StagingPath); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	}
	if err := syncMutationPath(root, recovery.StagingPath, operation.TargetParentIdentity); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("sync staging directory after removal: %w", err)}
	}
	return nil
}

func (executor OSFileExecutor) removeVerifiedStaged(root string, operation domain.FileOperation, recovery domain.OverwriteRecovery, expectedParent domain.FileIdentity) error {
	if !fileidentity.IsPresent(expectedParent) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staging cleanup parent identity was not authorized")}
	}
	if err := verifyStagedObservation(recovery.StagingPath, recovery.StagedObservation); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify staged object before removal: %w", err)}
	}
	if err := removeVerifiedEntry(root, recovery.StagingPath, recovery.StagedObservation, executor.Hooks.BeforeStagingRemove, expectedParent); err != nil {
		return err
	}
	if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staging entry reappeared after removal")}
		}
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify staging removal: %w", err)}
	}
	if executor.Hooks.AfterStagingRemove != nil {
		if err := executor.Hooks.AfterStagingRemove(recovery.StagingPath); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	}
	if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staging entry reappeared after removal")}
		}
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify staging removal after hook: %w", err)}
	}
	if err := syncMutationPath(root, recovery.StagingPath, expectedParent); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("sync staging directory after removal: %w", err)}
	}
	return nil
}

func quarantinePath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(filepath.Dir(path), ".bansoko-quarantine-"+hex.EncodeToString(sum[:12]))
}

func quarantineVerifiedEntry(sourceRoot, targetRoot string, path string, expected domain.FileObservation, beforeRename func(string) error, expectedParent domain.FileIdentity) error {
	if !fileidentity.IsPresent(expectedParent) {
		return errors.New("cleanup parent identity was not authorized")
	}
	quarantine := quarantinePath(path)
	if info, err := os.Lstat(quarantine); err == nil {
		if _, pathErr := os.Lstat(path); pathErr == nil {
			return errors.New("operation-private cleanup quarantine and source both exist")
		} else if !errors.Is(pathErr, os.ErrNotExist) {
			return fmt.Errorf("inspect cleanup source with existing quarantine: %w", pathErr)
		}
		actual, observeErr := observeFileAs(quarantine, expected.Fingerprint)
		if observeErr != nil {
			return fmt.Errorf("verify existing cleanup quarantine: %w", observeErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !sameMutationFileObservation(actual, expected) {
			return errors.New("existing cleanup quarantine does not match frozen identity or handle evidence")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect cleanup quarantine: %w", err)
	}
	if beforeRename != nil {
		if err := beforeRename(path); err != nil {
			return err
		}
	}
	current, err := observeFileAs(path, expected.Fingerprint)
	if err != nil || !sameMutationFileObservation(current, expected) {
		return errors.New("cleanup source identity or handle evidence changed before quarantine")
	}
	if err := atomicQuarantine(sourceRoot, targetRoot, path, quarantine, expectedParent); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("cleanup source remained after quarantine")
		}
		return fmt.Errorf("verify cleanup source quarantine: %w", err)
	}
	if err := syncMutationPath(targetRoot, path, expectedParent); err != nil {
		return fmt.Errorf("sync cleanup directory after quarantine: %w", err)
	}
	actual, err := observeFileAs(quarantine, expected.Fingerprint)
	if err != nil {
		info, infoErr := os.Lstat(quarantine)
		if infoErr != nil {
			return errors.Join(err, infoErr)
		}
		return fmt.Errorf("verify quarantined cleanup entry: %w (type=%s identity=%+v)", err, displacedEntryType(info), fileIdentity(info))
	}
	if !sameMutationFileObservation(actual, expected) {
		return errors.New("quarantined cleanup entry does not match frozen identity")
	}
	// Rename is the logical deletion boundary. The entry is re-observed after
	// the rename and retained in the operation-private quarantine; this is not
	// an identity-bound rename syscall, so a post-rename mismatch fails closed.
	return nil
}

func removeVerifiedEntry(targetRoot string, path string, expected domain.FileObservation, beforeRename func(string) error, expectedParent domain.FileIdentity) error {
	actual, err := observeFileAs(path, expected.Fingerprint)
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe entry before verified removal: %w", err)}
	}
	if !sameFrozenFileObservation(actual, expected) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staging entry identity changed before verified removal")}
	}
	if !fileidentity.IsPresent(expectedParent) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("verified removal parent identity was not authorized")}
	}
	if err := quarantineVerifiedEntry(targetRoot, targetRoot, path, actual, beforeRename, expectedParent); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("quarantine verified staging entry: %w", err)}
	}
	return nil
}

func removeVerifiedNonRegular(targetRoot, path, expectedType string, expectedIdentity domain.FileIdentity, beforeRename func(string) error, expectedParent domain.FileIdentity) error {
	if !fileidentity.IsPresent(expectedParent) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("non-regular cleanup parent identity was not authorized")}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe non-regular entry before removal: %w", err)}
	}
	identity, identityErr := fileidentity.FromPath(path)
	if identityErr != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe non-regular entry identity before removal: %w", identityErr)}
	}
	if displacedEntryType(info) != expectedType || !fileidentity.Matches(identity, expectedIdentity) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("non-regular staging entry changed before verified removal")}
	}
	if beforeRename != nil {
		if err := beforeRename(path); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	}
	current, err := os.Lstat(path)
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe non-regular entry before quarantine: %w", err)}
	}
	currentIdentity, identityErr := fileidentity.FromPath(path)
	if identityErr != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe non-regular entry identity before quarantine: %w", identityErr)}
	}
	if displacedEntryType(current) != expectedType || !fileidentity.Matches(currentIdentity, expectedIdentity) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("non-regular staging entry changed before quarantine")}
	}
	quarantine := quarantinePath(path)
	if err := atomicQuarantine(targetRoot, targetRoot, path, quarantine, expectedParent); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("quarantine verified non-regular entry: %w", err)}
	}
	quarantined, err := os.Lstat(quarantine)
	quarantineIdentity, identityErr := fileidentity.FromPath(quarantine)
	if err != nil || identityErr != nil || displacedEntryType(quarantined) != expectedType || !fileidentity.Matches(quarantineIdentity, expectedIdentity) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("quarantined non-regular entry does not match frozen identity")}
	}
	if err := syncMutationPath(targetRoot, path, expectedParent); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("sync non-regular quarantine directory: %w", err)}
	}
	return nil
}

type recoveryEntryObservation struct {
	observation domain.FileObservation
	entryType   string
	regular     bool
}

// observeRecoveryEntry observes a recovery path in the fingerprint mode of
// reference (see observeFileAs).
func observeRecoveryEntry(path, reference string) (recoveryEntryObservation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return recoveryEntryObservation{}, err
	}
	if info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		observation, observeErr := observeFileAs(path, reference)
		if observeErr != nil {
			return recoveryEntryObservation{}, observeErr
		}
		return recoveryEntryObservation{observation: observation, entryType: "regular", regular: true}, nil
	}
	identity, identityErr := fileidentity.FromPath(path)
	if identityErr != nil {
		return recoveryEntryObservation{}, fmt.Errorf("non-regular recovery evidence identity: %w", identityErr)
	}
	if !fileidentity.IsPresent(identity) {
		return recoveryEntryObservation{}, errors.New("non-regular recovery evidence identity is unavailable")
	}
	return recoveryEntryObservation{
		observation: domain.FileObservation{SizeBytes: -1, Identity: identity},
		entryType:   displacedEntryType(info),
	}, nil
}

func recoveryEntryMatchesDisplaced(entry recoveryEntryObservation, recovery domain.OverwriteRecovery) bool {
	if recovery.DisplacedNonRegular {
		return !entry.regular && entry.entryType == recovery.DisplacedType && fileidentity.Matches(entry.observation.Identity, recovery.DisplacedIdentity)
	}
	return entry.regular && sameFileIdentity(entry.observation, recovery.DisplacedObservation)
}

func recoveryEntryMatchesPlacement(entry recoveryEntryObservation, expected domain.FileObservation) bool {
	return entry.regular && sameFrozenFileObservation(entry.observation, expected)
}

func setForeignEvidence(recovery *domain.OverwriteRecovery, path string, entry recoveryEntryObservation) {
	recovery.ForeignEvidencePath = path
	if entry.regular {
		recovery.ForeignNonRegularEvidence = false
		recovery.ForeignEvidenceType = ""
		recovery.ForeignEvidenceIdentity = domain.FileIdentity{}
		recovery.ForeignEvidence = entry.observation
		return
	}
	recovery.ForeignEvidence = domain.FileObservation{}
	recovery.ForeignNonRegularEvidence = true
	recovery.ForeignEvidenceType = entry.entryType
	recovery.ForeignEvidenceIdentity = entry.observation.Identity
}

func foreignEvidenceMatchesEntry(recovery domain.OverwriteRecovery, entry recoveryEntryObservation) bool {
	if recovery.ForeignNonRegularEvidence {
		return !entry.regular && entry.entryType == recovery.ForeignEvidenceType && fileidentity.Matches(entry.observation.Identity, recovery.ForeignEvidenceIdentity)
	}
	return entry.regular && sameFileIdentity(entry.observation, recovery.ForeignEvidence)
}

func (executor OSFileExecutor) quarantineForeignEntry(root string, operation domain.FileOperation, path string, entry recoveryEntryObservation) error {
	if entry.regular {
		return quarantineVerifiedEntry(root, root, path, entry.observation, nil, operation.TargetParentIdentity)
	}
	return removeVerifiedNonRegular(root, path, entry.entryType, entry.observation.Identity, nil, operation.TargetParentIdentity)
}

func (executor OSFileExecutor) verifyForeignEvidence(recovery domain.OverwriteRecovery) error {
	if recovery.ForeignEvidencePath == "" {
		return errors.New("foreign evidence path is missing")
	}
	entry, err := observeRecoveryEntry(recovery.ForeignEvidencePath, recovery.SourceObservation.Fingerprint)
	if err != nil {
		return err
	}
	if !foreignEvidenceMatchesEntry(recovery, entry) {
		return errors.New("foreign evidence no longer matches frozen identity")
	}
	return nil
}

func (executor OSFileExecutor) checkpointForeignAtStaging(ctx context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery, entry recoveryEntryObservation) (domain.FileOperation, error) {
	setForeignEvidence(&recovery, recovery.StagingPath, entry)
	recovery.Stage = domain.OverwriteStageExchanged
	return executor.checkpoint(ctx, operation, recovery)
}

func (executor OSFileExecutor) finalizeRecoveredForeign(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery, foreign recoveryEntryObservation) (domain.FileOperation, error) {
	quarantine := quarantinePath(recovery.StagingPath)
	if _, stagingErr := os.Lstat(recovery.StagingPath); errors.Is(stagingErr, os.ErrNotExist) {
		quarantined, quarantineErr := observeRecoveryEntry(quarantine, operation.SourceFingerprint)
		if quarantineErr != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("inspect recovered foreign quarantine: %w", quarantineErr)}
		}
		if !foreignEntryMatches(quarantined, foreign) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("existing quarantined foreign entry does not match frozen identity")}
		}
		setForeignEvidence(&recovery, quarantine, quarantined)
	} else if stagingErr != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("inspect recovered foreign staging: %w", stagingErr)}
	} else {
		if err := executor.quarantineForeignEntry(root, operation, recovery.StagingPath, foreign); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("quarantine recovered foreign entry: %w", err)}
		}
		quarantined, err := observeRecoveryEntry(quarantine, operation.SourceFingerprint)
		if err != nil || !foreignEntryMatches(quarantined, foreign) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("quarantined foreign entry does not match frozen identity")}
		}
		setForeignEvidence(&recovery, quarantine, quarantined)
	}
	if err := executor.verifyForeignEvidence(recovery); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify recovered foreign quarantine: %w", err)}
	}
	if operation.Operation == domain.OperationMove {
		recovery.Stage = domain.OverwriteStageBlocked
		recovery.BlockedReason = "final target collision requires operator review"
	} else {
		recovery.Stage = domain.OverwriteStageRestored
	}
	updated, err := executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return updated, err
	}
	updated.OverwriteRecovery = &recovery
	if operation.Operation == domain.OperationMove {
		return updated, &OperationError{Code: CodeRecoveryBlocked, Err: errors.New(recovery.BlockedReason)}
	}
	return updated, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("overwrite was restored after a target race; foreign evidence is quarantined")}
}
func (executor OSFileExecutor) recoverExchangedForeignLayouts(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery, target recoveryEntryObservation, targetPresent bool, staged recoveryEntryObservation, stagedPresent bool, placementExpected domain.FileObservation) (domain.FileOperation, bool, error) {
	if recovery.ForeignEvidencePath != "" {
		if recovery.ForeignEvidencePath == quarantinePath(recovery.StagingPath) {
			if err := executor.verifyForeignEvidence(recovery); err != nil {
				return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify quarantined foreign evidence: %w", err)}
			}
			updated, err := executor.completeForeignRecovery(ctx, operation, recovery)
			return updated, true, err
		}
		if recovery.ForeignEvidencePath != recovery.StagingPath {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("exchanged foreign evidence path is invalid")}
		}
		if stagedPresent && foreignEvidenceMatchesEntry(recovery, staged) {
			updated, err := executor.finalizeRecoveredForeign(ctx, operation, root, recovery, staged)
			return updated, true, err
		}
		quarantine := quarantinePath(recovery.StagingPath)
		if !stagedPresent {
			quarantined, quarantineErr := observeRecoveryEntry(quarantine, operation.SourceFingerprint)
			if quarantineErr == nil && foreignEvidenceMatchesEntry(recovery, quarantined) {
				recovery.ForeignEvidencePath = quarantine
				updated, err := executor.completeForeignRecovery(ctx, operation, recovery)
				return updated, true, err
			}
			if quarantineErr != nil && !errors.Is(quarantineErr, os.ErrNotExist) {
				return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("inspect recovered foreign quarantine: %w", quarantineErr)}
			}
		}
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("exchanged foreign staging evidence is inconsistent")}
	}

	targetDisplaced := targetPresent && recoveryEntryMatchesDisplaced(target, recovery)
	stagedDisplaced := stagedPresent && recoveryEntryMatchesDisplaced(staged, recovery)
	targetPlacement := targetPresent && recoveryEntryMatchesPlacement(target, placementExpected)
	if targetDisplaced && !stagedPresent {
		quarantine := quarantinePath(recovery.StagingPath)
		if foreign, err := observeRecoveryEntry(quarantine, operation.SourceFingerprint); err == nil {
			setForeignEvidence(&recovery, quarantine, foreign)
			completed, completeErr := executor.completeForeignRecovery(ctx, operation, recovery)
			return completed, true, completeErr
		} else if !errors.Is(err, os.ErrNotExist) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("inspect operation-private foreign quarantine: %w", err)}
		}
		return operation, false, nil
	}
	if targetPlacement && stagedPresent && !stagedDisplaced {
		operation, checkpointErr := executor.checkpointForeignAtStaging(ctx, operation, recovery, staged)
		if checkpointErr != nil {
			return operation, true, checkpointErr
		}
		recovery = *operation.OverwriteRecovery
		updated, err := executor.finalizeRecoveredForeign(ctx, operation, root, recovery, staged)
		return updated, true, err
	}
	if !targetDisplaced && !targetPlacement && stagedDisplaced {
		if err := reauthorize(root, &operation); err != nil {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
		if exchangeErr := atomicReplaceError(executor.exchange(root, recovery.StagingPath, operation.TargetPath, operation.TargetParentIdentity)); exchangeErr != nil {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("exchange-back during overwrite recovery: %w", exchangeErr)}
		}
		restored, restoredErr := observeRecoveryEntry(operation.TargetPath, operation.SourceFingerprint)
		foreign, foreignErr := observeRecoveryEntry(recovery.StagingPath, operation.SourceFingerprint)
		if restoredErr != nil || foreignErr != nil || !recoveryEntryMatchesDisplaced(restored, recovery) || foreignEntryMatches(foreign, staged) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("exchange-back recovery layout could not be verified")}
		}
		operation, checkpointErr := executor.checkpointForeignAtStaging(ctx, operation, recovery, foreign)
		if checkpointErr != nil {
			return operation, true, checkpointErr
		}
		recovery = *operation.OverwriteRecovery
		updated, err := executor.finalizeRecoveredForeign(ctx, operation, root, recovery, foreign)
		return updated, true, err
	}
	return operation, false, nil
}

func (executor OSFileExecutor) completeForeignRecovery(ctx context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
	if recovery.ForeignEvidencePath == "" || recovery.ForeignEvidencePath == recovery.StagingPath {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("foreign evidence is not quarantined")}
	}
	if err := executor.verifyForeignEvidence(recovery); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify foreign quarantine: %w", err)}
	}
	if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("quarantined foreign recovery still has staging evidence")}
	}
	if recovery.Stage != domain.OverwriteStageBlocked && recovery.Stage != domain.OverwriteStageRestored {
		if operation.Operation == domain.OperationMove {
			recovery.Stage = domain.OverwriteStageBlocked
			recovery.BlockedReason = "final target collision requires operator review"
		} else {
			recovery.Stage = domain.OverwriteStageRestored
		}
		updated, err := executor.checkpoint(ctx, operation, recovery)
		if err != nil {
			return updated, err
		}
		operation = updated
	}
	operation.OverwriteRecovery = &recovery
	if recovery.Stage == domain.OverwriteStageBlocked {
		return operation, &OperationError{Code: CodeRecoveryBlocked, Err: errors.New(recovery.BlockedReason)}
	}
	return operation, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("overwrite was restored after a target race; foreign evidence is quarantined")}
}

func (executor OSFileExecutor) restoreFinalMismatch(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery, raced recoveryEntryObservation) (domain.FileOperation, error) {
	if !fileidentity.IsPresent(raced.observation.Identity) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("final mismatch has no frozen raced target identity")}
	}
	staged, stagedErr := observeRecoveryEntry(recovery.StagingPath, operation.SourceFingerprint)
	if stagedErr != nil || !recoveryEntryMatchesDisplaced(staged, recovery) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("displaced staging is absent or changed before final mismatch restore")}
	}
	current, err := observeRecoveryEntry(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !foreignEntryMatches(current, raced) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("final raced target changed before restore")}
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("restore displaced target after final mismatch: %w", err)}
	}
	if exchangeErr := atomicReplaceError(executor.exchange(root, recovery.StagingPath, operation.TargetPath, operation.TargetParentIdentity)); exchangeErr != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("restore displaced target after final mismatch: %w", exchangeErr)}
	}
	restored, err := observeRecoveryEntry(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !recoveryEntryMatchesDisplaced(restored, recovery) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("restored displaced target does not match frozen identity")}
	}
	foreignAtStaging, err := observeRecoveryEntry(recovery.StagingPath, operation.SourceFingerprint)
	if err != nil || !foreignEntryMatches(foreignAtStaging, raced) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("raced final target was not preserved in staging")}
	}
	// The exchange-back is a durable recovery boundary. Persist foreign-at-staging
	// before the next mutation so a crash cannot make the layout ambiguous.
	operation, checkpointErr := executor.checkpointForeignAtStaging(ctx, operation, recovery, foreignAtStaging)
	if checkpointErr != nil {
		return operation, checkpointErr
	}
	recovery = *operation.OverwriteRecovery
	if err := executor.quarantineForeignEntry(root, operation, recovery.StagingPath, foreignAtStaging); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("quarantine raced final target: %w", err)}
	}
	quarantine := quarantinePath(recovery.StagingPath)
	foreignAtQuarantine, err := observeRecoveryEntry(quarantine, operation.SourceFingerprint)
	if err != nil || !foreignEntryMatches(foreignAtQuarantine, raced) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("quarantined raced final target does not match frozen identity")}
	}
	setForeignEvidence(&recovery, quarantine, foreignAtQuarantine)
	if operation.Operation == domain.OperationMove {
		recovery.Stage = domain.OverwriteStageBlocked
		recovery.BlockedReason = "final target collision requires operator review"
	} else {
		recovery.Stage = domain.OverwriteStageRestored
	}
	updated, checkpointErr := executor.checkpoint(ctx, operation, recovery)
	if checkpointErr != nil {
		return updated, checkpointErr
	}
	updated.OverwriteRecovery = &recovery
	if operation.Operation == domain.OperationMove {
		return updated, &OperationError{Code: CodeRecoveryBlocked, Err: errors.New(recovery.BlockedReason)}
	}
	return updated, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("final target changed after overwrite exchange; raced object quarantined for collision review")}
}

func foreignEntryMatches(entry recoveryEntryObservation, raced recoveryEntryObservation) bool {
	if entry.regular != raced.regular || entry.entryType != raced.entryType || !fileidentity.Matches(entry.observation.Identity, raced.observation.Identity) {
		return false
	}
	if !entry.regular {
		return true
	}
	return sameFrozenFileObservation(entry.observation, raced.observation)
}

func validateForeignEvidenceForRecovery(recovery domain.OverwriteRecovery) error {
	if recovery.ForeignEvidencePath == "" && recovery.ForeignEvidence == (domain.FileObservation{}) && !recovery.ForeignNonRegularEvidence {
		return nil
	}
	if recovery.ForeignEvidencePath == "" {
		return errors.New("overwrite recovery foreign evidence is incomplete")
	}
	if recovery.ForeignEvidencePath != filepath.Clean(recovery.ForeignEvidencePath) ||
		(recovery.Stage != domain.OverwriteStageExchanged && recovery.ForeignEvidencePath != quarantinePath(recovery.StagingPath)) {
		return errors.New("overwrite recovery foreign evidence path is not operation-private")
	}
	if recovery.ForeignNonRegularEvidence {
		if !fileidentity.IsPresent(recovery.ForeignEvidenceIdentity) {
			return errors.New("overwrite recovery non-regular foreign evidence is incomplete")
		}
		return nil
	}
	if !fileidentity.IsPresent(recovery.ForeignEvidence.Identity) || recovery.ForeignEvidence.SizeBytes < 0 {
		return errors.New("overwrite recovery foreign evidence identity or size is invalid")
	}
	return nil
}

func validateAbortedRecoveryEvidence(operation domain.FileOperation, recovery domain.OverwriteRecovery) error {
	if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("aborted overwrite still has staging evidence")}
		}
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify aborted overwrite staging: %w", err)}
	}
	if target, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint); err != nil || !observationMatchesExpected(target, recovery.TargetObservation) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("aborted overwrite target no longer matches frozen target")}
	}
	if operation.Operation == domain.OperationMove {
		source, err := observeFileAs(operation.SourcePath, operation.SourceFingerprint)
		if err != nil || !observationMatchesExpected(source, recovery.SourceObservation) {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("aborted move source was not restored from frozen identity")}
		}
	}
	return nil
}

func (executor OSFileExecutor) recoverOverwrite(ctx context.Context, operation domain.FileOperation, root string) (domain.FileOperation, bool, error) {
	recovery := *operation.OverwriteRecovery
	if !fileidentity.IsPresent(recovery.SourceParentIdentity) || !fileidentity.IsPresent(recovery.TargetParentIdentity) ||
		!fileidentity.Matches(operation.SourceParentIdentity, recovery.SourceParentIdentity) || !fileidentity.Matches(operation.TargetParentIdentity, recovery.TargetParentIdentity) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite recovery parent identities are missing or changed")}
	}
	if recovery.CanonicalRoot != root || recovery.CanonicalSource != operation.SourcePath || recovery.CanonicalTarget != operation.TargetPath ||
		recovery.StagingPath != stagingPath(operation) || !fileObservationMatches(recovery.SourceObservation, operation.SourceFingerprint, expectedSourceSize(operation)) ||
		!fileidentity.IsPresent(recovery.SourceObservation.Identity) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite recovery identity does not match this operation")}
	}
	if err := validateForeignEvidenceForRecovery(recovery); err != nil {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	if recovery.Stage == domain.OverwriteStageAborted {
		if err := validateAbortedRecoveryEvidence(operation, recovery); err != nil {
			return operation, true, err
		}
		return operation, true, &OperationError{Code: CodeAtomicReplaceUnsupported, Err: errors.New("overwrite recovery was aborted after unsupported cleanup")}
	}

	targetEntry, targetErr := observeRecoveryEntry(operation.TargetPath, operation.SourceFingerprint)
	stagedEntry, stagedErr := observeRecoveryEntry(recovery.StagingPath, operation.SourceFingerprint)
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe overwrite target during recovery: %w", targetErr)}
	}
	if stagedErr != nil && !errors.Is(stagedErr, os.ErrNotExist) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe overwrite staging during recovery: %w", stagedErr)}
	}
	targetPresent := targetErr == nil
	stagedPresent := stagedErr == nil
	placementExpected, placementOK := recoveryPlacementObservation(operation, recovery)

	switch recovery.Stage {
	case domain.OverwriteStagePrepared:
		if !targetPresent || !targetEntry.regular || !sameFrozenFileObservation(targetEntry.observation, recovery.TargetObservation) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("prepared overwrite target does not match frozen target")}
		}
		if !stagedPresent {
			return operation, false, nil
		}
		if !stagedEntry.regular || !sameFrozenFileObservation(stagedEntry.observation, recovery.SourceObservation) ||
			(operation.Operation == domain.OperationCopy && !sameFileObservation(stagedEntry.observation, recovery.SourceObservation)) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("prepared overwrite staging does not match frozen source")}
		}
		recovery.Stage = domain.OverwriteStageStaged
		recovery.StagedObservation = stagedEntry.observation
		updated, err := executor.checkpoint(ctx, operation, recovery)
		if err != nil {
			return operation, true, err
		}
		return executor.resumeExchange(ctx, updated, root, recovery, targetEntry.observation)

	case domain.OverwriteStageStaged:
		if targetPresent && targetEntry.regular && stagedPresent && stagedEntry.regular &&
			sameFrozenFileObservation(targetEntry.observation, recovery.TargetObservation) &&
			sameFrozenFileObservation(stagedEntry.observation, recovery.StagedObservation) {
			return executor.resumeExchange(ctx, operation, root, recovery, targetEntry.observation)
		}
		if targetPresent && targetEntry.regular && stagedPresent && stagedEntry.regular && placementOK &&
			sameFrozenFileObservation(targetEntry.observation, placementExpected) &&
			sameFrozenFileObservation(stagedEntry.observation, recovery.TargetObservation) {
			recovery.Stage = domain.OverwriteStageExchanged
			recovery.DisplacedObservation = stagedEntry.observation
			updated, err := executor.checkpoint(ctx, operation, recovery)
			if err != nil {
				return operation, true, err
			}
			return executor.recoverOverwrite(ctx, updated, root)
		}

	case domain.OverwriteStageExchanged:
		if !placementOK {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite recovery placement identity is incomplete")}
		}
		updated, done, err := executor.recoverExchangedForeignLayouts(ctx, operation, root, recovery, targetEntry, targetPresent, stagedEntry, stagedPresent, placementExpected)
		if done || err != nil {
			return updated, done, err
		}
		if updated.OverwriteRecovery != nil {
			operation = updated
			recovery = *updated.OverwriteRecovery
		}
		targetEntry, targetErr = observeRecoveryEntry(operation.TargetPath, operation.SourceFingerprint)
		stagedEntry, stagedErr = observeRecoveryEntry(recovery.StagingPath, operation.SourceFingerprint)
		if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("re-observe overwrite target during recovery: %w", targetErr)}
		}
		if stagedErr != nil && !errors.Is(stagedErr, os.ErrNotExist) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("re-observe overwrite staging during recovery: %w", stagedErr)}
		}
		targetPresent = targetErr == nil
		stagedPresent = stagedErr == nil
		targetPlacement := targetPresent && recoveryEntryMatchesPlacement(targetEntry, placementExpected)
		stagedDisplaced := stagedPresent && recoveryEntryMatchesDisplaced(stagedEntry, recovery)
		if targetPlacement && (!stagedPresent || stagedDisplaced) {
			if operation.Operation == domain.OperationMove {
				absent, absentErr := observeSourceAbsent(operation.SourcePath)
				if absentErr != nil {
					return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("check move source before displaced cleanup: %w", absentErr)}
				}
				if !absent {
					operation, restoreErr := executor.restoreExchangedTarget(ctx, operation, root, recovery)
					if restoreErr != nil {
						return operation, true, restoreErr
					}
					return operation, true, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("move source reappeared during overwrite recovery")}
				}
			}
			if stagedPresent {
				if err := executor.removeVerifiedDisplacedStaging(root, operation, recovery); err != nil {
					return operation, true, err
				}
			}
			return executor.finishOverwrite(ctx, operation, root, recovery, targetEntry.observation)
		}
		if targetPlacement && stagedPresent && !stagedDisplaced {
			operation, checkpointErr := executor.checkpointForeignAtStaging(ctx, operation, recovery, stagedEntry)
			if checkpointErr != nil {
				return operation, true, checkpointErr
			}
			recovery = *operation.OverwriteRecovery
			updated, err := executor.finalizeRecoveredForeign(ctx, operation, root, recovery, stagedEntry)
			return updated, true, err
		}
	case domain.OverwriteStageRestored, domain.OverwriteStageBlocked:
		if !targetPresent || !recoveryEntryMatchesDisplaced(targetEntry, recovery) || stagedPresent {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("recovered overwrite terminal evidence changed")}
		}
		if recovery.ForeignEvidencePath != "" {
			if err := executor.verifyForeignEvidence(recovery); err != nil {
				return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify recovered foreign evidence: %w", err)}
			}
		}
		if recovery.Stage == domain.OverwriteStageBlocked {
			return operation, true, &OperationError{Code: CodeRecoveryBlocked, Err: errors.New(recovery.BlockedReason)}
		}
		if operation.Operation == domain.OperationMove {
			if err := verifyRestoredMoveSource(operation, recovery); err != nil {
				return operation, true, err
			}
		}
		return operation, true, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("overwrite was restored after a target race")}

	case domain.OverwriteStageCommitted:
		if !placementOK || !targetPresent || !recoveryEntryMatchesPlacement(targetEntry, placementExpected) || stagedPresent {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("committed overwrite state does not match filesystem evidence")}
		}
		if operation.Operation == domain.OperationMove {
			absent, absentErr := observeSourceAbsent(operation.SourcePath)
			if absentErr != nil || !absent {
				return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("committed move source is present")}
			}
		}
		return completeOperation(operation, targetEntry.observation, false), true, nil
	}
	return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite recovery state does not match filesystem evidence")}
}

func (executor OSFileExecutor) resumeExchange(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery, target domain.FileObservation) (domain.FileOperation, bool, error) {
	if !sameFrozenFileObservation(target, recovery.TargetObservation) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("target changed before recovered exchange")}
	}
	operation, err := executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, true, err
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	staged, stagedErr := observeFileAs(recovery.StagingPath, operation.SourceFingerprint)
	if stagedErr != nil || !sameFrozenFileObservation(staged, recovery.StagedObservation) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staging identity changed before recovered exchange")}
	}
	current, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !sameFrozenFileObservation(current, recovery.TargetObservation) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("target changed immediately before recovered exchange")}
	}
	exchangeCallErr := executor.exchange(root, recovery.StagingPath, operation.TargetPath, operation.TargetParentIdentity)

	if exchangeErr := atomicReplaceError(exchangeCallErr); exchangeErr != nil {
		if !isAtomicReplaceUnsupported(exchangeErr) {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("recovered overwrite exchange outcome is uncertain: %w", exchangeErr)}
		}
		if cleanupErr := executor.cleanupUnsupportedStaging(ctx, &operation, root, recovery); cleanupErr != nil {
			return operation, true, cleanupErr
		}
		return operation, true, exchangeErr
	}
	recovery.Stage = domain.OverwriteStageExchanged
	displaced, displacedErr := observeFileAs(recovery.StagingPath, operation.SourceFingerprint)
	if displacedErr != nil {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe recovered displaced target: %w", displacedErr)}
	}
	recovery.DisplacedObservation = displaced
	if !sameFrozenFileObservation(displaced, recovery.TargetObservation) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("recovered displaced target changed during exchange")}
	}
	if operation.Operation == domain.OperationMove && !sourceAbsent(operation.SourcePath) {
		operation, restoreErr := executor.restoreExchangedTarget(ctx, operation, root, recovery)
		if restoreErr != nil {
			return operation, true, restoreErr
		}
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("recovered move source was occupied after exchange")}
	}
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, true, err
	}
	return executor.recoverOverwrite(ctx, operation, root)
}

func (executor OSFileExecutor) restoreExchangedTarget(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
	if recovery.DisplacedObservation.Fingerprint == "" {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cannot restore exchange without a frozen displaced observation")}
	}
	if present, err := executor.verifyDisplacedStaging(recovery); err != nil || !present {
		if err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify displaced staging before exchange-back: %w", err)}
		}
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("displaced staging is absent before exchange-back")}
	}
	operation, err := executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, err
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	currentTarget, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	placementExpected, placementOK := recoveryPlacementObservation(operation, recovery)
	if !placementOK || err != nil || !sameFrozenFileObservation(currentTarget, placementExpected) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("raced target changed before restore exchange")}
	}
	exchangeErr := executor.exchange(root, recovery.StagingPath, operation.TargetPath, operation.TargetParentIdentity)
	if err := atomicReplaceError(exchangeErr); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("raced target restoration exchange: %w", err)}
	}
	restored, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !sameFrozenFileObservation(restored, recovery.DisplacedObservation) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("raced target restoration could not be verified")}
	}
	if operation.Operation == domain.OperationMove {
		operation, err = executor.checkpoint(ctx, operation, recovery)
		if err != nil {
			return operation, err
		}
		if err := reauthorize(root, &operation); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
		if err := executor.restoreMovedSourceAuthorized(root, operation, recovery.StagingPath); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
		if err := verifyRestoredMoveSource(operation, recovery); err != nil {
			return operation, err
		}
		if err := syncRecoveryDirectories(operation, root, recovery); err != nil {
			return operation, err
		}
	} else if err := executor.removeVerifiedStaged(root, operation, recovery, operation.TargetParentIdentity); err != nil {
		return operation, err
	}
	recovery.Stage = domain.OverwriteStageRestored
	operation, checkpointErr := executor.checkpoint(ctx, operation, recovery)
	if checkpointErr != nil {
		return operation, checkpointErr
	}
	operation.OverwriteRecovery = &recovery
	return operation, nil
}

func (executor OSFileExecutor) finishOverwrite(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery, _ domain.FileObservation) (domain.FileOperation, bool, error) {
	if err := syncMutationPath(root, operation.TargetPath, operation.TargetParentIdentity); err != nil {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	placementExpected, placementOK := recoveryPlacementObservation(operation, recovery)
	final, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	if !placementOK || err != nil || !sameFrozenFileObservation(final, placementExpected) {
		return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("recovered final target verification failed")}
	}
	if operation.Operation == domain.OperationMove {
		absent, sourceErr := observeSourceAbsent(operation.SourcePath)
		if sourceErr != nil {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("check recovered move source: %w", sourceErr)}
		}
		if !absent {
			return operation, true, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("recovered move source still exists")}
		}
	}
	recovery.Stage = domain.OverwriteStageCommitted
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, true, err
	}
	operation = completeOperation(operation, final, false)
	operation.OverwriteRecovery = &recovery
	return operation, true, nil
}

func (executor OSFileExecutor) abandonStagedOverwrite(ctx context.Context, operation domain.FileOperation, root string, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	staged, err := observeFileAs(recovery.StagingPath, operation.SourceFingerprint)
	if err != nil || !sameFrozenFileObservation(staged, recovery.StagedObservation) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("abandoned overwrite staging identity changed")}
	}
	if operation.Operation == domain.OperationMove {
		if err := executor.restoreMovedSourceAuthorized(root, operation, recovery.StagingPath); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
		if err := verifyRestoredMoveSource(operation, recovery); err != nil {
			return operation, err
		}
	} else if err := executor.removeVerifiedStaged(root, operation, recovery, operation.TargetParentIdentity); err != nil {
		return operation, err
	}
	if err := syncRecoveryDirectories(operation, root, recovery); err != nil {
		return operation, err
	}
	recovery.Stage = domain.OverwriteStageAborted
	return executor.checkpoint(ctx, operation, recovery)
}

func (executor OSFileExecutor) cleanupUnsupportedStaging(ctx context.Context, operation *domain.FileOperation, root string, recovery domain.OverwriteRecovery) error {
	if err := reauthorize(root, operation); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	currentTarget, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !sameFrozenFileObservation(currentTarget, recovery.TargetObservation) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("target changed while cleaning unsupported overwrite staging")}
	}
	staged, err := observeFileAs(recovery.StagingPath, operation.SourceFingerprint)
	if err != nil || !sameFrozenFileObservation(staged, recovery.StagedObservation) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("unsupported overwrite staging identity changed before cleanup")}
	}
	if operation.Operation == domain.OperationMove {
		if err := executor.restoreMovedSourceAuthorized(root, *operation, recovery.StagingPath); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	} else if err := executor.removeVerifiedStaged(root, *operation, recovery, operation.TargetParentIdentity); err != nil {
		return err
	}
	recovery.Stage = domain.OverwriteStageAborted
	updated, err := executor.checkpoint(ctx, *operation, recovery)
	if err != nil {
		return err
	}
	*operation = updated
	return nil
}

func observeSourceAbsent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func sourceAbsent(path string) bool {
	absent, err := observeSourceAbsent(path)
	return err == nil && absent
}

func canonicalSource(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", errors.New("source path must be absolute")
	}
	clean := filepath.Clean(path)
	originalInfo, err := os.Lstat(clean)
	if err != nil {
		return clean, err
	}
	if originalInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source must not be a symlink")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return clean, err
	}
	info, err := os.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("source must be a regular non-symlink file")
	}
	return filepath.Clean(resolved), nil
}

func (executor OSFileExecutor) checkpoint(ctx context.Context, operation domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
	operation.OverwriteRecovery = &recovery
	if executor.Checkpoint == nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite checkpoint callback is required")}
	}
	if recovery.Stage != domain.OverwriteStagePrepared {
		if err := syncRecoveryDirectories(operation, recovery.CanonicalRoot, recovery); err != nil {
			return operation, err
		}
	}
	updated, err := executor.Checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, domain.MarkRecoveryUncertain(err)
	}
	updated.ID = operation.ID
	updated.Operation = operation.Operation
	updated.SourcePath = operation.SourcePath
	updated.TargetPath = operation.TargetPath
	updated.SourceFingerprint = operation.SourceFingerprint
	updated.SourceSizeBytes = operation.SourceSizeBytes
	updated.SourceSizeKnown = operation.SourceSizeKnown
	updated.CollisionPolicy = operation.CollisionPolicy
	updated.SourceParentIdentity = operation.SourceParentIdentity
	updated.TargetParentIdentity = operation.TargetParentIdentity
	updated.OverwriteRecovery = &recovery
	return updated, nil
}

func (executor OSFileExecutor) executeOverwrite(ctx context.Context, operation domain.FileOperation, root string, source, target domain.FileObservation) (domain.FileOperation, error) {
	if err := executor.atomicExchangeSupported(); err != nil {
		return operation, err
	}
	if strings.TrimSpace(operation.ID) == "" {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("overwrite requires an operation ID")}
	}
	recovery := domain.OverwriteRecovery{
		Stage: domain.OverwriteStagePrepared, CanonicalRoot: root,
		CanonicalSource: filepath.Clean(operation.SourcePath), CanonicalTarget: operation.TargetPath,
		StagingPath: stagingPath(operation), SourceObservation: source, TargetObservation: target,
		SourceParentIdentity: operation.SourceParentIdentity, TargetParentIdentity: operation.TargetParentIdentity,
	}
	var err error
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, err
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	if operation.Operation == domain.OperationMove {
		currentSource, sourceErr := observeFileAs(operation.SourcePath, operation.SourceFingerprint)
		matchesSource := sourceErr == nil && sameMutationFileObservation(currentSource, recovery.SourceObservation)
		if !matchesSource {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("source identity or handle evidence changed before overwrite staging")}
		}
	}
	if existing, statErr := observeFileAs(recovery.StagingPath, operation.SourceFingerprint); statErr == nil {
		if operation.Operation == domain.OperationCopy {
			if !sameFileObservation(existing, source) {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("operation staging path contains unexpected data")}
			}
		} else if !sameFrozenFileObservation(existing, source) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("operation staging path contains unexpected physical identity")}
		}
		recovery.StagedObservation = existing
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: statErr}
	} else if err := executor.stageExact(ctx, operation, root, recovery.StagingPath, source); err != nil {
		return operation, err
	}
	staged, err := observeFileAs(recovery.StagingPath, operation.SourceFingerprint)
	stagedMatchesSource := sameFileObservation(staged, source)
	if operation.Operation != domain.OperationCopy {
		stagedMatchesSource = sameMutationFileObservation(staged, source)
	}
	if err != nil || !stagedMatchesSource {
		mismatchErr := &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("staged effect does not match frozen source identity")}
		if operation.Operation == domain.OperationMove && err == nil {
			if restoreErr := executor.restoreMovedSourceAuthorized(root, operation, recovery.StagingPath); restoreErr != nil {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: restoreErr}
			}
		}
		return operation, mismatchErr
	}
	recovery.Stage, recovery.StagedObservation = domain.OverwriteStageStaged, staged
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, err
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	currentSource, err := observeFileAs(operation.SourcePath, operation.SourceFingerprint)
	if operation.Operation != domain.OperationMove && (err != nil || !sameMutationFileObservation(currentSource, recovery.SourceObservation)) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("source changed immediately before exchange")}
	}
	currentTarget, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !sameMutationFileObservation(currentTarget, recovery.TargetObservation) {
		operation, cleanupErr := executor.abandonStagedOverwrite(ctx, operation, root, recovery)
		if cleanupErr != nil {
			return operation, cleanupErr
		}
		return operation, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("target changed or lacks handle evidence before overwrite exchange")}
	}
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, err
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	if executor.Hooks.BeforeExchange != nil {
		if err := executor.Hooks.BeforeExchange(recovery.StagingPath, operation.TargetPath); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	}
	if err := verifyStagedObservation(recovery.StagingPath, recovery.StagedObservation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("staging identity changed immediately before overwrite exchange: %w", err)}
	}
	currentTarget, err = observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	if err != nil || !sameMutationFileObservation(currentTarget, recovery.TargetObservation) {
		operation, cleanupErr := executor.abandonStagedOverwrite(ctx, operation, root, recovery)
		if cleanupErr != nil {
			return operation, cleanupErr
		}
		return operation, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("target changed or lacks handle evidence immediately before overwrite exchange")}
	}
	exchangeCallErr := executor.exchange(root, recovery.StagingPath, operation.TargetPath, operation.TargetParentIdentity)

	if exchangeErr := atomicReplaceError(exchangeCallErr); exchangeErr != nil {
		if !isAtomicReplaceUnsupported(exchangeErr) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("atomic overwrite exchange outcome is uncertain: %w", exchangeErr)}
		}
		if cleanupErr := executor.cleanupUnsupportedStaging(ctx, &operation, root, recovery); cleanupErr != nil {
			return operation, cleanupErr
		}
		return operation, fmt.Errorf("atomic overwrite exchange: %w", exchangeErr)
	}
	if err := syncMutationPath(root, operation.TargetPath, operation.TargetParentIdentity); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("sync target directory after overwrite exchange: %w", err)}
	}
	recovery.Stage = domain.OverwriteStageExchanged
	if executor.Hooks.AfterExchange != nil {
		if err := executor.Hooks.AfterExchange(recovery.StagingPath, operation.TargetPath); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	}
	displaced, err := observeFileAs(recovery.StagingPath, operation.SourceFingerprint)
	if err != nil {
		displacedInfo, infoErr := os.Lstat(recovery.StagingPath)
		if infoErr != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("raced target cannot be observed for restoration")}
		}
		recovery.DisplacedNonRegular = true
		recovery.DisplacedType = displacedEntryType(displacedInfo)
		displacedIdentity, identityErr := fileidentity.FromPath(recovery.StagingPath)
		if identityErr != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe non-regular displaced identity: %w", identityErr)}
		}
		recovery.DisplacedIdentity = displacedIdentity
		if !fileidentity.IsPresent(recovery.DisplacedIdentity) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("non-regular displaced identity is unavailable")}
		}
		operation, checkpointErr := executor.checkpoint(ctx, operation, recovery)
		if checkpointErr != nil {
			return operation, checkpointErr
		}
		if err := reauthorize(root, &operation); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
		exchangeCallErr := executor.exchange(root, recovery.StagingPath, operation.TargetPath, operation.TargetParentIdentity)
		if swapErr := atomicReplaceError(exchangeCallErr); swapErr != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("restore non-regular raced target: %w", swapErr)}
		}
		restoredIdentity, restoredIdentityErr := fileidentity.FromPath(operation.TargetPath)
		if restoredIdentityErr != nil || !fileidentity.Matches(recovery.DisplacedIdentity, restoredIdentity) {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("non-regular raced target restoration could not be verified")}
		}
		if operation.Operation == domain.OperationMove {
			operation, checkpointErr = executor.checkpoint(ctx, operation, recovery)
			if checkpointErr != nil {
				return operation, checkpointErr
			}
			if err := reauthorize(root, &operation); err != nil {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
			}
			if err := executor.restoreMovedSourceAuthorized(root, operation, recovery.StagingPath); err != nil {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
			}
			if err := verifyRestoredMoveSource(operation, recovery); err != nil {
				return operation, err
			}
		} else if err := executor.removeVerifiedStaged(root, operation, recovery, operation.TargetParentIdentity); err != nil {
			return operation, err
		}
		if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("non-regular recovery staging remains after restoration")}
			}
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify non-regular recovery staging removal: %w", err)}
		}
		if err := syncRecoveryDirectories(operation, root, recovery); err != nil {
			return operation, err
		}
		recovery.Stage = domain.OverwriteStageRestored
		operation, checkpointErr = executor.checkpoint(ctx, operation, recovery)
		if checkpointErr != nil {
			return operation, checkpointErr
		}
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("target became non-regular during atomic overwrite")}
	}
	recovery.DisplacedObservation = displaced
	if operation.Operation == domain.OperationMove && !sourceAbsent(operation.SourcePath) {
		operation, restoreErr := executor.restoreExchangedTarget(ctx, operation, root, recovery)
		if restoreErr != nil {
			return operation, restoreErr
		}
		return operation, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("move source was occupied after overwrite exchange")}
	}
	if !sameFrozenFileObservation(displaced, currentTarget) {
		operation, restoreErr := executor.restoreExchangedTarget(ctx, operation, root, recovery)
		if restoreErr != nil {
			return operation, restoreErr
		}
		return operation, &OperationError{Code: CodeTargetCollisionRace, Err: errors.New("target changed during atomic overwrite")}
	}
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, err
	}
	if err := reauthorize(root, &operation); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	if executor.Hooks.BeforeFinalRead != nil {
		if err := executor.Hooks.BeforeFinalRead(operation.TargetPath); err != nil {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	}
	final, err := observeFileAs(operation.TargetPath, operation.SourceFingerprint)
	placementExpected, placementOK := recoveryPlacementObservation(operation, recovery)
	if !placementOK || err != nil || !sameFrozenFileObservation(final, placementExpected) {
		traced := recoveryEntryObservation{}
		if entry, entryErr := observeRecoveryEntry(operation.TargetPath, operation.SourceFingerprint); entryErr == nil {
			traced = entry
		}
		if restored, restoreErr := executor.restoreFinalMismatch(ctx, operation, root, recovery, traced); restoreErr != nil {
			return restored, restoreErr
		} else {
			return restored, &OperationError{Code: CodeTargetVerification, Err: errors.New("final target does not match staged effect")}
		}
	}
	if operation.Operation == domain.OperationMove && !sourceAbsent(operation.SourcePath) {
		operation, restoreErr := executor.restoreExchangedTarget(ctx, operation, root, recovery)
		if restoreErr != nil {
			return operation, restoreErr
		}
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("move source was occupied before displaced cleanup")}
	}
	if operation.Operation == domain.OperationMove && !sourceAbsent(operation.SourcePath) {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("move source reappeared before displaced cleanup")}
	}
	if err := executor.removeVerifiedDisplacedStaging(root, operation, recovery); err != nil {
		return operation, err
	}
	if operation.Operation == domain.OperationMove {
		if absent, absentErr := observeSourceAbsent(operation.SourcePath); absentErr != nil || !absent {
			return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("move source reappeared after displaced cleanup")}
		}
	}
	if err := syncMutationPath(root, operation.TargetPath, operation.TargetParentIdentity); err != nil {
		return operation, &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	recovery.Stage = domain.OverwriteStageCommitted
	operation, err = executor.checkpoint(ctx, operation, recovery)
	if err != nil {
		return operation, err
	}
	operation = completeOperation(operation, final, false)
	operation.OverwriteRecovery = &recovery
	return operation, nil
}

func stagingPath(operation domain.FileOperation) string {
	sum := sha256.Sum256([]byte(operation.ID))
	return filepath.Join(filepath.Dir(operation.TargetPath), ".bansoko-overwrite-"+hex.EncodeToString(sum[:12]))
}

func (executor OSFileExecutor) stageExact(ctx context.Context, operation domain.FileOperation, root, staging string, expected domain.FileObservation) error {
	if err := reauthorize(root, &operation); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	if err := executor.beforeAbsentMutation(operation.SourcePath, staging, expected); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}

	switch operation.Operation {
	case domain.OperationCopy:
		if err := copyToExactPath(ctx, "", root, operation.SourcePath, staging, expected, operation.SourceParentIdentity, operation.TargetParentIdentity, executor.Hooks.BeforeExactCopy, executor.Hooks.BeforeTemporaryRemove); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	case domain.OperationMove:
		if err := executor.moveNoReplace("", root, operation.SourcePath, staging, expected, operation.SourceParentIdentity, operation.TargetParentIdentity); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
	case domain.OperationHardlink:
		if err := safeLink("", root, operation.SourcePath, staging, operation.SourceParentIdentity, operation.TargetParentIdentity); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("hardlink source into staging: %w", err)}
		}
	default:
		return fmt.Errorf("unsupported file operation %q", operation.Operation)
	}
	if err := syncMutationPath(root, staging, operation.TargetParentIdentity); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("sync staging directory after mutation: %w", err)}
	}
	return nil
}

func copyToExactPath(ctx context.Context, sourceRoot, targetRoot, source, target string, expected domain.FileObservation, expectedSourceParent, expectedTargetParent domain.FileIdentity, beforeCopy func(string, string) error, beforeTemporaryRemove func(string) error) (resultErr error) {
	if !fileidentity.IsPresent(expectedSourceParent) || !fileidentity.IsPresent(expectedTargetParent) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("exact copy parent identities were not authorized")}
	}
	if beforeCopy != nil {
		if err := beforeCopy(source, target); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
		}
		currentSource, observeErr := observeFileAs(source, expected.Fingerprint)
		if observeErr != nil || !sameMutationFileObservation(currentSource, expected) {
			return &OperationError{Code: CodeSourceChanged, Err: errors.New("source changed immediately before exact copy")}
		}
	}
	sourceEntry, err := openMutationEntry(sourceRoot, source, expectedSourceParent)
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	input, err := openEntryNoFollow(sourceEntry)
	if err != nil {
		_ = sourceEntry.parent.Close()
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	parent := filepath.Dir(target)
	name := fmt.Sprintf(".bansoko-copy-%x", sha256.Sum256([]byte(target+strconv.FormatInt(time.Now().UnixNano(), 10))))
	var output *os.File
	var temporaryEntry verifiedEntry
	output, temporaryEntry, err = createNoReplaceTempHandle(targetRoot, parent, expectedTargetParent, name)
	if err != nil {
		inputCloseErr := input.Close()
		sourceParentCloseErr := sourceEntry.parent.Close()
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.Join(err, inputCloseErr, sourceParentCloseErr)}
	}
	temporary := output.Name()
	var temporaryIdentity domain.FileIdentity
	defer func() {
		if closeErr := output.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("close staging: %w", closeErr))
		}
		if _, observeErr := os.Lstat(temporary); observeErr == nil {
			removeErr := removeVerifiedEntryHandle(temporaryEntry, temporaryIdentity, beforeTemporaryRemove)
			if removeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove exact-copy temporary: %w", removeErr))
			}
		} else if !errors.Is(observeErr, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("verify exact-copy temporary cleanup: %w", observeErr))
		}
		if closeErr := input.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close source: %w", closeErr))
		}
		if closeErr := sourceEntry.parent.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close source parent: %w", closeErr))
		}
		if closeErr := temporaryEntry.parent.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close temporary parent: %w", closeErr))
		}
		if resultErr != nil {
			resultErr = &OperationError{Code: CodeRecoveryUnsafe, Err: resultErr}
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hash), input)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	actual := domain.FileObservation{Fingerprint: hex.EncodeToString(hash.Sum(nil)), SizeBytes: written}
	if !sameFileObservation(actual, expected) {
		return errors.New("source changed while copying")
	}
	if err := output.Sync(); err != nil {
		return err
	}
	temporaryIdentity, err = fileidentity.FromFile(output)
	if err != nil {
		return err
	}
	if !fileidentity.IsPresent(temporaryIdentity) {
		return errors.New("temporary identity is unavailable")
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := commitTemporaryLink(targetRoot, temporaryEntry, target, expectedTargetParent); err != nil {
		return fmt.Errorf("commit exact copy without replacement: %w", err)
	}
	return nil
}

func (executor OSFileExecutor) executeAbsent(ctx context.Context, operation domain.FileOperation, root string, expected domain.FileObservation) error {
	if err := reauthorize(root, &operation); err != nil {
		return err
	}
	if err := executor.beforeAbsentMutation(operation.SourcePath, operation.TargetPath, expected); err != nil {
		return err
	}
	switch operation.Operation {
	case domain.OperationCopy:
		return copyNoOverwrite(ctx, "", root, operation.SourcePath, operation.TargetPath, expected, operation.SourceParentIdentity, operation.TargetParentIdentity, executor.Hooks.BeforeTemporaryRemove, executor.Hooks.BeforePublish)
	case domain.OperationMove:
		return executor.moveNoReplace("", root, operation.SourcePath, operation.TargetPath, expected, operation.SourceParentIdentity, operation.TargetParentIdentity)
	case domain.OperationHardlink:
		if executor.Hooks.BeforeHardlinkMutation != nil {
			if err := executor.Hooks.BeforeHardlinkMutation(operation.SourcePath, operation.TargetPath); err != nil {
				return err
			}
		}
		currentSource, err := observeFileAs(operation.SourcePath, operation.SourceFingerprint)
		if err != nil || !sameMutationFileObservation(currentSource, expected) {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("source changed immediately before hardlink mutation")}
		}
		if executor.Hooks.BeforePublish != nil {
			if err := executor.Hooks.BeforePublish(operation.SourcePath, operation.TargetPath); err != nil {
				return err
			}
		}
		if err := safeLink("", root, operation.SourcePath, operation.TargetPath, operation.SourceParentIdentity, operation.TargetParentIdentity); err != nil {
			return fmt.Errorf("create hardlink placement file: %w", err)
		}
		return nil
	case domain.OperationNoop:
		return nil
	default:
		return fmt.Errorf("unsupported file operation %q", operation.Operation)
	}
}

func (executor OSFileExecutor) beforeAbsentMutation(source, target string, expected domain.FileObservation, _ ...bool) error {
	if executor.Hooks.BeforeAbsentMutation != nil {
		if err := executor.Hooks.BeforeAbsentMutation(source, target); err != nil {
			return err
		}
	}
	current, err := observeFileAs(source, expected.Fingerprint)
	if err != nil || !sameMutationFileObservation(current, expected) {
		return &OperationError{Code: CodeSourceChanged, Err: errors.New("source changed immediately before absent-target mutation")}
	}
	return nil
}

func (executor OSFileExecutor) moveNoReplace(sourceRoot, targetRoot, source, target string, expected domain.FileObservation, parents ...domain.FileIdentity) error {
	if err := authorizeExistingSourceMutation(source); err != nil {
		return pathError(err)
	}
	if len(parents) < 2 || !fileidentity.IsPresent(parents[0]) || !fileidentity.IsPresent(parents[1]) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("move parent identities were not authorized")}
	}
	sourceParent, targetParent := parents[0], parents[1]
	rename := func(source, target string) error {
		return safeRenameNoReplace(sourceRoot, targetRoot, source, target, sourceParent, targetParent)
	}
	if executor.Hooks.AtomicRenameNoReplace != nil {
		rename = executor.Hooks.AtomicRenameNoReplace
	}
	if executor.Hooks.BeforePublish != nil {
		if err := executor.Hooks.BeforePublish(source, target); err != nil {
			return err
		}
	}
	if err := rename(source, target); err != nil {
		return fmt.Errorf("rename move placement without replacement: %w", err)
	}
	if executor.Hooks.AfterAbsentMutation != nil {
		if err := executor.Hooks.AfterAbsentMutation(source, target); err != nil {
			if restoreErr := restoreMovedSourceChecked(sourceRoot, targetRoot, source, target, targetParent, sourceParent); restoreErr != nil {
				return restoreErr
			}
			return &OperationError{Code: CodeSourceChanged, Err: err}
		}
	}
	placed, err := observeFileAs(target, expected.Fingerprint)
	if err == nil && observationMatchesExpected(placed, expected) {
		return nil
	}
	if restoreErr := restoreMovedSourceChecked(sourceRoot, targetRoot, source, target, targetParent, sourceParent); restoreErr != nil {
		return restoreErr
	}
	return &OperationError{Code: CodeSourceChanged, Err: errors.New("moved file does not match frozen source")}
}

func restoreMovedSourceChecked(sourceRoot, targetRoot, source, target string, parents ...domain.FileIdentity) error {
	if err := authorizeSourceRestore(source); err != nil {
		return pathError(err)
	}
	return restoreMovedSource(sourceRoot, targetRoot, source, target, nil, parents...)
}

// restoreReference is the fingerprint mode of a restore: the frozen source's
// when one is supplied, else the hashing mode.
func restoreReference(expected []domain.FileObservation) string {
	if len(expected) > 0 {
		return expected[0].Fingerprint
	}
	return ""
}

func restoreMovedSource(sourceRoot, targetRoot, source, target string, expected []domain.FileObservation, parents ...domain.FileIdentity) error {
	if !sourceAbsent(source) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cannot restore moved file because source path is occupied")}
	}
	moved, err := observeFileAs(target, restoreReference(expected))
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("observe moved file before source restoration: %w", err)}
	}
	if len(expected) > 0 && !observationMatchesExpected(moved, expected[0]) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("moved file does not match frozen source identity before restoration")}
	}
	if len(parents) < 2 || !fileidentity.IsPresent(parents[0]) || !fileidentity.IsPresent(parents[1]) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("restore parent identities were not authorized")}
	}
	sourceParent, targetParent := parents[0], parents[1]
	if err := safeRenameNoReplace(targetRoot, sourceRoot, target, source, targetParent, sourceParent); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("restore moved file to source: %w", err)}
	}
	restored, err := observeFileAs(source, restoreReference(expected))
	if err != nil || !observationMatchesExpected(restored, moved) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("restored move source does not match the moved object")}
	}
	return nil
}

func verifyRestoredMoveSource(operation domain.FileOperation, recovery domain.OverwriteRecovery) error {
	if operation.Operation != domain.OperationMove {
		return nil
	}
	source, err := observeFileAs(operation.SourcePath, operation.SourceFingerprint)
	if err != nil || !observationMatchesExpected(source, recovery.SourceObservation) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("restored move source does not match frozen source identity")}
	}
	if _, err := os.Lstat(recovery.StagingPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("restored move staging remains present")}
		}
		return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("verify restored move staging: %w", err)}
	}
	return nil
}

func (executor OSFileExecutor) restoreMovedSourceAuthorized(root string, operation domain.FileOperation, stagedPath string) error {
	if executor.Hooks.BeforeSourceRestore != nil {
		if err := executor.Hooks.BeforeSourceRestore(operation.SourcePath, stagedPath); err != nil {
			return err
		}
	}
	if err := reauthorize(root, &operation); err != nil {
		return err
	}
	if err := authorizeSourceRestore(operation.SourcePath); err != nil {
		return pathError(err)
	}
	expected := recoverySourceObservation(operation, stagedPath)
	if fileidentity.IsPresent(expected.Identity) {
		return restoreMovedSource("", root, operation.SourcePath, stagedPath, []domain.FileObservation{expected}, operation.TargetParentIdentity, operation.SourceParentIdentity)
	}
	return restoreMovedSource("", root, operation.SourcePath, stagedPath, nil, operation.TargetParentIdentity, operation.SourceParentIdentity)
}

func recoverySourceObservation(operation domain.FileOperation, stagedPath string) domain.FileObservation {
	if operation.OverwriteRecovery != nil && filepath.Clean(operation.OverwriteRecovery.StagingPath) == filepath.Clean(stagedPath) {
		return operation.OverwriteRecovery.SourceObservation
	}
	return domain.FileObservation{}
}

func authorizeSourceParent(source string) (string, error) {
	if strings.TrimSpace(source) == "" || !filepath.IsAbs(source) {
		return "", errors.New("move source path must be absolute")
	}
	clean := filepath.Clean(source)
	if _, err := verifiedParentIdentityWithinRoot("", clean, false); err != nil {
		return "", errors.New("move source parent must be an existing non-symlink directory")
	}
	return clean, nil
}

func authorizeSourceRestore(source string) error {
	clean, err := authorizeSourceParent(source)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(clean); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("move source restore path became a symlink")
		}
		return errors.New("move source restore path is occupied")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect move source restore path: %w", err)
	}
	return nil
}

func authorizeExistingSourceMutation(source string) error {
	clean, err := authorizeSourceParent(source)
	if err != nil {
		return err
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("move source must remain a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || filepath.Clean(resolved) != clean {
		return errors.New("move source changed through a symlink")
	}
	return nil
}

// reauthorize rechecks the durable overwrite preconditions before each
// mutation and freezes the parent identities that the following syscall must
// verify. The chain anchor is the filesystem root: only target hygiene and the
// directory chain are reasserted, never a location allowlist.
func reauthorize(root string, operation *domain.FileOperation) error {
	if operation == nil {
		return pathError(errors.New("file operation is nil"))
	}
	if err := placementTargetHygiene(operation.TargetPath); err != nil {
		return pathError(err)
	}
	if operation.OverwriteRecovery != nil && (!fileidentity.IsPresent(operation.SourceParentIdentity) || !fileidentity.IsPresent(operation.TargetParentIdentity)) {
		return pathError(errors.New("recovery parent identities are required before authorization"))
	}
	if operation.OverwriteRecovery == nil {
		if err := ensureDirectoryChain(root, filepath.Dir(operation.TargetPath)); err != nil {
			return pathError(err)
		}
	}
	currentSourceParent, err := verifiedParentIdentityWithinRoot("", operation.SourcePath, false)
	if err != nil {
		return pathError(fmt.Errorf("authorize source parent: %w", err))
	}
	if !fileidentity.IsPresent(operation.SourceParentIdentity) {
		operation.SourceParentIdentity = currentSourceParent
	} else if !fileidentity.Matches(operation.SourceParentIdentity, currentSourceParent) {
		return pathError(errors.New("source parent identity changed after authorization"))
	}
	currentTargetParent, err := verifiedParentIdentityWithinRoot(root, operation.TargetPath, false)
	if err != nil {
		return pathError(fmt.Errorf("authorize target parent: %w", err))
	}
	if !fileidentity.IsPresent(operation.TargetParentIdentity) {
		operation.TargetParentIdentity = currentTargetParent
	} else if !fileidentity.Matches(operation.TargetParentIdentity, currentTargetParent) {
		return pathError(errors.New("target parent identity changed after authorization"))
	}
	return nil
}

// placementTargetHygiene enforces target well-formedness only: absolute,
// canonical, and naming a file below the filesystem root.
func placementTargetHygiene(target string) error {
	if !filepath.IsAbs(target) {
		return errors.New("placement target must be absolute")
	}
	if filepath.Clean(target) != target {
		return errors.New("placement target must be canonical")
	}
	if target == string(filepath.Separator) {
		return errors.New("placement target must name a file below the filesystem root")
	}
	return nil
}

func (executor OSFileExecutor) atomicExchangeSupported() error {
	if !atomicExchangeAvailable() {
		return &OperationError{Code: CodeAtomicReplaceUnsupported, Err: errAtomicReplaceUnsupported}
	}
	if executor.Hooks.AtomicExchangeCheck != nil {
		return atomicReplaceError(executor.Hooks.AtomicExchangeCheck())
	}
	return nil
}

func (executor OSFileExecutor) exchange(root, left, right string, expectedParent ...domain.FileIdentity) error {
	if executor.Hooks.AtomicExchange != nil {
		return executor.Hooks.AtomicExchange(left, right)
	}
	parent := domain.FileIdentity{}
	if len(expectedParent) > 0 {
		parent = expectedParent[0]
	}
	if !fileidentity.IsPresent(parent) {
		return errors.New("exchange parent identity was not authorized")
	}
	return safeExchange(root, left, right, parent)
}

func atomicReplaceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errAtomicReplaceUnsupported) {
		return &OperationError{Code: CodeAtomicReplaceUnsupported, Err: err}
	}
	return err
}

func isAtomicReplaceUnsupported(err error) bool {
	var operationErr *OperationError
	return errors.As(err, &operationErr) && operationErr.Code == CodeAtomicReplaceUnsupported
}

func pathError(err error) error { return &OperationError{Code: CodePathUnsafe, Err: err} }

func syncMutationPath(root, path string, expectedParent domain.FileIdentity) error {
	canonical, err := canonicalMutationPath(path)
	if err != nil {
		return err
	}
	entry, err := openVerifiedEntry(root, canonical, false, expectedParent)
	if err != nil {
		return err
	}
	defer entry.parent.Close()
	return entry.parent.Sync()
}

func syncRecoveryDirectories(operation domain.FileOperation, root string, recovery domain.OverwriteRecovery) error {
	paths := []struct {
		path string
		root string
	}{
		{path: recovery.CanonicalTarget, root: root},
		{path: recovery.StagingPath, root: root},
	}
	if operation.Operation == domain.OperationMove {
		paths = append(paths, struct {
			path string
			root string
		}{path: recovery.CanonicalSource, root: ""})
	}
	seen := make(map[string]struct{}, len(paths))
	for _, item := range paths {
		path := filepath.Clean(item.path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		parentIdentity, err := verifiedParentIdentityWithinRoot(item.root, path, false)
		if err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("authorize recovery directory %s: %w", path, err)}
		}
		if err := syncMutationPath(item.root, path, parentIdentity); err != nil {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("sync recovery directory %s: %w", filepath.Dir(path), err)}
		}
	}
	return nil
}

func sourceObservation(operation domain.FileOperation) domain.FileObservation {
	size := int64(-1)
	if sourceSizeKnown(operation) {
		size = operation.SourceSizeBytes
	}
	return domain.FileObservation{Fingerprint: operation.SourceFingerprint, SizeBytes: size}
}

func sourceSizeKnown(operation domain.FileOperation) bool {
	return operation.SourceSizeKnown || operation.SourceSizeBytes > 0
}

// OSFileRemover is the destructive half of the Library filesystem port. It
// verifies the expected fingerprint, size, and (when supplied) identity before
// moving a regular file under the chain anchor into quarantine.
type OSFileRemover struct {
	// BeforeQuarantine is test-only fault injection for the observe-to-rename
	// race. Production removers leave it nil.
	BeforeQuarantine func(string) error
}

// Remove preserves the original filesystem port while delegating to the
// size-aware implementation. The legacy port has no persisted size fact, so
// it validates the fingerprint while still obtaining the live size from the
// same descriptor read.
func (remover OSFileRemover) Remove(ctx context.Context, anchor, targetPath, expectedFingerprint string) error {
	return remover.RemoveFile(ctx, anchor, targetPath, domain.FileObservation{Fingerprint: expectedFingerprint, SizeBytes: -1})
}

func (remover OSFileRemover) RemoveFile(ctx context.Context, anchor, targetPath string, expected domain.FileObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := canonicalRoot(anchor)
	if err != nil {
		return err
	}
	targetPath, err = rebaseTarget(anchor, root, targetPath)
	if err != nil {
		return err
	}
	if err := targetUnderRoot(root, targetPath); err != nil {
		return err
	}
	if _, err := verifiedDirectoryChain(root, filepath.Dir(targetPath), false); err != nil {
		return fmt.Errorf("verify cleanup directory chain: %w", err)
	}
	if _, err := os.Lstat(targetPath); errors.Is(err, os.ErrNotExist) {
		quarantine := quarantinePath(targetPath)
		if _, quarantineErr := os.Lstat(quarantine); errors.Is(quarantineErr, os.ErrNotExist) {
			return nil
		} else if quarantineErr != nil {
			return fmt.Errorf("inspect completed cleanup quarantine: %w", quarantineErr)
		}
		actual, observeErr := observeFileAs(quarantine, expected.Fingerprint)
		if observeErr != nil {
			return errors.New("completed cleanup quarantine could not be observed")
		}
		if fileidentity.IsPresent(expected.Identity) && !fileidentity.Matches(actual.Identity, expected.Identity) {
			return errors.New("completed cleanup quarantine does not match expected identity")
		}
		if !quarantineReplayMatchesExpected(actual, expected) {
			return errors.New("completed cleanup quarantine does not match expected fingerprint or size")
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect cleanup target: %w", err)
	}
	if _, quarantineErr := os.Lstat(quarantinePath(targetPath)); quarantineErr == nil {
		return errors.New("operation-private cleanup quarantine and source both exist")
	} else if !errors.Is(quarantineErr, os.ErrNotExist) {
		return fmt.Errorf("inspect cleanup quarantine: %w", quarantineErr)
	}
	actual, err := observeFileAs(targetPath, expected.Fingerprint)
	if err != nil {
		return fmt.Errorf("verify cleanup target: %w", err)
	}
	if !observationMatchesExpected(actual, expected) {
		return errors.New("cleanup target fingerprint, size, or identity changed")
	}
	frozen := actual
	if fileidentity.IsPresent(expected.Identity) && !fileidentity.Matches(actual.Identity, expected.Identity) {
		return errors.New("cleanup target identity changed")
	}
	parentIdentity, err := verifiedParentIdentityWithinRoot(root, targetPath, false)
	if err != nil {
		return fmt.Errorf("authorize cleanup target parent: %w", err)
	}
	if remover.BeforeQuarantine != nil {
		if err := remover.BeforeQuarantine(targetPath); err != nil {
			return err
		}
	}
	if err := quarantineVerifiedEntry(root, root, targetPath, frozen, nil, parentIdentity); err != nil {
		return fmt.Errorf("quarantine cleanup target: %w", err)
	}
	return nil
}

func chooseTarget(operation domain.FileOperation) (string, bool, domain.FileObservation, error) {
	base := filepath.Clean(operation.TargetPath)
	for index := 0; ; index++ {
		candidate := base
		if index > 0 {
			if operation.CollisionPolicy != domain.CollisionVersion {
				return "", false, domain.FileObservation{}, &OperationError{Code: CodeTargetOccupied, Err: errors.New("placement target already exists with a different fingerprint or size")}
			}
			candidate = versionedPath(base, index+1)
		}
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, false, domain.FileObservation{}, nil
		}
		if err != nil {
			return "", false, domain.FileObservation{}, fmt.Errorf("inspect placement target: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, domain.FileObservation{}, errors.New("refusing to use a symlink placement target")
		}
		if !info.Mode().IsRegular() {
			return "", false, domain.FileObservation{}, errors.New("placement target must be a regular file")
		}
		observation, err := observeFileAs(candidate, operation.SourceFingerprint)
		if err != nil {
			return "", false, domain.FileObservation{}, fmt.Errorf("inspect existing placement target: %w", err)
		}
		if sameFileObservation(observation, sourceObservation(operation)) {
			return candidate, true, observation, nil
		}
		if operation.CollisionPolicy == domain.CollisionOverwrite {
			return candidate, false, observation, nil
		}
		if operation.CollisionPolicy != domain.CollisionVersion {
			return "", false, domain.FileObservation{}, &OperationError{Code: CodeTargetOccupied, Err: errors.New("placement target already exists with a different fingerprint or size")}
		}
		if index >= 1000 {
			return "", false, domain.FileObservation{}, errors.New("unable to resolve placement collision within the version limit")
		}
	}
}

func versionedPath(path string, version int) string {
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(path, extension)
	return base + " (" + strconv.Itoa(version) + ")" + extension
}

func copyNoOverwrite(ctx context.Context, sourceRoot, targetRoot, source, target string, expected domain.FileObservation, expectedSourceParent, expectedTargetParent domain.FileIdentity, beforeTemporaryRemove func(string) error, beforePublish ...func(string, string) error) (resultErr error) {
	if !fileidentity.IsPresent(expectedSourceParent) || !fileidentity.IsPresent(expectedTargetParent) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("copy parent identities were not authorized")}
	}
	sourceEntry, err := openMutationEntry(sourceRoot, source, expectedSourceParent)
	if err != nil {
		return fmt.Errorf("open source placement file: %w", err)
	}
	input, err := openEntryNoFollow(sourceEntry)
	if err != nil {
		_ = sourceEntry.parent.Close()
		return fmt.Errorf("open source placement file: %w", err)
	}
	initial, err := statOpenFile(input)
	if err != nil {
		_ = input.Close()
		_ = sourceEntry.parent.Close()
		return fmt.Errorf("stat source placement file: %w", err)
	}
	if !sizeMatches(initial.Size(), expected.SizeBytes) {
		_ = input.Close()
		_ = sourceEntry.parent.Close()
		return errors.New("source size changed before copying placement file")
	}

	name := fmt.Sprintf(".bansoko-placement-%x", sha256.Sum256([]byte(target+strconv.FormatInt(time.Now().UnixNano(), 10))))
	var output *os.File
	var temporaryEntry verifiedEntry
	output, temporaryEntry, err = createNoReplaceTempHandle(targetRoot, filepath.Dir(target), expectedTargetParent, name)
	if err != nil {
		_ = input.Close()
		_ = sourceEntry.parent.Close()
		return fmt.Errorf("create temporary placement file: %w", err)
	}
	temporary := output.Name()
	var temporaryIdentity domain.FileIdentity
	var cleanupErr error
	defer func() {
		if closeErr := output.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("close temporary placement file: %w", closeErr))
		}
		if _, statErr := os.Lstat(temporary); statErr == nil {
			if !fileidentity.IsPresent(temporaryIdentity) {
				cleanupErr = errors.Join(cleanupErr, errors.New("temporary identity was not frozen before cleanup"))
			} else if removeErr := removeVerifiedEntryHandle(temporaryEntry, temporaryIdentity, beforeTemporaryRemove); removeErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove temporary placement file: %w", removeErr))
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("inspect temporary placement file: %w", statErr))
		}
		cleanupErr = errors.Join(cleanupErr, input.Close(), sourceEntry.parent.Close(), temporaryEntry.parent.Close())
		if cleanupErr != nil && resultErr == nil {
			resultErr = &OperationError{Code: CodeRecoveryUnsafe, Err: cleanupErr}
		} else if cleanupErr != nil {
			resultErr = errors.Join(resultErr, &OperationError{Code: CodeRecoveryUnsafe, Err: cleanupErr})
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	if copyErr != nil {
		return fmt.Errorf("copy placement file: %w", copyErr)
	}
	final, err := statOpenFile(input)
	if err != nil {
		return fmt.Errorf("stat source placement file after copy: %w", err)
	}
	if initial.Size() != final.Size() || written != final.Size() {
		return errors.New("source size changed while copying placement file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	actual := domain.FileObservation{Fingerprint: hex.EncodeToString(hash.Sum(nil)), SizeBytes: final.Size()}
	if !sameFileObservation(actual, expected) {
		return errors.New("source fingerprint or size changed while copying placement file")
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync temporary placement file: %w", err)
	}
	temporaryIdentity, err = fileidentity.FromFile(output)
	if err != nil {
		return err
	}
	if !fileidentity.IsPresent(temporaryIdentity) {
		return errors.New("temporary identity is unavailable")
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close temporary placement file: %w", err)
	}
	if len(beforePublish) > 0 && beforePublish[0] != nil {
		if err := beforePublish[0](temporary, target); err != nil {
			return err
		}
	}
	if err := commitTemporaryLink(targetRoot, temporaryEntry, target, expectedTargetParent); err != nil {
		return fmt.Errorf("commit placement file without overwrite: %w", err)
	}
	cleanupErr = nil
	return nil
}

func quarantineReplayMatchesExpected(actual, expected domain.FileObservation) bool {
	if fileidentity.IsPresent(expected.Identity) {
		return sameFrozenFileObservation(actual, expected)
	}
	// Legacy remove operations froze no inode. The fixed operation-private path
	// plus the observed fingerprint and size are the complete evidence available
	// for replay; never compare a zero identity as if it matched another inode.
	return sameFileObservation(actual, expected)
}

func observationMatchesExpected(actual, expected domain.FileObservation) bool {
	if fileidentity.IsPresent(expected.Identity) {
		return sameFrozenFileObservation(actual, expected)
	}
	return sameFileObservation(actual, expected)
}

func removeVerifiedSource(sourceRoot, targetRoot, path string, expected domain.FileObservation, expectedParent domain.FileIdentity) error {
	if !fileidentity.IsPresent(expectedParent) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("source cleanup parent identity was not authorized")}
	}
	actual, err := observeFileAs(path, expected.Fingerprint)
	if err != nil {
		return err
	}
	if !observationMatchesExpected(actual, expected) {
		return errors.New("source fingerprint or size changed before move removal")
	}
	if err := quarantineVerifiedEntry(sourceRoot, targetRoot, path, actual, nil, expectedParent); err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	return nil
}

// canonicalRoot canonicalizes a directory-chain anchor: the scanned tree top
// or the cleanup anchor a composition root injected. The anchor bounds the walk
// to the directory the deployment handed this process, so it may not itself be
// a symlink.
func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return "", errors.New("chain anchor must be an absolute path")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect chain anchor: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("chain anchor must be an existing non-symlink directory")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve chain anchor: %w", err)
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil || resolvedInfo.Mode()&os.ModeSymlink != 0 || !resolvedInfo.IsDir() {
		return "", errors.New("resolved chain anchor must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func targetUnderRoot(root, target string) error {
	if !filepath.IsAbs(target) {
		return errors.New("placement target must be absolute")
	}
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("placement target escapes the chain anchor")
	}
	return nil
}

func rebaseTarget(originalRoot, resolvedRoot, target string) (string, error) {
	originalRoot = filepath.Clean(originalRoot)
	resolvedRoot = filepath.Clean(resolvedRoot)
	target = filepath.Clean(target)
	if relative, ok := relativeUnder(originalRoot, target); ok {
		return filepath.Join(resolvedRoot, relative), nil
	}
	if _, ok := relativeUnder(resolvedRoot, target); ok {
		return target, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err == nil {
		candidate := filepath.Join(parent, filepath.Base(target))
		if _, ok := relativeUnder(resolvedRoot, candidate); ok {
			return candidate, nil
		}
	}
	return "", errors.New("placement target is not under the chain anchor")
}

func relativeUnder(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

// ensureDirectoryChain creates and verifies the placement directory chain.
// An empty anchor walks the chain from the filesystem root — the deployment
// boundary owns where writes may land.
func ensureDirectoryChain(root, directory string) error {
	_, err := verifiedDirectoryChain(root, directory, true)
	return err
}

// observeFile hashes and stats one open descriptor. The descriptor's initial
// and final sizes must agree, so a growing/truncated file is never reported as
// a coherent fingerprint/size fact.
func observeFile(path string) (domain.FileObservation, error) {
	return observeFileWithHook(path, nil)
}

// observeFileMetadata is the path-level observation: the same no-follow,
// regular-file, descriptor-identity and stability checks as observeFile, but
// the content is never read. Its Fingerprint is the deterministic metadata
// identity (domain.MetadataFingerprint), so NFS/SMB libraries cost one stat
// and one open per file instead of a full read.
func observeFileMetadata(path string) (domain.FileObservation, error) {
	return observeFileMode(path, nil, false)
}

// observeFileAs observes path in the fingerprint mode of reference: a
// metadata fingerprint is matched by a metadata observation, anything else (a
// content digest) by the hashing observation.
func observeFileAs(path, reference string) (domain.FileObservation, error) {
	if domain.IsMetadataFingerprint(reference) {
		return observeFileMetadata(path)
	}
	return observeFile(path)
}

func observeFileWithHook(path string, beforeOpen func(string) error) (domain.FileObservation, error) {
	return observeFileMode(path, beforeOpen, true)
}

func observeFileMode(path string, beforeOpen func(string) error, hashContent bool) (domain.FileObservation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return domain.FileObservation{}, errors.New("file must be a regular non-symlink file")
	}
	pathIdentity, err := fileidentity.FromPath(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file identity: %w", err)
	}
	if !fileidentity.IsPresent(pathIdentity) {
		return domain.FileObservation{}, errors.New("file identity is unavailable")
	}
	initialChange, err := fileidentity.ChangeNanosFromPath(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file change marker: %w", err)
	}
	if beforeOpen != nil {
		if err := beforeOpen(path); err != nil {
			return domain.FileObservation{}, err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	defer file.Close()
	initial, err := statOpenFile(file)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat opened file: %w", err)
	}
	initialIdentity, err := fileidentity.FromFile(file)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat opened file identity: %w", err)
	}
	if !fileidentity.Matches(pathIdentity, initialIdentity) {
		return domain.FileObservation{}, errors.New("file path changed while opening descriptor")
	}
	initialIdentity = carryIdentityGeneration(initialIdentity, pathIdentity)
	handleEvidence, err := fileidentity.HandleEvidenceFromFile(file)
	if err != nil && !errors.Is(err, fileidentity.ErrEvidenceUnavailable) {
		return domain.FileObservation{}, fmt.Errorf("observe opened file handle: %w", err)
	}
	hash := sha256.New()
	readSize := initial.Size()
	if hashContent {
		readSize, err = io.Copy(hash, file)
		if err != nil {
			return domain.FileObservation{}, err
		}
	}
	final, err := statOpenFile(file)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat opened file after read: %w", err)
	}
	finalIdentity, err := fileidentity.FromFile(file)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat opened file identity after read: %w", err)
	}
	if initial.Size() != final.Size() || readSize != final.Size() {
		return domain.FileObservation{}, errors.New("file size changed while reading")
	}
	if !fileidentity.Matches(initialIdentity, finalIdentity) {
		return domain.FileObservation{}, errors.New("file identity changed while reading")
	}
	finalIdentity = carryIdentityGeneration(finalIdentity, initialIdentity)
	finalChange, err := fileidentity.ChangeNanosFromPath(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file change marker after read: %w", err)
	}
	if initialChange != 0 && finalChange != 0 && initialChange != finalChange {
		return domain.FileObservation{}, errors.New("file changed while reading")
	}
	fingerprint := domain.MetadataFingerprint(final.Size(), finalIdentity)
	if hashContent {
		fingerprint = hex.EncodeToString(hash.Sum(nil))
	}
	return domain.FileObservation{
		Fingerprint:    fingerprint,
		SizeBytes:      final.Size(),
		Identity:       finalIdentity,
		HandleEvidence: handleEvidence,
		ChangeNanos:    finalChange,
	}, nil
}

func carryIdentityGeneration(identity, source domain.FileIdentity) domain.FileIdentity {
	if identity.GenerationNanos == 0 && source.GenerationNanos != 0 &&
		identity.Device == source.Device && identity.Inode == source.Inode {
		identity.GenerationNanos = source.GenerationNanos
	}
	return identity
}

func sameDisplacedIdentity(path string, info os.FileInfo, recovery domain.OverwriteRecovery) bool {
	if info == nil || !recovery.DisplacedNonRegular {
		return false
	}
	identity, err := fileidentity.FromPath(path)
	if err != nil {
		return false
	}
	return displacedEntryType(info) == recovery.DisplacedType && fileidentity.Matches(identity, recovery.DisplacedIdentity)
}

func displacedEntryType(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode&os.ModeNamedPipe != 0:
		return "named_pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0:
		return "device"
	default:
		return mode.String()
	}
}

// verifiedParentIdentityWithinRoot freezes the identity of a path's parent
// directory after walking the whole chain with no-follow opens from the anchor
// (an empty anchor walks from the filesystem root), so an intermediate symlink
// is never followed.
func verifiedParentIdentityWithinRoot(root, path string, create bool) (domain.FileIdentity, error) {
	return verifiedDirectoryChain(root, filepath.Dir(path), create)
}

func fileIdentity(info os.FileInfo) domain.FileIdentity {
	return platformFileIdentity(info)
}

func statOpenFile(file *os.File) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("file descriptor must reference a regular file")
	}
	return info, nil
}

func fileObservationMatches(observation domain.FileObservation, expectedFingerprint string, expectedSize int64) bool {
	if !sizeMatches(observation.SizeBytes, expectedSize) {
		return false
	}
	if expected, ok := domain.ParseMetadataFingerprint(expectedFingerprint); ok {
		// Path-level identity: size plus inode when both sides know it.
		return metadataFactsCompatible(observationMetadataFacts(observation), expected)
	}
	return observation.Fingerprint == expectedFingerprint
}

// metadataFactsCompatible is domain.MetadataFactsMatch where a negative size
// is the legacy "size unknown" sentinel of a frozen expectation.
func metadataFactsCompatible(left, right domain.MetadataFacts) bool {
	if left.SizeBytes < 0 || right.SizeBytes < 0 {
		left.SizeBytes, right.SizeBytes = 0, 0
	}
	return domain.MetadataFactsMatch(left, right)
}

// observationMetadataFacts projects any observation (metadata or hashed) onto
// its path-level facts: the recorded size and the device/inode identity.
func observationMetadataFacts(observation domain.FileObservation) domain.MetadataFacts {
	if facts, ok := domain.ParseMetadataFingerprint(observation.Fingerprint); ok {
		if facts.Inode == 0 && fileidentity.IsPresent(observation.Identity) {
			facts.Device, facts.Inode = observation.Identity.Device, observation.Identity.Inode
		}
		return facts
	}
	facts := domain.MetadataFacts{SizeBytes: observation.SizeBytes}
	if fileidentity.IsPresent(observation.Identity) {
		facts.Device, facts.Inode = observation.Identity.Device, observation.Identity.Inode
	}
	return facts
}

func sizeMatches(actual, expected int64) bool {
	return expected < 0 || actual == expected
}

func sameFileObservation(left, right domain.FileObservation) bool {
	if !sizeMatches(left.SizeBytes, right.SizeBytes) {
		return false
	}
	if domain.IsMetadataFingerprint(left.Fingerprint) || domain.IsMetadataFingerprint(right.Fingerprint) {
		// At least one side is a path-level observation: compare the
		// path-level facts (size, inode when both know it), never a digest
		// against a metadata identity.
		return metadataFactsCompatible(observationMetadataFacts(left), observationMetadataFacts(right))
	}
	return left.Fingerprint == right.Fingerprint
}

func sameFrozenFileObservation(left, right domain.FileObservation) bool {
	if !sameFileObservation(left, right) {
		return false
	}
	return sameFileIdentity(left, right)
}

func sameFileIdentity(left, right domain.FileObservation) bool {
	if !fileidentity.IsPresent(left.Identity) || !fileidentity.IsPresent(right.Identity) {
		return false
	}
	return fileidentity.Matches(left.Identity, right.Identity)
}

func sameMutationFileObservation(left, right domain.FileObservation) bool {
	if !sameFileObservation(left, right) {
		return false
	}
	if domain.IsMetadataFingerprint(left.Fingerprint) || domain.IsMetadataFingerprint(right.Fingerprint) {
		// A network remount may change the client device number. Cleanup's
		// path-level identity is size + inode; retain the stronger generation or
		// change-marker evidence without reintroducing device equality.
		if !fileidentity.IsPresent(left.Identity) || !fileidentity.IsPresent(right.Identity) || left.Identity.Inode != right.Identity.Inode {
			return false
		}
		if left.Identity.GenerationNanos != 0 && right.Identity.GenerationNanos != 0 {
			return left.Identity.GenerationNanos == right.Identity.GenerationNanos
		}
		return left.ChangeNanos != 0 && right.ChangeNanos != 0 && left.ChangeNanos == right.ChangeNanos
	}
	if handleEvidenceComplete(left.HandleEvidence) && handleEvidenceComplete(right.HandleEvidence) {
		return fileidentity.MatchesHandleEvidence(left.HandleEvidence, right.HandleEvidence)
	}
	if fileidentity.IsPresent(left.Identity) && fileidentity.IsPresent(right.Identity) {
		if left.Identity.GenerationNanos != 0 && right.Identity.GenerationNanos != 0 {
			return fileidentity.Matches(left.Identity, right.Identity)
		}
		return left.ChangeNanos != 0 && right.ChangeNanos != 0 && left.ChangeNanos == right.ChangeNanos && fileidentity.Matches(left.Identity, right.Identity)
	}
	return left.ChangeNanos != 0 && right.ChangeNanos != 0 && left.ChangeNanos == right.ChangeNanos
}

func handleEvidenceComplete(evidence *domain.HandleEvidence) bool {
	return evidence != nil && evidence.MountID > 0 && evidence.Type > 0 && evidence.Opaque != ""
}

// OSFileObserver is the targeted observation adapter used after a placement
// effect. Observe keeps the historical fingerprint-only method, while
// ObserveFile exposes the complete size-aware fact to new callers.
type OSFileObserver struct{}

func (OSFileObserver) Observe(ctx context.Context, path string) (string, error) {
	observation, err := (OSFileObserver{}).ObserveFile(ctx, path)
	if err != nil {
		return "", err
	}
	return observation.Fingerprint, nil
}

func (OSFileObserver) ObserveFile(ctx context.Context, path string) (domain.FileObservation, error) {
	if err := ctx.Err(); err != nil {
		return domain.FileObservation{}, err
	}
	return observeFile(path)
}

// ObserveMetadataFile returns the complete path-level observation of path
// without reading its content: the size-aware twin of ObserveMetadata, for
// callers that need the frozen size as well as the fingerprint (the scan
// alignment recovery probe freezes both). Its Fingerprint is a
// domain.MetadataFingerprint, so a candidate frozen here is comparable with
// the same observation mode at confirm time.
func (OSFileObserver) ObserveMetadataFile(ctx context.Context, path string) (domain.FileObservation, error) {
	if err := ctx.Err(); err != nil {
		return domain.FileObservation{}, err
	}
	return observeFileMetadata(path)
}

// ObserveMetadata returns the path-level metadata fingerprint of path
// (domain.MetadataFingerprint) without reading its content. Callers holding a
// metadata fingerprint (a scan-derived alignment source) compare with it.
func (OSFileObserver) ObserveMetadata(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	observation, err := observeFileMetadata(path)
	if err != nil {
		return "", err
	}
	return observation.Fingerprint, nil
}

// OSFileScanner records regular files under an allowlisted scan scope. It
// never follows symlinks and never mutates the filesystem. A targeted scan of
// a missing path is a successful absence observation for that exact scope;
// callers must still use the coverage state before drawing any broader
// conclusion.
type OSFileScanner struct {
	Version string
}

func (s OSFileScanner) Scan(ctx context.Context, request domain.ScanRequest) (domain.ScanResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ScanResult{}, err
	}
	if !request.Scope.Valid() {
		return domain.ScanResult{}, errors.New("invalid scan scope")
	}
	version := strings.TrimSpace(s.Version)
	if version == "" {
		version = defaultScannerVersion
	}
	result := domain.ScanResult{ScannerVersion: version, Files: make([]domain.ScanFile, 0)}

	// Placement-owned scans observe only the explicit locator paths of the
	// instance's owned Placements (current paths + bounded provenance
	// fallbacks). There is no directory enumeration and no library-root walk:
	// a missing locator is skipped, never an error, and the caller records the
	// missing disposition from its own expected set.
	if request.Scope.Kind == domain.ScanScopePlacementOwned {
		observedAt := time.Now().UTC()
		seen := make(map[string]struct{}, len(request.Paths))
		for _, path := range request.Paths {
			if err := ctx.Err(); err != nil {
				return domain.ScanResult{}, err
			}
			if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
				return domain.ScanResult{}, fmt.Errorf("placement-owned scan requires absolute locator paths, got %q", path)
			}
			clean := filepath.Clean(path)
			if _, dup := seen[clean]; dup {
				continue
			}
			seen[clean] = struct{}{}
			info, err := os.Lstat(clean)
			if errors.Is(err, os.ErrNotExist) {
				continue // absent locator: caller records missing
			}
			if err != nil {
				return domain.ScanResult{}, fmt.Errorf("inspect placement locator %s: %w", clean, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				continue // never follow symlinks; non-regular locators are ignored
			}
			// Path-level verification: size plus file
			// identity, never a content read.
			observation, err := observeFileMetadata(clean)
			if err != nil {
				return domain.ScanResult{}, fmt.Errorf("observe placement locator %s: %w", clean, err)
			}
			result.Files = append(result.Files, domain.ScanFile{
				Path: clean, Fingerprint: observation.Fingerprint, SizeBytes: observation.SizeBytes,
				SpecialKind: "normal", ObservedAt: observedAt,
			})
		}
		sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
		return result, nil
	}

	root, err := canonicalRoot(request.TreeTop)
	if err != nil {
		return domain.ScanResult{}, err
	}
	scopePath, err := scannerScopePath(root, request.Scope)
	if err != nil {
		return domain.ScanResult{}, err
	}
	result.TreeTop = root
	info, err := os.Lstat(scopePath)
	if errors.Is(err, os.ErrNotExist) && request.Scope.Kind == domain.ScanScopeTargeted {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("inspect scan scope: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("scan scope must not be a symlink")
	}

	observedAt := time.Now().UTC()
	err = filepath.WalkDir(scopePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			return nil
		}
		observation, err := observeFileMetadata(path)
		if err != nil {
			return err
		}
		result.Files = append(result.Files, domain.ScanFile{
			Path: path, Fingerprint: observation.Fingerprint, SizeBytes: observation.SizeBytes,
			SpecialKind: "normal", ObservedAt: observedAt,
		})
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan files: %w", err)
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func scannerScopePath(root string, scope domain.ScanScope) (string, error) {
	if scope.Kind == domain.ScanScopeFull {
		return root, nil
	}
	relative := filepath.Clean(scope.RelativePath)
	if filepath.IsAbs(scope.RelativePath) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("targeted scan path must stay within the scan scope")
	}
	target := filepath.Join(root, relative)
	if err := targetUnderRoot(root, target); err != nil {
		return "", err
	}
	return target, nil
}

// OSMediaInspector provides a small provider-independent local snapshot. A
// richer MediaInfo implementation can implement the same application port
// without changing naming or placement use cases.
//
// It is not part of the production execution path: nothing constructs it. The
// execution-time media facts (media.resolution, media.video_codec,
// media.audio_codec, media.container) are read by artifact/adapter.OSMediaProber
// during artifact inspection and stored in artifact_files.facts.
type OSMediaInspector struct {
	Version string
	Binary  string
}

func (i OSMediaInspector) Inspect(ctx context.Context, artifactID, path, expectedFingerprint string) (domain.LocalMediaFactsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.LocalMediaFactsSnapshot{}, err
	}
	observation, err := observeFile(path)
	if err != nil {
		return domain.LocalMediaFactsSnapshot{}, fmt.Errorf("inspect local media file: %w", err)
	}
	if expectedFingerprint != "" && observation.Fingerprint != expectedFingerprint {
		return domain.LocalMediaFactsSnapshot{}, errors.New("local media fingerprint does not match artifact")
	}
	version := i.Version
	if version == "" {
		version = defaultInspectorVersion
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	values := map[string]domain.LocalMediaFact{
		"media.file_size_bytes": {State: "value", Value: observation.SizeBytes, Source: "local_media", FactRef: artifactID, DefinitionVersion: version},
		"media.file_extension":  {State: "value", Value: extension, Source: "local_media", FactRef: artifactID, DefinitionVersion: version},
		"media.file_basename":   {State: "value", Value: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Source: "local_media", FactRef: artifactID, DefinitionVersion: version},
	}
	metadata, metadataErr := i.readMediaInfo(ctx, path)
	if metadataErr != nil {
		if err := ctx.Err(); err != nil {
			return domain.LocalMediaFactsSnapshot{}, err
		}
		addUnavailableMediaFacts(values, artifactID, version, metadataErr.Error())
	} else {
		addMediaInfoFacts(values, metadata, artifactID, version)
	}
	finalObservation, err := observeFile(path)
	if err != nil {
		return domain.LocalMediaFactsSnapshot{}, fmt.Errorf("verify local media file after inspection: %w", err)
	}
	if !sameFileObservation(finalObservation, observation) {
		return domain.LocalMediaFactsSnapshot{}, errors.New("local media file changed during inspection")
	}
	return domain.LocalMediaFactsSnapshot{
		ID:               artifactID + ":" + observation.Fingerprint,
		ArtifactID:       artifactID,
		Path:             path,
		Fingerprint:      observation.Fingerprint,
		SizeBytes:        observation.SizeBytes,
		Version:          version + ":" + observation.Fingerprint,
		InspectorVersion: version,
		Values:           values,
		InspectedAt:      time.Now().UTC(),
	}, nil
}

type mediaInfoDocument struct {
	Media struct {
		Track []mediaInfoTrack `json:"track"`
	} `json:"media"`
}

type mediaInfoTrack struct {
	Type               string `json:"@type"`
	Format             string `json:"Format"`
	CodecID            string `json:"CodecID"`
	Duration           string `json:"Duration"`
	Width              string `json:"Width"`
	Height             string `json:"Height"`
	Profile            string `json:"Format_Profile"`
	BitDepth           string `json:"BitDepth"`
	HDRFormat          string `json:"HDR_Format"`
	Channels           string `json:"Channel(s)"`
	ChannelLayout      string `json:"ChannelLayout"`
	Language           string `json:"Language"`
	DisplayAspectRatio string `json:"DisplayAspectRatio"`
}

func (i OSMediaInspector) readMediaInfo(ctx context.Context, path string) (mediaInfoDocument, error) {
	binary := i.Binary
	if binary == "" {
		binary = "mediainfo"
	}
	output, err := exec.CommandContext(ctx, binary, "--Output=JSON", path).Output()
	if err != nil {
		return mediaInfoDocument{}, fmt.Errorf("MediaInfo unavailable: %w", err)
	}
	var document mediaInfoDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return mediaInfoDocument{}, fmt.Errorf("MediaInfo JSON invalid: %w", err)
	}
	return document, nil
}

const mediaInfoDiagnostic = "MediaInfo did not provide this fact"

var richMediaFactNames = []string{
	"media.container", "media.duration_seconds", "media.width", "media.height", "media.resolution",
	"media.video_codec", "media.video_profile", "media.video_bit_depth", "media.hdr_format",
	"media.audio_codec", "media.audio_channels", "media.audio_languages", "media.subtitle_languages",
}

func addUnavailableMediaFacts(values map[string]domain.LocalMediaFact, artifactID, version, reason string) {
	for _, name := range richMediaFactNames {
		values[name] = domain.LocalMediaFact{State: "null", Source: "local_media", FactRef: artifactID, DefinitionVersion: version, Diagnostics: []string{reason}}
	}
}

func addMediaInfoFacts(values map[string]domain.LocalMediaFact, document mediaInfoDocument, artifactID, version string) {
	general, video, audio, subtitles := mediaInfoTracks(document.Media.Track)
	setMediaFact(values, "media.container", firstNonEmpty(general.Format, video.Format), artifactID, version, mediaInfoDiagnostic)
	if duration, ok := parseMediaDuration(firstNonEmpty(general.Duration, video.Duration)); ok {
		setMediaFact(values, "media.duration_seconds", duration, artifactID, version, mediaInfoDiagnostic)
	} else {
		setMediaFact(values, "media.duration_seconds", "", artifactID, version, mediaInfoDiagnostic)
	}
	setMediaFact(values, "media.video_codec", firstNonEmpty(video.Format, video.CodecID), artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.video_profile", video.Profile, artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.video_bit_depth", video.BitDepth, artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.hdr_format", video.HDRFormat, artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.audio_codec", firstNonEmpty(audio.Format, audio.CodecID), artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.audio_channels", firstNonEmpty(audio.Channels, audio.ChannelLayout), artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.audio_languages", joinTrackLanguages(audio), artifactID, version, mediaInfoDiagnostic)
	setMediaFact(values, "media.subtitle_languages", joinTrackLanguages(subtitles...), artifactID, version, mediaInfoDiagnostic)
	if width, ok := parseMediaInt(video.Width); ok {
		setMediaFact(values, "media.width", width, artifactID, version, mediaInfoDiagnostic)
	} else {
		setMediaFact(values, "media.width", "", artifactID, version, mediaInfoDiagnostic)
	}
	if height, ok := parseMediaInt(video.Height); ok {
		setMediaFact(values, "media.height", height, artifactID, version, mediaInfoDiagnostic)
		setMediaFact(values, "media.resolution", deriveResolution(height), artifactID, version, mediaInfoDiagnostic)
	} else {
		setMediaFact(values, "media.height", "", artifactID, version, mediaInfoDiagnostic)
		setMediaFact(values, "media.resolution", "", artifactID, version, mediaInfoDiagnostic)
	}
}

func mediaInfoTracks(tracks []mediaInfoTrack) (general, video, audio mediaInfoTrack, subtitles []mediaInfoTrack) {
	for _, track := range tracks {
		switch strings.ToLower(track.Type) {
		case "general":
			if general.Type == "" {
				general = track
			}
		case "video":
			if video.Type == "" {
				video = track
			}
		case "audio":
			if audio.Type == "" {
				audio = track
			}
		case "text":
			subtitles = append(subtitles, track)
		}
	}
	return general, video, audio, subtitles
}

func setMediaFact(values map[string]domain.LocalMediaFact, name string, value any, artifactID, version, diagnostic string) {
	fact := domain.LocalMediaFact{State: "null", Source: "local_media", FactRef: artifactID, DefinitionVersion: version}
	if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
		fact.State = "value"
		fact.Value = value
	} else {
		fact.Diagnostics = []string{diagnostic}
	}
	values[name] = fact
}

func parseMediaInt(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil && parsed >= 0
}

func parseMediaDuration(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		return seconds, true
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, hourErr := strconv.ParseFloat(parts[0], 64)
	minutes, minuteErr := strconv.ParseFloat(parts[1], 64)
	seconds, secondErr := strconv.ParseFloat(parts[2], 64)
	if hourErr != nil || minuteErr != nil || secondErr != nil || hours < 0 || minutes < 0 || seconds < 0 {
		return 0, false
	}
	return hours*3600 + minutes*60 + seconds, true
}

func deriveResolution(height int64) string {
	switch {
	case height >= 2000:
		return "2160p"
	case height >= 1300:
		return "1440p"
	case height >= 1000:
		return "1080p"
	case height >= 650:
		return "720p"
	case height >= 500:
		return "576p"
	case height > 0:
		return "480p"
	default:
		return ""
	}
}

func joinTrackLanguages(tracks ...mediaInfoTrack) string {
	seen := make(map[string]struct{})
	ordered := make([]string, 0, len(tracks))
	for _, track := range tracks {
		for _, language := range strings.Split(track.Language, ",") {
			language = strings.TrimSpace(language)
			if language == "" {
				continue
			}
			if _, exists := seen[language]; exists {
				continue
			}
			seen[language] = struct{}{}
			ordered = append(ordered, language)
		}
	}
	return strings.Join(ordered, ",")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
