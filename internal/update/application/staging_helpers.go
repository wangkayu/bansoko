package application

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	hardeningdomain "bansoko/internal/hardening/domain"
)

func VerifyPayloadFile(path string, target *hardeningdomain.EmbeddedUpdateSegment, publicKey ed25519.PublicKey) error {
	return verifyDownloadedPayload(path, target, publicKey)
}
func hashFileForSidecar(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func treeMatchesReleaseDir(releaseDir string, expected map[string]string) bool {
	for path, digest := range expected {
		actual, err := hashFileForSidecar(filepath.Join(releaseDir, filepath.FromSlash(path)))
		if err != nil || actual != digest {
			return false
		}
	}
	return true
}
func hashFile(path string) (string, error) {
	digest, err := hashFileForSidecar(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", filepath.Base(path), err)
	}
	return digest, nil
}
