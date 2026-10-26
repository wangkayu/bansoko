package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
)

func TestMaintenanceQuarantineReceiptBeforeCaptureDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("frozen source"), 0600); err != nil {
		t.Fatal(err)
	}
	e := NewOSCleanupExecutor(root)
	observed, err := e.Observe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "cleanup_operation", OwnerID: "owner", Kind: "cleanup_capture", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"stage":"capture"}`), Source: source, Target: filepath.Join(root, ".bansoko-cleanup-owner", "object"), Fingerprint: observed.Fingerprint}
	if _, err := e.ReadMaintenanceReceipt(context.Background(), i); !errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("missing capture namespace: %v", err)
	}
	st, err := os.Stat(filepath.Dir(i.Target))
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("private namespace: %v %v", st, err)
	}
	if _, err := os.Stat(i.Target); !os.IsNotExist(err) {
		t.Fatalf("reader created object: %v", err)
	}
	unlock, err := lockMaintenanceInvocation(i)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := e.ReadMaintenanceReceipt(context.Background(), i)
	unlock()
	if readErr == nil || errors.Is(readErr, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("live writer was treated as not started: %v", readErr)
	}
	if _, err := e.MoveMaintenance(context.Background(), source, i.Target, observed, i); err != nil {
		t.Fatal(err)
	}
	if receipt, err := e.ReadMaintenanceReceipt(context.Background(), i); err != nil || receipt.Validate(i) != nil {
		t.Fatalf("resumed exact invocation: %+v %v", receipt, err)
	}
}

func TestMaintenanceQuarantineReceiptRejectsUnsafeCaptureDirectory(t *testing.T) {
	for _, mode := range []string{"public", "symlink", "wrong_owner"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(root, ".bansoko-cleanup-owner")
			i := maintenance.MaintenanceInvocation{OwnerType: "cleanup_operation", OwnerID: "owner", Kind: "cleanup_capture", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"stage":"capture"}`), Source: filepath.Join(root, "source"), Target: filepath.Join(capture, "object"), Fingerprint: "fp"}
			switch mode {
			case "public":
				if err := os.Mkdir(capture, 0755); err != nil {
					t.Fatal(err)
				}
				// Mkdir honours the umask (the qB lane runs under 077).
				if err := os.Chmod(capture, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(root, capture); err != nil {
					t.Fatal(err)
				}
			case "wrong_owner":
				i.OwnerID = "foreign"
			}
			e := NewOSCleanupExecutor(root)
			if _, err := e.ReadMaintenanceReceipt(context.Background(), i); err == nil || errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
				t.Fatalf("unsafe namespace accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "object")); !os.IsNotExist(err) {
				t.Fatalf("unsafe reader touched object: %v", err)
			}
		})
	}
}
