package application

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/update/packagemaker"
)

// Staging caps (ora-2 §3.3 extensions). A payload that exceeds any cap is
// refused instead of unpacking unbounded content onto the updates volume.
const (
	maxStagedEntries    = 20000
	maxTotalStagedBytes = 2 << 30 // 2 GiB
	maxSingleFileBytes  = 1 << 30 // 1 GiB
	// PayloadArchiveEntryPath and SignedManifestEntryPath are deployment-local
	// trust evidence added beside an extracted release. They are intentionally
	// outside the payload allowlist: the archive itself is the signed source,
	// while these entries let a later bootstrap prove that the selected tree is
	// still the tree produced by that signed archive.
	PayloadArchiveEntryPath = ".embedded-payload.tar.gz"
	SignedManifestEntryPath = ".release-manifest.json"
)

// safeEntryName validates an archive member against the release layout
// allowlist: binaries, migration SQL, frontend dist files and the release
// metadata, never absolute paths, backslashes or parent escapes.
func safeEntryName(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("archive entry %q escapes the release layout", name)
	}
	cleaned := filepath.Clean("/" + name)
	if cleaned != "/"+name {
		return "", fmt.Errorf("archive entry %q is not canonical", name)
	}
	switch {
	case name == packagemaker.ServerEntryPath,
		name == packagemaker.MetaEntryPath,
		name == "deployment/defaults/attr-rules.yaml",
		name == "deployment/defaults/naming-vars.js",
		strings.HasPrefix(name, packagemaker.MigrationsPrefix),
		strings.HasPrefix(name, packagemaker.FrontendPrefix):
	default:
		return "", fmt.Errorf("archive entry %q is outside the payload allowlist", name)
	}
	return name, nil
}

// extractPayloadToTemp unpacks a verified gzip tar into tempDir while
// enforcing path traversal, symlink/device/hardlink and allowlist rules.
// Every regular file entry's sha256 is computed and returned keyed by archive
// path so the caller can bind the tree to the signed per-file manifest.
func extractPayloadToTemp(payloadPath, tempDir string) (map[string]string, error) {
	file, err := os.Open(payloadPath)
	if err != nil {
		return nil, fmt.Errorf("open staged payload: %w", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("staged payload is not a gzip stream: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	hashes := map[string]string{}
	var totalBytes int64

	for entries := 0; ; entries++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read staged payload: %w", err)
		}
		if entries >= maxStagedEntries {
			return nil, errors.New("payload exceeds the staged entry limit")
		}
		name := header.Name
		switch header.Typeflag {
		case tar.TypeReg:
			validName, nameErr := safeEntryName(name)
			if nameErr != nil {
				return nil, nameErr
			}
			if _, duplicate := hashes[validName]; duplicate {
				return nil, fmt.Errorf("payload contains duplicate archive entry %s", validName)
			}
			if header.Size > maxSingleFileBytes {
				return nil, fmt.Errorf("payload entry %s exceeds the single-file size limit", validName)
			}
			totalBytes += header.Size
			if totalBytes > maxTotalStagedBytes {
				return nil, errors.New("payload exceeds the total staging size limit")
			}
			target := filepath.Join(tempDir, filepath.FromSlash(validName))
			if dirErr := os.MkdirAll(filepath.Dir(target), 0o755); dirErr != nil {
				return nil, fmt.Errorf("create staging directory for %s: %w", validName, dirErr)
			}
			out, openErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
			if openErr != nil {
				return nil, fmt.Errorf("stage payload entry %s: %w", validName, openErr)
			}
			digest := sha256.New()
			written, copyErr := io.Copy(io.MultiWriter(out, digest), reader)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil {
				_ = os.Remove(target)
				return nil, fmt.Errorf("stage payload entry %s: write failed", validName)
			}
			if written != header.Size {
				return nil, fmt.Errorf("payload entry %s truncated during staging", validName)
			}
			mode := os.FileMode(header.Mode) & 0o777
			if mode&0o111 != 0 {
				if chmodErr := os.Chmod(target, 0o700); chmodErr != nil {
					return nil, fmt.Errorf("protect staged executable %s: %w", validName, chmodErr)
				}
			}
			hashes[validName] = hex.EncodeToString(digest.Sum(nil))
		case tar.TypeDir:
			validName, nameErr := safeEntryName(strings.TrimSuffix(name, "/"))
			if nameErr != nil {
				return nil, nameErr
			}
			if mkdirErr := os.MkdirAll(filepath.Join(tempDir, filepath.FromSlash(validName)), 0o755); mkdirErr != nil {
				return nil, fmt.Errorf("stage directory %s: %w", validName, mkdirErr)
			}
		default:
			return nil, fmt.Errorf(
				"payload entry %s uses an unsupported archive type (%c); links and devices are refused",
				name, header.Typeflag)
		}
	}
	return hashes, nil
}

// verifyStagedTree binds the extracted tree on disk to the signed per-file
// manifest: every listed file must exist as a regular file with the exact
// recorded size and sha256 (re-hashed from disk, never trusted from memory),
// and every disk file must be accounted for by the manifest or be the release
// metadata itself (no smuggled extras).
func verifyStagedTree(releaseDir string) (packagemaker.Metadata, error) {
	releaseInfo, err := os.Lstat(releaseDir)
	if err != nil {
		return packagemaker.Metadata{}, fmt.Errorf("release directory unreadable: %w", err)
	}
	if !releaseInfo.IsDir() {
		return packagemaker.Metadata{}, errors.New("release path is not a directory")
	}
	metadataPath := filepath.Join(releaseDir, packagemaker.MetaEntryPath)
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil {
		return packagemaker.Metadata{}, fmt.Errorf("release metadata unreadable in staged tree: %w", err)
	}
	if !metadataInfo.Mode().IsRegular() {
		return packagemaker.Metadata{}, errors.New("release metadata is not a regular file")
	}
	if metadataInfo.Size() > maxManifestBytes {
		return packagemaker.Metadata{}, errors.New("release metadata exceeds the bounded size")
	}
	metaBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return packagemaker.Metadata{}, fmt.Errorf("release metadata unreadable in staged tree: %w", err)
	}
	var meta packagemaker.Metadata
	decoder := json.NewDecoder(bytes.NewReader(metaBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return packagemaker.Metadata{}, fmt.Errorf("release metadata invalid: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return packagemaker.Metadata{}, errors.New("release metadata contains trailing JSON data")
		}
		return packagemaker.Metadata{}, fmt.Errorf("decode trailing release metadata: %w", err)
	}
	if strings.TrimSpace(meta.ReleaseID) == "" || len(meta.Files) == 0 {
		return packagemaker.Metadata{}, errors.New("release metadata lacks identity or file records")
	}
	expected := make(map[string]struct{}, len(meta.Files)+1)
	for _, record := range meta.Files {
		validPath, pathErr := safeEntryName(record.Path)
		if pathErr != nil || validPath != record.Path {
			return packagemaker.Metadata{}, fmt.Errorf("release metadata contains an invalid file path %q", record.Path)
		}
		if _, duplicate := expected[record.Path]; duplicate {
			return packagemaker.Metadata{}, fmt.Errorf("release metadata contains duplicate file path %s", record.Path)
		}
		expected[record.Path] = struct{}{}
		absolutePath := filepath.Join(releaseDir, filepath.FromSlash(record.Path))
		info, statErr := os.Lstat(absolutePath)
		if statErr != nil {
			return packagemaker.Metadata{}, fmt.Errorf("signed manifest lists file %s missing from the staged tree", record.Path)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return packagemaker.Metadata{}, fmt.Errorf("staged entry %s is not a regular file", record.Path)
		}
		if info.Size() != record.Size {
			return packagemaker.Metadata{}, fmt.Errorf("staged file %s size mismatch", record.Path)
		}
		digest, hashErr := hashFileForSidecar(absolutePath)
		if hashErr != nil {
			return packagemaker.Metadata{}, fmt.Errorf("hash staged file %s: %w", record.Path, hashErr)
		}
		if digest != record.SHA256 {
			return packagemaker.Metadata{}, fmt.Errorf("staged file %s does not match its signed digest", record.Path)
		}
	}
	expected[packagemaker.MetaEntryPath] = struct{}{}

	onDisk := map[string]struct{}{}
	walkErr := filepath.Walk(releaseDir, func(path string, info os.FileInfo, visitErr error) error {
		if visitErr != nil {
			return visitErr
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(releaseDir, path)
		if relErr != nil {
			return relErr
		}
		onDisk[filepath.ToSlash(rel)] = struct{}{}
		return nil
	})
	if walkErr != nil {
		return packagemaker.Metadata{}, fmt.Errorf("walk staged tree: %w", walkErr)
	}
	for path := range onDisk {
		if _, ok := expected[path]; !ok && path != PayloadArchiveEntryPath && path != SignedManifestEntryPath {
			return packagemaker.Metadata{}, fmt.Errorf("staged tree contains file %s absent from the signed manifest", path)
		}
	}
	for _, required := range []string{
		packagemaker.ServerEntryPath,
		"deployment/defaults/attr-rules.yaml",
		"deployment/defaults/naming-vars.js",
	} {
		info, statErr := os.Lstat(filepath.Join(releaseDir, required))
		if statErr != nil || !info.Mode().IsRegular() {
			return packagemaker.Metadata{}, fmt.Errorf("payload misses required release file %s", required)
		}
	}
	hasMigrations, hasIndex := false, false
	for _, record := range meta.Files {
		if strings.HasPrefix(record.Path, packagemaker.MigrationsPrefix) {
			hasMigrations = true
		}
		if record.Path == packagemaker.FrontendPrefix+"index.html" {
			hasIndex = true
		}
	}
	if !hasMigrations || !hasIndex {
		return packagemaker.Metadata{}, errors.New("payload misses migration SQL or frontend index")
	}
	return meta, nil
}

// VerifyReleaseTree proves the selected release against the signed outer
// manifest and the retained payload archive, then verifies that the extracted
// files still match the exact archive contents. A metadata file alone is not a
// trust root because an attacker could replace it together with the tree.
func VerifyReleaseTree(releaseDir string, publicKey ed25519.PublicKey) error {
	_, err := VerifyReleaseTreeMetadata(releaseDir, publicKey)
	return err
}

// VerifyReleaseTreeIdentity returns the exact signed outer manifest alongside
// the verified payload metadata. Callers that persist deployment provenance use
// this single verification result rather than rereading writable evidence.
func VerifyReleaseTreeIdentity(releaseDir string, publicKey ed25519.PublicKey) (packagemaker.Metadata, hardeningdomain.ReleaseManifest, error) {
	metadata, manifest, err := verifyReleaseTree(releaseDir, publicKey, true)
	return metadata, manifest, err
}

// VerifyReleaseTreeMetadata proves a selected release and returns the metadata
// that was verified as part of the same operation. Callers use the returned
// value instead of reading a writable metadata file again after verification.
func VerifyReleaseTreeMetadata(releaseDir string, publicKey ed25519.PublicKey) (packagemaker.Metadata, error) {
	metadata, _, err := VerifyReleaseTreeIdentity(releaseDir, publicKey)
	return metadata, err
}

// VerifyStagedReleaseTree verifies a release before it is atomically renamed
// into releases/<release-id>/. The temporary directory intentionally has an
// operation-derived name, so path-to-release binding is checked after publish
// by VerifyReleaseTree.
func VerifyStagedReleaseTree(releaseDir string, publicKey ed25519.PublicKey) error {
	_, _, err := verifyReleaseTree(releaseDir, publicKey, false)
	return err
}

func verifyReleaseTree(releaseDir string, publicKey ed25519.PublicKey, requireReleasePath bool) (packagemaker.Metadata, hardeningdomain.ReleaseManifest, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("release tree verification requires an ed25519 public key")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(releaseDir, SignedManifestEntryPath))
	if err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("signed release manifest unreadable: %w", err)
	}
	var manifest hardeningdomain.ReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("signed release manifest invalid: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("signed release manifest contains trailing JSON data")
		}
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("decode trailing signed release manifest data: %w", err)
	}
	if err := manifest.VerifySignature(publicKey); err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("signed release manifest rejected: %w", err)
	}
	if err := manifest.VerifyEmbeddedPayloadSignature(publicKey); err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("signed embedded payload rejected: %w", err)
	}
	if manifest.ReleaseID == "" || (requireReleasePath && filepath.Base(filepath.Clean(releaseDir)) != manifest.ReleaseID) {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("signed release manifest does not match its release directory")
	}
	if releaseInfo, statErr := os.Lstat(releaseDir); statErr != nil || !releaseInfo.IsDir() {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("release path is not a directory")
	}

	payloadPath := filepath.Join(releaseDir, PayloadArchiveEntryPath)
	payloadInfo, err := os.Lstat(payloadPath)
	if err != nil || !payloadInfo.Mode().IsRegular() {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("retained payload archive is not a regular file")
	}
	if payloadInfo.Size() > maxTotalStagedBytes {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("retained payload archive exceeds the bounded size")
	}
	payloadBytes, err := os.ReadFile(payloadPath)
	if err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("retained payload archive unreadable: %w", err)
	}
	if int64(len(payloadBytes)) != manifest.EmbeddedUpdate.PayloadSize {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("retained payload archive size does not match the signed manifest")
	}
	payloadSum := sha256.Sum256(payloadBytes)
	if digest := hex.EncodeToString(payloadSum[:]); digest != manifest.EmbeddedUpdate.PayloadDigest {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("retained payload archive digest does not match the signed manifest")
	}

	tempDir, err := os.MkdirTemp("", "bansoko-release-verify-")
	if err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("create release verification directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	if _, err := extractPayloadToTemp(payloadPath, tempDir); err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("verify retained payload archive contents: %w", err)
	}
	archiveMeta, err := verifyStagedTree(tempDir)
	if err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("verify retained payload tree: %w", err)
	}
	currentMeta, err := verifyStagedTree(releaseDir)
	if err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, fmt.Errorf("verify selected release tree: %w", err)
	}
	if !sameReleaseMetadata(archiveMeta, currentMeta) {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("selected release metadata differs from the signed payload")
	}
	if archiveMeta.ReleaseID != manifest.ReleaseID || archiveMeta.Version != manifest.Version ||
		strings.TrimSpace(archiveMeta.BuildID) == "" || archiveMeta.TargetSchemaVersion != manifest.TargetMigrationVersion {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, errors.New("selected release metadata does not match the signed release identity")
	}
	return currentMeta, manifest, nil
}

func sameReleaseMetadata(left, right packagemaker.Metadata) bool {
	// Both values were decoded from the signed archive and the selected tree.
	// Deep equality intentionally covers every metadata field, including fields
	// added in the future, instead of allowing a new identity field to be
	// accidentally omitted from this security comparison.
	return reflect.DeepEqual(left, right)
}

// publishReleaseTree finalizes the verified tree: restore executable bits on
// the whitelisted binaries, fsync every file plus the directories so the
// rename into place survives crashes, then atomically rename. The caller is
// responsible for refusing an existing non-empty target before invoking this.
func publishReleaseTree(tempDir, targetDir string, execPaths []string) error {
	for _, rel := range execPaths {
		target := filepath.Join(tempDir, filepath.FromSlash(rel))
		if err := os.Chmod(target, 0o755); err != nil {
			return fmt.Errorf("restore executable bit on %s: %w", rel, err)
		}
	}
	syncErr := func(root string) error {
		return filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			syncErr := file.Sync()
			closeErr := file.Close()
			if syncErr != nil {
				return syncErr
			}
			return closeErr
		})
	}(tempDir)
	if syncErr != nil {
		return fmt.Errorf("sync staged tree: %w", syncErr)
	}
	if renameErr := os.Rename(tempDir, targetDir); renameErr != nil {
		return fmt.Errorf("publish staged release tree: %w", renameErr)
	}
	dirHandle, dirErr := os.Open(filepath.Dir(targetDir))
	if dirErr != nil {
		return fmt.Errorf("open release parent directory after publish: %w", dirErr)
	}
	if syncDirErr := dirHandle.Sync(); syncDirErr != nil {
		_ = dirHandle.Close()
		return fmt.Errorf("sync release parent directory after publish: %w", syncDirErr)
	}
	if closeErr := dirHandle.Close(); closeErr != nil {
		return fmt.Errorf("close release parent directory after publish: %w", closeErr)
	}
	return nil
}
