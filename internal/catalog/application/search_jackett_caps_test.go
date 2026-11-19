package application

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestJackettSearchKeepsFirstValidDistinctCategories(t *testing.T) {
	var attrs, caps strings.Builder
	caps.WriteString(`<caps><categories>`)
	attrs.WriteString(`<torznab:attr name="category" value="missing"/><torznab:attr name="category" value="oversize"/>`)
	fmt.Fprintf(&caps, `<category id="oversize" name="%s"/>`, strings.Repeat("X", 257))
	var want []acquisitiondomain.SeedCategory
	for i := 0; i < acquisitiondomain.MaxSeedCategories+4; i++ {
		id, name := fmt.Sprintf("%d", 6000-i), fmt.Sprintf("Category %d", i)
		fmt.Fprintf(&attrs, `<torznab:attr name="category" value="%s"/><torznab:attr name="category" value="%s"/>`, id, id)
		fmt.Fprintf(&caps, `<category id="%s" name="%s"/>`, id, name)
		if i < acquisitiondomain.MaxSeedCategories {
			want = append(want, acquisitiondomain.SeedCategory{ID: id, Name: name})
		}
	}
	caps.WriteString(`</categories></caps>`)
	feed := strings.Replace(jackettTestBody(), `<jackettindexer`, attrs.String()+`<jackettindexer`, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "search":
			_, _ = io.WriteString(w, feed)
		case "caps":
			_, _ = io.WriteString(w, caps.String())
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()
	service := NewProviderService(nil, nil)
	store := &searchTestStore{}
	service.SetFeedSnapshotStore(store)
	app := SeedSearchApplication{
		Service: service,
		Endpoints: CatalogSearchEndpointsFunc(func(context.Context, string) (CatalogSearchEndpoint, error) {
			return CatalogSearchEndpoint{Endpoint: upstream.URL, Client: upstream.Client(), APIKey: jackettTestAPIKey}, nil
		}),
	}
	result, err := app.Fetch(context.Background(), FetchSearchSnapshotInput{Provider: ProviderJackett, Query: "Sample"})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.written) != 1 || len(store.written[0].Seeds) != 1 || len(result.Seeds) != 1 {
		t.Fatalf("expected one persisted and visible seed, got %d writes and %d visible seeds", len(store.written), len(result.Seeds))
	}
	if got := store.written[0].Seeds[0].Categories; !slices.Equal(got, want) {
		t.Fatalf("snapshot categories = %v; want %v", got, want)
	}
}

func TestJackettCapsBudgetPreservesPartialAndCachedResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		searcher := HTTPJackettSearcher{Endpoint: "https://caps-" + strings.ToLower(rand.Text()) + ".example.test", APIKey: jackettTestAPIKey}
		var mu sync.Mutex
		var active, peak int
		attempts := make(map[string]int)
		response := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		client := &http.Client{Transport: jackettCapsTestTransport(func(r *http.Request) (*http.Response, error) {
			id := strings.Split(r.URL.Path, "/")[4]
			mu.Lock()
			attempts[id]++
			active++
			peak = max(peak, active)
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()
			switch id {
			case "cached":
				return response(http.StatusOK, jackettCapsTestBody())
			case "fast":
				time.Sleep(50 * time.Millisecond)
				return response(http.StatusOK, jackettCapsTestBody())
			case "failed":
				return response(http.StatusBadGateway, "")
			default:
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		})}
		item := func(site string) acquisitiondomain.FeedItem {
			return acquisitiondomain.FeedItem{SourceSite: &acquisitiondomain.SeedSourceSite{ID: site}, SeedCategoryIDs: []string{"5071"}}
		}
		searcher.resolveSeedCategories(t.Context(), client, []acquisitiondomain.FeedItem{item("cached")})
		items := []acquisitiondomain.FeedItem{item("slow-0"), item("fast"), item("failed"), item("fast")}
		for i := 1; i < 20; i++ {
			items = append(items, item(fmt.Sprintf("slow-%d", i)))
		}
		items = append(items, item("cached"))
		start := time.Now()
		searcher.resolveSeedCategories(t.Context(), client, items)
		if elapsed := time.Since(start); elapsed != jackettCapsBudget {
			t.Fatalf("caps elapsed = %s, want %s", elapsed, jackettCapsBudget)
		}
		if peak != jackettCapsWorkers || active != 0 {
			t.Fatalf("peak concurrency = %d, still active = %d", peak, active)
		}
		if attempts["fast"] != 1 || attempts["cached"] != 1 || attempts["failed"] != 1 || attempts["slow-19"] != 0 {
			t.Fatalf("unexpected caps requests: %v", attempts)
		}
		for _, row := range items {
			switch row.SourceSite.ID {
			case "fast", "cached":
				if len(row.SeedCategories) != 1 || row.SeedCategories[0].Name != "TV/Anime" {
					t.Fatalf("%s lost available categories: %v", row.SourceSite.ID, row.SeedCategories)
				}
			default:
				if len(row.SeedCategories) != 0 {
					t.Fatalf("%s got another site's categories: %v", row.SourceSite.ID, row.SeedCategories)
				}
			}
		}
		searcher.resolveSeedCategories(t.Context(), client, []acquisitiondomain.FeedItem{item("failed"), item("slow-0")})
		if attempts["failed"] != 2 || attempts["slow-0"] != 2 || time.Since(start) != jackettCapsBudget+jackettCapsSiteBudget {
			t.Fatalf("failures must retry with the per-site budget; attempts = %v, elapsed = %s", attempts, time.Since(start))
		}
	})
}

type jackettCapsTestTransport func(*http.Request) (*http.Response, error)

func (fn jackettCapsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}
