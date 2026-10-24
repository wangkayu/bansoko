package adapter

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOSArtifactInspectorReportsStableReadFacts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stable.mkv")
	content := []byte("stable artifact content")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	facts, err := (OSArtifactInspector{}).Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if facts.SizeBytes != int64(len(content)) || facts.SHA256 == "" {
		t.Fatalf("unexpected stable artifact facts: %+v", facts)
	}
	if facts.SHA256 != "d28a38a98d1ed1e8ff03cedf18f5083cc35556e90e27e6d8d26c8b617e4c4300" {
		t.Fatalf("unexpected stable artifact hash: %s", facts.SHA256)
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

// cancelAfterFirstRead serves endless bytes and cancels its context once the
// first chunk has been read, standing in for an attempt deadline mid-hash.
type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	reads  int
}

func (c *cancelAfterFirstRead) Read(p []byte) (int, error) {
	c.reads++
	if c.reads == 1 {
		c.cancel()
	}
	return len(p), nil
}

// The hash read stops at the next chunk once its context is done, instead of
// hashing the rest of the file past the attempt deadline.
func TestContextReaderStopsHashingWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &cancelAfterFirstRead{cancel: cancel}
	n, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx: ctx, r: source}, 1<<40))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy err=%v after %d bytes", err, n)
	}
	if source.reads != 1 {
		t.Fatalf("reads after cancel=%d want 1", source.reads)
	}
}
