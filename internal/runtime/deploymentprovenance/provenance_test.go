package deploymentprovenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validRecord() Record {
	return Record{
		SchemaVersion:          FormatVersion,
		ReleaseID:              "release-1",
		PayloadDigest:          strings.Repeat("a", 64),
		ProducingOperationID:   "op-1",
		ExpectedProcessBuildID: "build-1",
	}
}

func TestWriteReadRoundTripAndIdempotency(t *testing.T) {
	root := t.TempDir()
	record := validRecord()
	if err := Write(root, record); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root, record.ReleaseID)
	if err != nil || got != record {
		t.Fatalf("read = %+v, err=%v; want %+v", got, err, record)
	}
	if err := WriteIfMatching(root, record); err != nil {
		t.Fatal(err)
	}
	if err := WriteIfMatching(root, Record{ReleaseID: record.ReleaseID, SchemaVersion: FormatVersion, PayloadDigest: strings.Repeat("b", 64), ProducingOperationID: "op-2", ExpectedProcessBuildID: "build-2"}); err == nil {
		t.Fatal("different provenance must be rejected")
	}
}

func TestReadRejectsMalformedAndSymlinkEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, Directory), 0o700); err != nil {
		t.Fatal(err)
	}
	path := Path(root, "release-1")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "release-1"); err == nil {
		t.Fatal("incomplete provenance must be rejected")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "release-1"); err == nil {
		t.Fatal("symlink provenance must be rejected")
	}
}

func TestValidateBindsReleasePayloadAndBuild(t *testing.T) {
	record := validRecord()
	for name, args := range map[string][3]string{
		"release": {"release-2", record.PayloadDigest, record.ExpectedProcessBuildID},
		"payload": {record.ReleaseID, strings.Repeat("b", 64), record.ExpectedProcessBuildID},
		"build":   {record.ReleaseID, record.PayloadDigest, "build-2"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := record.Validate(args[0], args[1], args[2]); err == nil {
				t.Fatal("mismatched provenance must be rejected")
			}
		})
	}
}

// TestWriteRefusesSymlinkedDirectory pins the hardening of the provenance write
// path: a symlink planted at the provenance directory (or at its parent) must
// fail closed, and nothing may be created outside the updates root.
func TestWriteRefusesSymlinkedDirectory(t *testing.T) {
	record := Record{
		SchemaVersion:          FormatVersion,
		ReleaseID:              "release-symlink",
		PayloadDigest:          strings.Repeat("a", 64),
		ProducingOperationID:   "embedded-symlink-test",
		ExpectedProcessBuildID: "build-symlink",
	}

	t.Run("provenance directory", func(t *testing.T) {
		outside := t.TempDir()
		root := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, Directory)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := Write(root, record); err == nil {
			t.Fatal("Write published through a symlinked provenance directory")
		}
		if err := WriteIfMatching(root, record); err == nil {
			t.Fatal("WriteIfMatching published through a symlinked provenance directory")
		}
		assertDirEmpty(t, outside)
	})

	t.Run("updates root", func(t *testing.T) {
		outside := t.TempDir()
		parent := t.TempDir()
		root := filepath.Join(parent, "updates")
		if err := os.Symlink(outside, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := Write(root, record); err == nil {
			t.Fatal("Write published through a symlinked updates root")
		}
		assertDirEmpty(t, outside)
	})
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a write escaped into %s: %v", dir, entries)
	}
}
