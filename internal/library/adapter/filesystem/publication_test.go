package filesystem

import (
	"bansoko/internal/library/domain"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationBarrierDenialHasNoFinalEffect(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationHardlink, domain.OperationMove} {
		t.Run(string(kind), func(t *testing.T) {
			root := canonicalTemp(t)
			root, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
			observed, err := observeFile(source)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			receipts := 0
			_, err = (OSFileExecutor{}).ExecuteWithPublication(context.Background(), domain.FileOperation{ID: "op", Operation: kind, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, SourceSizeKnown: true, SourceSizeBytes: 7, ExpectedSizeKnown: true, ExpectedSizeBytes: 7, CollisionPolicy: domain.CollisionBlock}, nil, domain.PublicationCallbacks{Begin: func(_ context.Context, _ domain.FileOperation, e domain.FileObservation) (string, error) {
				calls++
				if e.SizeBytes != 7 {
					t.Fatal("not final staging/source evidence")
				}
				return "", errors.New("reservation denied")
			}, Finish: func(context.Context, string, domain.FileOperation, domain.FileObservation, error) error {
				receipts++
				return nil
			}})
			if err == nil || calls != 1 || receipts != 0 {
				t.Fatalf("err=%v begins=%d receipts=%d", err, calls, receipts)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("denied reservation published: %v", err)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatalf("source lost: %v", err)
			}
		})
	}
}

func TestPublicationMarkerAndReceiptCarryRealIdentity(t *testing.T) {
	root := canonicalTemp(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFile(source)
	if err != nil {
		t.Fatal(err)
	}
	receipts := 0
	_, err = (OSFileExecutor{}).ExecuteWithPublication(context.Background(), domain.FileOperation{ID: "op", Operation: domain.OperationCopy, SourcePath: source, TargetPath: target, SourceFingerprint: observed.Fingerprint, SourceSizeKnown: true, SourceSizeBytes: 7, ExpectedSizeKnown: true, ExpectedSizeBytes: 7, CollisionPolicy: domain.CollisionBlock}, nil, domain.PublicationCallbacks{Begin: func(context.Context, domain.FileOperation, domain.FileObservation) (string, error) {
		return "test-permit", nil
	}, Finish: func(_ context.Context, token string, _ domain.FileOperation, e domain.FileObservation, err error) error {
		receipts++
		if token != "test-permit" || err != nil || e.SizeBytes != 7 || e.Identity.Inode == 0 {
			t.Fatalf("receipt token=%s observation=%+v err=%v", token, e, err)
		}
		return nil
	}})
	if err != nil || receipts != 1 {
		t.Fatalf("err=%v receipts=%d", err, receipts)
	}
	if _, err := os.Stat(filepath.Join(root, ".bansoko-publication-test-permit.json")); err != nil {
		t.Fatal(err)
	}
}
