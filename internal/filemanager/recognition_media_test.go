package filemanager

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/download/domain"
)

type recognitionProbeFunc func(context.Context, string) (domain.MediaFacts, error)

func (f recognitionProbeFunc) Probe(ctx context.Context, path string) (domain.MediaFacts, error) {
	return f(ctx, path)
}

func TestRecognitionMediaRefreshSupersedesTheOldFlight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "video.mkv")
	writeFile(t, path, 4)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	file := inspection{absolute: path, info: info}
	file.detail.Revision = "rev-1"
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cache := newRecognitionMediaCache(recognitionProbeFunc(func(ctx context.Context, _ string) (domain.MediaFacts, error) {
		switch calls.Add(1) {
		case 1:
			close(started)
			select {
			case <-ctx.Done():
				return domain.MediaFacts{}, ctx.Err()
			case <-release:
				return domain.MediaFacts{Resolution: "old"}, nil
			}
		case 2:
			return domain.MediaFacts{Resolution: "refreshed"}, nil
		default:
			return domain.MediaFacts{Resolution: "new-revision"}, nil
		}
	}))
	finished := make(chan error, 1)
	go func() {
		_, _, err := cache.probe(ctx, file, false, false)
		finished <- err
	}()
	defer close(release)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	probe := func(file inspection, refresh, cacheOnly bool, resolution, reason string) {
		t.Helper()
		facts, gotReason, err := cache.probe(ctx, file, refresh, cacheOnly)
		if err != nil || gotReason != reason || facts.Resolution != resolution {
			t.Fatalf("media resolution=%q, reason=%q, err=%v", facts.Resolution, gotReason, err)
		}
	}
	probe(file, true, false, "refreshed", "")
	// Release the original probe only after the refresh is cached.
	release <- struct{}{}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	probe(file, false, true, "refreshed", "")
	changed := file
	writeFile(t, path, 8)
	changed.info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed.detail.Revision = "rev-2"
	probe(file, false, true, "", "probe_failed")
	probe(changed, false, true, "", "not_loaded")
	probe(changed, false, false, "new-revision", "")
	if calls.Load() != 3 {
		t.Fatalf("probe calls=%d", calls.Load())
	}
}
