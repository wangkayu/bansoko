package application

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/catalog/domain"
)

// TestAniBTSearchEncodesWholeQuery pins the upstream URL construction of the
// AniBT search adapter: the query travels as one whole url.Values-encoded
// parameter, so URL-significant characters (&, %, +), multibyte CJK and spaces
// are never split or spliced into extra parameters (issue #9).
func TestAniBTSearchEncodesWholeQuery(t *testing.T) {
	cases := []struct {
		query   string
		wantRaw string
	}{
		{query: "a&b", wantRaw: "/api/bgm/search?q=a%26b"},
		{query: "100%", wantRaw: "/api/bgm/search?q=100%25"},
		{query: "a+b", wantRaw: "/api/bgm/search?q=a%2Bb"},
		{query: "命运石之门", wantRaw: "/api/bgm/search?q=%E5%91%BD%E8%BF%90%E7%9F%B3%E4%B9%8B%E9%97%A8"},
		{query: "sp ace", wantRaw: "/api/bgm/search?q=sp+ace"},
	}
	for _, tc := range cases {
		var gotRaw string
		var gotValues []string
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotRaw = r.URL.RequestURI()
			gotValues = r.URL.Query()["q"]
			io.WriteString(w, `{"ok":true,"data":[{"bgmId":400602,"name":"葬送のフリーレン","nameCn":"葬送的芙莉莲"}]}`)
		}))
		reader := HTTPReader{Endpoint: mock.URL}

		result, err := reader.Search(context.Background(), tc.query, domain.ReadHints{})
		mock.Close()

		if err != nil {
			t.Fatalf("query %q: unexpected error: %v", tc.query, err)
		}
		if gotRaw != tc.wantRaw {
			t.Fatalf("query %q: upstream request URI = %s, want %s", tc.query, gotRaw, tc.wantRaw)
		}
		if len(gotValues) != 1 || gotValues[0] != tc.query {
			t.Fatalf("query %q: upstream decoded q values = %v, want exactly [%q]", tc.query, gotValues, tc.query)
		}
		if len(result.Entries) != 1 || result.Entries[0].BGMID != "400602" {
			t.Fatalf("query %q: entries = %+v", tc.query, result.Entries)
		}
		if result.ContractVersion != "anibt-search-v1" {
			t.Fatalf("query %q: contract version = %s", tc.query, result.ContractVersion)
		}
	}
}

// TestAniBTSearchUpstreamFailureIsTyped ensures every failure originating at
// or behind the provider upstream wraps ErrProviderUpstream, giving the
// transport a typed signal for a structured provider error instead of a 500
// INTERNAL (issue #9).
func TestAniBTSearchUpstreamFailureIsTyped(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "upstream 503", status: http.StatusServiceUnavailable, body: "unavailable"},
		{name: "upstream 400 html page", status: http.StatusBadRequest, body: "<html>bad request</html>"},
		{name: "unreadable payload", status: http.StatusOK, body: "<not-json>"},
		{name: "payload not ok", status: http.StatusOK, body: `{"ok":false,"data":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer mock.Close()

			_, err := (HTTPReader{Endpoint: mock.URL}).Search(context.Background(), "a&b", domain.ReadHints{})
			if !errors.Is(err, ErrProviderUpstream) {
				t.Fatalf("error = %v, want wrapped ErrProviderUpstream", err)
			}
		})
	}
}

// TestSearchProviderQueryBoundsReturnQueryInvalid keeps the OpenAPI q bound
// violation on the dedicated query sentinel; it must not surface as a provider
// capability error (ErrSearchNotSupported), which would mislead callers.
func TestSearchProviderQueryBoundsReturnQueryInvalid(t *testing.T) {
	service := NewProviderService(nil, map[string]Reader{
		ProviderAniBT: HTTPReader{Endpoint: "https://anibt.invalid"},
	}, nil)

	for name, query := range map[string]string{"empty": "", "too long": strings.Repeat("a", 257), "too long multibyte": strings.Repeat("界", 257)} {
		_, err := service.SearchProvider(context.Background(), ProviderAniBT, query)
		if !errors.Is(err, ErrSearchQueryInvalid) {
			t.Fatalf("%s query: error = %v, want ErrSearchQueryInvalid", name, err)
		}
		if errors.Is(err, ErrSearchNotSupported) {
			t.Fatalf("%s query must not reuse ErrSearchNotSupported", name)
		}
	}
}

// The program-search q bound has the same character semantics as the seed
// search contract; multibyte input is not rejected merely for its byte length.
func TestSearchProviderAccepts256MultibyteCharacters(t *testing.T) {
	query := strings.Repeat("界", 256)
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if got := r.URL.Query().Get("q"); got != query {
			t.Errorf("upstream q has %d characters, want 256", len([]rune(got)))
		}
		io.WriteString(w, `{"ok":true,"data":[]}`)
	}))
	defer upstream.Close()
	service := NewProviderService(nil, map[string]Reader{ProviderAniBT: HTTPReader{Endpoint: upstream.URL}}, nil)
	if _, err := service.SearchProvider(context.Background(), ProviderAniBT, query); err != nil {
		t.Fatalf("256-character query: %v", err)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

// TestSearchProviderCapabilityStillNotSupported guards the capability mapping:
// a provider whose reader does not implement Searcher keeps returning
// ErrSearchNotSupported after the query-validation split, so the sentinel keeps
// meaning "this provider has no keyword search at all".
func TestSearchProviderCapabilityStillNotSupported(t *testing.T) {
	service := NewProviderService(nil, map[string]Reader{
		ProviderMikan: seasonOnlyReader{},
	}, nil)

	_, err := service.SearchProvider(context.Background(), ProviderMikan, "x")
	if !errors.Is(err, ErrSearchNotSupported) {
		t.Fatalf("error = %v, want ErrSearchNotSupported", err)
	}
}

// Both discovery providers expose a keyword program search now: Mikan answers it
// from the Home/Search program block instead of the retired SEARCH_NOT_SUPPORTED
// answer. The fake endpoint below is not a valid URL on purpose, so the call
// proves the capability path is taken (an upstream failure) and not the
// capability refusal.
func TestSearchProviderReachesBothDiscoveryProviders(t *testing.T) {
	service := NewProviderService(nil, map[string]Reader{
		ProviderAniBT: HTTPReader{Endpoint: "https://anibt.invalid"},
		ProviderMikan: &MikanHTTPReader{},
	}, nil)
	for _, provider := range []string{ProviderAniBT, ProviderMikan} {
		_, err := service.SearchProvider(context.Background(), provider, "示例作品")
		if errors.Is(err, ErrSearchNotSupported) {
			t.Fatalf("%s still reports no keyword search: %v", provider, err)
		}
	}
}

// seasonOnlyReader implements only the season read, i.e. a provider with no
// keyword search surface.
type seasonOnlyReader struct{}

func (seasonOnlyReader) ReadSeason(context.Context, string, domain.ReadHints) (domain.ReadResult, error) {
	return domain.ReadResult{}, nil
}
