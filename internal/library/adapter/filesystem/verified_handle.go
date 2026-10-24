package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

// verifiedDirectory is a directory descriptor obtained by walking a trusted
// anchor one component at a time with no-follow semantics. It remains open
// until the caller has completed the mutation and the corresponding fsync.
type verifiedDirectory struct {
	file      *os.File
	identity  domain.FileIdentity
	canonical string
}

type verifiedEntry struct {
	parent    *verifiedDirectory
	name      string
	canonical string
}

func (directory *verifiedDirectory) Close() error {
	if directory == nil || directory.file == nil {
		return nil
	}
	file := directory.file
	directory.file = nil
	return file.Close()
}

func (directory *verifiedDirectory) Sync() error {
	if directory == nil || directory.file == nil {
		return errors.New("verified directory is closed")
	}
	// exFAT and some FUSE filesystems return EINVAL for a directory fsync;
	// the entry change itself already happened, so EINVAL is not a failure.
	if err := directory.file.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

func (directory *verifiedDirectory) valid(expected domain.FileIdentity) error {
	if directory == nil || directory.file == nil {
		return errors.New("verified directory handle is closed")
	}
	if !fileidentity.IsPresent(expected) {
		return errors.New("verified directory identity is required")
	}
	if !fileidentity.Matches(directory.identity, expected) {
		return errors.New("verified directory identity changed")
	}
	return nil
}

func (entry verifiedEntry) valid() error {
	if entry.parent == nil {
		return errors.New("verified entry has no parent directory")
	}
	if entry.name == "" || entry.name == "." || entry.name == ".." || filepath.Base(entry.name) != entry.name {
		return fmt.Errorf("invalid verified entry basename %q", entry.name)
	}
	return nil
}

func openVerifiedEntry(root, path string, create bool, expectedParent domain.FileIdentity) (verifiedEntry, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return verifiedEntry{}, errors.New("verified entry path must be absolute")
	}
	parent, err := openVerifiedDirectory(root, filepath.Dir(clean), create)
	if err != nil {
		return verifiedEntry{}, err
	}
	if err := parent.valid(expectedParent); err != nil {
		_ = parent.Close()
		return verifiedEntry{}, err
	}
	return verifiedEntry{parent: parent, name: filepath.Base(clean), canonical: clean}, nil
}

func openVerifiedEntries(sourceRoot, targetRoot, source, target string, create bool, sourceParent, targetParent domain.FileIdentity) (verifiedEntry, verifiedEntry, error) {
	left, err := openVerifiedEntry(sourceRoot, source, create, sourceParent)
	if err != nil {
		return verifiedEntry{}, verifiedEntry{}, err
	}
	right, err := openVerifiedEntry(targetRoot, target, create, targetParent)
	if err != nil {
		_ = left.parent.Close()
		return verifiedEntry{}, verifiedEntry{}, err
	}
	return left, right, nil
}

func closeVerifiedEntries(entries ...verifiedEntry) error {
	var closeErr error
	seen := make(map[*verifiedDirectory]struct{}, len(entries))
	for _, entry := range entries {
		if entry.parent == nil {
			continue
		}
		if _, ok := seen[entry.parent]; ok {
			continue
		}
		seen[entry.parent] = struct{}{}
		closeErr = errors.Join(closeErr, entry.parent.Close())
	}
	return closeErr
}

func syncVerifiedEntries(entries ...verifiedEntry) error {
	seen := make(map[*verifiedDirectory]struct{}, len(entries))
	for _, entry := range entries {
		if entry.parent == nil {
			return errors.New("cannot sync a missing verified parent")
		}
		if _, ok := seen[entry.parent]; ok {
			continue
		}
		seen[entry.parent] = struct{}{}
		if err := entry.parent.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func syncAndCloseVerifiedEntries(entries ...verifiedEntry) error {
	syncErr := syncVerifiedEntries(entries...)
	return errors.Join(syncErr, closeVerifiedEntries(entries...))
}

func openMutationEntries(sourceRoot, targetRoot, source, target string, sourceParent, targetParent domain.FileIdentity) (verifiedEntry, verifiedEntry, error) {
	source, err := canonicalMutationPath(source)
	if err != nil {
		return verifiedEntry{}, verifiedEntry{}, err
	}
	target, err = canonicalMutationPath(target)
	if err != nil {
		return verifiedEntry{}, verifiedEntry{}, err
	}
	return openVerifiedEntries(sourceRoot, targetRoot, source, target, false, sourceParent, targetParent)
}

func openMutationEntry(root, path string, expectedParent domain.FileIdentity) (verifiedEntry, error) {
	canonical, err := canonicalMutationPath(path)
	if err != nil {
		return verifiedEntry{}, err
	}
	return openVerifiedEntry(root, canonical, false, expectedParent)
}

func canonicalMutationPath(path string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", errors.New("mutation path must be absolute")
	}
	return clean, nil
}

func safeExchange(targetRoot, left, right string, expectedParent domain.FileIdentity) error {
	leftEntry, rightEntry, err := openMutationEntries(targetRoot, targetRoot, left, right, expectedParent, expectedParent)
	if err != nil {
		return err
	}
	defer closeVerifiedEntries(leftEntry, rightEntry)
	if err := atomicExchangeVerified(leftEntry, rightEntry); err != nil {
		return err
	}
	return syncAndCloseVerifiedEntries(leftEntry, rightEntry)
}

func atomicQuarantine(sourceRoot, targetRoot, path, quarantine string, expectedParent domain.FileIdentity) error {
	if filepath.Dir(filepath.Clean(path)) != filepath.Dir(filepath.Clean(quarantine)) {
		return errors.New("quarantine entries must share a directory")
	}
	source, target, err := openMutationEntries(sourceRoot, targetRoot, path, quarantine, expectedParent, expectedParent)
	if err != nil {
		return err
	}
	defer closeVerifiedEntries(source, target)
	if err := atomicRenameNoReplaceVerified(source, target); err != nil {
		return err
	}
	return syncAndCloseVerifiedEntries(source, target)
}

func safeRenameNoReplace(sourceRoot, targetRoot, source, target string, expectedSourceParent, expectedTargetParent domain.FileIdentity) error {
	left, right, err := openMutationEntries(sourceRoot, targetRoot, source, target, expectedSourceParent, expectedTargetParent)
	if err != nil {
		return err
	}
	defer closeVerifiedEntries(left, right)
	if err := atomicRenameNoReplaceVerified(left, right); err != nil {
		return err
	}
	return syncAndCloseVerifiedEntries(left, right)
}

func safeLink(sourceRoot, targetRoot, source, target string, expectedSourceParent, expectedTargetParent domain.FileIdentity) error {
	left, right, err := openMutationEntries(sourceRoot, targetRoot, source, target, expectedSourceParent, expectedTargetParent)
	if err != nil {
		return err
	}
	defer closeVerifiedEntries(left, right)
	if err := atomicLinkVerified(left, right); err != nil {
		return err
	}
	return syncAndCloseVerifiedEntries(left, right)
}

func createNoReplaceTempHandle(targetRoot, parent string, expectedParent domain.FileIdentity, name string) (*os.File, verifiedEntry, error) {
	canonical, err := canonicalMutationPath(filepath.Join(parent, name))
	if err != nil {
		return nil, verifiedEntry{}, err
	}
	entry, err := openVerifiedEntry(targetRoot, canonical, false, expectedParent)
	if err != nil {
		return nil, verifiedEntry{}, err
	}
	file, err := atomicCreateNoReplaceVerified(entry, name)
	if err != nil {
		_ = entry.parent.Close()
		return nil, verifiedEntry{}, err
	}
	return file, entry, nil
}

func commitTemporaryLink(targetRoot string, temp verifiedEntry, target string, expectedTargetParent domain.FileIdentity) error {
	canonical, err := canonicalMutationPath(target)
	if err != nil {
		return err
	}
	if err := temp.parent.valid(expectedTargetParent); err != nil {
		return err
	}
	targetEntry, err := openVerifiedEntry(targetRoot, canonical, false, expectedTargetParent)
	if err != nil {
		return err
	}
	defer targetEntry.parent.Close()
	if !fileidentity.Matches(targetEntry.parent.identity, temp.parent.identity) {
		return errors.New("temporary and target parents differ")
	}
	if err := atomicRenameNoReplaceVerified(temp, targetEntry); err != nil {
		return err
	}
	return syncVerifiedEntries(temp, targetEntry)
}

func removeVerifiedEntryHandle(entry verifiedEntry, expectedEntry domain.FileIdentity, beforeRename ...func(string) error) (resultErr error) {
	if err := entry.valid(); err != nil {
		return err
	}
	opened, err := openEntryNoFollow(entry)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	info, statErr := opened.Stat()
	identity, identityErr := verifiedEntryIdentity(entry.canonical, opened)
	if statErr != nil {
		return statErr
	}
	if identityErr != nil {
		return identityErr
	}
	if !fileidentity.Matches(identity, expectedEntry) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("remove entry identity changed")}
	}
	frozenIdentity := identity

	quarantineCanonical := quarantinePath(entry.canonical)
	if filepath.Dir(filepath.Clean(quarantineCanonical)) != filepath.Dir(filepath.Clean(entry.canonical)) {
		return errors.New("remove entry quarantine escaped parent directory")
	}
	quarantineName := filepath.Base(quarantineCanonical)
	if quarantineName == entry.name {
		return errors.New("remove entry quarantine basename matches source")
	}
	quarantine := verifiedEntry{parent: entry.parent, name: quarantineName, canonical: quarantineCanonical}
	if len(beforeRename) > 0 && beforeRename[0] != nil {
		if err := beforeRename[0](entry.canonical); err != nil {
			return err
		}
		current, currentErr := openEntryNoFollow(entry)
		if currentErr != nil {
			if errors.Is(currentErr, os.ErrNotExist) {
				return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("remove entry identity changed before rename")}
			}
			return fmt.Errorf("reopen entry after pre-rename hook: %w", currentErr)
		}
		currentIdentity, currentIdentityErr := verifiedEntryIdentity(entry.canonical, current)
		currentCloseErr := current.Close()
		if currentIdentityErr != nil {
			if errors.Is(currentIdentityErr, os.ErrNotExist) {
				return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("remove entry identity changed before rename")}
			}
			return currentIdentityErr
		}
		if currentCloseErr != nil {
			return currentCloseErr
		}
		if !fileidentity.Matches(currentIdentity, frozenIdentity) {
			return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("remove entry identity changed before rename")}
		}
	}
	if err := atomicRenameNoReplaceVerified(entry, quarantine); err != nil {
		return err
	}
	if err := entry.parent.Sync(); err != nil {
		return fmt.Errorf("sync quarantined entry directory: %w", err)
	}

	quarantined, err := openEntryNoFollow(quarantine)
	if err != nil {
		return fmt.Errorf("open quarantined entry: %w", err)
	}
	quarantineInfo, statErr := quarantined.Stat()
	quarantineIdentity, identityErr := verifiedEntryIdentity(quarantine.canonical, quarantined)
	quarantineCloseErr := quarantined.Close()
	if statErr != nil {
		return statErr
	}
	if identityErr != nil {
		return identityErr
	}
	if quarantineCloseErr != nil {
		return quarantineCloseErr
	}
	if !fileidentity.Matches(quarantineIdentity, frozenIdentity) || quarantineInfo.Mode().Type() != info.Mode().Type() {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("quarantined entry identity or type changed")}
	}
	return nil
}

func verifiedEntryIdentity(path string, file *os.File) (domain.FileIdentity, error) {
	identity, err := fileidentity.FromFile(file)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	pathIdentity, err := fileidentity.FromPath(path)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	if !fileidentity.Matches(identity, pathIdentity) {
		return domain.FileIdentity{}, errors.New("verified entry path identity changed")
	}
	return carryIdentityGeneration(identity, pathIdentity), nil
}

func removeIdentityBound(root, path string, expectedParent, expectedEntry domain.FileIdentity) error {
	entry, err := openMutationEntry(root, path, expectedParent)
	if err != nil {
		return err
	}
	defer entry.parent.Close()
	return removeVerifiedEntryHandle(entry, expectedEntry)
}
