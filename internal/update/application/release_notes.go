package application

import (
	"os"
	"path/filepath"
	"strings"

	hardeningdomain "bansoko/internal/hardening/domain"
)

// currentReleaseNotes reads the running version's own notes from the signed
// manifest of the installed release (updates/releases/<build id>). A base
// image has no installed release, and an unreadable, unsigned or mismatched
// manifest yields no notes: they are informational and never gate the check.
func (s *Service) currentReleaseNotes() *hardeningdomain.ReleaseNotesEntry {
	id := strings.TrimSpace(s.buildID)
	if strings.TrimSpace(s.updatesRoot) == "" || id == "" || id != s.buildID ||
		strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return nil
	}
	path := filepath.Join(s.updatesRoot, "releases", id, SignedManifestEntryPath)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	manifest, err := decodeManifestDocument(data)
	if err != nil || manifest.VerifySignature(s.publicKey) != nil || manifest.ReleaseID != id {
		return nil
	}
	for _, entry := range manifest.ReleaseNotes {
		if entry.Version == s.buildVersion {
			found := entry
			return &found
		}
	}
	return nil
}
