package qbittorrent

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestSetShareLimitsSendsEveryLimitAndOnlyTheSetOptions(t *testing.T) {
	var forms []url.Values
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/setShareLimits" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		forms = append(forms, r.PostForm)
	})
	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.SetShareLimits(context.Background(), "abc", ShareLimits{RatioLimit: 1.5, SeedingTimeLimit: -1, InactiveSeedingTimeLimit: -2}); err != nil {
		t.Fatal(err)
	}
	if err := client.SetShareLimits(context.Background(), "abc", ShareLimits{RatioLimit: -1, SeedingTimeLimit: 1440, InactiveSeedingTimeLimit: -2, ShareLimitAction: "RemoveWithContent", ShareLimitsMode: "MatchAny"}); err != nil {
		t.Fatal(err)
	}
	legacy, current := forms[0], forms[1]
	if legacy.Get("hashes") != "abc" || legacy.Get("ratioLimit") != "1.5" || legacy.Get("seedingTimeLimit") != "-1" || legacy.Get("inactiveSeedingTimeLimit") != "-2" {
		t.Fatalf("legacy form = %v", legacy)
	}
	if _, ok := legacy["shareLimitAction"]; ok {
		t.Fatalf("an unset action was sent: %v", legacy)
	}
	if _, ok := legacy["shareLimitsMode"]; ok {
		t.Fatalf("an unset mode was sent: %v", legacy)
	}
	if current.Get("ratioLimit") != "-1" || current.Get("seedingTimeLimit") != "1440" || current.Get("shareLimitAction") != "RemoveWithContent" || current.Get("shareLimitsMode") != "MatchAny" {
		t.Fatalf("current form = %v", current)
	}
}

func TestSetShareLimitsReportsHTTPFailure(t *testing.T) {
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing parameter", http.StatusBadRequest)
	})
	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	if err := client.SetShareLimits(context.Background(), "abc", ShareLimits{RatioLimit: 2, SeedingTimeLimit: -1, InactiveSeedingTimeLimit: -2}); err == nil {
		t.Fatal("a 400 was accepted")
	}
}

func TestWebAPIVersionReadsTheVersionText(t *testing.T) {
	server := qbTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/app/webapiVersion" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("2.9.3\n"))
	})
	client := Client{Endpoint: server.URL, Username: "admin", Password: "secret"}
	version, err := client.WebAPIVersion(context.Background())
	if err != nil || version != "2.9.3" {
		t.Fatalf("version = %q %v", version, err)
	}
}
