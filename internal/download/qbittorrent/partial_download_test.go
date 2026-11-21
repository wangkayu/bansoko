package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"bansoko/internal/download/domain"
)

func TestPartialDownloadStoppedAddAndVerifiedStart(t *testing.T) {
	for _, tc := range []struct {
		version, stopField, stoppedState, startPath string
		ignoredPriorities                           bool
	}{
		{"v5.0.5", "stopped", "stoppedDL", "start", false},
		{"v4.6.7", "paused", "pausedDL", "resume", false},
		{"v5.0.5", "stopped", "stoppedDL", "start", true},
	} {
		t.Run(fmt.Sprintf("%s/ignored=%v", tc.version, tc.ignoredPriorities), func(t *testing.T) {
			const key = "partial-fixture"
			const video, subtitle, extra = "Pack/E01.mkv", "Pack/E01.ass", "Pack/E02.mkv"
			priorities := []int{1, 1, 1}
			var added, started, verified bool
			var writes []bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					fmt.Fprint(w, "Ok.")
				case "/api/v2/app/version":
					fmt.Fprint(w, tc.version)
				case "/api/v2/torrents/info":
					if !added {
						fmt.Fprint(w, "[]")
						return
					}
					state := tc.stoppedState
					if started {
						state = "uploading"
					}
					fmt.Fprintf(w, `[{"hash":%q,"tags":%q,"save_path":"/downloads","state":%q}]`, occupiedV1, "bansoko-"+key, state)
				case "/api/v2/torrents/add":
					if r.FormValue(tc.stopField) != "true" || r.FormValue("contentLayout") != "Original" || r.FormValue("autoTMM") != "false" {
						t.Error("add did not request a stopped torrent with the original layout")
					}
					added = true
					fmt.Fprint(w, "Ok.")
				case "/api/v2/torrents/files":
					if r.URL.Query().Get("hash") != occupiedV1 || !added {
						t.Error("file lookup did not address the added torrent")
					}
					verified = priorities[2] == 0
					var files []map[string]any
					// Deliberately do not return entries in index order.
					for _, i := range []int{2, 0, 1} {
						progress := 0
						if started && i != 2 {
							progress = 1
						}
						files = append(files, map[string]any{"name": []string{video, subtitle, extra}[i], "index": i, "size": []int{100, 10, 200}[i], "priority": priorities[i], "progress": progress})
					}
					_ = json.NewEncoder(w).Encode(files)
				case "/api/v2/torrents/filePrio":
					if started || r.FormValue("hash") != occupiedV1 || r.FormValue("id") != "2" || r.FormValue("priority") != "0" {
						t.Error("wrong file priority write")
					}
					if !tc.ignoredPriorities {
						priorities[2] = 0
					}
				case "/api/v2/torrents/" + tc.startPath:
					if started || !verified || r.FormValue("hashes") != occupiedV1 {
						t.Error("start was repeated or sent without verified priorities")
					}
					started = true
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx := context.Background()
			client := Client{Endpoint: server.URL}
			snapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}
			prepared, _, found, err := client.PreparePartialSubmission(ctx, []byte("fixture payload"), "pack.torrent", snapshot, key, []string{occupiedV1}, "")
			if err != nil || found || prepared == nil {
				t.Fatalf("prepare: found=%v err=%v", found, err)
			}
			if result, err := prepared.Execute(ctx); err != nil || !result.Accepted {
				t.Fatalf("add: %+v, %v", result, err)
			}
			request := PartialDownloadRequest{TaskID: "tag:bansoko-" + key, Hashes: []string{occupiedV1}, Snapshot: snapshot,
				Selection: domain.PartialDownload{Paths: []string{video, subtitle}, FileCount: 3},
				Manifest:  map[string]int64{video: 100, subtitle: 10, extra: 200},
				BeforeWrite: func(_ context.Context, start bool) (time.Duration, error) {
					writes = append(writes, start)
					return time.Second, nil
				},
			}
			observed, err := client.ObservePartialDownload(ctx, request)
			if tc.ignoredPriorities {
				if !errors.Is(err, ErrPartialDownloadIdentity) || started || !slices.Equal(writes, []bool{false}) {
					t.Fatalf("ignored priorities allowed progress: %v, started=%v, writes=%v", err, started, writes)
				}
				return
			}
			if err != nil || observed.State != domain.TaskCompleted || len(observed.Files) != 2 || *observed.ContentCompleteBytes != 110 || !slices.Equal(writes, []bool{false, true}) {
				t.Fatalf("observation: %+v, %v, writes=%v", observed, err, writes)
			}
			request.StartSent = true
			request.BeforeWrite = func(context.Context, bool) (time.Duration, error) {
				t.Error("an already-dispatched start must be read-only")
				return 0, errors.New("unexpected write")
			}
			if observed, err = client.ObservePartialDownload(ctx, request); err != nil || observed.State != domain.TaskCompleted {
				t.Fatalf("resumed observation: %+v, %v", observed, err)
			}
		})
	}
}
