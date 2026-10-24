package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	hardeningdomain "bansoko/internal/hardening/domain"
)

const oNoFollow = syscall.O_NOFOLLOW

func decodeManifestDocument(data []byte) (hardeningdomain.ReleaseManifest, error) {
	var manifest hardeningdomain.ReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return hardeningdomain.ReleaseManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return hardeningdomain.ReleaseManifest{}, errors.New("signed manifest contains trailing data")
	}
	return manifest, nil
}
func writeFileAtomic(dir, target string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(dir, ".update-file-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	return syncDirectory(dir)
}
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func readFileBounded(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds bounded read size %d", filepath.Base(path), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds bounded read size %d", filepath.Base(path), limit)
	}
	return data, nil
}
func ensureOperationDir(root, dir string, mode os.FileMode) error {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside updates root", dir)
	}
	if _, err = os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(root, mode); err != nil {
			return err
		}
	}
	if err = requireRealDirectory(root); err != nil {
		return err
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if _, err = os.Lstat(current); errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, mode); err != nil {
				return err
			}
		}
		if err = requireRealDirectory(current); err != nil {
			return err
		}
	}
	return nil
}
func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
}
