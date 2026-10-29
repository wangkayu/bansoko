package integration

import (
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/fileidentity"
	librarydomain "bansoko/internal/library/domain"
)

// pathMetadataFingerprint is the path-level fingerprint value an inspection
// records for the file at path: its size and device/inode, no content.
func pathMetadataFingerprint(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := fileidentity.FromPath(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return librarydomain.MetadataFingerprint(info.Size(), identity)
}

// requireMetadataFingerprint fails unless value is a metadata fingerprint of
// size that carries a device/inode identity.
func requireMetadataFingerprint(t *testing.T, what, value string, size int64) {
	t.Helper()
	facts, ok := librarydomain.ParseMetadataFingerprint(value)
	if !ok || facts.SizeBytes != size || facts.Device == 0 || facts.Inode == 0 {
		t.Fatalf("%s = %q, want the metadata fingerprint of a %d-byte file with its device/inode", what, value, size)
	}
}
