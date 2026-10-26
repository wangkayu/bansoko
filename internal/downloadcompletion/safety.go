package downloadcompletion

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// remove_with_data safety boundary.
//
// The product never deletes a file itself. remove_with_data asks qBittorrent
// to delete the task's own files, addressed by infohash only, and this file
// proves beforehand that the request cannot reach a media-library file.
//
// qBittorrent deletes exactly the files in the task's file list and then the
// directories that became empty. A library file can therefore only be lost if
// its path equals one of the task's file paths, or lies inside the task's
// content directory. CheckRemoveWithData refuses (fail closed, typed code)
// unless it can show that neither happens, comparing every path in its
// lexical form and its symlink-resolved form, and refuses whenever a path
// cannot be resolved. Hard links and copies in the library are different
// directory entries; removing the download-side name does not touch them.

// Typed refusal codes.
const (
	CodeQBTaskMismatch         = "qb_task_mismatch"
	CodeFileListInvalid        = "file_list_invalid"
	CodeContentUnresolved      = "content_unresolved"
	CodeContentOutsideRoot     = "content_outside_root"
	CodePathUnresolvable       = "path_unresolvable"
	CodePlacementInsideContent = "placement_inside_content"
	CodePlacementPathInvalid   = "placement_path_invalid"
	// CodeContentSharedWithOther means another qBittorrent task's files or
	// content folder overlap this task's (cross-seeding, a shared save path):
	// deleting this task's files would delete files that task still uses.
	CodeContentSharedWithOther = "content_shared_with_other_task"
	// CodeOtherTasksUnreadable means the other qBittorrent tasks could not be
	// listed, or one of them has no usable content location.
	CodeOtherTasksUnreadable = "other_tasks_unreadable"
)

// SafetyError is a typed remove_with_data refusal.
type SafetyError struct {
	Code   string
	Detail string
}

func (e *SafetyError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func refuse(code, format string, args ...any) error {
	return &SafetyError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Torrent is what qBittorrent reports about one task addressed by infohash.
// FileNames are the task's complete file list (including incomplete files),
// relative to SavePath exactly as qBittorrent names them.
type Torrent struct {
	Hash        string
	Name        string
	State       string
	SavePath    string
	ContentPath string
	FileNames   []string
}

// SafetyInput is everything the remove_with_data check reads.
type SafetyInput struct {
	// Hash is the frozen infohash of the completion row.
	Hash string
	// Torrent is the qBittorrent task just fetched by that hash.
	Torrent Torrent
	// RemoteRoot and ApplicationRoot are the frozen path roots: qBittorrent's
	// save root and the same tree as this process sees it.
	RemoteRoot      string
	ApplicationRoot string
	// OtherTasks lists every other qBittorrent task. A task whose content path
	// equals its save path carries its complete file list. Required: without it
	// "only this task's own content" cannot be shown and the check refuses.
	OtherTasks func() ([]Torrent, error)
	// EachLibraryPath yields every path a library file may live at: active
	// placements and the targets of file operations that wrote (or may still
	// write) a file. A non-nil error from visit stops the walk.
	EachLibraryPath func(visit func(path string) error) error
}

// CheckRemoveWithData returns nil only when asking qBittorrent to delete the
// task together with its files provably cannot delete a library file.
func CheckRemoveWithData(in SafetyInput) error {
	remoteRoot, err := absClean(in.RemoteRoot)
	if err != nil || remoteRoot == "" {
		return refuse(CodeContentUnresolved, "the frozen qBittorrent save root is not an absolute path")
	}
	appRoot := remoteRoot
	if strings.TrimSpace(in.ApplicationRoot) != "" {
		if appRoot, err = absClean(in.ApplicationRoot); err != nil {
			return refuse(CodeContentUnresolved, "the frozen application root is not an absolute path")
		}
	}

	// 1. Identity: the task is the one frozen, saving where it was frozen.
	if in.Hash == "" || !strings.EqualFold(strings.TrimSpace(in.Torrent.Hash), in.Hash) {
		return refuse(CodeQBTaskMismatch, "the qBittorrent task is not the frozen one")
	}
	savePath, err := absClean(in.Torrent.SavePath)
	if err != nil || savePath != remoteRoot {
		return refuse(CodeQBTaskMismatch, "the task saves outside the frozen save root")
	}

	// 2. The task's complete file list, as qBittorrent paths under the save root.
	if len(in.Torrent.FileNames) == 0 {
		return refuse(CodeContentUnresolved, "qBittorrent reported no files for the task")
	}
	remoteFiles := make([]string, 0, len(in.Torrent.FileNames))
	for _, name := range in.Torrent.FileNames {
		rel, ok := relativeName(name)
		if !ok {
			return refuse(CodeFileListInvalid, "a task file name is not a safe relative path")
		}
		remoteFiles = append(remoteFiles, filepath.Join(remoteRoot, filepath.FromSlash(rel)))
	}

	// 3. The content directory (strictly below the save root), if the layout
	// has one. A content path equal to the save root means files directly in
	// the save root: no directory belongs to the task.
	remoteDir := ""
	if strings.TrimSpace(in.Torrent.ContentPath) == "" {
		return refuse(CodeContentUnresolved, "qBittorrent reported no content path")
	}
	content, err := absClean(in.Torrent.ContentPath)
	if err != nil {
		return refuse(CodeContentUnresolved, "the content path is not an absolute path")
	}
	if !within(remoteRoot, content) {
		return refuse(CodeContentOutsideRoot, "the content path is outside the save root")
	}
	if content != remoteRoot {
		remoteDir = content
		for _, file := range remoteFiles {
			if !within(remoteDir, file) {
				return refuse(CodeContentUnresolved, "a task file is not inside the content path")
			}
		}
	}

	// 4. Application-visible zones.
	toApp := func(remote string) string {
		rel, _ := filepath.Rel(remoteRoot, remote)
		return filepath.Join(appRoot, rel)
	}
	resolver := newResolver()
	zone := zones{files: map[string]struct{}{}}
	for _, remote := range remoteFiles {
		lexical := toApp(remote)
		canonical, err := resolver.entry(lexical)
		if err != nil {
			return refuse(CodePathUnresolvable, "a task file path cannot be resolved")
		}
		zone.files[fold(lexical)] = struct{}{}
		zone.files[fold(canonical)] = struct{}{}
		zone.files[fold(remote)] = struct{}{}
		zone.fileForms = append(zone.fileForms, lexical, canonical, remote)
	}
	if remoteDir != "" {
		lexical := toApp(remoteDir)
		canonical, err := resolver.tree(lexical)
		if err != nil {
			return refuse(CodePathUnresolvable, "the content path cannot be resolved")
		}
		zone.dirs = []string{fold(lexical), fold(canonical), fold(remoteDir)}
	}

	// 5. No other qBittorrent task uses these files or this folder.
	if err := checkOtherTasks(in, zone, remoteRoot, toApp, resolver); err != nil {
		return err
	}

	// 6. No library path equals a task file or lies inside the content directory.
	if in.EachLibraryPath == nil {
		return refuse(CodePlacementPathInvalid, "the library paths cannot be listed")
	}
	return in.EachLibraryPath(func(path string) error {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || hasControl(path) || hasParentSegment(path) {
			return refuse(CodePlacementPathInvalid, "a library path is not a clean absolute path")
		}
		lexical := filepath.Clean(path)
		canonical, err := resolver.entry(lexical)
		if err != nil {
			return refuse(CodePathUnresolvable, "a library path cannot be resolved")
		}
		for _, form := range []string{fold(lexical), fold(canonical)} {
			if zone.contains(form) {
				return refuse(CodePlacementInsideContent, "a library path lies inside the task content")
			}
		}
		return nil
	})
}

// checkOtherTasks refuses when another qBittorrent task's content overlaps the
// zone: its content folder contains or lies inside ours, contains one of our
// files, or one of its files is one of ours or lies inside our folder. A task
// whose content path equals its save path is compared file by file. Paths are
// compared as qBittorrent names them, as this process sees them (when under the
// frozen save root) and symlink-resolved. Anything unreadable fails closed.
func checkOtherTasks(in SafetyInput, zone zones, remoteRoot string, toApp func(string) string, resolver *resolver) error {
	if in.OtherTasks == nil {
		return refuse(CodeOtherTasksUnreadable, "the other qBittorrent tasks cannot be listed")
	}
	others, err := in.OtherTasks()
	if err != nil {
		return err
	}
	forms := func(remote string, entry bool) ([]string, error) {
		candidates := []string{remote}
		if within(remoteRoot, remote) {
			candidates = append(candidates, toApp(remote))
		}
		out := make([]string, 0, len(candidates)*2)
		for _, candidate := range candidates {
			resolved, err := resolver.tree(candidate)
			if entry {
				resolved, err = resolver.entry(candidate)
			}
			if err != nil {
				return nil, refuse(CodePathUnresolvable, "another task's path cannot be resolved")
			}
			out = append(out, fold(candidate), fold(resolved))
		}
		return out, nil
	}
	for _, other := range others {
		if strings.EqualFold(strings.TrimSpace(other.Hash), strings.TrimSpace(in.Hash)) {
			continue
		}
		savePath, saveErr := absClean(other.SavePath)
		contentPath, contentErr := absClean(other.ContentPath)
		if saveErr != nil || contentErr != nil {
			return refuse(CodeOtherTasksUnreadable, "another task has no usable content location")
		}
		if contentPath != savePath {
			dirs, err := forms(contentPath, false)
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				for _, own := range zone.dirs {
					if within(dir, own) || within(own, dir) {
						return refuse(CodeContentSharedWithOther, "another task's folder overlaps this task's")
					}
				}
				for _, file := range zone.fileForms {
					if within(dir, fold(file)) {
						return refuse(CodeContentSharedWithOther, "another task's folder contains this task's files")
					}
				}
			}
			continue
		}
		if len(other.FileNames) == 0 {
			return refuse(CodeOtherTasksUnreadable, "another task in the save folder has no file list")
		}
		for _, name := range other.FileNames {
			rel, ok := relativeName(name)
			if !ok {
				return refuse(CodeOtherTasksUnreadable, "another task has an unsafe file name")
			}
			files, err := forms(filepath.Join(savePath, filepath.FromSlash(rel)), true)
			if err != nil {
				return err
			}
			for _, file := range files {
				if _, shared := zone.files[file]; shared {
					return refuse(CodeContentSharedWithOther, "another task uses one of this task's files")
				}
				for _, own := range zone.dirs {
					if within(own, file) {
						return refuse(CodeContentSharedWithOther, "another task has a file inside this task's folder")
					}
				}
			}
		}
	}
	return nil
}

// zones are the case-folded paths qBittorrent would delete.
type zones struct {
	files     map[string]struct{}
	fileForms []string
	dirs      []string
}

func (z zones) contains(path string) bool {
	if _, ok := z.files[path]; ok {
		return true
	}
	for _, dir := range z.dirs {
		if within(dir, path) {
			return true
		}
	}
	return false
}

// resolver canonicalizes paths through symlinks, caching per directory. A path
// that does not exist resolves through its deepest existing ancestor; a path
// that cannot be examined at all (permission, loop, dangling link) is an error
// and the caller fails closed.
type resolver struct {
	trees map[string]string
}

func newResolver() *resolver { return &resolver{trees: map[string]string{}} }

// tree resolves the whole path, following a final symlink when it exists.
func (r *resolver) tree(path string) (string, error) {
	path = filepath.Clean(path)
	if cached, ok := r.trees[path]; ok {
		return cached, nil
	}
	existing, rest := path, []string{}
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", err
		}
		rest = append([]string{filepath.Base(existing)}, rest...)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(append([]string{real}, rest...)...)
	r.trees[path] = resolved
	return resolved, nil
}

// entry resolves the directory of a directory entry and keeps the entry name:
// removing the entry never follows a final symlink.
func (r *resolver) entry(path string) (string, error) {
	path = filepath.Clean(path)
	dir, err := r.tree(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(path)), nil
}

func absClean(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || hasControl(value) || !filepath.IsAbs(value) || hasParentSegment(value) {
		return "", errors.New("not a clean absolute path")
	}
	return filepath.Clean(value), nil
}

// relativeName validates one qBittorrent file name (relative to the save path).
func relativeName(name string) (string, bool) {
	name = strings.ReplaceAll(strings.TrimSpace(name), `\`, "/")
	if name == "" || strings.HasPrefix(name, "/") || hasControl(name) || len(name) > 4096 {
		return "", false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	return name, true
}

func hasControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n") || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func hasParentSegment(value string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(value, `\`, "/"), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// within reports whether path equals root or lies below it (both clean).
func within(root, path string) bool {
	if root == path {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// fold is the comparison form: a case-insensitive filesystem must not hide an
// overlap, and folding can only add refusals.
func fold(path string) string { return strings.ToLower(filepath.Clean(path)) }
