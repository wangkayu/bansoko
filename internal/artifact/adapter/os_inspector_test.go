package adapter

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOSArtifactInspectorReportsPathLevelFacts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stable.mkv")
	content := []byte("stable artifact content")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}

	facts, err := (OSArtifactInspector{}).Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if facts.SizeBytes != int64(len(content)) || !facts.ModifiedAt.Equal(modified) || facts.MediaKind != "video" {
		t.Fatalf("unexpected path-level facts: %+v", facts)
	}
	if runtime.GOOS != "windows" && (facts.Device == 0 || facts.Inode == 0) {
		t.Fatalf("device/inode missing: %+v", facts)
	}
}

// A sparse file far larger than any read could finish in the test window
// proves the observation never reads content.
func TestOSArtifactInspectorDoesNotReadContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.mkv")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size = int64(1) << 40
	if err := file.Truncate(size); err != nil {
		file.Close()
		t.Skipf("sparse file unsupported: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	facts, err := (OSArtifactInspector{}).Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if facts.SizeBytes != size {
		t.Fatalf("size=%d want %d", facts.SizeBytes, size)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("inspection took %s; it must not scale with the file size", elapsed)
	}
}

func TestOSArtifactInspectorRejectsSymlinkEmptyAndDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.mkv")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := (OSArtifactInspector{}).Inspect(context.Background(), link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink err=%v", err)
	}
	empty := filepath.Join(root, "empty.mkv")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (OSArtifactInspector{}).Inspect(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "must be a non-empty regular file") {
		t.Fatalf("empty err=%v", err)
	}
	if _, err := (OSArtifactInspector{}).Inspect(context.Background(), root); err == nil || !strings.Contains(err.Error(), "must be a non-empty regular file") {
		t.Fatalf("directory err=%v", err)
	}
}

func TestOSArtifactInspectorInspectsAnyReportedPath(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	firstPath := filepath.Join(first, "first.mkv")
	secondPath := filepath.Join(second, "second.mkv")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The application performs no placement authorization: paths reported by
	// the provider are inspected wherever the deployment mounted them.
	inspector := OSArtifactInspector{}
	for _, path := range []string{firstPath, secondPath} {
		facts, err := inspector.Inspect(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if facts.Path != resolved || facts.MediaKind != "video" {
			t.Fatalf("unexpected facts for %q: %+v", path, facts)
		}
	}
}
