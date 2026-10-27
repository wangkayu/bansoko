//go:build linux || darwin

package filesystem

import (
	"bansoko/internal/library/domain"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

// The owner namespace is private to the service uid. Public basename races
// are resolved by capture first, verification second, never by precheck unlink.
// A mismatched capture stays intact as conflict evidence; no restore overwrites
// the public source. Same-uid hostile access is outside this isolation boundary.
type cleanupMutationProbe func(string) error

func cleanupProbe(probes []cleanupMutationProbe, point string) error {
	for _, probe := range probes {
		if probe != nil {
			if err := probe(point); err != nil {
				return err
			}
		}
	}
	return nil
}
func cleanupCaptureDirectory(root, path string, create bool, probes ...cleanupMutationProbe) (*verifiedDirectory, error) {
	dir := filepath.Dir(path)
	if filepath.Base(path) != "object" || !strings.HasPrefix(filepath.Base(dir), ".bansoko-cleanup-") {
		return nil, &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("cleanup requires owner private namespace")}
	}
	parent, err := openVerifiedDirectory(root, filepath.Dir(dir), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if create {
		if err := cleanupProbe(probes, "before_namespace_create"); err != nil {
			return nil, err
		}
		if err := unix.Mkdirat(int(parent.file.Fd()), filepath.Base(dir), 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, busyEffectError(err)
		}
		if err := cleanupProbe(probes, "namespace_created"); err != nil {
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			return nil, err
		}
		if err := cleanupProbe(probes, "namespace_synced"); err != nil {
			return nil, err
		}
	}
	handle, err := openVerifiedDirectory(root, dir, false)
	if err != nil {
		return nil, err
	}
	st, err := handle.file.Stat()
	if err != nil {
		handle.Close()
		return nil, err
	}
	var stat unix.Stat_t
	statErr := unix.Fstat(int(handle.file.Fd()), &stat)
	if statErr != nil || stat.Uid != uint32(os.Geteuid()) || st.Mode().Perm() != 0700 {
		handle.Close()
		return nil, errors.New("cleanup namespace is not service-owned private directory")
	}
	return handle, nil
}
func captureCleanupObject(root, source, target string, expected domain.FileObservation, beforeCapture func() error, probes ...cleanupMutationProbe) (string, error) {
	private, err := cleanupCaptureDirectory(root, target, true, probes...)
	if err != nil {
		return "", err
	}
	defer private.Close()
	sourceParent, err := openVerifiedDirectory(root, filepath.Dir(source), false)
	if err != nil {
		return "", err
	}
	defer sourceParent.Close()
	from := verifiedEntry{parent: sourceParent, name: filepath.Base(source), canonical: source}
	to := verifiedEntry{parent: private, name: "object", canonical: target}
	if beforeCapture != nil {
		if err := beforeCapture(); err != nil {
			return "", err
		}
	}
	if err := directoryPathMatches(private); err != nil {
		return "", err
	}
	if err := directoryPathMatches(sourceParent); err != nil {
		return "", err
	}
	if err := cleanupProbe(probes, "before_object_capture"); err != nil {
		return "", err
	}
	if err := cleanupEffects.rename(from, to); err != nil {
		return "", busyEffectError(err)
	}
	if err := cleanupProbe(probes, "object_captured"); err != nil {
		return "", err
	}
	if err := sourceParent.Sync(); err != nil {
		return "", err
	}
	if err := cleanupProbe(probes, "source_parent_synced"); err != nil {
		return "", err
	}
	if err := private.Sync(); err != nil {
		return "", err
	}
	if err := cleanupProbe(probes, "capture_parent_synced"); err != nil {
		return "", err
	}
	captured, err := observeFileAs(target, expected.Fingerprint)
	if err != nil || !sameMutationFileObservation(captured, expected) {
		return "", &OperationError{Code: CodeRecoveryUnsafe, Err: fmt.Errorf("captured foreign object retained at %s", target)}
	}
	if err := directoryPathMatches(private); err != nil {
		return "", err
	}
	if err := directoryPathMatches(sourceParent); err != nil {
		return "", err
	}
	return target, nil
}
func purgeCleanupObject(root, path string, expected domain.FileObservation, probes ...cleanupMutationProbe) error {
	private, err := cleanupCaptureDirectory(root, path, false)
	if err != nil {
		return err
	}
	defer private.Close()
	observed, err := observeFileAs(path, expected.Fingerprint)
	if err != nil {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: err}
	}
	if !sameMutationFileObservation(observed, expected) {
		return &OperationError{Code: CodeRecoveryUnsafe, Err: errors.New("private quarantine object changed")}
	}
	entry := verifiedEntry{parent: private, name: "object", canonical: path}
	if err := directoryPathMatches(private); err != nil {
		return err
	}
	if err := cleanupProbe(probes, "before_object_purge"); err != nil {
		return err
	}
	if err := cleanupEffects.unlink(entry); err != nil {
		return busyEffectError(err)
	}
	if err := cleanupProbe(probes, "object_purged"); err != nil {
		return err
	}
	if err := directoryPathMatches(private); err != nil {
		return err
	}
	if err := private.Sync(); err != nil {
		return err
	}
	return cleanupProbe(probes, "purge_parent_synced")
}
