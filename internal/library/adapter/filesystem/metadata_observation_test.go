package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
)

// A scan-derived alignment carries a metadata fingerprint. The maintenance
// move verifies it at path level (size + inode), writes and adopts its
// receipts the same way, and lands one file at the target.
func TestMaintenanceMoveWithMetadataFingerprint(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("maintenance"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.IsMetadataFingerprint(observed.Fingerprint) {
		t.Fatalf("metadata observation fingerprint = %q", observed.Fingerprint)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "alignment_operation", OwnerID: "owner", Kind: "alignment_execute", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"fencing_token":1}`), Source: source, Target: target, Fingerprint: observed.Fingerprint}
	executor := OSFileExecutor{}
	executed, err := executor.ExecuteMaintenance(context.Background(), domain.FileOperation{Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock}, i)
	if err != nil {
		t.Fatal(err)
	}
	if executed.TargetFingerprint != observed.Fingerprint || executed.TargetSizeBytes != observed.SizeBytes {
		t.Fatalf("executed target identity = %q/%d, want %q/%d", executed.TargetFingerprint, executed.TargetSizeBytes, observed.Fingerprint, observed.SizeBytes)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move left the source: %v", err)
	}
	r, err := executor.ReadMaintenanceReceipt(context.Background(), i)
	if err != nil || r.Validate(i) != nil {
		t.Fatalf("final receipt: %+v %v", r, err)
	}
	// Crash gap: the final receipt is lost; the started proof plus the
	// metadata identity at the target still adopt the applied move.
	token, err := i.Token()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := PublicationMarkerPath(target, token+"-final")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if r, err := executor.ReadMaintenanceReceipt(context.Background(), i); err != nil || r.Validate(i) != nil {
		t.Fatalf("quiesced metadata move not adopted: %+v %v", r, err)
	}
}

// A different size at the source is a changed file: the move fails closed
// with SOURCE_CHANGED and nothing moves.
func TestExecuteMetadataFingerprintSizeChangeIsSourceChanged(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("maintenance"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("maintenance grown"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock})
	var opErr *OperationError
	if !errors.As(err, &opErr) || opErr.Code != CodeSourceChanged {
		t.Fatalf("size change must be SOURCE_CHANGED, got %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target created despite changed source: %v", err)
	}
}

// A different file (new inode) of the same size at the source path is a
// changed file when the frozen fingerprint recorded an inode.
func TestExecuteMetadataFingerprintReplacedInodeIsSourceChanged(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.mkv")
	target := filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("maintenance"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement.mkv")
	if err := os.WriteFile(replacement, []byte("MAINTENANCE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatal(err)
	}
	_, err = (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock})
	var opErr *OperationError
	if !errors.As(err, &opErr) || opErr.Code != CodeSourceChanged {
		t.Fatalf("replaced inode must be SOURCE_CHANGED, got %v", err)
	}
}

func TestOSFileObserverObserveMetadata(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "file.mkv")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (OSFileObserver{}).ObserveMetadata(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	facts, ok := domain.ParseMetadataFingerprint(got)
	if !ok || facts.SizeBytes != 3 || facts.Inode == 0 {
		t.Fatalf("ObserveMetadata = %q (%+v, %v)", got, facts, ok)
	}
	viaLike, err := maintenance.ObserveLike(context.Background(), OSFileObserver{}, path, got)
	if err != nil || viaLike != got {
		t.Fatalf("ObserveLike(metadata) = %q, %v", viaLike, err)
	}
	digest, err := maintenance.ObserveLike(context.Background(), OSFileObserver{}, path, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil || domain.IsMetadataFingerprint(digest) {
		t.Fatalf("ObserveLike(digest) = %q, %v", digest, err)
	}
}
