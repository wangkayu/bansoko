package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	acquisitiontorrent "bansoko/internal/acquisition/torrent"
	"bansoko/internal/batchplan"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/downloadcompletion"
)

type neverShared struct{}

func (neverShared) SharedByOther(context.Context, string, string) (bool, error) { return false, nil }

type directoryLibrary struct {
	paths []string
}

func (l directoryLibrary) EachLibraryPath(_ context.Context, visit func(string) error) error {
	for _, path := range l.paths {
		if err := visit(path); err != nil {
			return err
		}
	}
	return nil
}

// TestDownloadCompletionRealQB runs the download-completion actions against
// the real qBittorrent of the lane: pause keeps task and files, remove keeps the
// files, and remove_with_data deletes only the task's own files, leaving the
// library copies untouched. A library path inside the task content is refused
// before qBittorrent is asked to delete anything.
func TestDownloadCompletionRealQB(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	username := strings.TrimSpace(os.Getenv("BANSOKO_QB_USERNAME"))
	password := os.Getenv("BANSOKO_QB_PASSWORD")
	downloadRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT"))
	if endpoint == "" || password == "" || downloadRoot == "" {
		t.Skip("BANSOKO_QB_ENDPOINT/PASSWORD and BANSOKO_HARNESS_DOWNLOAD_ROOT are required for the download completion real qB test")
	}
	ctx := context.Background()
	qb := qbittorrent.Client{Endpoint: endpoint, Username: username, Password: password, Timeout: 20 * time.Second}
	const remoteRoot = "/downloads"
	// The lane's qBittorrent is disposable and its tests run serially. Tasks an
	// earlier test left behind would (correctly) make remove_with_data refuse
	// because they share the fixture's content folder, so start from none.
	// Deleting every task with its data is only allowed against the instance
	// ci-qb-real-chain.sh started and marked disposable, never an endpoint
	// supplied by hand.
	if os.Getenv("BANSOKO_QB_LANE_DISPOSABLE") != "1" {
		t.Fatal("BANSOKO_QB_LANE_DISPOSABLE=1 is required: this test deletes every task of the qBittorrent it is given")
	}
	leftovers, err := qb.ListTorrents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, leftover := range leftovers {
		if err := qb.DeleteTorrent(ctx, leftover.Hash, true); err != nil {
			t.Fatal(err)
		}
	}
	const contentDir = "M1 Multi Real Show"

	// add submits a fresh two-file torrent (the run tag makes its infohash
	// unique) and waits until qBittorrent holds the complete content.
	add := func(t *testing.T, label, savePath string) (hash string, files map[string][]byte) {
		t.Helper()
		tag := time.Now().UTC().Format(time.RFC3339Nano)
		files = map[string][]byte{
			label + " - 01.mkv": bytes.Repeat([]byte(label+tag+"v"), 1200),
			label + " - 02.mkv": bytes.Repeat([]byte(label+tag+"w"), 900),
		}
		fixtureFiles := []torrentFile{{Name: label + " - 01.mkv", Content: files[label+" - 01.mkv"]}, {Name: label + " - 02.mkv", Content: files[label+" - 02.mkv"]}}
		_, _, payload, closeFixture := startMultiFileFixture(t, label, fixtureFiles)
		t.Cleanup(closeFixture)
		task, err := qb.SubmitFile(ctx, payload, label+".torrent", savePath, "completion-"+label+"-"+tag)
		if err != nil {
			t.Fatalf("submit %s: %v", label, err)
		}
		// qB's add answers no infohash (SubmitFile's task carries none): read it
		// from the torrent we submitted, the way the one-shot chain does.
		metadata, err := acquisitiontorrent.Parse(payload)
		if err != nil || metadata.InfoHashV1 == "" {
			t.Fatalf("fixture torrent %s has no v1 info hash: %v", label, err)
		}
		_ = task
		hash = strings.ToLower(metadata.InfoHashV1)
		t.Cleanup(func() { _ = qb.DeleteTorrent(ctx, hash, true) })
		deadline := time.Now().Add(90 * time.Second)
		for {
			facts, found, err := qb.TorrentByHash(ctx, hash)
			if err != nil {
				t.Fatal(err)
			}
			if found && strings.HasSuffix(strings.ToLower(facts.State), "up") {
				return hash, files
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not finish downloading (state %q)", label, facts.State)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	// A library with copies of the downloaded files, like a finished import.
	library := func(t *testing.T, files map[string][]byte) []string {
		t.Helper()
		root := filepath.Join(t.TempDir(), "library")
		var paths []string
		for name, content := range files {
			path := filepath.Join(root, "Show", "S01 "+name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, path)
		}
		return paths
	}

	executor := func(paths []string) downloadcompletion.Executor {
		return downloadcompletion.Executor{
			QB:      downloadcompletion.TorrentAdapter{Client: qb},
			Library: directoryLibrary{paths: paths},
			Sharing: neverShared{},
		}
	}
	completion := func(action, hash string) downloadcompletion.Completion {
		return downloadcompletion.Completion{ID: "c-" + action, AcquisitionID: "a-" + action, Action: action, InfoHash: hash, RemoteRoot: remoteRoot, ApplicationRoot: downloadRoot}
	}
	downloaded := func(name string) string { return filepath.Join(downloadRoot, contentDir, name) }

	t.Run("pause keeps the task and its files", func(t *testing.T) {
		hash, files := add(t, "Completion Pause", remoteRoot)
		paths := library(t, files)
		got := executor(paths).Run(ctx, completion(downloadcompletion.ActionPause, hash))
		if !got.Success || got.Code != downloadcompletion.ResultPaused {
			t.Fatalf("pause = %+v", got)
		}
		facts, found, err := qb.TorrentByHash(ctx, hash)
		if err != nil || !found {
			t.Fatalf("the paused task must stay in qBittorrent: found=%v err=%v", found, err)
		}
		if state := strings.ToLower(facts.State); !strings.HasPrefix(state, "paused") && !strings.HasPrefix(state, "stopped") {
			t.Fatalf("state after pause = %q", facts.State)
		}
		for name := range files {
			if _, err := os.Stat(downloaded(name)); err != nil {
				t.Fatalf("pause removed %s: %v", name, err)
			}
		}
		// Replaying the action on the already paused task is a no-op success.
		if again := executor(paths).Run(ctx, completion(downloadcompletion.ActionPause, hash)); !again.Success {
			t.Fatalf("replayed pause = %+v", again)
		}
	})

	t.Run("remove drops the task and keeps the files", func(t *testing.T) {
		hash, files := add(t, "Completion Remove", remoteRoot)
		paths := library(t, files)
		got := executor(paths).Run(ctx, completion(downloadcompletion.ActionRemove, hash))
		if !got.Success || got.Code != downloadcompletion.ResultRemoved {
			t.Fatalf("remove = %+v", got)
		}
		if _, found, err := qb.TorrentByHash(ctx, hash); err != nil || found {
			t.Fatalf("the task must be gone: found=%v err=%v", found, err)
		}
		for name := range files {
			if _, err := os.Stat(downloaded(name)); err != nil {
				t.Fatalf("remove deleted %s: %v", name, err)
			}
		}
		// Removing again finds nothing to do.
		if again := executor(paths).Run(ctx, completion(downloadcompletion.ActionRemove, hash)); !again.Success || again.Code != downloadcompletion.ResultTaskAbsent {
			t.Fatalf("replayed remove = %+v", again)
		}
		_ = os.RemoveAll(filepath.Join(downloadRoot, contentDir))
	})

	t.Run("remove_with_data deletes the task files and leaves the library alone", func(t *testing.T) {
		hash, files := add(t, "Completion Wipe", remoteRoot)
		paths := library(t, files)
		got := executor(paths).Run(ctx, completion(downloadcompletion.ActionRemoveWithData, hash))
		if !got.Success || got.Code != downloadcompletion.ResultRemovedData {
			t.Fatalf("remove_with_data = %+v", got)
		}
		if _, found, err := qb.TorrentByHash(ctx, hash); err != nil || found {
			t.Fatalf("the task must be gone: found=%v err=%v", found, err)
		}
		for name := range files {
			if _, err := os.Stat(downloaded(name)); !os.IsNotExist(err) {
				t.Fatalf("the downloaded %s must be gone, stat err=%v", name, err)
			}
		}
		// qBittorrent removes the folder it emptied; whatever it leaves must hold no file.
		if entries, err := os.ReadDir(filepath.Join(downloadRoot, contentDir)); err == nil && len(entries) != 0 {
			t.Fatalf("the content folder still holds %d entries", len(entries))
		}
		for _, path := range paths {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("library file %s was lost: %v", path, err)
			}
			if len(content) == 0 {
				t.Fatalf("library file %s was emptied", path)
			}
		}
	})

	t.Run("remove_with_data is refused when a library path lies inside the task content", func(t *testing.T) {
		hash, files := add(t, "Completion Refuse", remoteRoot)
		paths := library(t, files)
		// One placement points into the task's own folder.
		inside := append(append([]string(nil), paths...), downloaded("Completion Refuse - 01.mkv"))
		got := executor(inside).Run(ctx, completion(downloadcompletion.ActionRemoveWithData, hash))
		if got.Success || got.Retryable || got.Code != downloadcompletion.CodePlacementInsideContent {
			t.Fatalf("refusal = %+v", got)
		}
		if _, found, err := qb.TorrentByHash(ctx, hash); err != nil || !found {
			t.Fatalf("a refused action must keep the task: found=%v err=%v", found, err)
		}
		for name := range files {
			if _, err := os.Stat(downloaded(name)); err != nil {
				t.Fatalf("a refused action deleted %s: %v", name, err)
			}
		}
	})

	t.Run("remove_with_data is not blocked by a task in another folder", func(t *testing.T) {
		otherHash, _ := add(t, "Completion Neighbour", remoteRoot+"/neighbour")
		hash, files := add(t, "Completion Alone", remoteRoot)
		paths := library(t, files)
		got := executor(paths).Run(ctx, completion(downloadcompletion.ActionRemoveWithData, hash))
		if !got.Success || got.Code != downloadcompletion.ResultRemovedData {
			t.Fatalf("a task in another folder blocked the delete: %+v", got)
		}
		if _, found, err := qb.TorrentByHash(ctx, otherHash); err != nil || !found {
			t.Fatalf("the other task must be untouched: found=%v err=%v", found, err)
		}
		if _, found, err := qb.TorrentByHash(ctx, hash); err != nil || found {
			t.Fatalf("the task must be gone: found=%v err=%v", found, err)
		}
	})

	t.Run("remove_with_data is refused when another task shares the content folder", func(t *testing.T) {
		otherHash, otherFiles := add(t, "Completion Sibling", remoteRoot)
		hash, files := add(t, "Completion Shared", remoteRoot)
		paths := library(t, files)
		got := executor(paths).Run(ctx, completion(downloadcompletion.ActionRemoveWithData, hash))
		if got.Success || got.Retryable || got.Code != downloadcompletion.CodeContentSharedWithOther {
			t.Fatalf("shared content = %+v", got)
		}
		for _, h := range []string{hash, otherHash} {
			if _, found, err := qb.TorrentByHash(ctx, h); err != nil || !found {
				t.Fatalf("a refused action must keep both tasks: found=%v err=%v", found, err)
			}
		}
		for name := range otherFiles {
			if _, err := os.Stat(downloaded(name)); err != nil {
				t.Fatalf("the other task's file %s is gone: %v", name, err)
			}
		}
	})

	t.Run("seed limits are written to the task and the task keeps seeding", func(t *testing.T) {
		hash, files := add(t, "Completion Limits", remoteRoot+"/limits")
		paths := library(t, files)
		version, err := qb.WebAPIVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		minutes, ratio := int64(4320), 3.5
		c := completion(downloadcompletion.ActionSeed, hash)
		// The frozen roots are the task's own save path, as acceptance freezes them.
		c.RemoteRoot, c.ApplicationRoot = remoteRoot+"/limits", filepath.Join(downloadRoot, "limits")
		c.SeedLimits = &batchplan.SeedLimits{SeedingTimeMinutes: &minutes, Ratio: &ratio, AtLimit: batchplan.SeedLimitRemoveWithData}
		got := executor(paths).Run(ctx, c)
		// qBittorrent 4.6 (WebAPI 2.9.x, the VM chain) has no per-task limit
		// action: the limits are set and its global action applies.
		want := downloadcompletion.ResultSeedLimitsSetGlobalAction
		if parts := strings.Split(version, "."); len(parts) >= 2 {
			major, _ := strconv.Atoi(parts[0])
			minor, _ := strconv.Atoi(parts[1])
			if major > 2 || major == 2 && minor >= 12 {
				want = downloadcompletion.ResultSeedLimitsSet
			}
		}
		if !got.Success || got.Code != want {
			t.Fatalf("seed limits on WebAPI %s = %+v, want %s", version, got, want)
		}
		listed, err := qb.ListTorrents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var task *qbittorrent.TorrentInfo
		for i := range listed {
			if strings.EqualFold(listed[i].Hash, hash) {
				task = &listed[i]
			}
		}
		if task == nil {
			t.Fatal("the task must stay in qBittorrent")
		}
		if task.SeedingTimeLimit != minutes || task.RatioLimit < 3.49 || task.RatioLimit > 3.51 {
			t.Fatalf("limits on the task: ratio %v time %v", task.RatioLimit, task.SeedingTimeLimit)
		}
		if state := strings.ToLower(task.State); strings.HasPrefix(state, "paused") || strings.HasPrefix(state, "stopped") {
			t.Fatalf("the task stopped seeding: %q", task.State)
		}
	})
}
