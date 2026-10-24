//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

// verifiedDirectoryChain retains the legacy identity-only API for preflight
// checks. Mutation paths use openVerifiedDirectory and keep its descriptor
// alive through the syscall and directory fsync.
func verifiedDirectoryChain(root, directory string, create bool) (domain.FileIdentity, error) {
	handle, err := openVerifiedDirectory(root, directory, create)
	if err != nil {
		return domain.FileIdentity{}, err
	}
	identity := handle.identity
	if closeErr := handle.Close(); closeErr != nil {
		return domain.FileIdentity{}, closeErr
	}
	return identity, nil
}

// openVerifiedDirectory opens every component from the filesystem root (or a
// root-bounded canonical anchor) with O_NOFOLLOW. In particular, it never
// opens the root with a single path-based syscall that could follow an
// ancestor symlink.
func openVerifiedDirectory(root, directory string, create bool) (*verifiedDirectory, error) {
	if root != "" {
		root = filepath.Clean(root)
	}
	directory = filepath.Clean(directory)
	if !filepath.IsAbs(directory) || root != "" && !filepath.IsAbs(root) {
		return nil, errors.New("verified directory paths must be absolute")
	}
	if root != "" {
		rel, err := filepath.Rel(root, directory)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || stringsHasParent(rel) {
			return nil, errors.New("verified directory escapes the chain anchor")
		}
		rootHandle, err := openAbsoluteDirectoryChain(root, false)
		if err != nil {
			return nil, fmt.Errorf("open verified root: %w", err)
		}
		if rel == "." {
			return rootHandle, nil
		}
		return walkVerifiedDirectory(rootHandle.file, rel, directory, create)
	}
	return walkVerifiedDirectoryFromRoot(directory, create)
}

func walkVerifiedDirectoryFromRoot(directory string, create bool) (*verifiedDirectory, error) {
	root, err := openDirectoryNoFollow(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	return walkVerifiedDirectory(os.NewFile(uintptr(root), string(filepath.Separator)), strings.TrimPrefix(filepath.Clean(directory), string(filepath.Separator)), filepath.Clean(directory), create)
}

func openAbsoluteDirectoryChain(path string, create bool) (*verifiedDirectory, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("verified directory path must be absolute")
	}
	root, err := openDirectoryNoFollow(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	if clean == string(filepath.Separator) {
		return directoryHandle(root, clean)
	}
	return walkVerifiedDirectory(os.NewFile(uintptr(root), string(filepath.Separator)), strings.TrimPrefix(clean, string(filepath.Separator)), clean, create)
}

func walkVerifiedDirectory(current *os.File, relative, canonical string, create bool) (*verifiedDirectory, error) {
	fd := int(current.Fd())
	for _, part := range splitDirectoryParts(relative) {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(fd, part, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = current.Close()
				return nil, fmt.Errorf("create directory %q: %w", part, mkdirErr)
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, fmt.Errorf("open directory %q without following symlink: %w", part, openErr)
		}
		if err := current.Close(); err != nil {
			_ = unix.Close(next)
			return nil, err
		}
		current = os.NewFile(uintptr(next), "")
		if current == nil {
			_ = unix.Close(next)
			return nil, errors.New("open verified directory returned an invalid file")
		}
		fd = next
	}
	return directoryHandleFile(current, canonical)
}

func directoryHandle(fd int, canonical string) (*verifiedDirectory, error) {
	file := os.NewFile(uintptr(fd), canonical)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open verified directory returned an invalid file")
	}
	return directoryHandleFile(file, canonical)
}

func directoryHandleFile(file *os.File, canonical string) (*verifiedDirectory, error) {
	if file == nil {
		return nil, errors.New("open verified directory returned an invalid file")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.IsDir() {
		_ = file.Close()
		return nil, errors.New("verified path is not a directory")
	}
	identity, err := fileidentity.FromFile(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !fileidentity.IsPresent(identity) {
		_ = file.Close()
		return nil, errors.New("verified directory identity is unavailable")
	}
	return &verifiedDirectory{file: file, identity: identity, canonical: filepath.Clean(canonical)}, nil
}

func openDirectoryNoFollow(path string) (int, error) {
	return unix.Open(filepath.Clean(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

func splitDirectoryParts(relative string) []string {
	parts := make([]string, 0)
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func stringsHasParent(path string) bool {
	return path == ".." || len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}
