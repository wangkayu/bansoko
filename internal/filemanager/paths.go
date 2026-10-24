package filemanager

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxAbsolutePathBytes bounds one browsed absolute path.
	maxAbsolutePathBytes = 4096
	// MaxScanEntries bounds one directory read: a larger directory is
	// reported truncated and only its first entries are sorted and paged.
	MaxScanEntries = 10000
)

// cleanAbsolute validates and cleans an absolute path without touching the
// filesystem: NUL, invalid UTF-8 and over-long input are refused, and the
// result is Clean'ed, so ".." segments can only ever move the path toward the
// filesystem root.
func cleanAbsolute(value string) (string, bool) {
	if len(value) > maxAbsolutePathBytes || strings.IndexByte(value, 0) >= 0 || !utf8.ValidString(value) {
		return "", false
	}
	if !filepath.IsAbs(value) {
		return "", false
	}
	return filepath.Clean(value), true
}

// resolveMount resolves a configured mount to the symlink-resolved directory
// it points at. The mount's own path is trusted (it is either the filesystem
// root, a download connection's validated application root, or a work's
// persisted target directory); everything below it is checked component by
// component.
func resolveMount(config MountConfig) Mount {
	mount := Mount{MountConfig: config}
	cleaned, ok := cleanAbsolute(strings.TrimSpace(config.Path))
	if !ok {
		mount.Unavailable = MountNotConfigured
		return mount
	}
	canonical, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		mount.Unavailable = MountMissing
		return mount
	}
	info, err := os.Stat(canonical)
	if err != nil {
		mount.Unavailable = MountMissing
		return mount
	}
	if !info.IsDir() {
		mount.Unavailable = MountNotDirectory
		return mount
	}
	mount.Available = true
	mount.canonical = canonical
	return mount
}

// splitAbsolute splits a Clean'ed absolute path into its segment names.
func splitAbsolute(cleaned string) []string {
	volume := filepath.VolumeName(cleaned)
	rest := strings.TrimPrefix(cleaned[len(volume):], string(filepath.Separator))
	if rest == "" {
		return nil
	}
	return strings.Split(rest, string(filepath.Separator))
}

// walkAbsolute resolves an absolute path with Lstat, one component at a time
// from the filesystem root, and refuses any symlink component: a browse never
// follows a symbolic link. When allowFinalSymlink is set the last component
// may itself be a symlink (it is described, never followed, so nothing below
// it is ever read).
//
// The returned path is the cleaned absolute path as walked. Owner facts are
// looked up through absoluteSpellings instead, because a deployment may reach
// the same directory under more than one spelling.
func walkAbsolute(value string, allowFinalSymlink bool) (string, fs.FileInfo, error) {
	cleaned, ok := cleanAbsolute(value)
	if !ok {
		return "", nil, invalid(CodePathInvalid, "path must be an absolute path")
	}
	current := string(filepath.Separator)
	info, err := os.Lstat(current)
	if err != nil {
		return "", nil, fsError(err, cleaned)
	}
	segments := splitAbsolute(cleaned)
	for index, segment := range segments {
		if !info.IsDir() {
			return "", nil, notFound(CodePathNotFound, "not a directory: "+current)
		}
		current = filepath.Join(current, segment)
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, fsError(err, cleaned)
		}
		last := index == len(segments)-1
		if info.Mode()&fs.ModeSymlink != 0 && !(last && allowFinalSymlink) {
			return "", nil, invalid(CodeSymlinkNotFollowed, "path passes through a symbolic link: "+current)
		}
	}
	return current, info, nil
}

// refuseDenied fails closed when a path reaches the denial zone. A path that
// is not a usable absolute path is refused as invalid before the zone is
// consulted; the zone is then checked on the browsed spelling and, when the
// path resolves, on the resolved one too, so a denial prefix cannot be reached
// through a second name for it.
func refuseDenied(zone DenialZone, absolute string) error {
	cleaned, ok := cleanAbsolute(absolute)
	if !ok {
		return invalid(CodePathInvalid, "path must be an absolute path")
	}
	if zone.Denies(cleaned) {
		return invalid(CodePathDenied, "path is inside a directory the file manager does not browse: "+absolute)
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && zone.Denies(resolved) {
		return invalid(CodePathDenied, "path is inside a directory the file manager does not browse: "+absolute)
	}
	return nil
}

// absoluteSpellings returns the spellings owner rows may use for one browsed
// absolute path: the browsed path itself and, when the path resolves, its
// symlink-resolved spelling. EvalSymlinks is used for comparison only — a
// browse itself never follows a link.
func absoluteSpellings(absolute string) []string {
	spellings := []string{absolute}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved == absolute {
		return spellings
	}
	return append(spellings, resolved)
}

func fsError(err error, path string) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return notFound(CodePathNotFound, "path does not exist: "+path)
	case errors.Is(err, fs.ErrPermission):
		return conflict(CodePathUnreadable, "path cannot be read: "+path)
	default:
		return err
	}
}

// rawEntry is one Lstat'ed directory entry.
type rawEntry struct {
	name    string
	kind    EntryKind
	size    *int64
	modTime *time.Time
}

// readDirectory reads at most MaxScanEntries entries of a directory that was
// resolved by walkAbsolute. The opened handle is checked to be the same
// directory the walk saw, so a component swapped for a symlink between the
// walk and the open is refused instead of followed.
func readDirectory(directory string, walked fs.FileInfo) ([]rawEntry, bool, error) {
	if !walked.IsDir() {
		return nil, false, invalid(CodeNotADirectory, "path is not a directory: "+directory)
	}
	handle, err := os.Open(directory)
	if err != nil {
		return nil, false, fsError(err, directory)
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil {
		return nil, false, fsError(err, directory)
	}
	if !os.SameFile(opened, walked) {
		return nil, false, invalid(CodeSymlinkNotFollowed, "path changed while it was read: "+directory)
	}
	dirents, err := handle.ReadDir(MaxScanEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fsError(err, directory)
	}
	truncated := len(dirents) > MaxScanEntries
	if truncated {
		dirents = dirents[:MaxScanEntries]
	}
	entries := make([]rawEntry, 0, len(dirents))
	for _, dirent := range dirents {
		name := dirent.Name()
		if !utf8.ValidString(name) {
			// A name that cannot round-trip through a path is not browsable;
			// it is skipped rather than shown mangled.
			continue
		}
		info, infoErr := dirent.Info()
		if infoErr != nil {
			// The entry vanished between the read and the Lstat.
			continue
		}
		entries = append(entries, describe(name, info))
	}
	return entries, truncated, nil
}

func describe(name string, info fs.FileInfo) rawEntry {
	entry := rawEntry{name: name, kind: kindOf(info)}
	modified := info.ModTime().UTC()
	entry.modTime = &modified
	if entry.kind == KindFile {
		size := info.Size()
		entry.size = &size
	}
	return entry
}

func kindOf(info fs.FileInfo) EntryKind {
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return KindSymlink
	case mode.IsDir():
		return KindDirectory
	case mode.IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

// joinAbsolute joins a browsed directory and a child name.
func joinAbsolute(directory, name string) string {
	if directory == string(filepath.Separator) {
		return string(filepath.Separator) + name
	}
	return directory + string(filepath.Separator) + name
}

// containsPath reports whether an absolute path lies inside a directory.
func containsPath(directory, absolute string) bool {
	return absolute == directory || strings.HasPrefix(absolute, directory+string(filepath.Separator))
}

// directoryExists reports whether an absolute path currently is a directory,
// without following a symlink.
func directoryExists(absolute string) bool {
	_, info, err := walkAbsolute(absolute, false)
	return err == nil && info.IsDir()
}

// regularFileExists reports whether an absolute path currently is a regular
// file, without following a symlink.
func regularFileExists(absolute string) bool {
	_, info, err := walkAbsolute(absolute, false)
	return err == nil && info.Mode().IsRegular()
}
