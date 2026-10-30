package qbittorrent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bansoko/internal/download/domain"
)

type testRuntimeConfigSource struct {
	config RuntimeConfig
	err    error
	calls  int
}

func (s *testRuntimeConfigSource) GetQBittorrentRuntimeConfig(context.Context) (RuntimeConfig, error) {
	s.calls++
	if s.err != nil {
		return RuntimeConfig{}, s.err
	}
	return s.config, nil
}

func TestClientSyncMapsCompletedTorrentAndArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Set-Cookie", "SID=test; Path=/")
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-key" {
				t.Fatalf("unexpected query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"added_on":2,"content_path":"/downloads/Show","hash":"abc","name":"Show","progress":1,"state":"stalledup"}]`))
		case "/api/v2/torrents/files":
			if r.URL.Query().Get("hash") != "abc" {
				t.Fatalf("unexpected hash query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"name":"Show/cover.jpg","progress":1,"size":2048},{"name":"Show/Show - 01.mkv","progress":1,"size":42},{"name":"Show/Show.zh-Hans.ass","progress":1,"size":8},{"name":"Show/Show.ass","progress":0.5,"size":8}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	task, err := client.Sync(context.Background(), "tag:bansoko-key")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskCompleted || task.Hash != "abc" || task.Progress != 1 {
		t.Fatalf("unexpected provider task: %+v", task)
	}
	if len(task.Paths) != 3 || task.Paths[0] != "/downloads/Show/Show - 01.mkv" {
		t.Fatalf("unexpected artifact paths: %+v", task.Paths)
	}
	if task.Path != task.Paths[0] {
		t.Fatalf("primary artifact path should be first completed media file: %+v", task)
	}
	var subtitle domain.ProviderFile
	for _, file := range task.Files {
		if file.MediaKind == "subtitle" {
			subtitle = file
		}
	}
	if len(task.Files) != 3 || subtitle.Path == "" || subtitle.Language != "zh-Hans" {
		t.Fatalf("completed provider files did not preserve subtitle facts: %+v", task.Files)
	}
}

// TestClientSyncDropsIncoherentByteObservation covers the one read qB cannot
// guarantee: it derives the content-complete count and the payload total
// separately, so one response can pair a content-complete count with a smaller
// total. Complete bytes above the total are provider evidence corruption rather
// than progress, and such a read carries no trustworthy mark at all -- the
// observation must hand the owner neither field instead of a corrupt pair.
func TestClientSyncDropsIncoherentByteObservation(t *testing.T) {
	observation := `[{"added_on":2,"content_path":"/downloads/Show","completed":4096,"hash":"abc","name":"Show","progress":1,"size":2048,"state":"stalledup"}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Header().Set("Set-Cookie", "SID=test; Path=/")
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(observation))
		case "/api/v2/torrents/files":
			_, _ = w.Write([]byte(`[{"name":"Show/Show - 01.mkv","progress":1,"size":2048}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	task, err := client.Sync(context.Background(), "tag:bansoko-key")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskCompleted || task.ContentCompleteBytes != nil || task.PayloadTotalBytes != nil {
		t.Fatalf("incoherent provider bytes must not reach the observation: %+v", task)
	}
	// The same torrent read coherently keeps both fields, so this is a judgment
	// about one observation rather than a lost capability.
	observation = `[{"added_on":2,"content_path":"/downloads/Show","completed":2048,"hash":"abc","name":"Show","progress":1,"size":2048,"state":"stalledup"}]`
	task, err = client.Sync(context.Background(), "tag:bansoko-key")
	if err != nil {
		t.Fatal(err)
	}
	if task.ContentCompleteBytes == nil || *task.ContentCompleteBytes != 2048 || task.PayloadTotalBytes == nil || *task.PayloadTotalBytes != 2048 {
		t.Fatalf("coherent provider bytes must reach the observation: %+v", task)
	}
	// The reported download rate rides along for display; a reported zero
	// stays a zero.
	if task.DownloadSpeed == nil || *task.DownloadSpeed != 0 {
		t.Fatalf("the reported speed must reach the observation: %+v", task.DownloadSpeed)
	}
	observation = `[{"completed":1024,"hash":"abc","name":"Show","progress":0.5,"size":2048,"state":"downloading","dlspeed":4096}]`
	task, err = client.Sync(context.Background(), "tag:bansoko-key")
	if err != nil {
		t.Fatal(err)
	}
	if task.DownloadSpeed == nil || *task.DownloadSpeed != 4096 {
		t.Fatalf("the reported speed must reach the observation: %+v", task.DownloadSpeed)
	}
}

func TestClientSubmitFileUsesMultipartFieldsAndNoURLs(t *testing.T) {
	payload := []byte("torrent-payload")
	var added bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-file-key" {
				t.Fatalf("unexpected tag lookup: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/add":
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Fatalf("expected multipart content type, got %q", r.Header.Get("Content-Type"))
			}
			mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
				t.Fatalf("invalid multipart content type: %q, err=%v", r.Header.Get("Content-Type"), err)
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			fields := make(map[string]string)
			files := make(map[string]struct {
				filename string
				body     []byte
			})
			for {
				part, nextErr := reader.NextPart()
				if errors.Is(nextErr, io.EOF) {
					break
				}
				if nextErr != nil {
					t.Fatal(nextErr)
				}
				partBody, readErr := io.ReadAll(part)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if part.FileName() != "" {
					files[part.FormName()] = struct {
						filename string
						body     []byte
					}{filename: part.FileName(), body: partBody}
					continue
				}
				fields[part.FormName()] = string(partBody)
			}
			if _, exists := fields["urls"]; exists {
				t.Fatal("file submission must not include urls")
			}
			torrent, exists := files["torrents"]
			if !exists || torrent.filename != "show.torrent" || !bytes.Equal(torrent.body, payload) {
				t.Fatalf("unexpected torrent file: %+v", torrent)
			}
			if fields["savepath"] != "/downloads" || fields["category"] != "anime" || fields["tags"] != "bansoko-file-key,seasonal,tv" {
				t.Fatalf("unexpected multipart fields: %v", fields)
			}
			added = true
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, err := (Client{Endpoint: server.URL, Category: "anime", UserTags: []string{"seasonal", "tv"}}).
		SubmitFile(context.Background(), payload, "show.torrent", "/downloads", "file-key")
	if err != nil {
		t.Fatal(err)
	}
	if !added || task.ID != "tag:bansoko-file-key" || task.State != domain.TaskSubmitted {
		t.Fatalf("unexpected file submission result: %+v, added=%v", task, added)
	}
}

func TestClientSubmitFileWithSnapshotUsesFrozenRemoteRoot(t *testing.T) {
	snapshot := domain.PathSnapshot{
		RemoteRoot: "/frozen/downloads",
	}
	var savePath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-frozen-file-key" {
				t.Fatalf("unexpected tag lookup: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			savePath = r.FormValue("savepath")
			if r.FormValue("urls") != "" {
				t.Fatal("snapshot file submission must not include urls")
			}
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, err := (Client{Endpoint: server.URL}).SubmitFileWithSnapshot(context.Background(), []byte("payload"), "show.torrent", snapshot, "frozen-file-key")
	if err != nil {
		t.Fatal(err)
	}
	if savePath != "/frozen/downloads" {
		t.Fatalf("multipart savepath = %q, want frozen root", savePath)
	}
	wantSnapshot := domain.PathSnapshot{
		RemoteRoot:      "/frozen/downloads",
		ApplicationRoot: "/frozen/downloads",
	}
	if task.PathSnapshot == nil || !PathSnapshotsEqual(*task.PathSnapshot, wantSnapshot) {
		t.Fatalf("submission did not preserve normalized snapshot: %+v", task.PathSnapshot)
	}
}

func TestClientSubmitFileWithSnapshotReusesExistingTag(t *testing.T) {
	snapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/data/downloads"}
	var added int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-existing-frozen-file-key" {
				t.Fatalf("unexpected tag lookup: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"added_on":2,"hash":"abc","name":"Show","progress":0.5,"state":"downloading"}]`))
		case "/api/v2/torrents/add":
			added++
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, err := (Client{Endpoint: server.URL}).SubmitFileWithSnapshot(context.Background(), []byte("payload"), "show.torrent", snapshot, "existing-frozen-file-key")
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || task.ID != "tag:bansoko-existing-frozen-file-key" || task.PathSnapshot == nil || !PathSnapshotsEqual(*task.PathSnapshot, snapshot) {
		t.Fatalf("existing tag must be reused with frozen snapshot: task=%+v added=%d", task, added)
	}
}

func TestClientSubmitFileReusesExistingTagBeforeUploading(t *testing.T) {
	var added int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-existing-file-key" {
				t.Fatalf("unexpected tag lookup: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"added_on":2,"hash":"abc","name":"Show","progress":0.5,"state":"downloading"}]`))
		case "/api/v2/torrents/add":
			added++
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, err := (Client{Endpoint: server.URL}).SubmitFile(context.Background(), []byte("payload"), "show.torrent", "/downloads", "existing-file-key")
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || task.ID != "tag:bansoko-existing-file-key" || task.State != domain.TaskDownloading {
		t.Fatalf("existing qB tag should be reused without upload, got task=%+v added=%d", task, added)
	}
}

func TestClientSubmitFileRejectsEmptyPayloadAndInvalidFilename(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("invalid file submission must not make requests: %s", r.URL.Path)
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL}
	for _, test := range []struct {
		name     string
		payload  []byte
		filename string
	}{
		{name: "empty payload", payload: nil, filename: "show.torrent"},
		{name: "path filename", payload: []byte("payload"), filename: "../show.torrent"},
		{name: "slash filename", payload: []byte("payload"), filename: "nested/show.torrent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.SubmitFile(context.Background(), test.payload, test.filename, "/downloads", "invalid-key"); err == nil {
				t.Fatal("invalid file submission should fail")
			}
		})
	}
}

func TestConfiguredClientSubmitFileUsesRuntimeConfig(t *testing.T) {
	payload := []byte("configured-payload")
	var added bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "typed-user" || r.Form.Get("password") != "typed-secret" {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-configured-file-key" {
				t.Fatalf("unexpected tag lookup: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/add":
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Fatalf("expected multipart content type, got %q", r.Header.Get("Content-Type"))
			}
			mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/form-data" {
				t.Fatalf("invalid multipart content type: %q, err=%v", r.Header.Get("Content-Type"), err)
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, nextErr := reader.NextPart()
				if errors.Is(nextErr, io.EOF) {
					break
				}
				if nextErr != nil {
					t.Fatal(nextErr)
				}
				partBody, readErr := io.ReadAll(part)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if part.FormName() == "torrents" && part.FileName() == "configured.torrent" && bytes.Equal(partBody, payload) {
					added = true
				}
			}
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := &testRuntimeConfigSource{config: RuntimeConfig{Endpoint: server.URL, Username: "typed-user", Password: "typed-secret"}}
	task, err := (ConfiguredClient{Source: source}).SubmitFile(context.Background(), payload, "configured.torrent", "/downloads", "configured-file-key")
	if err != nil {
		t.Fatal(err)
	}
	if !added || task.ID != "tag:bansoko-configured-file-key" || source.calls != 1 {
		t.Fatalf("configured file submission failed: task=%+v added=%v source.calls=%d", task, added, source.calls)
	}
}

func TestConfiguredClientSubmitFileWithSnapshotUsesFrozenConfig(t *testing.T) {
	snapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}
	var added bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("savepath") != "/downloads" {
				t.Fatalf("savepath = %q, want frozen root", r.FormValue("savepath"))
			}
			added = true
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := &testRuntimeConfigSource{config: RuntimeConfig{
		Endpoint: server.URL, SaveRoot: "/downloads",
	}}
	task, err := (ConfiguredClient{Source: source}).SubmitFileWithSnapshot(context.Background(), []byte("payload"), "show.torrent", snapshot, "configured-frozen-file-key")
	if err != nil {
		t.Fatal(err)
	}
	if !added || source.calls != 1 || task.PathSnapshot == nil || !PathSnapshotsEqual(*task.PathSnapshot, snapshot) {
		t.Fatalf("configured snapshot upload failed: task=%+v added=%v source.calls=%d", task, added, source.calls)
	}
}

func TestConfiguredClientSubmitFileWithSnapshotRejectsPathDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("path drift must fail before qB request: %s", r.URL.Path)
	}))
	defer server.Close()

	source := &testRuntimeConfigSource{config: RuntimeConfig{
		Endpoint: server.URL, SaveRoot: "/changed/downloads",
	}}
	snapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}
	_, err := (ConfiguredClient{Source: source}).SubmitFileWithSnapshot(context.Background(), []byte("payload"), "show.torrent", snapshot, "drift-file-key")
	if !errors.Is(err, ErrPathSnapshotChanged) || source.calls != 1 {
		t.Fatalf("path drift must be classified before upload: err=%v source.calls=%d", err, source.calls)
	}
}

func TestClientSubmitFileUsesTypedCategoryAndUserTags(t *testing.T) {
	var addForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			addForm = r.Form
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := (Client{Endpoint: server.URL, Category: "anime", UserTags: []string{"seasonal", "tv"}}).
		SubmitFile(context.Background(), []byte("payload"), "show.torrent", "/downloads", "typed-key")
	if err != nil {
		t.Fatal(err)
	}
	if addForm.Get("category") != "anime" || addForm.Get("tags") != "bansoko-typed-key,seasonal,tv" {
		t.Fatalf("typed qB fields were not submitted: %v", addForm)
	}
}

func TestDerivePathSnapshotFreezesApplicationRootAndDetectsDrift(t *testing.T) {
	config := RuntimeConfig{
		SaveRoot: "/downloads/anime",
	}
	snapshot, err := DerivePathSnapshot(config)
	if err != nil {
		t.Fatal(err)
	}
	// Under the same-path deployment convention the application root defaults
	// to the qB-reported root.
	if snapshot.RemoteRoot != "/downloads/anime" || snapshot.ApplicationRoot != "/downloads/anime" {
		t.Fatalf("unexpected path snapshot roots: %+v", snapshot)
	}
	if !PathSnapshotsEqual(snapshot, domain.PathSnapshot{
		RemoteRoot:      "/downloads/anime",
		ApplicationRoot: "/downloads/anime",
	}) {
		t.Fatal("equivalent path snapshots should compare equal")
	}
	changed := snapshot
	changed.RemoteRoot = "/mnt/anime"
	if PathSnapshotsEqual(snapshot, changed) {
		t.Fatal("changed root should be detected as drift")
	}
}

func TestDerivePathSnapshotMapsExplicitDownloaderRoot(t *testing.T) {
	// When the qB savepath lives in a container (download_root) but the app
	// reads from a different host path (downloader_root), the frozen snapshot
	// keeps RemoteRoot = qB savepath and ApplicationRoot = host root.
	config := RuntimeConfig{
		SaveRoot:        "/downloads",
		ApplicationRoot: "/user/downloads",
	}
	snapshot, err := DerivePathSnapshot(config)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RemoteRoot != "/downloads" || snapshot.ApplicationRoot != "/user/downloads" {
		t.Fatalf("mapped snapshot must keep both roots distinct: %+v", snapshot)
	}
	// Drift on either root is detected: changing the host root back to the
	// same-path value no longer matches the frozen mapping.
	changed := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}
	if PathSnapshotsEqual(snapshot, changed) {
		t.Fatal("downloader_root drift must be detected as a changed snapshot")
	}
}

func TestClientCancelResolvesTagToHash(t *testing.T) {
	var deletedHash, deletedTags string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "" {
				if r.URL.Query().Get("tag") != "bansoko-cancel-key" {
					t.Fatalf("unexpected tag lookup: %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`[{"added_on":2,"hash":"abc","name":"Show","progress":0.5,"state":"downloading"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/delete":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deletedHash = r.Form.Get("hashes")
		case "/api/v2/torrents/deleteTags":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deletedTags = r.Form.Get("tags")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := (Client{Endpoint: server.URL}).Cancel(context.Background(), "tag:bansoko-cancel-key"); err != nil {
		t.Fatal(err)
	}
	if deletedHash != "abc" || deletedTags != "bansoko-cancel-key" {
		t.Fatalf("delete hash=%q cleanup tags=%q", deletedHash, deletedTags)
	}
}

func TestClientCancelWaitsForAsynchronousDeleteBeforeCleaningTheTag(t *testing.T) {
	var deletedTags string
	plainReads, readsAtCleanup := 0, -1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "" {
				_, _ = w.Write([]byte(`[{"hash":"abc","name":"Show","state":"downloading","tags":"bansoko-cancel-async"}]`))
				return
			}
			plainReads++
			// qB applies the delete asynchronously: the first list right after the
			// delete request still shows the task.
			if plainReads == 1 {
				_, _ = w.Write([]byte(`[{"hash":"abc","name":"Show","state":"downloading","tags":"bansoko-cancel-async"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/delete":
		case "/api/v2/torrents/deleteTags":
			readsAtCleanup = plainReads
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deletedTags = r.Form.Get("tags")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := (Client{Endpoint: server.URL}).Cancel(context.Background(), "tag:bansoko-cancel-async"); err != nil {
		t.Fatal(err)
	}
	if readsAtCleanup < 1 {
		t.Fatalf("cleanup ran after %d list reads, want the delete confirmed first", readsAtCleanup)
	}
	if plainReads < 2 {
		t.Fatalf("list reads = %d, want the delete confirmed by a second read", plainReads)
	}
	if deletedTags != "bansoko-cancel-async" {
		t.Fatalf("cleanup tags = %q", deletedTags)
	}
}

func TestClientCancelKeepsTheTagWhileTheDeletedTaskStaysListed(t *testing.T) {
	previousWindow := deleteVerifyWindow
	deleteVerifyWindow = 0
	t.Cleanup(func() { deleteVerifyWindow = previousWindow })

	var deletedTags string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"hash":"abc","name":"Show","state":"downloading","tags":"bansoko-cancel-stuck"}]`))
		case "/api/v2/torrents/delete":
		case "/api/v2/torrents/deleteTags":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deletedTags = r.Form.Get("tags")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := (Client{Endpoint: server.URL}).Cancel(context.Background(), "tag:bansoko-cancel-stuck")
	if err == nil || !strings.Contains(err.Error(), "still listed") {
		t.Fatalf("a task still listed after delete must fail the cancel: %v", err)
	}
	if deletedTags != "" {
		t.Fatalf("a still-listed task's tag was cleaned: %q", deletedTags)
	}
}

func TestClientCancelMissingTaggedTaskIsAlreadyCanceled(t *testing.T) {
	var deletedTags string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/delete":
			t.Fatal("qB delete must not run when the tagged task is already absent")
		case "/api/v2/torrents/deleteTags":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			deletedTags = r.Form.Get("tags")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := (Client{Endpoint: server.URL}).Cancel(context.Background(), "tag:bansoko-already-gone"); err != nil {
		t.Fatalf("missing tagged task should be an idempotent cancellation: %v", err)
	}
	if deletedTags != "bansoko-already-gone" {
		t.Fatalf("absent cancel cleanup tags = %q", deletedTags)
	}
}

func TestClientLookupByOperationKeyResolvesStableTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "bansoko-crash-key" {
				t.Fatalf("unexpected operation lookup tag: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"added_on":4,"hash":"abc","name":"Show","progress":0.5,"state":"downloading"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, found, err := (Client{Endpoint: server.URL}).LookupByOperationKey(context.Background(), "crash-key")
	if err != nil || !found || task.ID != "tag:bansoko-crash-key" {
		t.Fatalf("stable operation lookup = task=%+v found=%v err=%v", task, found, err)
	}
}

func TestClientRejectsMultipleStableTagMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"added_on":4,"hash":"abc"},{"added_on":5,"hash":"def"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, found, err := (Client{Endpoint: server.URL}).LookupByOperationKey(context.Background(), "duplicated-key")
	if !errors.Is(err, ErrLookupAmbiguous) || found {
		t.Fatalf("multiple stable-tag matches must fail closed: found=%v err=%v", found, err)
	}
}

func TestClientLookupByInfoHashesResolvesFullV2Hash(t *testing.T) {
	fullV2Hash := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if got := r.URL.Query().Get("hashes"); got != fullV2Hash {
				t.Fatalf("unexpected hashes query: %q", got)
			}
			_, _ = w.Write([]byte(`[{"content_path":"/downloads/Show/Show - 01.mkv","hash":"` + fullV2Hash + `","name":"Show","progress":0.5,"save_path":"/downloads","state":"downloading"}]`))
		case "/api/v2/torrents/files":
			if got := r.URL.Query().Get("hash"); got != fullV2Hash {
				t.Fatalf("unexpected files hash query: %q", got)
			}
			_, _ = w.Write([]byte(`[{"name":"Show - 01.mkv","progress":1,"size":42}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, found, err := (Client{Endpoint: server.URL}).LookupByInfoHashesWithSnapshot(
		context.Background(), []string{fullV2Hash}, domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"})
	if err != nil || !found || task.ID != "hash:"+fullV2Hash || task.SavePath != "/downloads" {
		t.Fatalf("full v2 hash lookup = task=%+v found=%v err=%v", task, found, err)
	}
	if task.ContentPath != "/downloads/Show/Show - 01.mkv" {
		t.Fatalf("provider task lost the qB content path evidence: %+v", task)
	}
	if len(task.Paths) != 1 || task.Paths[0] != task.ContentPath {
		t.Fatalf("provider task content evidence paths = %+v", task.Paths)
	}
}

func TestClientLookupByInfoHashesRejectsMultipleMatches(t *testing.T) {
	fullV2Hash := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if got := r.URL.Query().Get("hashes"); got != fullV2Hash {
				t.Fatalf("unexpected hashes query: %q", got)
			}
			_, _ = w.Write([]byte(`[{"hash":"` + fullV2Hash + `"},{"hash":"` + strings.Repeat("c", 40) + `"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, found, err := (Client{Endpoint: server.URL}).LookupByInfoHashesWithSnapshot(
		context.Background(), []string{fullV2Hash}, domain.PathSnapshot{},
	)
	if err == nil || found || !strings.Contains(err.Error(), "multiple qBittorrent tasks") {
		t.Fatalf("multiple hash matches must fail closed: found=%v err=%v", found, err)
	}
}

// TestClientLookupByInfoHashesPropagatesFilesReadFailure proves the
// cross-owner hash fallback fails closed on a files-read outage instead of
// collapsing into a task with no content evidence.
func TestClientLookupByInfoHashesPropagatesFilesReadFailure(t *testing.T) {
	hash := strings.Repeat("d", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"content_path":"/downloads/Show/Show - 01.mkv","hash":"` + hash + `","name":"Show","progress":1,"save_path":"/downloads","state":"pausedup"}]`))
		case "/api/v2/torrents/files":
			http.Error(w, "files backend exploded", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, found, err := (Client{Endpoint: server.URL}).LookupByInfoHashesWithSnapshot(
		context.Background(), []string{hash}, domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"})
	if err == nil || found || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("files read outage must propagate: found=%v err=%v", found, err)
	}
	if got := (Client{}).ClassifyProviderError(err); got != domain.ProviderErrorRetryable {
		t.Fatalf("files read outage classification = %q, want retryable", got)
	}
}

func TestClientLookupByInfoHashesPropagatesMalformedFilesPayload(t *testing.T) {
	hash := strings.Repeat("e", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"content_path":"/downloads/Show/Show - 01.mkv","hash":"` + hash + `","name":"Show","progress":1,"save_path":"/downloads","state":"pausedup"}]`))
		case "/api/v2/torrents/files":
			_, _ = w.Write([]byte(`not-a-json-payload`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, found, err := (Client{Endpoint: server.URL}).LookupByInfoHashesWithSnapshot(
		context.Background(), []string{hash}, domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"})
	if err == nil || found || !strings.Contains(err.Error(), "decode qB torrent files") {
		t.Fatalf("malformed files payload must propagate: found=%v err=%v", found, err)
	}
}

// TestClientLookupByInfoHashesKeepsContentPathWithoutFiles proves an empty
// files list is not a client error: the qB content_path evidence survives so
// the admission layer can decide on real facts.
func TestClientLookupByInfoHashesKeepsContentPathWithoutFiles(t *testing.T) {
	hash := strings.Repeat("f", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"content_path":"/downloads/Show","hash":"` + hash + `","name":"Show","progress":1,"save_path":"/downloads","state":"pausedup"}]`))
		case "/api/v2/torrents/files":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	task, found, err := (Client{Endpoint: server.URL}).LookupByInfoHashesWithSnapshot(
		context.Background(), []string{hash}, domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"})
	if err != nil || !found {
		t.Fatalf("empty files list must stay a found task: found=%v err=%v", found, err)
	}
	if task.ContentPath != "/downloads/Show" || len(task.Paths) != 0 {
		t.Fatalf("content evidence = contentPath %q paths %+v", task.ContentPath, task.Paths)
	}
}

func TestProviderErrorClassificationDefaultsUnknownToAmbiguous(t *testing.T) {
	client := Client{}
	if got := client.ClassifyProviderError(errors.New("provider returned an unexpected response")); got != domain.ProviderErrorAmbiguous {
		t.Fatalf("unclassified external error = %q, want ambiguous", got)
	}
	if got := client.ClassifyProviderError(errors.New("qB task submission failed with HTTP 503")); got != domain.ProviderErrorRetryable {
		t.Fatalf("503 error = %q, want retryable", got)
	}
	if got := client.ClassifyProviderError(errors.New("invalid credentials")); got != domain.ProviderErrorTerminal {
		t.Fatalf("credential error = %q, want terminal", got)
	}
}

func TestClientSyncReportsMissingTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/login") {
			_, _ = w.Write([]byte("Ok."))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	_, err := (Client{Endpoint: server.URL}).Sync(context.Background(), "tag:missing")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("expected missing-task error, got %v", err)
	}
	if !errors.Is(err, domain.ErrProviderTaskAbsent) {
		t.Fatalf("a successful empty lookup must be definitive absence, got %v", err)
	}
	if _, err := (Client{Endpoint: server.URL}).Sync(context.Background(), "hash:0123456789abcdef0123456789abcdef01234567"); !errors.Is(err, domain.ErrProviderTaskAbsent) {
		t.Fatalf("hash lookup absence = %v, want ErrProviderTaskAbsent", err)
	}
}

// Only a successful lookup reply without the task is absence: an HTTP error,
// an undecodable reply, a failed login or an unreachable endpoint must never be
// reported as the task having been deleted.
func TestClientSyncLookupFailuresAreNotAbsence(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"http 500": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/auth/login") {
				_, _ = w.Write([]byte("Ok."))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		},
		"http 403": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/auth/login") {
				_, _ = w.Write([]byte("Ok."))
				return
			}
			w.WriteHeader(http.StatusForbidden)
		},
		"undecodable": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/auth/login") {
				_, _ = w.Write([]byte("Ok."))
				return
			}
			_, _ = w.Write([]byte(`<html>`))
		},
		"login refused": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/auth/login") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			_, err := (Client{Endpoint: server.URL}).Sync(context.Background(), "tag:missing")
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, domain.ErrProviderTaskAbsent) {
				t.Fatalf("%s must not be absence: %v", name, err)
			}
		})
	}
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := server.URL
	server.Close()
	if _, err := (Client{Endpoint: endpoint}).Sync(context.Background(), "tag:missing"); err == nil || errors.Is(err, domain.ErrProviderTaskAbsent) {
		t.Fatalf("an unreachable endpoint must not be absence: %v", err)
	}
}

func TestClientHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			_, _ = w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/app/version" {
			_, _ = w.Write([]byte("v4.6.7"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL}
	version, err := client.Health(context.Background())
	if err != nil || version != "v4.6.7" {
		t.Fatalf("health = %q, err = %v", version, err)
	}
}

func TestConfiguredClientResolvesTypedRuntimeConfigForEachOperation(t *testing.T) {
	var added, deleted, infoCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "typed-user" || r.Form.Get("password") != "typed-secret" {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			infoCalls++
			if infoCalls == 1 || infoCalls >= 4 {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"added_on":1,"content_path":"/downloads/Show","hash":"abc","name":"Show","progress":0.5,"state":"downloading"}]`))
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("savepath") != "/downloads" || r.FormValue("tags") != "bansoko-runtime-key" {
				t.Fatalf("unexpected qB multipart add form: %v", r.Form)
			}
			file, header, err := r.FormFile("torrents")
			if err != nil {
				t.Fatalf("missing torrent payload: %v", err)
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil || header.Filename != "show.torrent" || string(body) != "payload" {
				t.Fatalf("unexpected torrent payload filename=%q body=%q err=%v", header.Filename, body, err)
			}
			added++
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			if err := r.ParseForm(); err != nil || r.Form.Get("hashes") != "abc" {
				t.Fatalf("unexpected qB delete form: %v", r.Form)
			}
			deleted++
		case "/api/v2/torrents/deleteTags":
			if err := r.ParseForm(); err != nil || r.Form.Get("tags") != "bansoko-runtime-key" {
				t.Fatalf("unexpected qB delete-tags form: %v", r.Form)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := &testRuntimeConfigSource{config: RuntimeConfig{
		Endpoint: server.URL, Username: "typed-user", Password: "typed-secret",
	}}
	provider := ConfiguredClient{Source: source}
	if _, err := provider.SubmitFile(context.Background(), []byte("payload"), "show.torrent", "/downloads", "runtime-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Sync(context.Background(), "tag:bansoko-runtime-key"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Cancel(context.Background(), "tag:bansoko-runtime-key"); err != nil {
		t.Fatal(err)
	}
	if added != 1 || deleted != 1 || source.calls != 3 {
		t.Fatalf("configured provider did not use the runtime source per operation: added=%d deleted=%d calls=%d", added, deleted, source.calls)
	}
}

func TestConfiguredClientMarksRuntimeConfigUnavailable(t *testing.T) {
	cause := errors.New("no usable qBittorrent connection is available")
	provider := ConfiguredClient{Source: &testRuntimeConfigSource{err: cause}}
	if _, err := provider.Sync(context.Background(), "tag:missing"); !errors.Is(err, ErrRuntimeConfigUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("runtime config error must preserve internal classification and cause: %v", err)
	}
	if _, err := (ConfiguredClient{}).Sync(context.Background(), "tag:missing"); !errors.Is(err, ErrRuntimeConfigUnavailable) {
		t.Fatalf("nil runtime config source must be classified as unavailable: %v", err)
	}
}
