package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweepTransientResidueRemovesOnlyAbandonedTransients(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	residue := []string{
		// Lowercase hex only; "0" keeps the uppercase near-miss below a
		// distinct name on a case-insensitive test filesystem.
		transientCopyPrefix + strings.Repeat("0", 64),
		transientPlacementPrefix + strings.Repeat("b", 64),
		transientPermitPrefix + strings.Repeat("c", 64),
		transientLinkProbePrefix + strings.Repeat("d", 16),
		transientLinkProbePrefix + strings.Repeat("e", 16) + ".link",
	}
	keep := []string{
		// Fresh entries may belong to a running action.
		transientCopyPrefix + strings.Repeat("f", 64),
		// Durable staging and quarantine names belong to database recovery.
		".bansoko-overwrite-" + strings.Repeat("1", 12),
		".bansoko-quarantine-" + strings.Repeat("2", 24),
		".bansoko-cleanup-" + strings.Repeat("3", 16) + "-object",
		// Near misses of the transient shapes.
		transientCopyPrefix + strings.Repeat("A", 64),
		transientCopyPrefix + strings.Repeat("a", 63),
		transientCopyPrefix + "not-a-digest",
		transientLinkProbePrefix + strings.Repeat("d", 15),
		"Show - E01.mkv",
	}
	for _, name := range residue {
		writeSweepFile(t, filepath.Join(root, name), old)
	}
	for _, name := range keep[:1] {
		writeSweepFile(t, filepath.Join(root, name), time.Now())
	}
	for _, name := range keep[1:] {
		writeSweepFile(t, filepath.Join(root, name), old)
	}
	// A directory and a symlink with a residue shape are not regular files.
	directoryName := transientPlacementPrefix + strings.Repeat("9", 64)
	if err := os.Mkdir(filepath.Join(root, directoryName), 0700); err != nil {
		t.Fatal(err)
	}
	symlinkName := transientCopyPrefix + strings.Repeat("8", 64)
	if err := os.Symlink(filepath.Join(root, "Show - E01.mkv"), filepath.Join(root, symlinkName)); err != nil {
		t.Fatal(err)
	}
	// Residue one level down is out of scope.
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	nestedResidue := filepath.Join(nested, transientCopyPrefix+strings.Repeat("7", 64))
	writeSweepFile(t, nestedResidue, old)

	dirs := []string{root, root, "", "relative/dir", filepath.Join(root, "missing")}
	report, err := SweepTransientResidue(context.Background(), dirs, TransientResidueSweepOptions{
		ModifiedBefore:         time.Now().Add(-time.Hour),
		MaxDirectories:         10,
		MaxEntriesPerDirectory: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Directories != 1 || report.Failed != 0 || report.Truncated {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Removed != len(residue) {
		t.Fatalf("removed=%d want %d: %+v", report.Removed, len(residue), report)
	}
	for _, name := range residue {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("residue %q remains: %v", name, err)
		}
	}
	for _, name := range keep {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("kept entry %q is gone: %v", name, err)
		}
	}
	for _, name := range []string{directoryName, symlinkName} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("non-regular entry %q is gone: %v", name, err)
		}
	}
	if _, err := os.Lstat(nestedResidue); err != nil {
		t.Fatalf("nested residue must not be swept: %v", err)
	}
}

func TestSweepTransientResidueBoundsDirectories(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	firstName := transientCopyPrefix + strings.Repeat("a", 64)
	secondName := transientCopyPrefix + strings.Repeat("b", 64)
	writeSweepFile(t, filepath.Join(first, firstName), old)
	writeSweepFile(t, filepath.Join(second, secondName), old)

	report, err := SweepTransientResidue(context.Background(), []string{first, second}, TransientResidueSweepOptions{
		ModifiedBefore:         time.Now().Add(-time.Hour),
		MaxDirectories:         1,
		MaxEntriesPerDirectory: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated || report.Directories != 1 || report.Removed != 1 {
		t.Fatalf("unexpected bounded report: %+v", report)
	}
	if _, err := os.Lstat(filepath.Join(second, secondName)); err != nil {
		t.Fatalf("the directory past the bound must be untouched: %v", err)
	}
}

func TestSweepTransientResidueEntryBoundIsPerDirectory(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		writeSweepFile(t, filepath.Join(first, name), old)
	}
	residue := filepath.Join(second, transientCopyPrefix+strings.Repeat("a", 64))
	writeSweepFile(t, residue, old)

	report, err := SweepTransientResidue(context.Background(), []string{first, second}, TransientResidueSweepOptions{
		ModifiedBefore:         time.Now().Add(-time.Hour),
		MaxDirectories:         10,
		MaxEntriesPerDirectory: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated || report.Removed != 1 {
		t.Fatalf("the entry bound must apply per directory: %+v", report)
	}
	if _, err := os.Lstat(residue); !os.IsNotExist(err) {
		t.Fatalf("the directory after a truncated one is still swept: %v", err)
	}
}

func TestSweepTransientResidueRequiresCutoff(t *testing.T) {
	if _, err := SweepTransientResidue(context.Background(), []string{t.TempDir()}, TransientResidueSweepOptions{
		MaxDirectories:         1,
		MaxEntriesPerDirectory: 1,
	}); err == nil {
		t.Fatal("a sweep without a modification cutoff must be refused")
	}
}

func TestSweepTransientResidueHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SweepTransientResidue(ctx, []string{t.TempDir()}, TransientResidueSweepOptions{
		ModifiedBefore:         time.Now(),
		MaxDirectories:         1,
		MaxEntriesPerDirectory: 1,
	}); err == nil {
		t.Fatal("a cancelled sweep must return its context error")
	}
}

func writeSweepFile(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}
