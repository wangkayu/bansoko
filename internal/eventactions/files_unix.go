//go:build linux || darwin

package eventactions

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const Root = "/config/scripts"
const maxPreview = 64 * 1024

const (
	Ready         = "checkPassed"
	Missing       = "missingFile"
	NoExecute     = "missingPermission"
	NoInterpreter = "missingInterpreter"
	Unreadable    = "unreadable"
)

var filename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// File is a preflight result, not a promise that the process will start.
type File struct {
	Name        string `json:"name"`
	Check       string `json:"check"`
	Interpreter string `json:"interpreter,omitempty"`
	Source      string `json:"source,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

func openDirectory(path string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (s *Service) Files() ([]File, error) {
	out := make([]File, 0)
	dir, err := openDirectory(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// Bound per-request work even if a non-script directory was mounted.
	entries, err := dir.ReadDir(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !filename.MatchString(entry.Name()) {
			continue
		}
		view, file := inspect(dir, entry.Name(), false)
		if file != nil {
			_ = file.Close()
		}
		if view.Check != Missing {
			out = append(out, view)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Service) File(name string) File {
	view, file := s.openScript(name, true)
	if file != nil {
		_ = file.Close()
	}
	return view
}

func (s *Service) openScript(name string, preview bool) (File, *os.File) {
	if !filename.MatchString(name) {
		return File{Name: name, Check: Unreadable}, nil
	}
	dir, err := openDirectory(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return File{Name: name, Check: Missing}, nil
	}
	if err != nil {
		return File{Name: name, Check: Unreadable}, nil
	}
	defer dir.Close()
	return inspect(dir, name, preview)
}

func inspect(dir *os.File, name string, preview bool) (File, *os.File) {
	view := File{Name: name, Check: Unreadable}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		view.Check = Missing
	}
	if err != nil {
		return view, nil
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 {
		_ = file.Close()
		return view, nil
	}
	limit := int64(256)
	if preview {
		limit = maxPreview + 1
	}
	content, err := io.ReadAll(io.NewSectionReader(file, 0, limit))
	if err != nil {
		return view, file
	}
	if preview && !strings.ContainsRune(string(content), 0) {
		view.Truncated = len(content) > maxPreview
		if view.Truncated {
			content = content[:maxPreview]
		}
		view.Source = strings.ToValidUTF8(string(content), "�")
	}
	view.Interpreter = interpreter(string(content))
	switch {
	case stat.Mode&0o111 == 0 || !canExecuteFile(int(dir.Fd()), fd, name):
		view.Check = NoExecute
	case !availableInterpreter(view.Interpreter):
		view.Check = NoInterpreter
	default:
		view.Check = Ready
	}
	return view, file
}

const executionPath = "/usr/local/bin:/usr/bin:/bin"

func interpreter(text string) string {
	if !strings.HasPrefix(text, "#!") {
		return ""
	}
	line, _, _ := strings.Cut(text, "\n")
	if len(line) >= 255 || strings.ContainsAny(line, "\r\x00") || !utf8.ValidString(line) {
		return ""
	}
	return strings.Trim(line[2:], " \t")
}

func availableInterpreter(line string) bool {
	if line == "" {
		return false
	}
	path, arg, _ := strings.Cut(strings.ReplaceAll(line, "\t", " "), " ")
	if !filepath.IsAbs(path) || unix.Faccessat(unix.AT_FDCWD, path, unix.X_OK, unix.AT_EACCESS) != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if path != "/usr/bin/env" && path != "/bin/env" {
		return true
	}
	arg = strings.TrimSpace(arg)
	if !filename.MatchString(arg) {
		return false
	}
	for dir := range strings.SplitSeq(executionPath, ":") {
		candidate := filepath.Join(dir, arg)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && unix.Faccessat(unix.AT_FDCWD, candidate, unix.X_OK, unix.AT_EACCESS) == nil {
			return true
		}
	}
	return false
}
