//go:build linux || darwin

package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"bansoko/internal/library/domain"
)

// Path-level verification. Every filesystem is treated alike: an
// observation is one no-follow open plus fstat of the final component, it
// accepts only a regular file, and it records size plus device/inode. No
// content is read. Device/inode are compared only inside one invocation.

var errNonRegularObject = errors.New("not a regular file")

// observePath stats the regular file at an absolute path. The final component
// is never followed; an absent entry returns an error wrapping os.ErrNotExist.
// open+fstat is preferred because NFS close-to-open then returns fresh
// attributes; an unreadable file falls back to a no-follow lstat.
func observePath(path string) (domain.PathObservation, error) {
	if !filepath.IsAbs(path) {
		return domain.PathObservation{}, errors.New("observed path must be absolute")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		defer unix.Close(fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return domain.PathObservation{}, &os.PathError{Op: "fstat", Path: path, Err: err}
		}
		return pathObservationOf(path, &st)
	}
	if errors.Is(err, unix.ELOOP) {
		return domain.PathObservation{}, &os.PathError{Op: "open", Path: path, Err: errNonRegularObject}
	}
	if !errors.Is(err, unix.EACCES) {
		return domain.PathObservation{}, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return domain.PathObservation{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return pathObservationOf(path, &st)
}

// observeVerifiedEntry stats an entry relative to a verified parent
// descriptor, so a parent swap after the walk cannot redirect the stat.
func observeVerifiedEntry(entry verifiedEntry) (domain.PathObservation, error) {
	if err := entry.valid(); err != nil {
		return domain.PathObservation{}, err
	}
	parent := int(entry.parent.file.Fd())
	fd, err := unix.Openat(parent, entry.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		defer unix.Close(fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return domain.PathObservation{}, &os.PathError{Op: "fstat", Path: entry.canonical, Err: err}
		}
		return pathObservationOf(entry.canonical, &st)
	}
	if errors.Is(err, unix.ELOOP) {
		return domain.PathObservation{}, &os.PathError{Op: "open", Path: entry.canonical, Err: errNonRegularObject}
	}
	if !errors.Is(err, unix.EACCES) {
		return domain.PathObservation{}, &os.PathError{Op: "open", Path: entry.canonical, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstatat(parent, entry.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return domain.PathObservation{}, &os.PathError{Op: "lstat", Path: entry.canonical, Err: err}
	}
	return pathObservationOf(entry.canonical, &st)
}

func pathObservationOf(path string, st *unix.Stat_t) (domain.PathObservation, error) {
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return domain.PathObservation{}, &os.PathError{Op: "stat", Path: path, Err: errNonRegularObject}
	}
	return domain.PathObservation{SizeBytes: st.Size, Identity: domain.FileIdentity{Device: uint64(st.Dev), Inode: uint64(st.Ino)}, ObservedAt: time.Now().UTC()}, nil
}

// entryAbsent reports whether the entry is absent (lstat ENOENT).
func entryAbsent(entry verifiedEntry) (bool, error) {
	if err := entry.valid(); err != nil {
		return false, err
	}
	var st unix.Stat_t
	err := unix.Fstatat(int(entry.parent.file.Fd()), entry.name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	return false, err
}

func sameObject(a, b domain.PathObservation) bool {
	return a.Identity.Device == b.Identity.Device && a.Identity.Inode == b.Identity.Inode && a.Identity.Inode != 0
}

// ObservePath implements domain.PathObserver.
func (OSFileObserver) ObservePath(ctx context.Context, path string) (domain.PathObservation, error) {
	if err := ctx.Err(); err != nil {
		return domain.PathObservation{}, err
	}
	return observePath(filepath.Clean(path))
}

// SameDevice compares st_dev of the source file's directory with the nearest
// existing ancestor of the target's directory (the target parent may not exist
// before publication creates it).
func (OSFileObserver) SameDevice(ctx context.Context, source, target string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var src unix.Stat_t
	if err := unix.Stat(filepath.Dir(filepath.Clean(source)), &src); err != nil {
		return false, err
	}
	dir := filepath.Dir(filepath.Clean(target))
	for {
		var st unix.Stat_t
		err := unix.Stat(dir, &st)
		if err == nil {
			return uint64(st.Dev) == uint64(src.Dev), nil
		}
		if !errors.Is(err, unix.ENOENT) {
			return false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, err
		}
		dir = parent
	}
}

// publicationSyscalls is the one injectable seam for the publication
// mutations. Production uses the descriptor-relative syscalls below; unit
// tests substitute errno results (EXDEV, EPERM, EINVAL, NFS retransmit false
// errors) without a special filesystem.
type publicationSyscalls struct {
	renameNoReplace func(from, to verifiedEntry) error
	rename          func(from, to verifiedEntry) error
	link            func(from, to verifiedEntry) error
	unlink          func(entry verifiedEntry) error
	create          func(entry verifiedEntry) (*os.File, error)
	// observeTarget, when set, runs before the overwrite target is
	// observed just ahead of its replacing rename; its error is that
	// observation's answer (fault hook).
	observeTarget func(path string) error
}

// withObserveHook runs the executor's PublicationObserveTarget hook before
// the overwrite target observation of the publish step.
func (s publicationSyscalls) withObserveHook(hook func(path string) error) publicationSyscalls {
	s.observeTarget = hook
	return s
}

// observeReplaced observes the overwrite target right before its replacing
// rename, through the fault hook when one is set.
func (s publicationSyscalls) observeReplaced(final verifiedEntry) (domain.PathObservation, error) {
	if s.observeTarget != nil {
		if err := s.observeTarget(final.canonical); err != nil {
			return domain.PathObservation{}, &os.PathError{Op: "open", Path: final.canonical, Err: err}
		}
	}
	return observeVerifiedEntry(final)
}

func defaultPublicationSyscalls() publicationSyscalls {
	return publicationSyscalls{
		renameNoReplace: atomicRenameNoReplaceVerified,
		rename:          atomicRenameReplaceVerified,
		link:            atomicLinkVerified,
		unlink:          atomicUnlinkVerified,
		create:          func(entry verifiedEntry) (*os.File, error) { return atomicCreateNoReplaceVerified(entry, entry.name) },
	}
}

func (s publicationSyscalls) withDefaults() publicationSyscalls {
	d := defaultPublicationSyscalls()
	if s.renameNoReplace == nil {
		s.renameNoReplace = d.renameNoReplace
	}
	if s.rename == nil {
		s.rename = d.rename
	}
	if s.link == nil {
		s.link = d.link
	}
	if s.unlink == nil {
		s.unlink = d.unlink
	}
	if s.create == nil {
		s.create = d.create
	}
	return s
}

// withRenameHook runs the executor's PublicationRename hook before the
// replacing rename; its error is the rename's answer.
func (s publicationSyscalls) withRenameHook(hook func(temp, final string) error) publicationSyscalls {
	if hook == nil {
		return s
	}
	rename := s.rename
	s.rename = func(from, to verifiedEntry) error {
		if err := hook(from.canonical, to.canonical); err != nil {
			return err
		}
		return rename(from, to)
	}
	return s
}

func publicationErr(code string, err error) error {
	return &domain.PublicationError{Code: code, Err: err}
}

func errnoIs(err error, targets ...unix.Errno) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// targetMutationError maps a create/rename/link failure at the target to the
// per-file codes. Unknown errors pass through unchanged.
func targetMutationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errnoIs(err, unix.EEXIST, unix.ENOTEMPTY):
		return publicationErr(domain.FileErrorCodeTargetOccupied, err)
	case errnoIs(err, unix.ENAMETOOLONG):
		return publicationErr(domain.CodeTargetNameTooLong, err)
	case errnoIs(err, unix.EINVAL, unix.EILSEQ):
		return publicationErr(domain.CodeTargetNameInvalid, err)
	case errnoIs(err, unix.EACCES, unix.EPERM, unix.EROFS):
		return publicationErr(domain.CodeTargetPermissionDenied, err)
	}
	return err
}

// renameFlagsUnsupported: renameat2(RENAME_NOREPLACE) is not implemented by
// the filesystem (NFS returns EINVAL, older kernels ENOSYS, SMB on macOS
// ENOTSUP). The ladder then tries link.
func renameFlagsUnsupported(err error) bool {
	return errnoIs(err, unix.EINVAL, unix.ENOSYS, unix.ENOTSUP, unix.EOPNOTSUPP)
}

// linkUnsupported: the filesystem has no hardlinks (SMB without unix
// extensions, exFAT) or refuses another link. The ladder then checks absence
// and renames under the process target lock.
func linkUnsupported(err error) bool {
	return errnoIs(err, unix.EPERM, unix.ENOTSUP, unix.EOPNOTSUPP, unix.EMLINK, unix.ENOSYS)
}

// publishAbsent publishes temp onto an absent final entry (block): the
// absent-target ladder. It never replaces an existing entry. After the
// link rung the temp stays present; temp_cleanup removes it.
func (s publicationSyscalls) publishAbsent(temp, final verifiedEntry, staged domain.PathObservation) error {
	if err := stagedIntact(temp, staged); err != nil {
		return err
	}
	err := s.renameNoReplace(temp, final)
	if err == nil {
		return nil
	}
	if errnoIs(err, unix.EEXIST, unix.ENOENT) && publishLanded(temp, final, staged) {
		return nil
	}
	if !renameFlagsUnsupported(err) {
		return targetMutationError(err)
	}
	err = s.link(temp, final)
	if err == nil {
		return nil
	}
	if errnoIs(err, unix.EEXIST, unix.ENOENT) && linkLanded(final, staged) {
		return nil
	}
	if !linkUnsupported(err) {
		return targetMutationError(err)
	}
	unlock := lockCanonicalTarget(final.canonical)
	defer unlock()
	absent, statErr := entryAbsent(final)
	if statErr != nil {
		return targetMutationError(statErr)
	}
	if !absent {
		return publicationErr(domain.FileErrorCodeTargetOccupied, errors.New("publication target appeared before rename"))
	}
	if err := s.rename(temp, final); err != nil {
		if errnoIs(err, unix.ENOENT) && publishLanded(temp, final, staged) {
			return nil
		}
		return targetMutationError(err)
	}
	return nil
}

// publishReplace renames temp over an existing final entry that must still be
// the expected object: regular with the expected size, and for a reservation
// made in this invocation also the same device/inode. It returns the replaced
// object's observation. rename replaces atomically on POSIX, NFS and SMB.
func (s publicationSyscalls) publishReplace(temp, final verifiedEntry, staged, expected domain.PathObservation, sameInvocation bool) (domain.PathObservation, error) {
	if err := stagedIntact(temp, staged); err != nil {
		return domain.PathObservation{}, err
	}
	current, err := s.observeReplaced(final)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.PathObservation{}, publicationErr(domain.FileErrorCodeTargetOccupied, errors.New("replaced target is no longer present"))
		}
		// An overwrite target held open exclusively elsewhere refuses even
		// this observation; nothing was replaced, so it settles like a
		// refused rename (temp removed, busy).
		if !sameInvocation && domain.FileBusy(busyObservationError(err)) {
			return domain.PathObservation{}, s.overwriteRefused(temp, staged, err)
		}
		return domain.PathObservation{}, publicationErr(domain.CodeTargetUnreadable, err)
	}
	if current.SizeBytes != expected.SizeBytes || (sameInvocation && !sameObject(current, expected)) {
		return domain.PathObservation{}, publicationErr(domain.FileErrorCodeTargetOccupied, errors.New("replaced target changed since it was frozen"))
	}
	if err := s.rename(temp, final); err != nil {
		// A replacing rename may report an error although it landed: an
		// NFSv3 retransmit (ENOENT/EEXIST) or an SMB reply lost after the
		// server applied it (EIO). The staged object at the target decides.
		if errnoIs(err, unix.ENOENT, unix.EEXIST, unix.EIO) && publishLanded(temp, final, staged) {
			return current, nil
		}
		if !sameInvocation {
			return domain.PathObservation{}, s.overwriteRefused(temp, staged, err)
		}
		return domain.PathObservation{}, targetMutationError(err)
	}
	return current, nil
}

// overwriteRefused settles an overwrite rename that did not land: the target
// was not replaced. When temp is still the object this invocation staged, it
// is removed here, so the refused publication leaves nothing behind. A rename
// refused because the target is busy (held open over SMB/CIFS) or the change
// is not permitted is TARGET_BUSY; with the temp removed its error carries
// domain.ErrFileBusy, so the caller may retry the whole publication later. A
// temp that is no longer provably ours, or whose removal fails, stays in place
// and the error is terminal.
func (s publicationSyscalls) overwriteRefused(temp verifiedEntry, staged domain.PathObservation, cause error) error {
	busy := errnoIs(cause, busyEffectErrnos...)
	refused := targetMutationError(cause)
	if busy {
		refused = publicationErr(domain.CodeTargetBusy, cause)
	}
	if stagedIntact(temp, staged) != nil {
		return refused
	}
	if err := s.unlink(temp); err != nil {
		return errors.Join(refused, fmt.Errorf("remove staged publication object: %w", err))
	}
	if busy {
		return publicationErr(domain.CodeTargetBusy, busyEffectError(cause))
	}
	return refused
}

// stagedIntact refuses to publish a temp entry that is no longer the object
// this invocation staged (same device/inode and size). A foreign object at the
// permit temp is left in place and the effect stays unknown.
func stagedIntact(temp verifiedEntry, staged domain.PathObservation) error {
	current, err := observeVerifiedEntry(temp)
	if err != nil {
		return fmt.Errorf("staged publication object unavailable: %w", err)
	}
	if current.SizeBytes != staged.SizeBytes || !sameObject(current, staged) {
		return errors.New("staged publication object was replaced; left in place")
	}
	return nil
}

// publishLanded recognises a rename that succeeded although the call reported
// an error (an NFSv3 non-idempotent retransmit): temp absent and final holding
// the staged object at the staged size.
func publishLanded(temp, final verifiedEntry, staged domain.PathObservation) bool {
	absent, err := entryAbsent(temp)
	if err != nil || !absent {
		return false
	}
	current, err := observeVerifiedEntry(final)
	return err == nil && current.SizeBytes == staged.SizeBytes && sameObject(current, staged)
}

// linkLanded recognises a link that succeeded although the call reported an
// error: final is the same object (same device/inode) at the same size.
func linkLanded(final verifiedEntry, linked domain.PathObservation) bool {
	current, err := observeVerifiedEntry(final)
	return err == nil && current.SizeBytes == linked.SizeBytes && sameObject(current, linked)
}

// hardlinkProbeCache remembers, per target directory, whether the filesystem
// accepts a link of a file the app itself created (true) or refuses links
// outright (false). It lives for the process lifetime.
var hardlinkProbeCache sync.Map

// linkSource creates final as a hardlink of source. Errors map to the typed
// hardlink codes. There is no fallback to copy or move, ever.
func (s publicationSyscalls) linkSource(source, final verifiedEntry, sourceObject domain.PathObservation) error {
	err := s.link(source, final)
	if err == nil {
		return nil
	}
	if errnoIs(err, unix.EEXIST, unix.ENOENT) && linkLanded(final, sourceObject) {
		return nil
	}
	return s.hardlinkError(err, final)
}

func (s publicationSyscalls) hardlinkError(err error, final verifiedEntry) error {
	switch {
	case errnoIs(err, unix.EXDEV):
		return publicationErr(domain.CodeHardlinkCrossDevice, err)
	case errnoIs(err, unix.EMLINK):
		return publicationErr(domain.CodeHardlinkLimit, err)
	case errnoIs(err, unix.ENOTSUP, unix.EOPNOTSUPP, unix.ENOSYS):
		return publicationErr(domain.CodeHardlinkUnsupported, err)
	case errnoIs(err, unix.EPERM, unix.EACCES):
		supported, probeErr := s.probeHardlinks(final.parent)
		if probeErr != nil {
			return probeErr
		}
		if supported {
			return publicationErr(domain.CodeHardlinkNotPermitted, err)
		}
		return publicationErr(domain.CodeHardlinkUnsupported, err)
	case errnoIs(err, unix.ENOENT):
		return err
	}
	return targetMutationError(err)
}

// probeHardlinks links a file the app itself created, in the target
// directory, to tell an unsupported filesystem from a refused link. Both probe
// entries are removed before returning; the result is cached per directory.
func (s publicationSyscalls) probeHardlinks(dir *verifiedDirectory) (bool, error) {
	if cached, ok := hardlinkProbeCache.Load(dir.canonical); ok {
		return cached.(bool), nil
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return false, err
	}
	name := ".bansoko-link-probe-" + hex.EncodeToString(raw[:])
	first := verifiedEntry{parent: dir, name: name, canonical: filepath.Join(dir.canonical, name)}
	second := verifiedEntry{parent: dir, name: name + ".link", canonical: filepath.Join(dir.canonical, name+".link")}
	file, err := s.create(first)
	if err != nil {
		if errnoIs(err, unix.EACCES, unix.EPERM, unix.EROFS) {
			return false, publicationErr(domain.CodeTargetPermissionDenied, err)
		}
		return false, err
	}
	_ = file.Close()
	linkErr := s.link(first, second)
	cleanup := s.unlink(first)
	if linkErr == nil {
		cleanup = errors.Join(cleanup, s.unlink(second))
	}
	if cleanup != nil {
		return false, fmt.Errorf("remove hardlink probe: %w", cleanup)
	}
	supported := linkErr == nil
	if !supported && !errnoIs(linkErr, unix.EPERM, unix.ENOTSUP, unix.EOPNOTSUPP, unix.ENOSYS) {
		// Not a capability answer; do not cache.
		return false, linkErr
	}
	hardlinkProbeCache.Store(dir.canonical, supported)
	return supported, nil
}

// removePublicationObject removes one publication object (the staged temp or
// a moved source) by path and size. An absent entry is the converged end
// state. A present entry must be a regular file of the expected size, and, when
// expected comes from this invocation, the same device/inode; anything else is
// left in place and reported.
func (s publicationSyscalls) removePublicationObject(ctx context.Context, path string, expected domain.PathObservation, sameInvocation bool, parent domain.FileIdentity, before func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := openMutationEntry("", path, parent)
	if err != nil {
		return err
	}
	defer entry.parent.Close()
	current, err := observeVerifiedEntry(entry)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.SizeBytes != expected.SizeBytes || (sameInvocation && !sameObject(current, expected)) {
		return publicationErr(domain.CodeSourceChanged, fmt.Errorf("cleanup object at %s changed; left in place", filepath.Base(path)))
	}
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	if err := s.unlink(entry); err != nil && !errnoIs(err, unix.ENOENT) {
		if errnoIs(err, unix.EACCES, unix.EPERM, unix.EROFS) {
			return publicationErr(domain.CodeTargetPermissionDenied, err)
		}
		return err
	}
	if err := entry.parent.Sync(); err != nil {
		return err
	}
	absent, err := entryAbsent(entry)
	if err != nil {
		return err
	}
	if !absent {
		return errors.New("removed publication entry was reoccupied")
	}
	return nil
}

func sourceOpenError(err error) error {
	if errnoIs(err, unix.EACCES, unix.EPERM) {
		return publicationErr(domain.CodeSourcePermissionDenied, err)
	}
	return err
}
