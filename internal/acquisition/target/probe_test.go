package target

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilesystemProbeUsesRegularFileSizeOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "episode.mkv")
	if err := os.WriteFile(path, []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{ID: "target-1", TargetPath: path, ExpectedSizeBytes: 4}
	probe := FilesystemProbe{Now: func() time.Time { return time.Unix(10, 0) }}
	matched := probe.Observe(spec)
	if matched.State != ObservationPresentMatched || matched.ObservedSizeBytes == nil || *matched.ObservedSizeBytes != 4 || matched.ObservedAt != time.Unix(10, 0) {
		t.Fatalf("unexpected matched observation: %+v", matched)
	}
	spec.ExpectedSizeBytes = 5
	mismatched := probe.Observe(spec)
	if mismatched.State != ObservationPresentMismatched || mismatched.ObservedSizeBytes == nil || *mismatched.ObservedSizeBytes != 4 {
		t.Fatalf("unexpected mismatched observation: %+v", mismatched)
	}
}

func TestFilesystemProbeDoesNotFollowSymlinkAndReportsMissing(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Symlink("missing", target); err != nil {
		t.Fatalf("symlink setup unavailable: %v", err)
	}
	probe := FilesystemProbe{}
	observation := probe.Observe(Spec{ID: "target", TargetPath: target, ExpectedSizeBytes: 1})
	if observation.State != ObservationUnreadable || observation.ReasonCode != "target_symlink" {
		t.Fatalf("symlink must fail closed: %+v", observation)
	}
	missing := probe.Observe(Spec{ID: "missing", TargetPath: filepath.Join(root, "absent"), ExpectedSizeBytes: 1})
	if missing.State != ObservationMissing {
		t.Fatalf("missing target must be observable: %+v", missing)
	}
}
