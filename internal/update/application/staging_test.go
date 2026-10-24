package application

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
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
	writeTree("migrate", 0o755)
	writeTree("sql/001_a.sql", 0o644)
	writeTree("dist/index.html", 0o644)
	writeTree("dist/assets/app.js", 0o644)
	// Defaults are part of the signed payload contract.
	writeTree("defaults/attr-rules.yaml", 0o644)
	writeTree("defaults/naming-vars.js", 0o644)
	meta = packagemaker.Metadata{
		FormatVersion: "embedded-update-v1",
		ReleaseID:     "release-test-01",
		Version:       "0.9.0-rc.10",
	}
	payloadPath = filepath.Join(t.TempDir(), "embedded-update-v1.tar.gz")
	if _, err := packagemaker.BuildEmbeddedPayload(packagemaker.Options{
		ServerBinaryPath:  filepath.Join(root, "server"),
		MigrateBinaryPath: filepath.Join(root, "migrate"),
		MigrationsDir:     filepath.Join(root, "sql"),
		FrontendDistDir:   filepath.Join(root, "dist"),
		DefaultsDir:       filepath.Join(root, "defaults"),
		Metadata:          meta,
		OutputPath:        payloadPath,
		SourceDateEpoch:   1756000000,
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
	if err := publishReleaseTree(tempDir, releaseDir, []string{packagemaker.ServerEntryPath, packagemaker.MigrateEntryPath}); err != nil {
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

func TestVerifyReleaseTreeRequiresSignedRetainedPayload(t *testing.T) {
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReleaseTree(t.TempDir(), ed25519.PublicKey(publicKey)); err == nil {
		t.Fatal("missing signed descriptor must be rejected")
	}
	_ = privateKey
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
	valid := []string{"bansoko-server", "bansoko-migrate", "release-metadata.json",
		"deployment/defaults/attr-rules.yaml", "deployment/defaults/naming-vars.js",
		"db/migrations/067_x.sql", "frontend/dist/index.html"}
	for _, name := range valid {
		if _, err := safeEntryName(name); err != nil {
			t.Fatalf("%q should pass allowlist: %v", name, err)
		}
	}
	invalid := []string{"../escape", "/abs", "back\\slash", "tools/extra",
		"", ".", "..", "db/../db/x.sql"}
	for _, name := range invalid {
		if _, err := safeEntryName(name); err == nil {
			t.Fatalf("%q must be rejected by the allowlist", name)
		}
	}
}
