package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

// A directory is observed at path level: identity only, no size, and the
// metadata fingerprint carries the directory form.
func TestObserveFileMetadataDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "Season 1")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "ep1.mkv"), []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(directory)
	if err != nil {
		t.Fatal(err)
	}
	facts, ok := domain.ParseMetadataFingerprint(observed.Fingerprint)
	if !ok || !facts.Directory {
		t.Fatalf("directory observation = %q (%+v, %v)", observed.Fingerprint, facts, ok)
	}
	if observed.SizeBytes != 0 || !fileidentity.IsPresent(observed.Identity) {
		t.Fatalf("a directory observation carries identity only: %+v", observed)
	}
	// The hash observer still refuses a directory: content is never read.
	if _, err := observeFile(directory); err == nil {
		t.Fatal("observeFile accepted a directory")
	}
}

// A directory moves through the same Execute chain as a file: the rename takes
// the whole tree, and the target observation reports the directory form.
func TestExecuteDirectoryMove(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Season 1")
	target := filepath.Join(root, "Archive", "Season 1")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "ep1.mkv"), []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	executed, err := (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: target,
		SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock,
	})
	if err != nil {
		t.Fatal(err)
	}
	facts, ok := domain.ParseMetadataFingerprint(executed.TargetFingerprint)
	if !ok || !facts.Directory {
		t.Fatalf("moved directory fingerprint = %q", executed.TargetFingerprint)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move left the source: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(target, "nested", "ep1.mkv"))
	if err != nil || string(raw) != "video" {
		t.Fatalf("moved tree lost its content: %q %v", raw, err)
	}
}

// A directory has no content to copy or exchange: copy is refused outright,
// and a move under the overwrite policy is refused before anything is touched.
func TestExecuteDirectoryRejectsCopyAndOverwrite(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Season 1")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationCopy, SourcePath: source, TargetPath: filepath.Join(root, "copy"),
		SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock,
	})
	var opErr *OperationError
	if !errors.As(err, &opErr) || opErr.Code != CodeSourceChanged {
		t.Fatalf("copy of a directory must fail closed, got %v", err)
	}
	// The overwrite path runs with its durable checkpoint; the directory guard
	// still refuses it after the source is verified.
	i := maintenance.MaintenanceInvocation{OwnerType: "file_manager_operation", OwnerID: "owner", Kind: "file_manager_execute", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"fencing_token":1}`), Source: source, Target: filepath.Join(root, "moved"), Fingerprint: observed.Fingerprint}
	_, err = (OSFileExecutor{}).ExecuteMaintenanceOverwrite(context.Background(), domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: filepath.Join(root, "moved"),
		SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionOverwrite,
	}, i, func(context.Context, domain.FileOperation, domain.OverwriteRecovery) (domain.FileOperation, error) {
		return domain.FileOperation{}, errors.New("checkpoint must never run")
	})
	if !errors.As(err, &opErr) || opErr.Code != CodeSourceChanged {
		t.Fatalf("overwrite of a directory must fail closed, got %v", err)
	}
	if info, err := os.Lstat(source); err != nil || !info.IsDir() {
		t.Fatalf("the refused operations touched the source: %v", err)
	}
}

// An occupied target blocks a directory move under block; under version the
// move lands beside it. Overwrite never applies (covered above).
func TestExecuteDirectoryMoveCollision(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Season 1")
	target := filepath.Join(root, "Season 1", "..", "Taken")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: filepath.Clean(target),
		SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock,
	})
	var opErr *OperationError
	if !errors.As(err, &opErr) || opErr.Code != CodeTargetOccupied {
		t.Fatalf("an occupied directory target must block, got %v", err)
	}
	executed, err := (OSFileExecutor{}).Execute(context.Background(), domain.FileOperation{
		Operation: domain.OperationMove, SourcePath: source, TargetPath: filepath.Clean(target),
		SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed.TargetPath != filepath.Join(root, "Taken (2)") {
		t.Fatalf("versioned directory target = %q", executed.TargetPath)
	}
}
