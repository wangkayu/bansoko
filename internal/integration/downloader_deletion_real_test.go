package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
)

// TestDownloaderDeletionIsDefinitiveAbsenceRealQB checks the classification
// against a real qBittorrent: while a task
// exists the sync lookup finds it by tag and by hash; after it is deleted in qB
// the same lookup is a successful reply without it (domain.ErrProviderTaskAbsent),
// and a refused login is never mistaken for that absence.
func TestDownloaderDeletionIsDefinitiveAbsenceRealQB(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	if endpoint == "" || os.Getenv("BANSOKO_QB_PASSWORD") == "" {
		t.Skip("BANSOKO_QB_ENDPOINT/PASSWORD are required for the opt-in real qB deletion test")
	}
	ctx := context.Background()
	files := []torrentFile{{Name: "removed.mkv", Content: bytes.Repeat([]byte("r"), 16384)}}
	_, _, payload, closeFixture := startMultiFileFixture(t, "downloader deletion", files)
	defer closeFixture()
	cookie := qbLogin(t, endpoint, os.Getenv("BANSOKO_QB_USERNAME"), os.Getenv("BANSOKO_QB_PASSWORD"))
	httpClient := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path string, body io.Reader, contentType string) []byte {
		t.Helper()
		req, err := http.NewRequest(method, endpoint+"/api/v2/torrents/"+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Referer", endpoint)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("qB %s HTTP %d", path, resp.StatusCode)
		}
		return raw
	}
	tag := "bansoko-deletion-" + time.Now().UTC().Format("20060102150405.000000")
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("torrents", "deletion.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(payload); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"savepath": "/downloads", "paused": "true", "stopped": "true", "tags": tag} {
		if err = w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "add", &body, w.FormDataContentType())
	var hash string
	deadline := time.Now().Add(10 * time.Second)
	for hash == "" {
		var rows []struct {
			Hash string `json:"hash"`
		}
		if err := json.Unmarshal(call(http.MethodGet, "info?tag="+url.QueryEscape(tag), nil, ""), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			hash = rows[0].Hash
			break
		}
		if len(rows) > 1 || time.Now().After(deadline) {
			t.Fatalf("qB add visibility: count=%d", len(rows))
		}
		time.Sleep(100 * time.Millisecond)
	}
	deleted := false
	defer func() {
		if !deleted {
			call(http.MethodPost, "delete", strings.NewReader(url.Values{"hashes": {hash}, "deleteFiles": {"true"}}.Encode()), "application/x-www-form-urlencoded")
		}
	}()

	client := qbittorrent.Client{Endpoint: endpoint, Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD")}
	for _, id := range []string{"tag:" + tag, "hash:" + hash} {
		if task, err := client.Sync(ctx, id); err != nil || !strings.EqualFold(task.Hash, hash) {
			t.Fatalf("present task %s: %+v %v", id, task, err)
		}
	}
	refused := qbittorrent.Client{Endpoint: endpoint, Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: "definitely-not-the-password"}
	if _, err := refused.Sync(ctx, "hash:"+hash); errors.Is(err, domain.ErrProviderTaskAbsent) {
		t.Fatalf("a refused login must not read as absence: %v", err)
	}

	call(http.MethodPost, "delete", strings.NewReader(url.Values{"hashes": {hash}, "deleteFiles": {"true"}}.Encode()), "application/x-www-form-urlencoded")
	deleted = true
	deadline = time.Now().Add(10 * time.Second)
	for {
		_, err := client.Sync(ctx, "hash:"+hash)
		if errors.Is(err, domain.ErrProviderTaskAbsent) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a deleted task never read as absent: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := client.Sync(ctx, "tag:"+tag); !errors.Is(err, domain.ErrProviderTaskAbsent) {
		t.Fatalf("the tag lookup of a deleted task = %v, want absence", err)
	}
}
