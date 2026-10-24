package filesystem

import (
	"bansoko/internal/library/domain"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationOverwriteDeniedPreservesFinalTarget(t *testing.T) {
	root, err := filepath.EvalSymlinks(canonicalTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	begins, receipts := 0, 0
	_, err = (OSFileExecutor{}).ExecuteWithPublication(context.Background(), domain.FileOperation{ID: "overwrite-deny", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: digest("new"), SourceSizeKnown: true, SourceSizeBytes: 3, ExpectedSizeKnown: true, ExpectedSizeBytes: 3, CollisionPolicy: domain.CollisionOverwrite}, func(_ context.Context, op domain.FileOperation, _ domain.OverwriteRecovery) (domain.FileOperation, error) {
		op.Version++
		return op, nil
	}, domain.PublicationCallbacks{Begin: func(context.Context, domain.FileOperation, domain.FileObservation) (string, error) {
		begins++
		return "", errors.New("denied")
	}, Finish: func(context.Context, string, domain.FileOperation, domain.FileObservation, error) error {
		receipts++
		return nil
	}})
	if err == nil || begins != 1 || receipts != 0 {
		t.Fatalf("err=%v begins=%d receipts=%d", err, begins, receipts)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatalf("final changed: %q %v", got, err)
	}
}

func TestPublicationCancelAfterPermitStillRecordsExitReceipt(t *testing.T) {
	root, err := filepath.EvalSymlinks(canonicalTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receipts := 0
	_, _ = (OSFileExecutor{}).ExecuteWithPublication(ctx, domain.FileOperation{ID: "cancel", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, SourceSizeKnown: true, SourceSizeBytes: 7, ExpectedSizeKnown: true, ExpectedSizeBytes: 7, CollisionPolicy: domain.CollisionBlock}, nil, domain.PublicationCallbacks{Begin: func(context.Context, domain.FileOperation, domain.FileObservation) (string, error) {
		cancel()
		return "cancel-permit", nil
	}, Finish: func(ctx context.Context, token string, _ domain.FileOperation, actual domain.FileObservation, executeErr error) error {
		receipts++
		if ctx.Err() != nil || token != "cancel-permit" {
			t.Fatal("receipt depended on cancelled execution context")
		}
		if executeErr == nil && (actual.Identity.Inode == 0 || actual.SizeBytes != 7) {
			t.Fatal("success receipt lacks real evidence")
		}
		return nil
	}})
	if receipts != 1 {
		t.Fatalf("exit receipts=%d", receipts)
	}
}
