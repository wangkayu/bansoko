package adapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	identitymetadataport "bansoko/internal/identitymetadata/port"
	plugintargetport "bansoko/internal/plugintarget/port"
)

type fakeNotificationCoverStore struct {
	cover notificationCover
	found bool
	err   error
}

func (f fakeNotificationCoverStore) NotificationCover(context.Context, string) (notificationCover, bool, error) {
	return f.cover, f.found, f.err
}

func TestNotificationImageResolverSelectsMikanAndAniBTProviderCovers(t *testing.T) {
	for _, tc := range []struct {
		provider string
		kind     configurationdomain.ConnectionKind
		endpoint string
		rawURL   string
		wantURL  string
	}{
		{provider: "mikan", kind: configurationdomain.ConnectionMikan, endpoint: "https://mikanani.me", rawURL: "https://mikanani.me/images/1.jpg?width=400&format=webp", wantURL: "https://mikanani.me/images/1.jpg?width=600"},
		{provider: "anibt", kind: configurationdomain.ConnectionAniBT, endpoint: "https://anibt.net", rawURL: "https://r2.anibt.net/covers/1.jpg", wantURL: "https://r2.anibt.net/covers/1.jpg"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			store := newMemoryImageStore()
			resolver := &NotificationImageResolver{
				coverStore: fakeNotificationCoverStore{cover: notificationCover{provider: tc.provider, rawURL: tc.rawURL}, found: true},
				covers: &ConfiguredCoverProxy{
					Connections: stubConnectionSource{connections: []configurationdomain.Connection{{Kind: tc.kind, Endpoint: tc.endpoint, State: configurationdomain.ConnectionConfigured}}},
					Store:       store, TTL: time.Hour,
				},
			}
			image, err := resolver.ResolveNotificationImage(context.Background(), plugintargetport.NotificationImageRequest{ManagedAnimeID: "anime-1", BGMID: "123"})
			if err != nil || image.Source != tc.provider || image.URL != tc.wantURL {
				t.Fatalf("image=%+v err=%v", image, err)
			}
		})
	}
}

func TestNotificationImageResolverUsesCachedBGMPoster(t *testing.T) {
	cache := newMemoryPosterCache()
	cache.entries["123"] = identitymetadataport.PosterCacheEntry{BGMSubjectID: "123", ImageURL: "https://lain.bgm.tv/pic/cover/l/123.jpg"}
	resolver := &NotificationImageResolver{
		coverStore: fakeNotificationCoverStore{},
		posters:    newPosterTestResolverWithCache("http://unused.invalid", cache),
	}
	image, err := resolver.ResolveNotificationImage(context.Background(), plugintargetport.NotificationImageRequest{BGMID: "123"})
	if err != nil || image.Source != "bgm" || image.URL != "https://lain.bgm.tv/pic/cover/l/123.jpg" {
		t.Fatalf("image=%+v err=%v", image, err)
	}
}

func TestNotificationImageResolverResolvesBoundTMDBWorkWithoutSearch(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/subject/123" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/3/tv/42" || r.URL.Query().Get("api_key") != "secret" {
			t.Fatalf("unexpected TMDB request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":42,"poster_path":"/poster.jpg"}`)
	}))
	defer server.Close()
	connections := testConnections{{Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3", SecretRef: "tmdb", State: configurationdomain.ConnectionConfigured}}
	resolver := &NotificationImageResolver{
		coverStore: fakeNotificationCoverStore{},
		posters:    newPosterTestResolver(server.URL),
		tmdb:       ConfiguredCandidateSearcher{Connections: connections, Secrets: testSecrets{"tmdb": "secret"}},
	}
	image, err := resolver.ResolveNotificationImage(context.Background(), plugintargetport.NotificationImageRequest{BGMID: "123", TMDBID: "42", TMDBKind: "tv"})
	if err != nil || image.Source != "tmdb" || image.URL != "https://image.tmdb.org/t/p/w342/poster.jpg" {
		t.Fatalf("image=%+v err=%v", image, err)
	}
}

func TestPosterSourceURLQueriesOnlyTheBoundBangumiSubject(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/subject/123" {
			t.Fatalf("unexpected BGM path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":123,"images":{"large":"https://lain.bgm.tv/pic/cover/l/1/123.jpg","common":"https://lain.bgm.tv/pic/cover/c/1/123.jpg"}}`)
	}))
	defer server.Close()
	resolver := newPosterTestResolver(server.URL)
	for range 2 {
		imageURL, err := resolver.PosterSourceURL(context.Background(), "123")
		if err != nil || imageURL != "https://lain.bgm.tv/r/400/pic/cover/l/1/123.jpg" {
			t.Fatalf("imageURL=%q err=%v", imageURL, err)
		}
	}
	if requests != 1 {
		t.Fatalf("Bangumi subject requests=%d, want one cached lookup", requests)
	}
}

func TestNotificationImageResolverNoMetadataIsNotAnError(t *testing.T) {
	resolver := &NotificationImageResolver{coverStore: fakeNotificationCoverStore{}}
	image, err := resolver.ResolveNotificationImage(context.Background(), plugintargetport.NotificationImageRequest{})
	if err != nil || image.URL != "" || image.Source != "" {
		t.Fatalf("image=%+v err=%v", image, err)
	}
}
