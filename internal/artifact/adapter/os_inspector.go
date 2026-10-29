package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/filekinds"
	"bansoko/internal/fileidentity"
)

// OSArtifactInspector observes a downloaded artifact at the path reported by
// the download provider. The observation is path-level: one fstat after an
// O_NOFOLLOW open, never a content read, so its cost does not grow with the
// file's size and network mounts work. Integrity rests on the download
// client's piece hashes. The application performs no placement
// authorization; the deployment layer is the read/write boundary.
type OSArtifactInspector struct{}

func (i OSArtifactInspector) Inspect(ctx context.Context, path string) (domain.ArtifactFacts, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArtifactFacts{}, err
	}
	if strings.TrimSpace(path) == "" {
		return domain.ArtifactFacts{}, errors.New("artifact path is required")
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("resolve artifact path: %w", err)
	}
	initialInfo, err := os.Lstat(cleanPath)
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("stat downloaded artifact: %w", err)
	}
	if initialInfo.Mode()&os.ModeSymlink != 0 {
		return domain.ArtifactFacts{}, errors.New("downloaded artifact must not be a symlink")
	}
	cleanPath, err = filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("resolve artifact links: %w", err)
	}
	// O_NOFOLLOW refuses a final symlink swapped in after the Lstat;
	// O_NONBLOCK keeps a special file swapped in from blocking the open.
	file, err := os.OpenFile(cleanPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return domain.ArtifactFacts{}, errors.New("downloaded artifact must not be a symlink")
		}
		return domain.ArtifactFacts{}, fmt.Errorf("open downloaded artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("stat downloaded artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return domain.ArtifactFacts{}, errors.New("downloaded artifact must be a non-empty regular file")
	}
	identity, err := fileidentity.FromFile(file)
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("stat downloaded artifact: %w", err)
	}
	return domain.ArtifactFacts{
		Path:       cleanPath,
		SizeBytes:  info.Size(),
		Device:     identity.Device,
		Inode:      identity.Inode,
		ModifiedAt: info.ModTime().UTC(),
		ObservedAt: time.Now().UTC(),
		MediaKind:  mediaKind(cleanPath),
	}, nil
}

func mediaKind(path string) string {
	if kind := filekinds.ClassifyCurrent(path); kind != filekinds.KindOther {
		return kind
	}
	return "unknown"
}
