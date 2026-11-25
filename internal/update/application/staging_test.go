package application

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/update/packagemaker"
)

// fixture builds a minimal valid payload (executables, migration SQL, frontend
// dist) through packagemaker so staging tests exercise the real producer.
func buildFixturePayload(t *testing.T) (payloadPath string, meta packagemaker.Metadata) {
	t.Helper()
	root := t.TempDir()
	writeTree := func(rel string, mode os.FileMode) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content-of-"+rel), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeTree("server", 0o755)
	writeTree("sql/001_a.sql", 0o644)
	writeTree("dist/index.html", 0o644)
	writeTree("dist/assets/app.js", 0o644)
	// Defaults are part of the payload contract. attr-rules.yaml and
	// naming-vars.js are optional for the new updater now, but must still
	// stage when a transition release carries them for older updaters.
	writeTree("defaults/attr-rules.yaml", 0o644)
	writeTree("defaults/naming-vars.js", 0o644)
	meta = packagemaker.Metadata{
		FormatVersion: "embedded-update-v1",
		ReleaseID:     "release-test-01",
		Version:       "0.9.0-rc.10",
	}
	payloadPath = filepath.Join(t.TempDir(), "embedded-update-v1.tar.gz")
	if _, err := packagemaker.BuildEmbeddedPayload(packagemaker.Options{
		ServerBinaryPath: filepath.Join(root, "server"),
		MigrationsDir:    filepath.Join(root, "sql"),
		FrontendDistDir:  filepath.Join(root, "dist"),
		DefaultsDir:      filepath.Join(root, "defaults"),
		Metadata:         meta,
		OutputPath:       payloadPath,
		SourceDateEpoch:  1756000000,
	}); err != nil {
		t.Fatalf("build payload fixture: %v", err)
	}
	return payloadPath, meta
}

func TestExtractVerifyPublishHappyPath(t *testing.T) {
	payloadPath, _ := buildFixturePayload(t)
	tempDir := t.TempDir()

	hashes, err := extractPayloadToTemp(payloadPath, tempDir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := verifyStagedTree(tempDir); err != nil {
		t.Fatalf("verify staged tree: %v", err)
	}

	releaseDir := filepath.Join(t.TempDir(), "releases", "release-test-01")
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishReleaseTree(tempDir, releaseDir, []string{packagemaker.ServerEntryPath}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	info, statErr := os.Stat(filepath.Join(releaseDir, packagemaker.ServerEntryPath))
	if statErr != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("published binary missing exec bit: %v", statErr)
	}
	if !treeMatchesReleaseDir(releaseDir, hashes) {
		t.Fatalf("published tree does not match staged hashes")
	}
}

func TestTransitionPayloadSatisfiesPre03Updaters(t *testing.T) {
	payloadPath, _ := buildFixturePayload(t)
	tempDir := t.TempDir()
	if _, err := extractPayloadToTemp(payloadPath, tempDir); err != nil {
		t.Fatalf("extract transition payload: %v", err)
	}
	if _, err := verifyStagedTree(tempDir); err != nil {
		t.Fatalf("current updater rejects transition payload: %v", err)
	}

	// This is the required-file loop shared by the 0.2.0, 0.2.1 and 0.2.2
	// updaters. Keep the compatibility proof at their boundary rather than
	// merely checking that the file happens to appear in package metadata.
	requiredByOldUpdater := func(releaseDir string) error {
		for _, required := range []string{
			packagemaker.ServerEntryPath,
			"deployment/defaults/naming-vars.js",
		} {
			info, err := os.Lstat(filepath.Join(releaseDir, required))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("payload misses required release file %s", required)
			}
		}
		return nil
	}
	for _, version := range []string{"0.2.0", "0.2.1", "0.2.2"} {
		t.Run(version, func(t *testing.T) {
			if err := requiredByOldUpdater(tempDir); err != nil {
				t.Fatalf("old updater required-file check: %v", err)
			}
		})
	}
	if err := os.Remove(filepath.Join(tempDir, "deployment/defaults/naming-vars.js")); err != nil {
		t.Fatal(err)
	}
	if err := requiredByOldUpdater(tempDir); err == nil {
		t.Fatal("old updater required-file check accepts a payload without naming-vars.js")
	}
}

func TestExtractVerifyWithoutLegacyDefaultsFiles(t *testing.T) {
	root := t.TempDir()
	writeTree := func(rel string, mode os.FileMode) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content-of-"+rel), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeTree("server", 0o755)
	writeTree("sql/001_a.sql", 0o644)
	writeTree("dist/index.html", 0o644)
	payloadPath := filepath.Join(t.TempDir(), "embedded-update-v1.tar.gz")
	if _, err := packagemaker.BuildEmbeddedPayload(packagemaker.Options{
		ServerBinaryPath: filepath.Join(root, "server"),
		MigrationsDir:    filepath.Join(root, "sql"),
		FrontendDistDir:  filepath.Join(root, "dist"),
		Metadata: packagemaker.Metadata{
			FormatVersion: "embedded-update-v1",
			ReleaseID:     "release-no-defaults-01",
			Version:       "0.3.0",
		},
		OutputPath:      payloadPath,
		SourceDateEpoch: 1756000000,
	}); err != nil {
		t.Fatalf("build payload without defaults: %v", err)
	}
	tempDir := t.TempDir()
	if _, err := extractPayloadToTemp(payloadPath, tempDir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := verifyStagedTree(tempDir); err != nil {
		t.Fatalf("verify staged tree without naming-vars.js: %v", err)
	}
}

func TestVerifyReleaseTreeRequiresRetainedPayload(t *testing.T) {
	if err := VerifyReleaseTree(t.TempDir()); err == nil {
		t.Fatal("a tree without its retained payload must be rejected")
	}
}

func TestVerifyReleaseTreeRejectsInvalidOverlayManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SignedManifestEntryPath)
	if err := os.WriteFile(path, []byte(`{"release_id":"historical"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStagedReleaseTree(dir); err == nil || !strings.Contains(err.Error(), "no embedded update segment") {
		t.Fatalf("historical manifest: %v", err)
	}
	if err := os.Truncate(path, maxManifestBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStagedReleaseTree(dir); err == nil || !strings.Contains(err.Error(), "bounded read size") {
		t.Fatalf("oversized manifest: %v", err)
	}
}

func TestExtractRejectsTraversalAndLinksAndForeignEntries(t *testing.T) {
	craft := func(build func(tw *tar.Writer)) string {
		out := filepath.Join(t.TempDir(), "crafted.tar")
		file, _ := os.Create(out)
		gz := gzip.NewWriter(file)
		tw := tar.NewWriter(gz)
		build(tw)
		_ = tw.Close()
		if gzErr := gz.Close(); gzErr != nil {
			t.Fatal(gzErr)
		}
		if fileErr := file.Close(); fileErr != nil {
			t.Fatal(fileErr)
		}
		return out
	}

	cases := []struct {
		label   string
		name    string
		typeFlg byte
		link    string
	}{
		{label: "traversal", name: "../evil", typeFlg: tar.TypeReg},
		{label: "absolute", name: "/etc/passwd", typeFlg: tar.TypeReg},
		{label: "symlink", name: "bansoko-server", typeFlg: tar.TypeSymlink, link: "/bin/sh"},
		{label: "hardlink", name: "bansoko-migrate", typeFlg: tar.TypeLink, link: "/bin/sh"},
		{label: "retired migrate layout", name: packagemaker.MigrateEntryPath, typeFlg: tar.TypeReg},
		{label: "foreign entry", name: "extra-tool", typeFlg: tar.TypeReg},
	}
	for _, item := range cases {
		t.Run(item.label, func(t *testing.T) {
			bad := craft(func(tw *tar.Writer) {
				hdr := &tar.Header{
					Typeflag: item.typeFlg,
					Name:     item.name,
					Linkname: item.link,
				}
				if err := tw.WriteHeader(hdr); err != nil {
					t.Fatal(err)
				}
			})
			target := filepath.Join(t.TempDir(), "extract")
			if mkErr := os.MkdirAll(target, 0o755); mkErr != nil {
				t.Fatal(mkErr)
			}
			if _, extractErr := extractPayloadToTemp(bad, target); extractErr == nil {
				t.Fatalf("crafted payload entry %q was not refused", item.name)
			}
		})
	}
}

func TestExtractRejectsDuplicateEntries(t *testing.T) {
	out := filepath.Join(t.TempDir(), "duplicate.tar.gz")
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, content := range []string{"first", "second"} {
		if err := tw.WriteHeader(&tar.Header{Name: packagemaker.ServerEntryPath, Mode: 0o755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractPayloadToTemp(out, t.TempDir()); err == nil {
		t.Fatal("duplicate archive entry was accepted")
	}
}

func TestVerifyStagedTreeDetectsTampering(t *testing.T) {
	payloadPath, _ := buildFixturePayload(t)
	tempDir := t.TempDir()

	if _, extractErr := extractPayloadToTemp(payloadPath, tempDir); extractErr != nil {
		t.Fatalf("extract: %v", extractErr)
	}
	// Tamper a tracked file after extraction: disk re-hashing must refuse.
	tampered := filepath.Join(tempDir, packagemaker.ServerEntryPath)
	if err := os.Chmod(tampered, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tampered, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, verifyErr := verifyStagedTree(tempDir); verifyErr == nil {
		t.Fatalf("tampered staged file accepted")
	}

	// An empty directory without release metadata is refused as well.
	if _, extraErr := verifyStagedTree(filepath.Join(t.TempDir(), "empty-dir")); extraErr == nil {
		t.Fatalf("missing release metadata accepted")
	}
}

func TestSafeEntryNameAllowlist(t *testing.T) {
	valid := []string{"bansoko-server", "release-metadata.json",
		"deployment/defaults/attr-rules.yaml", "deployment/defaults/naming-vars.js",
		"db/migrations/067_x.sql", "frontend/dist/index.html"}
	for _, name := range valid {
		if _, err := safeEntryName(name); err != nil {
			t.Fatalf("%q should pass allowlist: %v", name, err)
		}
	}
	invalid := []string{"../escape", "/abs", "back\\slash", "tools/extra", "bansoko-migrate",
		"", ".", "..", "db/../db/x.sql"}
	for _, name := range invalid {
		if _, err := safeEntryName(name); err == nil {
			t.Fatalf("%q must be rejected by the allowlist", name)
		}
	}
}
