package qbittorrent

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveHealth is opt-in so ordinary unit runs remain hermetic. CI can set
// the four BANSOKO_QB_* variables when a real qBittorrent service is available.
func TestLiveHealth(t *testing.T) {
	endpoint := os.Getenv("BANSOKO_QB_ENDPOINT")
	if endpoint == "" {
		t.Skip("BANSOKO_QB_ENDPOINT is not configured")
	}
	client := Client{Endpoint: endpoint, Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 5 * time.Second}
	version, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version == "" {
		t.Fatal("qBittorrent returned an empty version")
	}
}

// TestLiveSync is opt-in and exercises the qB task lookup and file listing
// against an already-existing torrent. Under the same-path deployment
// convention qB-reported paths are used as-is. The caller must apply the
// supplied tag to that torrent first.
func TestLiveSync(t *testing.T) {
	endpoint := os.Getenv("BANSOKO_QB_ENDPOINT")
	taskID := os.Getenv("BANSOKO_QB_LIVE_TASK_ID")
	if endpoint == "" || taskID == "" {
		t.Skip("BANSOKO_QB_ENDPOINT and BANSOKO_QB_LIVE_TASK_ID are required")
	}
	client := Client{Endpoint: endpoint, Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 5 * time.Second}
	task, err := client.Sync(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Hash == "" || task.State == "" {
		t.Fatalf("unexpected live qB task: %+v", task)
	}
	if task.State == "completed" && task.Path == "" {
		t.Fatalf("completed qB task did not return a primary artifact: %+v", task)
	}
}
