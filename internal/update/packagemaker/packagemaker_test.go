package packagemaker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePlain(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildFixture(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	distDir := filepath.Join(root, "dist")
	migDir := filepath.Join(root, "mig")
	defaultsDir := filepath.Join(root, "defaults")
	for _, d := range []string{binDir, distDir, migDir, defaultsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	server := writeExecutable(t, binDir, "server-bin", "SERVER-BINARY-CONTENT")
	writePlain(t, distDir, "index.html", "<html></html>")
	writePlain(t, distDir, "assets/app.js", "console.log(1)")
	writePlain(t, migDir, "001_init.sql", "CREATE TABLE a();")
	writePlain(t, migDir, "010_more.sql", "ALTER TABLE a ADD b int;")
	writePlain(t, defaultsDir, "attr-rules.yaml", "version: 1\nresolution: [1080p]\nquality: [web]\nlanguage: [ja]\nspecial_markers: [hdr]\ngroup_brackets: ['[', ']']\n")
	writePlain(t, defaultsDir, "naming-vars.js", "vars.anime = {ep: '1'};\n")
	return Options{
		ServerBinaryPath: server,
		MigrationsDir:    migDir,
		FrontendDistDir:  distDir,
		DefaultsDir:      defaultsDir,
		OutputPath:       filepath.Join(root, "payload.tar.gz"),
		SourceDateEpoch:  1735689600,
		Metadata: Metadata{
			FormatVersion:       "embedded-update-v1",
			ReleaseID:           "release-test",
			Version:             "0.9.0-test",
			SourceCommit:        "commit",
			BuildID:             "release-test",
			TargetSchemaVersion: "010_more",
			Platform:            "linux",
			Architecture:        "amd64",
		},
	}
}

func TestDeterministicOutput(t *testing.T) {
	opts := buildFixture(t)
	first, err := BuildEmbeddedPayload(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild with different input mtimes; output must stay byte-identical.
	if err := os.Chtimes(opts.ServerBinaryPath, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	second, err := BuildEmbeddedPayload(opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || first.Size != second.Size {
		t.Fatalf("digest/size drifted across builds: %+v vs %+v", first, second)
	}

	payloadBytes, err := os.ReadFile(opts.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payloadBytes)
	if hex.EncodeToString(sum[:]) != first.Digest {
		t.Fatalf("declared digest does not match archive bytes")
	}
}

func TestArchiveShapeAndMetadata(t *testing.T) {
	opts := buildFixture(t)
	result, err := BuildEmbeddedPayload(opts)
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := os.ReadFile(opts.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	gzr, err := gzip.NewReader(bytes.NewReader(payloadBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer gzr.Close()
	if gzr.Name != "" || !gzr.ModTime.IsZero() {
		t.Fatalf("gzip header leaks identity: name=%q modTime=%v", gzr.Name, gzr.ModTime)
	}
	tr := tar.NewReader(gzr)

	var names []string
	sawMeta := false
	var metaBytes []byte
	for {
		header, readErr := tr.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		names = append(names, header.Name)
		if header.Name == MetaEntryPath && header.Typeflag == tar.TypeReg {
			content, _ := io.ReadAll(tr)
			metaBytes = content
			continue
		}
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("ownership leaked for %s", header.Name)
		}
		if got := header.ModTime.UTC().Unix(); got != opts.SourceDateEpoch {
			t.Fatalf("%s mtime %d != epoch %d", header.Name, got, opts.SourceDateEpoch)
		}
		switch header.Name {
		case ServerEntryPath:
			if header.Mode != execMode {
				t.Fatalf("%s mode %#o", header.Name, header.Mode)
			}
		default:
			if header.Mode != fileMode && header.Mode != dirMode {
				t.Fatalf("%s mode %#o", header.Name, header.Mode)
			}
		}
		if header.Typeflag == tar.TypeReg && header.Name == MetaEntryPath {
			sawMeta = true
		}
	}
	_ = sawMeta
	if len(metaBytes) == 0 {
		t.Fatal("metadata entry missing")
	}
	wantLast := names[len(names)-1]
	if wantLast != MetaEntryPath {
		t.Fatalf("metadata must be the final entry, got order tail: %v", names[len(names)-3:])
	}

	var meta Metadata
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("metadata is not valid json: %v", err)
	}
	fileSet := map[string]FileRecord{}
	for _, record := range meta.Files {
		fileSet[record.Path] = record
	}
	for _, want := range []string{ServerEntryPath,
		"db/migrations/001_init.sql", "db/migrations/010_more.sql",
		"deployment/defaults/attr-rules.yaml", "deployment/defaults/naming-vars.js",
		"frontend/dist/index.html", "frontend/dist/assets/app.js"} {
		if _, ok := fileSet[want]; !ok {
			t.Fatalf("file manifest missing %s (entries: %v)", want, names)
		}
	}
	if result.FileCount != len(meta.Files) {
		t.Fatalf("result file count %d != metadata files %d", result.FileCount, len(meta.Files))
	}
	record := fileSet["frontend/dist/index.html"]
	if record.Size != int64(len("<html></html>")) || record.SHA256 == "" {
		t.Fatalf("bad record: %+v", record)
	}
}

func TestRejections(t *testing.T) {
	opts := buildFixture(t)

	nonExec := opts
	nonExec.ServerBinaryPath = func() string {
		path := filepath.Join(t.TempDir(), "not-exec")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}()
	if _, err := BuildEmbeddedPayload(nonExec); err == nil {
		t.Fatal("non-executable server binary accepted")
	}

	noDist := opts
	noDist.FrontendDistDir = filepath.Join(t.TempDir(), "empty")
	if _, err := BuildEmbeddedPayload(noDist); err == nil {
		t.Fatal("missing frontend dist index.html accepted")
	}

	noMigrations := opts
	noMigrations.MigrationsDir = t.TempDir()
	if _, err := BuildEmbeddedPayload(noMigrations); err == nil {
		t.Fatal("migration-less payload accepted")
	}

	noEpoch := opts
	noEpoch.SourceDateEpoch = 0
	if _, err := BuildEmbeddedPayload(noEpoch); err == nil {
		t.Fatal("zero source date epoch accepted")
	}
}
