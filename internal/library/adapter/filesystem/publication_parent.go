package filesystem

import (
	"errors"
	"path/filepath"
	"strings"
)

// EnsurePublicationParent creates the missing parent directories of a typed
// publication target (B5b). The caller invokes it only after the attempt
// grant and before preparation. Every component is walked with
// openat/mkdirat and O_NOFOLLOW|O_DIRECTORY: a symlink or a non-directory
// anywhere on the path is refused. Targets are absolute, so an empty root
// walks from the filesystem root; a non-empty root bounds the walk to that
// directory (used by the file-manager executor). Directories are created 0755
// (subject to umask). A directory created by an attempt that later fails stays
// in place: an empty directory is harmless and is never removed.
func (OSFileExecutor) EnsurePublicationParent(root, target string) error {
	if !filepath.IsAbs(target) || root != "" && !filepath.IsAbs(root) {
		return errors.New("publication parent requires an absolute target")
	}
	parent := filepath.Dir(filepath.Clean(target))
	if root != "" {
		rel, err := filepath.Rel(filepath.Clean(root), parent)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("publication parent escapes the frozen publication root")
		}
	}
	rootForWalk := root
	if rootForWalk != "" {
		rootForWalk = filepath.Clean(rootForWalk)
	}
	dir, err := openVerifiedDirectory(rootForWalk, parent, true)
	if err != nil {
		return err
	}
	return dir.Close()
}
