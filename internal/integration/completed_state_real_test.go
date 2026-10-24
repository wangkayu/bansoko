package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCompletedRealSemantics uses one 32 KiB, piece-aligned two-file
// torrent on the disposable real qB. It records raw fields rather than
// substituting progress*size or downloaded for completed.
func TestCompletedRealSemantics(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	if endpoint == "" || os.Getenv("BANSOKO_QB_PASSWORD") == "" {
		t.Skip("BANSOKO_QB_ENDPOINT/PASSWORD are required for the opt-in real qB semantics test")
	}
	files := []torrentFile{{Name: "selected.mkv", Content: bytes.Repeat([]byte("s"), 16384)}, {Name: "unselected.nfo", Content: bytes.Repeat([]byte("u"), 16384)}}
	_, _, payload, closeFixture := startMultiFileFixture(t, "S1b completed semantics", files)
	defer closeFixture()
	cookie := qbLogin(t, endpoint, os.Getenv("BANSOKO_QB_USERNAME"), os.Getenv("BANSOKO_QB_PASSWORD"))
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path string, body io.Reader, contentType string) []byte {
		t.Helper()
		req, err := http.NewRequest(method, endpoint+"/api/v1/"+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Referer", endpoint)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("qB %s HTTP %d", path, resp.StatusCode)
		}
		return raw
	}
	post := func(path string, form url.Values) {
		call(http.MethodPost, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	}
	version := string(call(http.MethodGet, "app/version", nil, ""))
	t.Logf("qB version=%s payload_bytes=32768 piece_length=16384", version)
	// qBittorrent 5 renamed torrents/pause|resume to torrents/stop|start and
	// removed the old routes; 4.x serves only the old ones.
	pauseRoute, resumeRoute, pausedUP := "torrents/pause", "torrents/resume", "pausedUP"
	if !strings.HasPrefix(strings.TrimSpace(version), "v4") {
		pauseRoute, resumeRoute, pausedUP = "torrents/stop", "torrents/start", "stoppedUP"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("torrents", "s1b.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(payload); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"savepath": "/downloads", "paused": "true", "tags": "s1b-semantics"} {
		if err = w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "torrents/add", &body, w.FormDataContentType())
	// qB add enqueues asynchronously; wait for the object, not for progress.
	addDeadline := time.Now().Add(10 * time.Second)
	for {
		var rows []json.RawMessage
		if err := json.Unmarshal(call(http.MethodGet, "torrents/info?tag=s1b-semantics", nil, ""), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			break
		}
		if len(rows) > 1 || time.Now().After(addDeadline) {
			t.Fatalf("qB add visibility: count=%d", len(rows))
		}
		time.Sleep(100 * time.Millisecond)
	}
	type observation struct {
		Hash       string `json:"hash"`
		State      string `json:"state"`
		Completed  *int64 `json:"completed"`
		Size       *int64 `json:"size"`
		Total      *int64 `json:"total_size"`
		Downloaded *int64 `json:"downloaded"`
	}
	read := func(label string) observation {
		t.Helper()
		raw := call(http.MethodGet, "torrents/info?tag=s1b-semantics", nil, "")
		var rows []observation
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected single torrent, got %d", len(rows))
		}
		o := rows[0]
		t.Logf("%s raw=%s", label, raw)
		if o.Completed == nil || o.Size == nil || o.Total == nil || o.Downloaded == nil {
			t.Fatal("missing integer fields")
		}
		if *o.Completed < 0 || *o.Completed > *o.Size {
			t.Fatalf("BC4.1 conflict: completed=%d size=%d", *o.Completed, *o.Size)
		}
		return o
	}
	first := read("initial-paused")
	defer post("torrents/delete", url.Values{"hashes": {first.Hash}, "deleteFiles": {"true"}})
	if *first.Completed != 0 {
		t.Fatalf("initial paused torrent has completed=%d", *first.Completed)
	}
	post("torrents/filePrio", url.Values{"hash": {first.Hash}, "id": {"1"}, "priority": {"0"}})
	selectionDeadline := time.Now().Add(10 * time.Second)
	for {
		selected := read("partial-selection-paused")
		if *selected.Size == 16384 && *selected.Total == 32768 {
			break
		}
		if time.Now().After(selectionDeadline) {
			t.Fatalf("partial selection did not converge: size=%d total=%d", *selected.Size, *selected.Total)
		}
		time.Sleep(100 * time.Millisecond)
	}
	post(resumeRoute, url.Values{"hashes": {first.Hash}})
	var last int64
	wait := func(label string, done func(observation) bool) observation {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			o := read(label)
			if *o.Completed < last {
				t.Logf("completed regression %d -> %d (highwater must retain max)", last, *o.Completed)
			}
			last = *o.Completed
			if done(o) {
				return o
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("%s did not converge", label)
		return observation{}
	}
	complete := wait("partial-selection-resumed", func(o observation) bool {
		return *o.Completed == 16384 && strings.HasSuffix(strings.ToLower(o.State), "up")
	})
	if *complete.Total != 32768 || *complete.Size != 16384 {
		t.Fatal("selection total changed")
	}
	// Pause/resume the exact same provider object, preserving completed bytes.
	post(pauseRoute, url.Values{"hashes": {first.Hash}})
	paused := wait("completed-paused", func(o observation) bool { return strings.EqualFold(o.State, pausedUP) })
	if *paused.Completed != 16384 {
		t.Fatal("pause lost completed content")
	}
	post(resumeRoute, url.Values{"hashes": {first.Hash}})
	resumed := wait("completed-resumed", func(o observation) bool { return !strings.EqualFold(o.State, pausedUP) })
	if *resumed.Completed != 16384 {
		t.Fatal("resume lost completed content")
	}
	// Keep the same hash and file selection, corrupt one selected piece, then
	// recheck while paused so no webseed repair can race the verification.
	post(pauseRoute, url.Values{"hashes": {first.Hash}})
	wait("before-corrupt-recheck", func(o observation) bool { return strings.EqualFold(o.State, pausedUP) })
	selectedPath := filepath.Join(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT"), "M1 Multi Real Show", "selected.mkv")
	if err := os.WriteFile(selectedPath, bytes.Repeat([]byte("x"), 16384), 0o644); err != nil {
		t.Fatal(err)
	}
	post("torrents/recheck", url.Values{"hashes": {first.Hash}})
	// A small recheck may finish between samples. Its postcondition is checked;
	// raw intermediate checking states are recorded whenever observable.
	time.Sleep(100 * time.Millisecond)
	rechecked := wait("recheck", func(o observation) bool {
		return *o.Completed == 0 && !strings.HasPrefix(strings.ToLower(o.State), "checking")
	})
	if *rechecked.Size != 16384 || *rechecked.Total != 32768 {
		t.Fatal("recheck changed frozen selection size")
	}
	post(resumeRoute, url.Values{"hashes": {first.Hash}})
	wait("repaired-after-recheck", func(o observation) bool {
		return *o.Completed == 16384 && strings.HasSuffix(strings.ToLower(o.State), "up")
	})
	t.Log("PASS partial selection / pause-resume / recheck; completed=selected content bytes, not cumulative downloaded; single object cleaned")
}
