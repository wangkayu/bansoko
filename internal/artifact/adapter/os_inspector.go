package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/filekinds"
)

// OSArtifactInspector fingerprints a downloaded artifact at the path reported
// by the download provider. The application performs no placement
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
	info, err := os.Lstat(cleanPath)
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("stat downloaded artifact: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return domain.ArtifactFacts{}, errors.New("downloaded artifact must be a non-empty regular file")
	}
	file, err := os.Open(cleanPath)
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("open downloaded artifact: %w", err)
	}
	defer file.Close()
	initialSize := info.Size()
	hash := sha256.New()
	// The read checks ctx between chunks, so the attempt ticket deadline stops
	// a long hash instead of letting it run past its attempt.
	readBytes, err := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, r: file}, initialSize))
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("fingerprint downloaded artifact: %w", err)
	}
	if readBytes != initialSize {
		return domain.ArtifactFacts{}, fmt.Errorf("downloaded artifact changed while reading: read %d bytes, expected %d", readBytes, initialSize)
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return domain.ArtifactFacts{}, fmt.Errorf("stat downloaded artifact after reading: %w", err)
	}
	if finalInfo.Size() != initialSize {
		return domain.ArtifactFacts{}, fmt.Errorf("downloaded artifact changed while reading: initial size %d, final size %d", initialSize, finalInfo.Size())
	}
	return domain.ArtifactFacts{
		Path:       cleanPath,
		SizeBytes:  initialSize,
		SHA256:     hex.EncodeToString(hash.Sum(nil)),
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

// contextReader fails the next Read once ctx is done. A single blocked read
// syscall still finishes first; the check bounds every following chunk.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
