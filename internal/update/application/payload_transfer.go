package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	hardeningdomain "bansoko/internal/hardening/domain"
)

func (s *Service) fetchPayload(ctx context.Context, source *UpdateHTTPSource, opID string, target *hardeningdomain.EmbeddedUpdateSegment) (result string, resultErr error) {
	downloadDir := filepath.Join(s.updatesRoot, "downloads", opID)
	if mkErr := ensureOperationDir(s.updatesRoot, downloadDir, 0o700); mkErr != nil {
		return "", fmt.Errorf("create download directory: %w", mkErr)
	}
	outPath := filepath.Join(downloadDir, PayloadFileName)

	if source == nil {
		source = s.source
	}
	if source == nil {
		return "", errors.New("update HTTP source is not configured")
	}
	payloadURL, payloadErr := source.PayloadURL()
	if payloadErr != nil {
		return "", payloadErr
	}
	req, reqErr := source.NewRequest(ctx, http.MethodGet, payloadURL)
	if reqErr != nil {
		return "", fmt.Errorf("payload URL invalid: %w", reqErr)
	}
	resp, doErr := source.Client().Do(req)
	if doErr != nil {
		return "", fmt.Errorf("fetch update payload: %w", doErr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("payload source answered HTTP %d", resp.StatusCode)
	}

	partial := outPath + ".part"
	partialDir := filepath.Dir(outPath)
	if info, statErr := os.Lstat(partial); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("stage payload download: partial path is not a regular file")
		}
		if removeErr := os.Remove(partial); removeErr != nil {
			return "", fmt.Errorf("reset partial payload download: %w", removeErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect partial payload download: %w", statErr)
	}
	if !payloadNoFollowSupported {
		return "", errors.New("stage payload download: symlink-safe opening is unsupported on this platform")
	}
	out, openErr := os.OpenFile(partial, os.O_RDWR|os.O_CREATE|os.O_EXCL|payloadNoFollowFlag, 0o600)
	if openErr != nil {
		return "", fmt.Errorf("stage payload download: %w", openErr)
	}
	defer out.Close()
	cleanupPartial := true
	defer func() {
		if cleanupPartial {
			_ = removePayloadSourceIfUnchanged(partial, out)
		}
	}()
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), resp.Body)
	if copyErr != nil || out.Sync() != nil {
		return "", errors.New("download update payload: transfer failed")
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if err := verifyPayloadSource(partial, out, written, target.PayloadSize, digest, target.PayloadDigest); err != nil {
		return "", err
	}
	if sigErr := hardeningdomain.VerifyArtifactDigestSignature(s.publicKey, digest, target.PayloadSignature); sigErr != nil {
		return "", fmt.Errorf("payload signature rejected after download: %w", sigErr)
	}
	beforePayloadPublish(partial)
	if err := verifyPayloadSource(partial, out, written, target.PayloadSize, digest, target.PayloadDigest); err != nil {
		return "", err
	}
	if rehashedBytes, rehashedDigest, err := rehashPayload(out); err != nil || rehashedBytes != target.PayloadSize || rehashedDigest != target.PayloadDigest {
		if err != nil {
			return "", fmt.Errorf("recheck downloaded payload: %w", err)
		}
		return "", errors.New("downloaded payload changed after initial verification")
	}

	private, err := os.CreateTemp(partialDir, ".payload-publish-*")
	if err != nil {
		return "", fmt.Errorf("create private payload publication: %w", err)
	}
	privatePath := private.Name()
	privateInfo, privateInfoErr := private.Stat()
	if privateInfoErr != nil {
		_ = private.Close()
		return "", privateInfoErr
	}
	defer func() {
		_ = removePathIfSameInfo(privatePath, privateInfo)
		_ = private.Close()
	}()
	if err := private.Chmod(0o600); err != nil {
		_ = private.Close()
		return "", err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		_ = private.Close()
		return "", err
	}
	privateHash := sha256.New()
	privateBytes, copyErr := io.Copy(io.MultiWriter(private, privateHash), out)
	if copyErr != nil || private.Sync() != nil {
		_ = private.Close()
		return "", errors.New("copy private payload publication failed")
	}
	privateDigest := hex.EncodeToString(privateHash.Sum(nil))
	if privateBytes != target.PayloadSize || privateDigest != target.PayloadDigest {
		_ = private.Close()
		return "", errors.New("private payload publication differs from signed payload")
	}
	if err := verifyRegularUnaliasedFile(private); err != nil {
		_ = private.Close()
		return "", err
	}
	afterPayloadPrivateVerification(privatePath)
	if _, err := private.Seek(0, io.SeekStart); err != nil {
		_ = private.Close()
		return "", err
	}
	recheck := sha256.New()
	recheckBytes, copyErr := io.Copy(recheck, private)
	if copyErr != nil || recheckBytes != target.PayloadSize || hex.EncodeToString(recheck.Sum(nil)) != target.PayloadDigest {
		_ = private.Close()
		return "", errors.New("private payload publication changed after verification")
	}
	if err := verifyRegularUnaliasedFile(private); err != nil {
		_ = private.Close()
		return "", err
	}
	privateInfo, err = private.Stat()
	if err != nil {
		_ = private.Close()
		return "", err
	}
	privatePathInfo, err := os.Lstat(privatePath)
	if err != nil || !os.SameFile(privateInfo, privatePathInfo) || !privatePathInfo.Mode().IsRegular() {
		return "", errors.New("private payload pathname was substituted")
	}
	oldPath, oldInfo, backupPath, backupInfo, err := backupPayloadIfPresent(outPath, partialDir)
	if err != nil {
		return "", err
	}
	afterPayloadBackup(backupPath)
	restore := true
	defer func() {
		if restore {
			if fallbackErr := restorePayloadFallback(outPath, oldPath, oldInfo, backupPath, backupInfo, privateInfo); fallbackErr != nil {
				if resultErr == nil {
					resultErr = fallbackErr
				} else {
					resultErr = fmt.Errorf("%w; payload fallback restoration failed: %v", resultErr, fallbackErr)
				}
				// Keep the identity-bound backup for a later operator/retry cleanup.
				return
			}
		}
		if backupPath != "" {
			if cleanupErr := removePathIfSameInfo(backupPath, backupInfo); cleanupErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("remove payload fallback: %w", cleanupErr)
			}
		}
	}()
	// This bounded seam closes the destination-substitution window immediately
	// before rename; it does not claim to defeat an unconstrained continuous
	// same-UID race after this check returns.
	beforePayloadRename(privatePath, outPath)
	currentOut, currentOutErr := os.Lstat(outPath)
	if oldInfo == nil {
		if !errors.Is(currentOutErr, os.ErrNotExist) {
			return "", errors.New("payload destination was substituted before publish")
		}
	} else if currentOutErr != nil || !os.SameFile(currentOut, oldInfo) {
		return "", errors.New("payload destination was substituted before publish")
	}
	privatePathInfo, err = os.Lstat(privatePath)
	if err != nil || !os.SameFile(privateInfo, privatePathInfo) || !privatePathInfo.Mode().IsRegular() {
		return "", errors.New("private payload pathname was substituted before publish")
	}
	if err := os.Rename(privatePath, outPath); err != nil {
		return "", fmt.Errorf("commit payload download: %w", err)
	}
	published, err := verifyPublishedPayload(outPath, privateInfo, target.PayloadSize, target.PayloadDigest)
	if err != nil {
		return "", err
	}
	_ = published.Close()
	if err := syncDirectory(partialDir); err != nil {
		return "", fmt.Errorf("sync payload download directory: %w", err)
	}
	if backupPath != "" {
		if err := removePathIfSameInfo(backupPath, backupInfo); err != nil {
			return "", fmt.Errorf("remove payload fallback: %w", err)
		}
	}
	restore = false
	cleanupPartial = false
	_ = removePayloadSourceIfUnchanged(partial, out)
	return outPath, nil
}

func backupPayloadIfPresent(outPath, dir string) (string, os.FileInfo, string, os.FileInfo, error) {
	info, err := os.Lstat(outPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, "", nil, nil
	}
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("inspect existing payload: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, "", nil, errors.New("commit payload download: destination is not regular")
	}
	source, err := os.Open(outPath)
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("open existing payload: %w", err)
	}
	defer source.Close()
	backup, err := os.CreateTemp(dir, ".payload-backup-*")
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("create payload fallback: %w", err)
	}
	backupPath := backup.Name()
	backupInfo, infoErr := backup.Stat()
	if infoErr != nil {
		_ = backup.Close()
		return "", nil, "", nil, fmt.Errorf("inspect payload fallback: %w", infoErr)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = removePathIfSameInfo(backupPath, backupInfo)
		}
		_ = backup.Close()
	}()
	if _, err := io.Copy(backup, source); err != nil || backup.Sync() != nil {
		return "", nil, "", nil, errors.New("copy payload fallback failed")
	}
	if err := backup.Close(); err != nil {
		return "", nil, "", nil, err
	}
	pathInfo, err := os.Lstat(backupPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, backupInfo) {
		return "", nil, "", nil, errors.New("payload fallback identity mismatch")
	}
	cleanup = false
	return outPath, info, backupPath, backupInfo, nil
}

func removePathIfSameInfo(path string, expected os.FileInfo) error {
	if expected == nil {
		return nil
	}
	actual, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(actual, expected) {
		return nil
	}
	return os.Remove(path)
}

func restorePayloadFallback(outPath, oldPath string, oldInfo os.FileInfo, backupPath string, backupInfo os.FileInfo, privateInfo os.FileInfo) error {
	current, err := os.Lstat(outPath)
	if err == nil && privateInfo != nil && os.SameFile(current, privateInfo) {
		if backupPath == "" {
			if err := os.Remove(outPath); err != nil {
				return err
			}
			return syncDirectory(filepath.Dir(outPath))
		}
		backupCurrent, backupErr := os.Lstat(backupPath)
		if backupErr != nil || backupInfo == nil || !os.SameFile(backupCurrent, backupInfo) {
			return errors.New("payload fallback identity was lost")
		}
		if err := os.Rename(backupPath, outPath); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(outPath))
	}
	if errors.Is(err, os.ErrNotExist) && backupPath != "" {
		backupCurrent, backupErr := os.Lstat(backupPath)
		if backupErr != nil || backupInfo == nil || !os.SameFile(backupCurrent, backupInfo) {
			return errors.New("payload fallback identity was lost")
		}
		if err := os.Rename(backupPath, outPath); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(outPath))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return errors.New("refusing to overwrite a foreign payload path")
}

func verifyPublishedPayload(path string, expected os.FileInfo, size int64, digest string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("verify committed payload: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(info, expected) {
		return nil, errors.New("published payload identity mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	count, got, hashErr := rehashPayload(file)
	if hashErr != nil {
		_ = file.Close()
		return nil, hashErr
	}
	if count != size || got != digest {
		_ = file.Close()
		return nil, errors.New("published payload content mismatch")
	}
	if err := verifyRegularUnaliasedFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func verifyRegularUnaliasedFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("payload is not regular")
	}
	nlink, err := regularLinkCount(info)
	if err != nil {
		return err
	}
	if nlink != 1 {
		return fmt.Errorf("payload has %d links", nlink)
	}
	return nil
}

func rehashPayload(file *os.File) (int64, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	count, err := io.Copy(hash, file)
	if _, seekErr := file.Seek(0, io.SeekStart); err == nil {
		err = seekErr
	}
	return count, hex.EncodeToString(hash.Sum(nil)), err
}

func verifyPayloadSource(path string, file *os.File, written, expectedSize int64, digest, expectedDigest string) error {
	if written != expectedSize {
		return fmt.Errorf("payload size mismatch (want %d got %d)", expectedSize, written)
	}
	if digest != expectedDigest {
		return errors.New("payload digest mismatch: downloaded bytes do not match the signed segment")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, pathInfo) {
		return errors.New("downloaded payload pathname was substituted")
	}
	return verifyRegularUnaliasedFile(file)
}

func removePayloadSourceIfUnchanged(path string, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(info, pathInfo) {
		return nil
	}
	return os.Remove(path)
}
