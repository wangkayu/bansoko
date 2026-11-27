package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchDecodesCatalogKeywordResult(t *testing.T) {
	withCache(t)
	for _, tc := range []struct{ state, items, want string }{
		{"available", `[{"title":"Example","provider_entry_ref":"123","bgm_id":"456"}]`, "Example\t123\tbgm:456"},
		{"empty", `[]`, "no results"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			payload := fmt.Sprintf(`{"items":%s,"upstream":{"state":%q,"checked_at":"2026-01-01T00:00:00Z"}}`, tc.items, tc.state)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/catalog/providers/anibt/search" || r.URL.Query().Get("q") != "Example" {
					t.Errorf("unexpected URL %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, payload)
			}))
			defer server.Close()
			args := []string{"search", "--endpoint", server.URL, "--token", "test-token", "--query", "Example"}
			execute := func(args []string) string {
				t.Helper()
				return capture(t, func() {
					if err := run(args); err != nil {
						t.Fatal(err)
					}
				})
			}
			if got := strings.TrimSpace(execute(args)); got != tc.want {
				t.Fatalf("human output=%q want=%q", got, tc.want)
			}
			var result struct {
				Items    json.RawMessage
				Upstream struct{ State string }
			}
			if raw := execute(append(args, "--json")); json.Unmarshal([]byte(raw), &result) != nil || result.Upstream.State != tc.state || len(result.Items) == 0 {
				t.Fatalf("JSON output=%s", raw)
			}
		})
	}
}
