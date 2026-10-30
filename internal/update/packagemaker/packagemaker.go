// Package packagemaker builds the deterministic embedded-update payload
// archive consumed by the container-embedded self-update chain.
//
// Determinism contract:
//   - entries are written in a fixed order: the unified server binary,
//     migration SQL sorted by filename, frontend dist directories sorted
//     lexicographically followed by frontend files sorted per directory walk,
//     release-metadata.json last;
//   - tar headers carry mtime = SOURCE_DATE_EPOCH, uid/gid = 0 and empty
//     uname/gname; mode is whitelisted to 0755 for binaries/directories and
//     0644 for regular payload files;
//   - gzip carries no extra header data (no name, no timestamp).
package packagemaker

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ServerEntryPath  = "bansoko-server"
	MigrateEntryPath = "bansoko-migrate"
	MetaEntryPath    = "release-metadata.json"

	MigrationsPrefix = "db/migrations/"
	FrontendPrefix   = "frontend/dist/"

	dirMode  = 0o755
	execMode = 0o755
	fileMode = 0o644
)

type FileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Metadata is the release-metadata.json entry. It only carries non-circular
// fields plus the per-file manifest; the archive's own digest is computed by
// the caller after packing and never appears inside the archive.
type Metadata struct {
	FormatVersion       string       `json:"format_version"`
	ReleaseID           string       `json:"release_id"`
	Version             string       `json:"version"`
	SourceCommit        string       `json:"source_commit"`
	BuildID             string       `json:"build_id"`
	TargetSchemaVersion string       `json:"target_schema_version"`
	Platform            string       `json:"platform"`
	Architecture        string       `json:"architecture"`
	Files               []FileRecord `json:"files"`
}

type Options struct {
	ServerBinaryPath string
	MigrationsDir    string
	FrontendDistDir  string
	DefaultsDir      string

	Metadata Metadata // Files is filled in from the concrete inputs.

	OutputPath      string
	SourceDateEpoch int64
}

type Result struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	FileCount int    `json:"file_count"`
}

type plannedEntry struct {
	headerName string
	sourcePath string
	mode       int64
	isDir      bool
	size       int64
	sha256     string
}

// BuildEmbeddedPayload writes a deterministic gzip-compressed tar to
// Options.OutputPath and returns the bare-hex sha256 digest of the compressed
// bytes with its size.
func BuildEmbeddedPayload(opts Options) (Result, error) {
	if opts.SourceDateEpoch <= 0 {
		return Result{}, fmt.Errorf("packagemaker: positive source date epoch is required")
	}
	if strings.TrimSpace(opts.ServerBinaryPath) == "" {
		return Result{}, fmt.Errorf("packagemaker: server binary is required")
	}
	if strings.TrimSpace(opts.OutputPath) == "" {
		return Result{}, fmt.Errorf("packagemaker: output path is required")
	}
	if strings.TrimSpace(opts.Metadata.FormatVersion) == "" {
		opts.Metadata.FormatVersion = "embedded-update-v1"
	}
	info, statErr := os.Stat(opts.ServerBinaryPath)
	if statErr != nil {
		return Result{}, fmt.Errorf("packagemaker: stat %s: %w", ServerEntryPath, statErr)
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("packagemaker: %s is not a regular file", ServerEntryPath)
	}
	if info.Mode()&0o111 == 0 {
		return Result{}, fmt.Errorf("packagemaker: %s must be executable", ServerEntryPath)
	}

	entries, err := planEntries(opts)
	if err != nil {
		return Result{}, err
	}
	if len(entries) == 0 {
		return Result{}, fmt.Errorf("packagemaker: refusing an empty payload")
	}

	records := make([]FileRecord, 0, len(entries))
	for _, e := range entries {
		if !e.isDir {
			records = append(records, FileRecord{Path: e.headerName, Size: e.size, SHA256: e.sha256})
		}
	}
	meta := opts.Metadata
	meta.Files = records
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("packagemaker: encode metadata: %w", err)
	}
	metaBytes = append(metaBytes, '\n')

	tmpFile, err := os.CreateTemp(filepath.Dir(opts.OutputPath), ".payload-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("packagemaker: create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	digest, size, writeErr := writeArchive(tmpFile, entries, metaBytes, time.Unix(opts.SourceDateEpoch, 0).UTC())
	closeErr := tmpFile.Close()
	if writeErr != nil {
		return Result{}, writeErr
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("packagemaker: close temp file: %w", closeErr)
	}
	if chmodErr := os.Chmod(tmpPath, fileMode); chmodErr != nil {
		return Result{}, fmt.Errorf("packagemaker: chmod temp file: %w", chmodErr)
	}
	if renameErr := os.Rename(tmpPath, opts.OutputPath); renameErr != nil {
		return Result{}, fmt.Errorf("packagemaker: publish payload: %w", renameErr)
	}
	return Result{Digest: digest, Size: size, FileCount: len(records)}, nil
}

func writeArchive(out io.Writer, entries []plannedEntry, metaBytes []byte, modTime time.Time) (string, int64, error) {
	hasher := sha256.New()
	sizeCounter := &countingWriter{w: io.MultiWriter(out, hasher)}
	gzw := gzip.NewWriter(sizeCounter)
	tw := tar.NewWriter(gzw)

	writeErr := func() error {
		regHeader := func(name string, size int64, mode int64) *tar.Header {
			return &tar.Header{
				Typeflag: tar.TypeReg,
				Name:     name,
				Size:     size,
				Mode:     mode,
				Uid:      0,
				Gid:      0,
				Uname:    "",
				Gname:    "",
				ModTime:  modTime,
				Format:   tar.FormatUSTAR,
			}
		}
		for _, e := range entries {
			header := regHeader(e.headerName, e.size, e.mode)
			if e.isDir {
				header.Typeflag = tar.TypeDir
				header.Size = 0
			}
			if err := tw.WriteHeader(header); err != nil {
				return fmt.Errorf("packagemaker: write tar header %s: %w", e.headerName, err)
			}
			if e.isDir {
				continue
			}
			src, openErr := os.Open(e.sourcePath)
			if openErr != nil {
				return fmt.Errorf("packagemaker: open source %s: %w", e.sourcePath, openErr)
			}
			copyBytes, copyErr := io.Copy(tw, src)
			closeErr := src.Close()
			if copyErr != nil {
				return fmt.Errorf("packagemaker: copy source %s: %w", e.sourcePath, copyErr)
			}
			if copyBytes != e.size {
				return fmt.Errorf("packagemaker: source %s changed between hashing and packing", e.sourcePath)
			}
			if closeErr != nil {
				return fmt.Errorf("packagemaker: close source %s: %w", e.sourcePath, closeErr)
			}
		}
		if err := tw.WriteHeader(regHeader(MetaEntryPath, int64(len(metaBytes)), fileMode)); err != nil {
			return fmt.Errorf("packagemaker: write tar header %s: %w", MetaEntryPath, err)
		}
		if _, err := tw.Write(metaBytes); err != nil {
			return fmt.Errorf("packagemaker: write metadata: %w", err)
		}
		return nil
	}()
	// Close order matters: finish the tar stream (padding blocks included),
	// then flush the gzip container so every byte reaches the counting writer.
	if closeTarErr := tw.Close(); writeErr == nil && closeTarErr != nil {
		writeErr = closeTarErr
	}
	if gzwCloseErr := gzw.Close(); writeErr == nil && gzwCloseErr != nil {
		writeErr = gzwCloseErr
	}
	if writeErr != nil {
		return "", 0, writeErr
	}
	return hex.EncodeToString(hasher.Sum(nil)), sizeCounter.n, nil
}

func planEntries(opts Options) ([]plannedEntry, error) {
	var entries []plannedEntry
	addFile := func(headerName, sourcePath string, mode int64) error {
		size, sum, err := hashFile(sourcePath)
		if err != nil {
			return err
		}
		entries = append(entries, plannedEntry{
			headerName: headerName,
			sourcePath: sourcePath,
			mode:       mode,
			size:       size,
			sha256:     sum,
		})
		return nil
	}

	if err := addFile(ServerEntryPath, opts.ServerBinaryPath, execMode); err != nil {
		return nil, err
	}
	sqlFiles, err := filepath.Glob(filepath.Join(opts.MigrationsDir, "*.sql"))
	if err != nil {
		return nil, fmt.Errorf("packagemaker: list migrations: %w", err)
	}
	sort.Strings(sqlFiles)
	if len(sqlFiles) == 0 {
		return nil, fmt.Errorf("packagemaker: no migration SQL found under %s", opts.MigrationsDir)
	}
	for _, path := range sqlFiles {
		if err := addFile(MigrationsPrefix+filepath.Base(path), path, fileMode); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(opts.DefaultsDir) != "" {
		defaultsPath := filepath.Join(opts.DefaultsDir, "attr-rules.yaml")
		if info, statErr := os.Stat(defaultsPath); statErr != nil || info.IsDir() {
			return nil, fmt.Errorf("packagemaker: attr rules defaults at %s are missing", defaultsPath)
		}
		if err := addFile("deployment/defaults/attr-rules.yaml", defaultsPath, fileMode); err != nil {
			return nil, err
		}
		namingVarsPath := filepath.Join(opts.DefaultsDir, "naming-vars.js")
		if info, statErr := os.Stat(namingVarsPath); statErr != nil || info.IsDir() {
			return nil, fmt.Errorf("packagemaker: naming vars defaults at %s are missing", namingVarsPath)
		}
		if err := addFile("deployment/defaults/naming-vars.js", namingVarsPath, fileMode); err != nil {
			return nil, err
		}
	}

	frontendFiles, frontendDirs, err := scanFrontendDist(opts.FrontendDistDir)
	if err != nil {
		return nil, err
	}
	sort.Strings(frontendDirs)
	for _, rel := range frontendDirs {
		entries = append(entries, plannedEntry{
			headerName: FrontendPrefix + rel + "/",
			mode:       dirMode,
			isDir:      true,
		})
	}
	for _, rel := range frontendFiles {
		if err := addFile(FrontendPrefix+rel, filepath.Join(opts.FrontendDistDir, filepath.FromSlash(rel)), fileMode); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func scanFrontendDist(distDir string) (files, dirs []string, err error) {
	indexInfo, statErr := os.Stat(filepath.Join(distDir, "index.html"))
	if statErr != nil || indexInfo.IsDir() {
		return nil, nil, fmt.Errorf("packagemaker: frontend dist at %s has no index.html", distDir)
	}
	walkErr := filepath.WalkDir(distDir, func(path string, d fs.DirEntry, visitErr error) error {
		if visitErr != nil {
			return fmt.Errorf("packagemaker: walk frontend dist: %w", visitErr)
		}
		rel, relErr := filepath.Rel(distDir, path)
		if relErr != nil {
			return fmt.Errorf("packagemaker: relativize frontend path: %w", relErr)
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			dirs = append(dirs, slashRel)
		case d.Type().IsRegular():
			files = append(files, slashRel)
		default:
			return fmt.Errorf("packagemaker: unsupported frontend entry type %s (%s)", FrontendPrefix+slashRel, d.Type())
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	sort.Strings(files)
	return files, dirs, nil
}

func hashFile(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("packagemaker: open %s: %w", path, err)
	}
	defer file.Close()
	hasher := sha256.New()
	count, err := io.Copy(hasher, file)
	if err != nil {
		return 0, "", fmt.Errorf("packagemaker: hash %s: %w", path, err)
	}
	return count, hex.EncodeToString(hasher.Sum(nil)), nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
