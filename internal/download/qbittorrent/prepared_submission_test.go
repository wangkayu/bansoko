package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"bansoko/internal/download/domain"
)

func TestPreparedSubmissionDoesNotReplayMutation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		redirect := status != http.StatusOK
		name := http.StatusText(status)
		t.Run(name, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					w.Write([]byte("Ok."))
				case "/api/v2/torrents/info":
					w.Write([]byte(`[]`))
				case "/api/v2/torrents/add":
					posts++
					if redirect {
						http.Redirect(w, r, "/replayed-add", status)
						return
					}
					w.Write([]byte("Ok."))
				case "/replayed-add":
					posts++
					w.Write([]byte("Ok."))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			prepared, _, found, err := (Client{Endpoint: server.URL}).PrepareSubmission(context.Background(), []byte("payload"), "show.torrent", domain.PathSnapshot{RemoteRoot: "/downloads"}, "prepared-key")
			if err != nil || found || prepared == nil {
				t.Fatalf("prepare found=%v err=%v", found, err)
			}
			result, executeErr := prepared.Execute(context.Background())
			if posts != 1 {
				t.Fatalf("one dispatch emitted %d mutation POSTs", posts)
			}
			if redirect {
				if executeErr == nil || result.Accepted || !result.ResponseReceived || result.StatusCode != status {
					t.Fatal("redirect accepted as an add receipt")
				}
			} else if executeErr != nil || !result.Accepted {
				t.Fatalf("accepted result=%+v err=%v", result, executeErr)
			}
			if _, err := prepared.Execute(context.Background()); err == nil {
				t.Fatal("consumed request replayed")
			}
			if posts != 1 {
				t.Fatalf("repeat execute emitted another POST: %d", posts)
			}
		})
	}
}
