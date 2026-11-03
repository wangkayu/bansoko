package filesystem

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transient names bansoko creates beside a target and removes before the action
// ends. They carry no durable registration, so one left behind by an
// interrupted process is residue nobody will revisit. Staging and quarantine
// names (".bansoko-overwrite-", ".bansoko-quarantine-") are deliberately absent:
// the database checkpoint that created them owns their removal. A cleanup
// namespace (".bansoko-cleanup-<owner>") is revisited only when it is empty: its
// captured object is the database's, but the directory itself has no other
// remover once the object is gone.
const (
	transientCopyPrefix      = ".bansoko-copy-"
	transientPlacementPrefix = ".bansoko-placement-"
	transientPermitPrefix    = ".bansoko-permit-"
	transientLinkProbePrefix = ".bansoko-link-probe-"
	cleanupNamespacePrefix   = ".bansoko-cleanup-"
)

// TransientResidueSweepOptions bounds one sweep of already-known directories.
type TransientResidueSweepOptions struct {
	// ModifiedBefore is the cutoff: an entry last modified at or after it may
	// belong to an action still running, so it is left alone. The caller
	// passes the background start time minus a margin.
	ModifiedBefore time.Time
	// MaxDirectories and MaxEntriesPerDirectory keep one pass bounded on a
	// large library.
	MaxDirectories         int
	MaxEntriesPerDirectory int
}

// TransientResidueSweep reports what one bounded pass saw and removed.
type TransientResidueSweep struct {
	Directories int
	Entries     int
	Removed     int
	Failed      int
	Truncated   bool
}

// SweepTransientResidue removes abandoned transient files and empty delete
// namespaces from the given directories, one level deep. Only regular files
// whose whole name matches a transient shape, and empty service-owned 0700
// cleanup namespace directories, are removed; other directories, symlinks,
// unknown names, and entries touched at or after the cutoff are left in place.
// A missing directory is not an error. The returned error reports only
// cancellation.
func SweepTransientResidue(ctx context.Context, dirs []string, opts TransientResidueSweepOptions) (TransientResidueSweep, error) {
	var report TransientResidueSweep
	if opts.ModifiedBefore.IsZero() {
		return report, errors.New("transient residue sweep requires a modification cutoff")
	}
	if opts.MaxDirectories <= 0 || opts.MaxEntriesPerDirectory <= 0 {
		return report, errors.New("transient residue sweep bounds must be positive")
	}
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		if dir == string(filepath.Separator) {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		if report.Directories >= opts.MaxDirectories {
			report.Truncated = true
			break
		}
		seen[dir] = struct{}{}
		opened, err := sweepTransientResidueDir(ctx, dir, opts, &report)
		if err != nil {
			return report, err
		}
		if opened {
			report.Directories++
		}
	}
	return report, nil
}

func sweepTransientResidueDir(ctx context.Context, dir string, opts TransientResidueSweepOptions, report *TransientResidueSweep) (bool, error) {
	handle, err := os.Open(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			report.Failed++
		}
		return false, nil
	}
	defer handle.Close()
	scanned := 0
	for {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		entries, readErr := handle.ReadDir(256)
		for _, entry := range entries {
			if scanned >= opts.MaxEntriesPerDirectory {
				report.Truncated = true
				return true, nil
			}
			scanned++
			report.Entries++
			if strings.HasPrefix(entry.Name(), cleanupNamespacePrefix) {
				removed, sweepErr := sweepAbandonedCleanupNamespace(dir, entry.Name(), opts.ModifiedBefore)
				if removed {
					report.Removed++
				} else if sweepErr != nil {
					report.Failed++
				}
				continue
			}
			if !isTransientResidueName(entry.Name()) {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				report.Failed++
				continue
			}
			if !info.Mode().IsRegular() || !info.ModTime().Before(opts.ModifiedBefore) {
				continue
			}
			if removeErr := os.Remove(filepath.Join(dir, entry.Name())); removeErr != nil {
				if !errors.Is(removeErr, os.ErrNotExist) {
					report.Failed++
				}
				continue
			}
			report.Removed++
		}
		if errors.Is(readErr, io.EOF) {
			return true, nil
		}
		if readErr != nil {
			report.Failed++
			return true, nil
		}
	}
}

// isTransientResidueName accepts the exact shapes the adapter writes: three
// prefixes followed by a 64-character lowercase hex digest, and the hardlink
// probe followed by an 8-byte lowercase hex value, optionally with its ".link"
// second name.
func isTransientResidueName(name string) bool {
	for _, prefix := range []string{transientCopyPrefix, transientPlacementPrefix, transientPermitPrefix} {
		if strings.HasPrefix(name, prefix) {
			return isLowerHex(strings.TrimPrefix(name, prefix), 64)
		}
	}
	if !strings.HasPrefix(name, transientLinkProbePrefix) {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, transientLinkProbePrefix), ".link")
	return isLowerHex(digest, 16)
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
