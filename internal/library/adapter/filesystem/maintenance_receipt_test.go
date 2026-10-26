package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMaintenanceActualMoveFinalReceiptAndCrashGap(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("maintenance"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "alignment_operation", OwnerID: "owner", Kind: "alignment_execute", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"fencing_token":1}`), Source: source, Target: target, Fingerprint: observed.Fingerprint}
	executor := OSFileExecutor{}
	// A target/source observation before a final receipt is never proof.
	if _, err := executor.ReadMaintenanceReceipt(context.Background(), i); !errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("absent start marker must report not-started, got %v", err)
	}
	_, err = executor.ExecuteMaintenance(context.Background(), domain.FileOperation{Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionBlock}, i)
	if err != nil {
		t.Fatal(err)
	}
	r, err := executor.ReadMaintenanceReceipt(context.Background(), i)
	if err != nil || r.Validate(i) != nil {
		t.Fatalf("actual final receipt: %+v %v", r, err)
	}
	wrong := i
	wrong.OwnerID = "another-owner"
	if _, err := executor.ReadMaintenanceReceipt(context.Background(), wrong); err == nil {
		t.Fatal("foreign owner accepted")
	}
	wrong = i
	wrong.Attempt = []byte(`{"fencing_token":2}`)
	if _, err := executor.ReadMaintenanceReceipt(context.Background(), wrong); err == nil {
		t.Fatal("foreign invocation accepted")
	}
	token, err := i.Token()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := PublicationMarkerPath(target, token+"-final")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the unavoidable process-crash gap after OS effect before fsync
	// receipt: target presence cannot fabricate executor-final proof.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if r, err := executor.ReadMaintenanceReceipt(context.Background(), i); err != nil || r.Validate(i) != nil {
		t.Fatalf("quiesced started move not adopted: %+v %v", r, err)
	}
	started, err := PublicationMarkerPath(target, token+"-started")
	if err != nil {
		t.Fatal(err)
	}
	// A started invocation is never reported as not started, even when its
	// effect cannot be adopted.
	if err := os.WriteFile(source, []byte("maintenance"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := executor.ReadMaintenanceReceipt(context.Background(), i); err == nil || errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("started invocation must stay unresolved, got %v", err)
	}
}
func TestMaintenanceGapSameHashForeignInodeRejected(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("same bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "alignment_operation", OwnerID: "owner", Kind: "alignment_execute", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"fencing_token":1}`), Source: source, Target: target, Fingerprint: o.Fingerprint}
	if err := writeMaintenanceStarted(i); err != nil {
		t.Fatal(err)
	}
	// Different inode with identical bytes must not satisfy rename proof.
	if err := os.WriteFile(target, []byte("same bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := (OSFileExecutor{}).ReadMaintenanceReceipt(context.Background(), i); err == nil {
		t.Fatal("same hash foreign inode adopted")
	}
}

func TestMaintenanceLiveInvocationBlocksReceiptReaderAndDuplicate(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	i := maintenance.MaintenanceInvocation{OwnerType: "alignment_operation", OwnerID: "live-owner", Kind: "alignment_execute", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"fencing_token":1}`), Source: filepath.Join(root, "source"), Target: filepath.Join(root, "target"), Fingerprint: "fp"}
	unlock, err := lockMaintenanceInvocation(i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockMaintenanceInvocation(i); err == nil {
		unlock()
		t.Fatal("duplicate invocation acquired live lock")
	}
	if _, err := (OSFileExecutor{}).ReadMaintenanceReceipt(context.Background(), i); err == nil {
		unlock()
		t.Fatal("reader ignored live executor")
	}
	unlock()
	second, err := lockMaintenanceInvocation(i)
	if err != nil {
		t.Fatal("released executor lock remained live", err)
	}
	second()
}

func TestMaintenancePurgeAbsenceCannotCreateReceipt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing")
	i := maintenance.MaintenanceInvocation{OwnerType: "replacement_deletion", OwnerID: "purge-owner", Kind: "cleanup_purge", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"stage":"purge"}`), Source: path, Target: path, Fingerprint: "fingerprint"}
	e := OSCleanupExecutor{}
	if err := e.PurgeMaintenance(context.Background(), path, domain.FileObservation{Fingerprint: "fingerprint"}, i); err == nil {
		t.Fatal("missing quarantine treated as successful purge")
	}
	if _, err := e.ReadMaintenanceReceipt(context.Background(), i); err == nil {
		t.Fatal("absence created deletion receipt")
	}
}

// TestMaintenanceOverwriteResumesFromPersistedRecord: an alignment overwrite
// interrupted right after any durable checkpoint is finished from the
// persisted recovery record alone; a free target is an ordinary move.
func TestMaintenanceOverwriteResumesFromPersistedRecord(t *testing.T) {
	for _, crashAt := range []domain.OverwriteStage{"", domain.OverwriteStagePrepared, domain.OverwriteStageStaged, domain.OverwriteStageExchanged} {
		for _, occupied := range []bool{false, true} {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source.ass")
			target := filepath.Join(root, "target.ass")
			if err := os.WriteFile(source, []byte("aligned subtitle"), 0600); err != nil {
				t.Fatal(err)
			}
			if occupied {
				if err := os.WriteFile(target, []byte("unmanaged occupant"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := observeFile(source)
			if err != nil {
				t.Fatal(err)
			}
			i := maintenance.MaintenanceInvocation{OwnerType: "alignment_operation", OwnerID: "owner", Kind: "alignment_execute", WorkItemID: "work", OwnerVersion: 2, Attempt: []byte(`{"fencing_token":1}`), Source: source, Target: target, Fingerprint: observed.Fingerprint}
			op := domain.FileOperation{ID: "alignment-1", Operation: domain.OperationMove, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, CollisionPolicy: domain.CollisionOverwrite}
			var persisted *domain.OverwriteRecovery
			crashed := false
			checkpoint := func(_ context.Context, current domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
				copied := recovery
				persisted = &copied
				if !crashed && crashAt != "" && recovery.Stage == crashAt {
					crashed = true
					return current, errors.New("process stopped")
				}
				return current, nil
			}
			name := string(crashAt) + map[bool]string{true: "/occupied", false: "/free"}[occupied]
			_, err = (OSFileExecutor{}).ExecuteMaintenanceOverwrite(context.Background(), op, i, checkpoint)
			if crashed {
				if err == nil {
					t.Fatalf("%s: the interrupted call must not report success", name)
				}
				if persisted == nil {
					t.Fatalf("%s: no record persisted", name)
				}
				resumed := op
				resumed.OverwriteRecovery = persisted
				_, err = (OSFileExecutor{}).ExecuteMaintenanceOverwrite(context.Background(), resumed, i, checkpoint)
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "aligned subtitle" {
				t.Fatalf("%s: target=%q err=%v", name, got, err)
			}
			if _, err := os.Stat(source); !os.IsNotExist(err) {
				t.Fatalf("%s: source must be moved: %v", name, err)
			}
			if entries, _ := filepath.Glob(filepath.Join(root, ".bansoko-overwrite-*")); len(entries) != 0 {
				t.Fatalf("%s: staging left behind: %v", name, entries)
			}
			if r, err := (OSFileExecutor{}).ReadMaintenanceReceipt(context.Background(), i); err != nil || r.Validate(i) != nil {
				t.Fatalf("%s: final receipt: %+v %v", name, r, err)
			}
		}
	}
}
