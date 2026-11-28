package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/episodetitle"
)

func TestBGMMainEpisodesRequireCompletePagination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		failure episodetitle.LookupErrorKind
	}{
		{name: "complete", status: http.StatusOK},
		{name: "page disappeared", status: http.StatusNotFound, failure: episodetitle.LookupInvalidResponse},
		{name: "rate limited", status: http.StatusTooManyRequests, failure: episodetitle.LookupRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				query := r.URL.Query()
				if r.URL.Path != "/v0/episodes" || query.Get("subject_id") != "42" || query.Get("type") != "0" || query.Get("limit") != "100" {
					t.Errorf("unexpected page request: %s", r.URL)
				}
				switch query.Get("offset") {
				case "0":
					items := make([]map[string]any, 100)
					for i := range items {
						items[i] = map[string]any{"ep": i + 1, "type": 0}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total": 101, "data": items})
				case "100":
					w.Header().Set("Retry-After", "17")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"total":101,"data":[{"ep":101,"type":0,"name_cn":"Last episode"}]}`))
				default:
					t.Errorf("unexpected offset: %s", query.Get("offset"))
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			client := BGMClient{Client: server.Client(), Connections: testConnections{{
				ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: server.URL, State: configurationdomain.ConnectionConfigured,
			}}}
			list, err := client.Episodes(context.Background(), episodetitle.Key{Source: episodetitle.SourceBGM, WorkID: "42"})
			if requests.Load() != 2 {
				t.Fatalf("requests = %d", requests.Load())
			}
			if tc.failure == "" {
				if err != nil || len(list.Episodes) != 101 || list.Episodes[100].Title != "Last episode" {
					t.Fatalf("complete episode count=%d, err=%v", len(list.Episodes), err)
				}
				return
			}
			var failure *episodetitle.LookupError
			if len(list.Episodes) != 0 || !errors.As(err, &failure) || failure.Kind != tc.failure || errors.Is(err, episodetitle.ErrNotFound) {
				t.Fatalf("incomplete episode count=%d, err=%v", len(list.Episodes), err)
			}
			if tc.status == http.StatusTooManyRequests && failure.RetryAfter != 17*time.Second {
				t.Fatalf("retry after = %v", failure.RetryAfter)
			}
		})
	}
}
