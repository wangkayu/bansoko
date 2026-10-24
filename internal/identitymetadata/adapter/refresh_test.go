package adapter

// The 刷新元数据 adapter over the disposable schema: one bound work's
// re-pull hits its provider once, records the native titles and rewrites the
// persistent episode title rows through the overwrite store, and every
// failure is the typed conflict the action surfaces — with the cache rows
// left exactly as they were.
import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/episodetitle"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
)

// recordingTitleCache is the persistent episode title store stub the adapter
// drives; it records the exact keys of every RefreshWork it sees.
type recordingTitleCache struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (c *recordingTitleCache) RefreshWork(ctx context.Context, source episodetitle.Source, workID string, season int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, string(source)+"#"+workID+"#"+strings.TrimSpace(intToText(season)))
	return c.err
}

func intToText(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

type recordingTitleRecorder struct {
	mu      sync.Mutex
	records []identitymetadatadomain.WorkTitles
}

func (r *recordingTitleRecorder) RecordWorkTitles(ctx context.Context, titles []identitymetadatadomain.WorkTitles) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, titles...)
	return nil
}

// TestConfiguredWorkRefresherRePullsWorkAndEpisodeTitles drives one BGM and
// one TMDB tv re-pull over local servers: each provider is read once, the
// native titles land in the recorder, and the episode title keys re-pulled are
// BGM season 0 and the bound TMDB season (an unrecorded season reads season 1).
func TestConfiguredWorkRefresherRePullsWorkAndEpisodeTitles(t *testing.T) {
	ctx := context.Background()
	bgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/subjects/10" {
			t.Fatalf("unexpected Bangumi request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"name":"Show","name_cn":"作品","date":"2024-03-01"}`))
	}))
	defer bgm.Close()
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/42" {
			t.Fatalf("unexpected TMDB request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"name":"作品","original_name":"Original Show","overview":"作品简介，记录于匹配后。","first_air_date":"2024-03-01"}`))
	}))
	defer tmdb.Close()

	cache := &recordingTitleCache{}
	titles := &recordingTitleRecorder{}
	refresher := ConfiguredWorkRefresher{
		Connections: testConnections{
			{ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: bgm.URL, State: configurationdomain.ConnectionConfigured},
			{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: tmdb.URL, SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured},
		},
		Secrets:       testSecrets{"tmdb-secret": "secret"},
		BGMEndpoint:   bgm.URL,
		EpisodeTitles: cache,
		Titles:        titles,
	}
	if err := refresher.RefreshWork(ctx, "bgm", "subject", "10", 0); err != nil {
		t.Fatalf("BGM re-pull: %v", err)
	}
	if err := refresher.RefreshWork(ctx, "tmdb", "tv", "42", 0); err != nil {
		t.Fatalf("TMDB re-pull: %v", err)
	}
	if strings.Join(cache.calls, " ") != "bgm#10#0 tmdb#42#1" {
		t.Fatalf("the episode title keys must follow the provider scopes, got %v", cache.calls)
	}
	if len(titles.records) != 2 {
		t.Fatalf("the re-pull records one native title per work, got %+v", titles.records)
	}
	if titles.records[0].Provider != "bgm" || titles.records[1].Provider != "tmdb" {
		t.Fatalf("the recorded titles must be provider-typed, got %+v", titles.records)
	}
	// The overview follows the re-pulled titles: TMDB records the work
	// summary, BGM (no equivalent surface) records none.
	if titles.records[1].Overview != "作品简介，记录于匹配后。" {
		t.Fatalf("the TMDB re-pull records the overview, got %q", titles.records[1].Overview)
	}
	if titles.records[0].Overview != "" {
		t.Fatalf("BGM has no overview surface, got %q", titles.records[0].Overview)
	}
}

// TestConfiguredWorkRefresherFailsTyped: a provider outage is the
// METADATA_REFRESH_FAILED conflict and the episode title rows never rewrite;
// a missing TMDB credential is the typed 409 and never reaches the provider.
func TestConfiguredWorkRefresherFailsTyped(t *testing.T) {
	ctx := context.Background()
	dialing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer dialing.Close()

	cache := &recordingTitleCache{}
	refresher := ConfiguredWorkRefresher{
		Connections: testConnections{
			{ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: dialing.URL, State: configurationdomain.ConnectionConfigured},
		},
		EpisodeTitles: cache,
	}
	err := refresher.RefreshWork(ctx, "bgm", "subject", "10", 0)
	if failed, ok := err.(*apperrors.Business); !ok || failed.Code != "METADATA_REFRESH_FAILED" {
		t.Fatalf("an unreachable provider is METADATA_REFRESH_FAILED, got %v", err)
	}
	if len(cache.calls) != 0 {
		t.Fatalf("a failed re-pull must not rewrite the cache rows, got %v", cache.calls)
	}

	// No TMDB credential: the typed 409 and no provider request — the request
	// is never built.
	hits := 0
	var hitsMu sync.Mutex
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsMu.Lock()
		hits++
		hitsMu.Unlock()
	}))
	defer proxy.Close()
	unkeyed := ConfiguredWorkRefresher{
		Connections: testConnections{
			{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: proxy.URL, SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured},
		},
		Secrets:       testSecrets{},
		EpisodeTitles: cache,
	}
	ctrlErr := unkeyed.RefreshWork(ctx, "tmdb", "tv", "42", 1)
	if credErr, ok := ctrlErr.(*apperrors.Business); !ok || credErr.Code != "TMDB_CREDENTIAL_NOT_CONFIGURED" {
		t.Fatalf("a missing credential is the typed 409, got %v", ctrlErr)
	}
	hitsMu.Lock()
	total := hits
	hitsMu.Unlock()
	if total != 0 {
		t.Fatalf("a missing credential never reaches the provider, hits=%d", total)
	}
}
