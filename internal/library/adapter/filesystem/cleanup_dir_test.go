//go:build linux || darwin

package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/testutil/maintenancestore"
)

// A captured directory is purged recursively: the two-phase delete captures the
// whole tree into the private namespace, and the purge removes it depth-first,
// following no symlink, and leaves the receipt behind.
func TestMaintenanceDirectoryQuarantinePurge(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Season 1")
	target := filepath.Join(root, ".bansoko-cleanup-owner", "object")
	if err := os.MkdirAll(filepath.Join(source, "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "ep1.mkv"), []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "deep", "ep2.mkv"), []byte("video2"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.mkv")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the captured tree is unlinked, never followed.
	if err := os.Symlink(outside, filepath.Join(source, "nested", "link.mkv")); err != nil {
		t.Fatal(err)
	}
	e := NewOSCleanupExecutor(root)
	e.Receipts = maintenancestore.New()
	expected, err := e.ObserveMetadataFile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "file_manager_operation", OwnerID: "owner", Kind: "file_manager_quarantine", WorkItemID: "work-q", OwnerVersion: 2, Attempt: []byte(`{"stage":"capture"}`), Source: source, Target: target, Fingerprint: expected.Fingerprint}
	if _, err := e.MoveMaintenance(context.Background(), source, target, expected, i); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadMaintenanceReceipt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	i.Kind = "file_manager_purge"
	i.WorkItemID = "work-p"
	i.OwnerVersion = 4
	i.Attempt = []byte(`{"stage":"purge"}`)
	i.Source = target
	if err := e.PurgeMaintenance(context.Background(), target, expected, i); err != nil {
		t.Fatal(err)
	}
	r, err := e.ReadMaintenanceReceipt(context.Background(), i)
	if err != nil || r.Validate(i) != nil {
		t.Fatalf("purge receipt %+v %v", r, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("purge did not delete captured directory: %v", err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
		t.Fatalf("the purge followed a symlink out of the namespace: %q %v", raw, err)
	}
}

// A directory captured under one fingerprint is not purged when the object at
// the target is another directory (a new inode): the identity check fails
// closed and the foreign tree stays.
func TestMaintenanceDirectoryPurgeRejectsReplacedObject(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Season 1")
	target := filepath.Join(root, ".bansoko-cleanup-owner", "object")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	e := NewOSCleanupExecutor(root)
	e.Receipts = maintenancestore.New()
	expected, err := e.ObserveMetadataFile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "file_manager_operation", OwnerID: "owner", Kind: "file_manager_quarantine", WorkItemID: "work-q", OwnerVersion: 2, Attempt: []byte(`{"stage":"capture"}`), Source: source, Target: target, Fingerprint: expected.Fingerprint}
	if _, err := e.MoveMaintenance(context.Background(), source, target, expected, i); err != nil {
		t.Fatal(err)
	}
	// Replace the captured object with a fresh directory.
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "foreign.mkv"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := purgeCleanupObject(root, target, expected); err == nil {
		t.Fatal("a replaced captured directory was purged")
	}
	if raw, err := os.ReadFile(filepath.Join(target, "foreign.mkv")); err != nil || string(raw) != "foreign" {
		t.Fatalf("the foreign directory lost content: %q %v", raw, err)
	}
}
