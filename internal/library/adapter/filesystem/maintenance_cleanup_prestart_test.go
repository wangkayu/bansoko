package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"bansoko/internal/testutil/maintenancestore"
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
	store := maintenancestore.New()
	e := NewOSCleanupExecutor(root)
	e.Receipts = store
	observed, err := e.Observe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "replacement_deletion", OwnerID: "owner", Kind: "cleanup_capture", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"stage":"capture"}`), Source: source, Target: filepath.Join(root, ".bansoko-cleanup-owner", "object"), Fingerprint: observed.Fingerprint}
	if _, err := e.ReadMaintenanceReceipt(context.Background(), i); !errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("missing start proof: %v", err)
	}
	// The receipt lives in the store now: a read never creates the capture
	// namespace, and no object appears at the target.
	if _, err := os.Stat(filepath.Dir(i.Target)); !os.IsNotExist(err) {
		t.Fatalf("reader created the namespace: %v", err)
	}
	if _, err := os.Stat(i.Target); !os.IsNotExist(err) {
		t.Fatalf("reader created object: %v", err)
	}
	token, err := i.Token()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.Lock(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := e.ReadMaintenanceReceipt(context.Background(), i)
	unlock()
	if !errors.Is(readErr, maintenance.ErrMaintenanceLocked) {
		t.Fatalf("live writer was treated as readable: %v", readErr)
	}
	if _, err := e.MoveMaintenance(context.Background(), source, i.Target, observed, i); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Dir(i.Target))
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("private namespace: %v %v", st, err)
	}
	if receipt, err := e.ReadMaintenanceReceipt(context.Background(), i); err != nil || receipt.Validate(i) != nil {
		t.Fatalf("resumed exact invocation: %+v %v", receipt, err)
	}
}

// The capture move refuses an unsafe private namespace before anything is
// captured: a pre-existing public directory, a symlink in its place, or a
// namespace that does not bind the exact owner.
func TestMaintenanceQuarantineMoveRejectsUnsafeCaptureDirectory(t *testing.T) {
	for _, mode := range []string{"public", "symlink", "wrong_owner"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(root, ".bansoko-cleanup-owner")
			i := maintenance.MaintenanceInvocation{OwnerType: "replacement_deletion", OwnerID: "owner", Kind: "cleanup_capture", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"stage":"capture"}`), Source: filepath.Join(root, "source"), Target: filepath.Join(capture, "object"), Fingerprint: "fp"}
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
			e.Receipts = maintenancestore.New()
			if _, err := e.MoveMaintenance(context.Background(), i.Source, i.Target, domain.FileObservation{Fingerprint: "fp"}, i); err == nil {
				t.Fatal("unsafe namespace accepted")
			}
			if _, err := os.Stat(filepath.Join(capture, "object")); !os.IsNotExist(err) {
				t.Fatalf("unsafe capture touched object: %v", err)
			}
		})
	}
}
