package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
)

// staticConnections is the connection source port in its smallest test form.
type staticConnections []configurationdomain.Connection

func (s staticConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return s, nil
}

// staticClientResolver hands the test's own client to the adapter, so every
// lookup test stays inside the process: no test reaches a real provider.
type staticClientResolver struct {
	client *http.Client
}

func (r staticClientResolver) ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error) {
	if r.client == nil {
		return nil, errors.New("test client is not configured")
	}
	return r.client, nil
}

// routedLookup wires a lookup that resolves the saved connection's network
// route to the test server's client. The route is always set so the adapter
// never falls back to the real direct transport.
func routedLookup(t *testing.T, server *httptest.Server, kind configurationdomain.ConnectionKind) ConfiguredGroupLookup {
	t.Helper()
	return ConfiguredGroupLookup{
		Connections: staticConnections{{
			ID: "conn-test", Kind: kind, Name: string(kind), Endpoint: server.URL,
			NetworkRouteID: "route-test", State: configurationdomain.ConnectionConfigured,
		}},
		ClientResolver: staticClientResolver{client: server.Client()},
	}
}

func mikanGroupPage(title string) string {
	return "<html><head><title>" + title + "</title></head><body><div id=\"page\"></div></body></html>"
}

func TestMikanReadGroupByIDReadsPublishedName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Home/PublishGroup/58" {
			t.Fatalf("unexpected Mikan request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(mikanGroupPage("Mikan Project - SampleGroup")))
	}))
	defer server.Close()

	entry, found, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), "58")
	if err != nil || !found {
		t.Fatalf("live Mikan group page not read: found=%v err=%v", found, err)
	}
	if entry.Ref.Key() != "mikan:58" || entry.DisplayName != "SampleGroup" || !entry.Available {
		t.Fatalf("unexpected Mikan entry: %+v", entry)
	}
}

func TestMikanReadGroupByIDNormalizesTheID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Home/PublishGroup/58" {
			t.Fatalf("id was not normalized before the request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(mikanGroupPage("Mikan Project - SampleGroup")))
	}))
	defer server.Close()

	entry, found, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), " 0058 ")
	if err != nil || !found || entry.Ref.ExternalID != "58" {
		t.Fatalf("normalization failed: entry=%+v found=%v err=%v", entry, found, err)
	}
	for _, invalid := range []string{"", "abc", "0", "-3", "58rss"} {
		if _, _, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), invalid); err == nil {
			t.Fatalf("invalid Mikan id %q was accepted", invalid)
		}
	}
}

// A retired id answers 302 to the site root. Following it would turn "no such
// group" into "200 and the site's default title", so the reader reports absence
// and never sees the redirect target.
func TestMikanReadGroupByIDReportsRetiredIDAsAbsent(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			redirected.Add(1)
			_, _ = w.Write([]byte(mikanGroupPage("Mikan Project")))
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	}))
	defer server.Close()

	entry, found, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), "68")
	if err != nil || found {
		t.Fatalf("retired id reported as a group: entry=%+v found=%v err=%v", entry, found, err)
	}
	if redirected.Load() != 0 {
		t.Fatal("the reader followed the retired id's redirect to the site root")
	}
}

func TestMikanReadGroupByIDReportsMissingPageAsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if _, found, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), "58"); err != nil || found {
		t.Fatalf("404 not treated as absence: found=%v err=%v", found, err)
	}
}

func TestMikanReadGroupByIDRejectsAStructureChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mikanGroupPage("Mikan Project")))
	}))
	defer server.Close()

	// A 200 without the group-name title marker is a page this reader does not
	// understand; it must not be read as a group named "Mikan Project".
	if _, found, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), "58"); err == nil || found {
		t.Fatalf("page without the title marker was accepted: found=%v err=%v", found, err)
	}
}

// A provider outage must never read as "that group does not exist".
func TestMikanReadGroupByIDRejectsProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if _, found, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupByID(context.Background(), "58"); err == nil || found {
		t.Fatalf("provider failure read as absence: found=%v err=%v", found, err)
	}
}

// aniBTDirectoryHTML renders the two channels of the AniBT directory page with
// the shapes the live page carries: one card per group carrying
// data-group-slug plus an accessible-name link, and the TanStack data island,
// whose group objects run _id, name, slug in that order inside a JavaScript
// object literal (unquoted keys), exactly as anibt.net serves them.
func aniBTDirectoryHTML(groups ...[2]string) string {
	var cards, island strings.Builder
	island.WriteString(`<script>self.__TSR__=[{dehydratedData:{query:{initial:[{state:{data:{groups:[`)
	for index, group := range groups {
		slug, name := group[0], group[1]
		cards.WriteString(`<div data-testid="subtitle-group-card" data-group-slug="` + slug + `">`)
		cards.WriteString(`<a aria-label="Open subtitle group: ` + name + `" href="/en/group/` + slug + `"></a></div>`)
		if index > 0 {
			island.WriteString(",")
		}
		island.WriteString(`{_id:"ns` + strconv.Itoa(index+1) + `",_creationTime:1,organizationId:"org",name:"` + name + `",slug:"` + slug + `"}`)
	}
	island.WriteString(`]}}}]}}}]</script>`)
	return "<html><body>" + cards.String() + island.String() + "</body></html>"
}

func TestAniBTReadGroupCatalogParsesCardsAndCrossChecksTheIsland(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/en/group" {
			t.Fatalf("unexpected AniBT request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(aniBTDirectoryHTML(
			[2]string{"samplegroup", "SampleGroup"},
			[2]string{"anibt-subs", "AniBT"},
		)))
	}))
	defer server.Close()

	entries, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected AniBT catalog: %+v", entries)
	}
	if entries[0].Ref.Key() != "anibt:samplegroup" || entries[0].DisplayName != "SampleGroup" {
		t.Fatalf("unexpected first AniBT entry: %+v", entries[0])
	}
	if entries[1].Ref.ExternalID != "anibt-subs" || entries[1].DisplayName != "AniBT" {
		t.Fatalf("unexpected second AniBT entry: %+v", entries[1])
	}
	for _, entry := range entries {
		if !entry.Available {
			t.Fatalf("AniBT entry lost its availability: %+v", entry)
		}
	}
}

func TestAniBTReadGroupCatalogToleratesAnAbsentIsland(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div data-group-slug="alicia"><a aria-label="Open subtitle group: 艾莉希婭" href="/en/group/alicia"></a></div></body></html>`))
	}))
	defer server.Close()

	entries, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupCatalog(context.Background())
	if err != nil || len(entries) != 1 || entries[0].DisplayName != "艾莉希婭" {
		t.Fatalf("card-only directory rejected: entries=%+v err=%v", entries, err)
	}
}

func TestAniBTReadGroupCatalogRejectsIslandDisagreement(t *testing.T) {
	page := strings.Replace(aniBTDirectoryHTML([2]string{"samplegroup", "SampleGroup"}), `name:"SampleGroup"`, `name:"Something Else"`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	if _, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupCatalog(context.Background()); err == nil {
		t.Fatal("a card/island name disagreement was accepted")
	}
}

func TestAniBTReadGroupCatalogRejectsBrokenPages(t *testing.T) {
	pages := map[string]string{
		"no cards":            `<html><body><div>nothing here</div></body></html>`,
		"card without a name": `<html><body><div data-group-slug="alicia"></div></body></html>`,
		"duplicate slug": `<html><body>` +
			`<div data-group-slug="alicia"><a aria-label="Open subtitle group: 艾莉希婭"></a></div>` +
			`<div data-group-slug="alicia"><a aria-label="Open subtitle group: 艾莉希婭"></a></div>` +
			`</body></html>`,
	}
	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(page))
			}))
			defer server.Close()
			if _, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupCatalog(context.Background()); err == nil {
				t.Fatalf("broken AniBT directory page (%s) was accepted", name)
			}
		})
	}
}

func TestAniBTReadGroupCatalogRejectsProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	if _, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroupCatalog(context.Background()); err == nil {
		t.Fatal("AniBT directory failure was accepted as an empty catalog")
	}
}

// countingLookupServer answers the Mikan group page with a title and counts
// every request it saw, so a test can prove how many provider reads a path made.
func countingLookupServer(t *testing.T, title string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(mikanGroupPage("Mikan Project - " + title)))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestConfiguredGroupLookupConfirmsAMikanKeyWithOneRead(t *testing.T) {
	server, requests := countingLookupServer(t, "SampleGroup", http.StatusOK)
	lookup := routedLookup(t, server, configurationdomain.ConnectionMikan)

	entries, err := lookup.Resolve(context.Background(), "mikan", "58")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Ref.Key() != "mikan:58" || entries[0].DisplayName != "SampleGroup" || !entries[0].Available {
		t.Fatalf("unexpected Mikan key candidates: %+v", entries)
	}
	if requests.Load() != 1 {
		t.Fatalf("Mikan key lookup made %d provider requests, want 1", requests.Load())
	}
}

func TestConfiguredGroupLookupConfirmsAMikanGroupPageURL(t *testing.T) {
	server, requests := countingLookupServer(t, "SampleGroup", http.StatusOK)
	lookup := routedLookup(t, server, configurationdomain.ConnectionMikan)

	for _, query := range []string{
		"https://mikanani.me/Home/PublishGroup/58",
		"mikanani.me/Home/PublishGroup/58",
		"https://mikanani.me/Home/PublishGroup/58?page=2",
	} {
		entries, err := lookup.Resolve(context.Background(), "mikan", query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if len(entries) != 1 || entries[0].Ref.ExternalID != "58" {
			t.Fatalf("%s resolved to %+v", query, entries)
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("three group page URLs made %d provider requests, want 3", requests.Load())
	}
}

// A retired key resolves to nothing at the provider. Reporting it is the
// dictionary's business (the collected snapshot knows the id), so the network
// resolver must not invent an answer.
func TestConfiguredGroupLookupResolvesARetiredMikanKeyToNothing(t *testing.T) {
	server, requests := countingLookupServer(t, "", http.StatusFound)
	lookup := routedLookup(t, server, configurationdomain.ConnectionMikan)

	entries, err := lookup.Resolve(context.Background(), "mikan", "68")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("retired key lookup made %d provider requests, want 1", requests.Load())
	}
	if len(entries) != 0 {
		t.Fatalf("retired key resolved to %+v", entries)
	}
}

// Mikan has no by-name endpoint, so a name is answered without a provider
// request — and without resolving a connection either: reporting the provider
// as unavailable would misdescribe a provider that was never asked anything.
func TestConfiguredGroupLookupMakesNoMikanRequestForAName(t *testing.T) {
	server, requests := countingLookupServer(t, "SampleGroup", http.StatusOK)
	lookup := routedLookup(t, server, configurationdomain.ConnectionMikan)

	for _, query := range []string{"samplegroup", "SampleGroup", "some 中文 name", "https://example.test/not/a/group/page"} {
		entries, err := lookup.Resolve(context.Background(), "mikan", query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if len(entries) != 0 {
			t.Fatalf("Mikan name query %q resolved to %+v", query, entries)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("Mikan name queries made %d provider requests, want 0", requests.Load())
	}

	// The same name against a lookup with no connection at all: the answer is
	// still "nothing", not 503 PROVIDER_GROUP_LOOKUP_FAILED.
	if entries, err := (ConfiguredGroupLookup{}).Resolve(context.Background(), "mikan", "SampleGroup"); err != nil || len(entries) != 0 {
		t.Fatalf("connection-less Mikan name lookup = %+v err=%v", entries, err)
	}
}

func TestConfiguredGroupLookupResolvesAniBTSlugsAndLinksInOneRead(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/en/group" {
			t.Fatalf("unexpected AniBT request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(aniBTDirectoryHTML(
			[2]string{"samplegroup", "SampleGroup"},
			[2]string{"sample-secondary", "SampleSecondary"},
		)))
	}))
	defer server.Close()
	lookup := routedLookup(t, server, configurationdomain.ConnectionAniBT)

	cases := map[string]string{
		// A slug, and a display name that happens to also be a legal slug.
		"samplegroup": "samplegroup",
		"SampleGroup": "samplegroup",
		// The group page URL, with and without its scheme.
		"https://anibt.net/en/group/sample-secondary": "sample-secondary",
		"anibt.net/en/group/sample-secondary":         "sample-secondary",
	}
	for query, want := range cases {
		entries, err := lookup.Resolve(context.Background(), "anibt", query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if len(entries) != 1 || entries[0].Ref.ExternalID != want {
			t.Fatalf("%s resolved to %+v, want exactly %s", query, entries, want)
		}
		if !entries[0].Available {
			t.Fatalf("%s lost its availability: %+v", query, entries[0])
		}
	}
	// Every path above is one directory read: there is no per-group follow-up.
	if got := requests.Load(); got != int32(len(cases)) {
		t.Fatalf("%d queries made %d AniBT requests", len(cases), got)
	}
}

// Discovery confirms keys only: an AniBT name makes no request at all, and a
// slug the directory does not list is an empty answer after its one read.
func TestConfiguredGroupLookupResolvesAniBTKeysOnly(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(aniBTDirectoryHTML(
			[2]string{"samplegroup", "SampleGroup"},
			[2]string{"sample-second", "示例字幕组"},
		)))
	}))
	defer server.Close()
	lookup := routedLookup(t, server, configurationdomain.ConnectionAniBT)

	for _, name := range []string{"示例", "Sub Group", "示例字幕组"} {
		if entries, err := lookup.Resolve(context.Background(), "anibt", name); err != nil || len(entries) != 0 {
			t.Fatalf("AniBT name %q resolved to %+v err=%v", name, entries, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("AniBT name queries made %d requests, want 0", requests.Load())
	}
	// "sub" is a legal slug that the directory does not list: no substring match.
	if entries, err := lookup.Resolve(context.Background(), "anibt", "sub"); err != nil || len(entries) != 0 {
		t.Fatalf("unlisted AniBT slug returned %+v err=%v, want an empty list", entries, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("unlisted slug made %d AniBT requests, want 1", requests.Load())
	}
}

// QueryKey classifies input without a provider read, which is what lets the
// dictionary answer a paste of a key or a link from its own layers.
func TestConfiguredGroupLookupQueryKeyClassifiesWithoutNetwork(t *testing.T) {
	lookup := ConfiguredGroupLookup{}
	cases := []struct {
		provider string
		query    string
		want     string
		ok       bool
	}{
		{"mikan", "58", "58", true},
		{"mikan", " 0058 ", "58", true},
		{"mikan", "https://mikanani.me/Home/PublishGroup/58", "58", true},
		{"mikan", "SampleGroup", "", false},
		{"anibt", "samplegroup", "samplegroup", true},
		{"anibt", "https://anibt.net/en/group/ani", "ani", true},
		{"anibt", "示例字幕组", "", false},
		{"bgm", "58", "", false},
	}
	for _, testCase := range cases {
		key, ok := lookup.QueryKey(testCase.provider, testCase.query)
		if ok != testCase.ok || key != testCase.want {
			t.Fatalf("QueryKey(%q, %q) = %q, %v; want %q, %v", testCase.provider, testCase.query, key, ok, testCase.want, testCase.ok)
		}
	}
}

func TestConfiguredGroupLookupRejectsBadInputWithoutAProviderRead(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(mikanGroupPage("Mikan Project - SampleGroup")))
	}))
	defer server.Close()
	lookup := routedLookup(t, server, configurationdomain.ConnectionMikan)

	if _, err := lookup.Resolve(context.Background(), "bgm", "58"); err == nil {
		t.Fatal("an unsupported provider was accepted")
	}
	if _, err := lookup.Resolve(context.Background(), "mikan", strings.Repeat("x", maxGroupLookupQueryLength+1)); err == nil {
		t.Fatal("an over-long query was accepted")
	}
	if entries, err := lookup.Resolve(context.Background(), "mikan", "   "); err != nil || len(entries) != 0 {
		t.Fatalf("blank query returned %+v err=%v", entries, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("rejected input made %d provider requests", requests.Load())
	}
}

// A connection-less lookup must fail closed with the service-unavailable shape
// instead of reaching the real direct transport.
func TestConfiguredGroupLookupReportsAnUnconfiguredConnection(t *testing.T) {
	_, err := (ConfiguredGroupLookup{}).Resolve(context.Background(), "mikan", "58")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "PROVIDER_GROUP_LOOKUP_FAILED" {
		t.Fatalf("unconfigured lookup error = %v", err)
	}
}
