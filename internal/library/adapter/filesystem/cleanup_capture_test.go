package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMaintenanceActualQuarantinePurgeFinalReceipts(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, ".bansoko-cleanup-owner", "object")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	e := NewOSCleanupExecutor(root)
	expected, err := e.Observe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "cleanup_operation", OwnerID: "owner", Kind: "cleanup_quarantine", WorkItemID: "work-q", OwnerVersion: 2, Attempt: []byte(`{"stage":"quarantine"}`), Source: source, Target: target, Fingerprint: expected.Fingerprint}
	if _, err := e.MoveMaintenance(context.Background(), source, target, expected, i); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadMaintenanceReceipt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	i.Kind = "cleanup_purge"
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
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("purge did not delete captured object: %v", err)
	}
}
func TestMaintenanceCaptureParentRenameRejectsWithoutReceipt(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, ".bansoko-cleanup-owner", "object")
	if err := os.WriteFile(source, []byte("expected"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = captureCleanupObject(root, source, target, expected, func() error { return os.Rename(filepath.Dir(target), filepath.Dir(target)+"-renamed") })
	if err == nil {
		t.Fatal("renamed private parent accepted")
	}
	raw, err := os.ReadFile(source)
	if err != nil || string(raw) != "expected" {
		t.Fatal("source lost on renamed parent")
	}
}

func TestMaintenanceNamespaceRejectsForeignOwner(t *testing.T) {
	i := maintenance.MaintenanceInvocation{OwnerType: "cleanup_operation", OwnerID: "owner-a", Target: "/root/.bansoko-cleanup-owner-b/object"}
	if err := validateMaintenanceNamespace(i); err == nil {
		t.Fatal("foreign owner directory accepted")
	}
}

func TestMaintenanceCaptureRaceRetainsForeignObjectNeverDeletes(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, ".bansoko-cleanup-owner", "object")
	original := filepath.Join(root, "original")
	if err := os.WriteFile(source, []byte("expected"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = captureCleanupObject(root, source, target, expected, func() error {
		if err := os.Rename(source, original); err != nil {
			return err
		}
		return os.WriteFile(source, []byte("foreign"), 0600)
	})
	if err == nil {
		t.Fatal("foreign capture accepted")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "foreign" {
		t.Fatalf("foreign conflict destroyed: %q %v", raw, err)
	}
	if err := purgeCleanupObject(root, target, expected); err == nil {
		t.Fatal("foreign quarantine deleted")
	}
	raw, err = os.ReadFile(target)
	if err != nil || string(raw) != "foreign" {
		t.Fatal("foreign object lost")
	}
	raw, err = os.ReadFile(original)
	if err != nil || string(raw) != "expected" {
		t.Fatal("original object lost")
	}
}
