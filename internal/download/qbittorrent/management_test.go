package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"bansoko/internal/download/domain"
)

// qbTestServer builds a mock qBittorrent WebUI server for management tests.
// It accepts the endpoint and a handler that customizes per-path responses.
func qbTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Set-Cookie", "SID=test; Path=/")
			_, _ = w.Write([]byte("Ok."))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestListTorrentsReturnsDecodedList(t *testing.T) {
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"hash":"abc","name":"Show A","state":"downloading","progress":0.5,"size":1024,"downloaded":512,"dlspeed":100,"eta":5,"category":"anime","tags":"bansoko-key"},
			{"hash":"def","name":"Show B","state":"pausedDL","progress":0.0,"size":2048}
		]`))
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	torrents, err := client.ListTorrents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 2 {
		t.Fatalf("expected 2 torrents, got %d", len(torrents))
	}
	if torrents[0].Hash != "abc" || torrents[0].Name != "Show A" {
		t.Fatalf("unexpected first torrent: %+v", torrents[0])
	}
	if torrents[0].Size != 1024 || torrents[0].Downloaded != 512 {
		t.Fatalf("unexpected size/downloaded: %d/%d", torrents[0].Size, torrents[0].Downloaded)
	}
	if torrents[1].State != "pausedDL" {
		t.Fatalf("unexpected second torrent state: %s", torrents[1].State)
	}
}

func TestPauseTorrentPostsHashes(t *testing.T) {
	var pausedHash string
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/pause" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		pausedHash = r.Form.Get("hashes")
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.PauseTorrent(context.Background(), "abc123"); err != nil {
		t.Fatal(err)
	}
	if pausedHash != "abc123" {
		t.Fatalf("expected pause to receive hash abc123, got %q", pausedHash)
	}
}

func TestResumeTorrentPostsHashes(t *testing.T) {
	var resumedHash string
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/resume" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		resumedHash = r.Form.Get("hashes")
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.ResumeTorrent(context.Background(), "xyz789"); err != nil {
		t.Fatal(err)
	}
	if resumedHash != "xyz789" {
		t.Fatalf("expected resume to receive hash xyz789, got %q", resumedHash)
	}
}

// TestPauseResumeUseQB5RoutesFirst pins the qBittorrent 5 routes
// (torrents/stop, torrents/start): a 5.x server serves only these, so the
// legacy 4.x route must not be needed.
func TestPauseResumeUseQB5RoutesFirst(t *testing.T) {
	var paths []string
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/stop" && r.URL.Path != "/api/v2/torrents/start" {
			http.NotFound(w, r)
			return
		}
		paths = append(paths, r.URL.Path)
	})
	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.PauseTorrent(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if err := client.ResumeTorrent(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/api/v2/torrents/stop" || paths[1] != "/api/v2/torrents/start" {
		t.Fatalf("qB 5 routes = %v", paths)
	}
}

// TestPauseResumeNonNotFoundErrorDoesNotFallBack keeps a real failure on the
// current route (for example 403) from being retried on the legacy route.
func TestPauseResumeNonNotFoundErrorDoesNotFallBack(t *testing.T) {
	legacy := false
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/torrents/pause" {
			legacy = true
		}
		w.WriteHeader(http.StatusForbidden)
	})
	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.PauseTorrent(context.Background(), "abc"); err == nil {
		t.Fatal("expected the 403 to surface")
	}
	if legacy {
		t.Fatal("a non-404 failure must not fall back to the legacy route")
	}
}

func TestDeleteTorrentPostsHashesAndDeleteFiles(t *testing.T) {
	var (
		deletedHash  string
		deleteFiles  string
		methodCalled string
	)
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/delete" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		deletedHash = r.Form.Get("hashes")
		deleteFiles = r.Form.Get("deleteFiles")
		methodCalled = r.Method
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.DeleteTorrent(context.Background(), "del456", true); err != nil {
		t.Fatal(err)
	}
	if deletedHash != "del456" {
		t.Fatalf("expected delete to receive hash del456, got %q", deletedHash)
	}
	if deleteFiles != "true" {
		t.Fatalf("expected deleteFiles=true, got %q", deleteFiles)
	}
	if methodCalled != http.MethodPost {
		t.Fatalf("expected POST, got %s", methodCalled)
	}
}

func TestDeleteTorrentWithoutFilesSendsFalse(t *testing.T) {
	var deleteFiles string
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/delete" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		deleteFiles = r.Form.Get("deleteFiles")
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.DeleteTorrent(context.Background(), "del789", false); err != nil {
		t.Fatal(err)
	}
	if deleteFiles != "false" {
		t.Fatalf("expected deleteFiles=false, got %q", deleteFiles)
	}
}

func TestGetTorrentFilesReturnsDecodedList(t *testing.T) {
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("hash") != "filehash" {
			t.Fatalf("unexpected hash query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[
			{"name":"Show/Show - 01.mkv","size":1048576,"progress":1.0,"priority":7},
			{"name":"Show/Show.ass","size":1024,"progress":0.5,"priority":0}
		]`))
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	files, err := client.GetTorrentFiles(context.Background(), "filehash")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Name != "Show/Show - 01.mkv" || files[0].Size != 1048576 {
		t.Fatalf("unexpected first file: %+v", files[0])
	}
	if files[0].Priority != 7 {
		t.Fatalf("expected priority 7, got %d", files[0].Priority)
	}
	if files[1].Progress != 0.5 {
		t.Fatalf("expected progress 0.5, got %f", files[1].Progress)
	}
}

func TestGetServerStatusReturnsVersionAndServerState(t *testing.T) {
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/sync/maindata":
			_, _ = w.Write([]byte(`{
				"server_state":{
					"connection_status":"connected",
					"dl_info_speed":500,
					"up_info_speed":100,
					"free_space_on_disk":1073741824,
					"total_size":10737418240
				}
			}`))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v4.6.5"))
		default:
			http.NotFound(w, r)
		}
	})

	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	status, err := client.GetServerStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != "v4.6.5" {
		t.Fatalf("expected version v4.6.5, got %q", status.Version)
	}
	if status.ConnectionStatus != "connected" {
		t.Fatalf("expected connection_status connected, got %q", status.ConnectionStatus)
	}
	if status.DlSpeed != 500 {
		t.Fatalf("expected dl_speed 500, got %d", status.DlSpeed)
	}
	if status.FreeSpace != 1073741824 {
		t.Fatalf("expected free_space 1073741824, got %d", status.FreeSpace)
	}
}

func TestListTorrentsAuthFailureReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL, Username: "bad", Password: "creds"}
	_, err := client.ListTorrents(context.Background())
	if err == nil {
		t.Fatal("expected auth failure error")
	}
}

// TestMapStateTrustsQBVerdictOverProgress: a failure state wins at any
// progress, completion needs qB's upload state, and a torrent near or at 100%
// that is still downloading, checking or moving is not complete. qB 5 stopped
// states map like their 4.x paused names.
func TestMapStateTrustsQBVerdictOverProgress(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  domain.TaskState
	}{
		{"error", domain.TaskFailed},
		{"missingFiles", domain.TaskFailed},
		{"unknown", domain.TaskFailed},
		{"stalledDL", domain.TaskDownloading},
		{"downloading", domain.TaskDownloading},
		{"checkingUP", domain.TaskDownloading},
		{"moving", domain.TaskDownloading},
		{"stoppedDL", domain.TaskDownloading},
		{"pausedDL", domain.TaskDownloading},
		{"stoppedUP", domain.TaskCompleted},
		{"pausedUP", domain.TaskCompleted},
		{"stalledUP", domain.TaskCompleted},
		{"uploading", domain.TaskCompleted},
	} {
		if got := mapState(tc.state); got != tc.want {
			t.Errorf("mapState(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TorrentByHash addresses the task by infohash only and lists every file,
// including the ones that are not complete.
func TestTorrentByHashReadsTheTaskAndAllItsFiles(t *testing.T) {
	var infoQuery, filesQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			infoQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`[{"hash":"abc123","name":"Show","state":"stoppedUP","save_path":"/downloads/","content_path":"/downloads/Show"}]`))
		case "/api/v2/torrents/files":
			filesQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`[{"name":"Show/01.mkv","progress":1,"size":10},{"name":"Show/02.mkv","progress":0.25,"size":10},{"name":"Show/extra/x.nfo","progress":0,"size":1}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	facts, found, err := (Client{Endpoint: server.URL}).TorrentByHash(context.Background(), " ABC123 ")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if infoQuery != "hashes=abc123" || filesQuery != "hash=abc123" {
		t.Fatalf("lookups = %q %q, want infohash only", infoQuery, filesQuery)
	}
	if facts.Hash != "abc123" || facts.State != "stoppedUP" || facts.SavePath != "/downloads/" || facts.ContentPath != "/downloads/Show" {
		t.Fatalf("facts = %+v", facts)
	}
	if len(facts.FileNames) != 3 || facts.FileNames[1] != "Show/02.mkv" || facts.FileNames[2] != "Show/extra/x.nfo" {
		t.Fatalf("every file must be listed, incomplete ones included: %v", facts.FileNames)
	}
}

func TestTorrentByHashReportsAnAbsentTaskAndRejectsAnEmptyHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v2/torrents/files":
			t.Fatal("files of an absent task must not be read")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL}
	if _, found, err := client.TorrentByHash(context.Background(), "abc123"); err != nil || found {
		t.Fatalf("absent task: found=%v err=%v", found, err)
	}
	if _, _, err := client.TorrentByHash(context.Background(), "  "); err == nil {
		t.Fatal("an empty hash was accepted")
	}
}
