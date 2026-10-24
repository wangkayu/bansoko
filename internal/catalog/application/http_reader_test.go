package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"bansoko/internal/catalog/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

type mikanCatalogWarningRecorder struct {
	failures []observabilityport.DiagnosticFailureRequest
	routines []observabilityport.RoutineRequest
}

func (r *mikanCatalogWarningRecorder) UpsertRoutine(_ context.Context, req observabilityport.RoutineRequest) error {
	r.routines = append(r.routines, req)
	return nil
}

func (r *mikanCatalogWarningRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}

func (*mikanCatalogWarningRecorder) RecordDiagnosticSuccess(context.Context, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func (*mikanCatalogWarningRecorder) UpsertRoutineTx(context.Context, txscope.Tx, observabilityport.RoutineRequest) error {
	return nil
}

func (*mikanCatalogWarningRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, observabilityport.DiagnosticFailureRequest) error {
	return nil
}

func (*mikanCatalogWarningRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func TestHTTPReaderReadsOfficialSeasonListAndConditionalHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/seasons/anime" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("season") != "2026-SUMMER" {
			t.Fatalf("season query = %s", r.URL.Query().Get("season"))
		}
		w.Header().Set("ETag", `W/"season-v1"`)
		w.Header().Set("Last-Modified", "Sat, 11 Jul 2026 04:10:08 GMT")
		if r.Header.Get("If-None-Match") == `W/"season-v1"` {
			if r.Header.Get("If-Modified-Since") == "" {
				t.Fatal("missing If-Modified-Since on conditional request")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "ok": true,
  "data": {
    "requestedSeason": "2026夏",
    "total": 1,
    "byWeekday": [{"weekday": 5, "animes": [{
      "bgmId": 543360,
      "animeId": "anibt-anime-1",
      "cover": "https://r2.anibt.net/cover.jpg",
      "title": {"primary": "Primary title", "japanese": "Original title"},
      "format": "TV",
      "kind": "TV",
      "episodes": 12,
      "scheduleStatus": "scheduled",
      "weekday": 5,
      "rssReleaseCount": 7,
      "hasRelease": true
    }]}]
  }
}`))
	}))
	defer server.Close()

	reader := HTTPReader{Endpoint: server.URL, Client: server.Client()}
	result, err := reader.ReadSeason(context.Background(), "2026-SUMMER", domain.ReadHints{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].BGMID != "543360" || result.Entries[0].Title != "Primary title" {
		t.Fatalf("unexpected entries: %+v", result.Entries)
	}
	if result.SourceVersion != "2026夏" || result.Cursor != "2026夏" || result.ETag == "" || result.ContractVersion != anibtSeasonContractVersion {
		t.Fatalf("unexpected provider metadata: %+v", result)
	}
	if result.Entries[0].SourceHint["animeId"] != "anibt-anime-1" {
		t.Fatalf("source provenance was not preserved: %+v", result.Entries[0].SourceHint)
	}
	// The stored hint carries AniBT's own release facts for the catalog card.
	item := domain.Item{Provider: ProviderAniBT, SourceHint: result.Entries[0].SourceHint}
	ApplyReleaseAvailability(&item)
	if item.ReleaseState != domain.ReleaseStateHasReleases || item.ReleaseCount == nil || *item.ReleaseCount != 7 || item.UpdateText != "" {
		t.Fatalf("AniBT release availability = %+v", item)
	}

	unchanged, err := reader.ReadSeason(context.Background(), "2026-SUMMER", domain.ReadHints{ETag: result.ETag, LastModified: "Sat, 11 Jul 2026 04:10:08 GMT", Cursor: result.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.NotModified || unchanged.ETag != result.ETag || unchanged.Cursor != result.Cursor {
		t.Fatalf("unexpected not-modified result: %+v", unchanged)
	}
}

func TestHTTPReaderRejectsDuplicateOrInvalidEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"requestedSeason":"2026夏","total":2,"byWeekday":[{"animes":[{"bgmId":1,"title":{"primary":"one"},"scheduleStatus":"scheduled"},{"bgmId":1,"title":{"primary":"duplicate"},"scheduleStatus":"scheduled"}]}]}}`))
	}))
	defer server.Close()

	_, err := (HTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "2026-SUMMER", domain.ReadHints{})
	if err == nil {
		t.Fatal("duplicate catalog identity was accepted")
	}
}

func TestMikanHTTPReaderReadsSeasonEntriesAndPreservesProviderIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Home/BangumiCoverFlowByDayOfWeek" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("year") != "2026" || r.URL.Query().Get("seasonStr") != "夏" {
			t.Fatalf("unexpected season query: %s", r.URL.RawQuery)
		}
		w.Header().Set("ETag", `W/"mikan-v1"`)
		_, _ = w.Write([]byte(`<div data-dayofweek="5"><ul>
<li><span data-bangumiid="12345" data-src="https://mikan.example/cover.jpg"></span><a class="an-text" title="Mikan title">fallback</a><div class="date-text">每周五更新</div></li>
</ul></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "SUMMER 2026", domain.ReadHints{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %+v", result.Entries)
	}
	entry := result.Entries[0]
	if entry.ProviderEntryRef != "12345" || entry.BGMID != "" || entry.Title != "Mikan title" {
		t.Fatalf("unexpected Mikan identity/title: %+v", entry)
	}
	if entry.SourceHint["bangumi_id"] != "12345" || entry.SourceHint["cover"] != "https://mikan.example/cover.jpg" {
		t.Fatalf("Mikan provenance was not preserved: %+v", entry.SourceHint)
	}
}

func TestMikanHTTPReaderResolvesRelativeCoverURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div data-dayofweek="5"><ul>
<li><span data-bangumiid="12345" data-src="/images/Bangumi/cover.webp?width=400"></span><a class="an-text" title="Mikan title">fallback</a><div class="date-text">每周五更新</div></li>
</ul></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "2026-summer", domain.ReadHints{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Entries[0].SourceHint["cover"]; got != server.URL+"/images/Bangumi/cover.webp?width=400" {
		t.Fatalf("relative Mikan cover was not resolved: %v", got)
	}
	if got := result.Entries[0].SourceHint["greyout"]; got != false {
		t.Fatalf("an entry with works must record greyout=false: %v", got)
	}
	item := domain.Item{Provider: ProviderMikan, SourceHint: result.Entries[0].SourceHint}
	ApplyReleaseAvailability(&item)
	if item.ReleaseState != domain.ReleaseStateHasReleases || item.UpdateText != "每周五" {
		t.Fatalf("Mikan availability = %+v", item)
	}
}

func TestMikanHTTPReaderReadsGreyoutTitleWithoutAnimeLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div data-dayofweek="7"><ul>
<li><span class="b-lazy greyout" data-bangumiid="3896"></span><div class="an-info"><div class="an-info-group"><div class="date-text">此番组下暂无作品</div><div class="date-text" title="着迷男">着迷男</div></div></div></li>
</ul></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "2026-summer", domain.ReadHints{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].ProviderEntryRef != "3896" || result.Entries[0].Title != "着迷男" {
		t.Fatalf("greyout title was not retained: %+v", result.Entries)
	}
	hint := result.Entries[0].SourceHint
	if hint["greyout"] != true || hint["update_text"] != "" {
		t.Fatalf("greyout fact was not recorded or the placeholder leaked as update text: %+v", hint)
	}
	item := domain.Item{Provider: ProviderMikan, SourceHint: hint}
	ApplyReleaseAvailability(&item)
	if item.ReleaseState != domain.ReleaseStateNoReleases || item.ReleaseCount != nil || item.UpdateText != "" {
		t.Fatalf("Mikan greyout availability = %+v", item)
	}
}

func TestMikanHTTPReaderTreatsEmptyWeekdayContainersAsValidEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div data-dayofweek="1"><ul></ul></div><div data-dayofweek="2"></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "2026-summer", domain.ReadHints{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 0 || result.ContractVersion != mikanSeasonContractVersion {
		t.Fatalf("unexpected empty Mikan result: %+v", result)
	}
}

func TestMikanHTTPReaderRecordsWarningForEmptyCatalog(t *testing.T) {
	recorder := &mikanCatalogWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div data-dayofweek="1"><ul></ul></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadSeason(context.Background(), "2026-summer", domain.ReadHints{})
	if err != nil || len(result.Entries) != 0 {
		t.Fatalf("empty Mikan catalog failed: %+v err=%v", result, err)
	}
	if len(recorder.failures) != 1 || recorder.failures[0].Identity.ErrorCode != observabilityport.ErrorProviderParserEmpty {
		t.Fatalf("empty catalog warning = %+v", recorder.failures)
	}
	if len(recorder.routines) != 1 || recorder.routines[0].Identity.Outcome != observabilityport.RoutineOutcomeWarning || recorder.routines[0].Identity.ResultCode != observabilityport.RoutineResultProviderParserWarning {
		t.Fatalf("empty catalog routine = %+v", recorder.routines)
	}
}

func TestMikanHTTPReaderRecordsWarningForSchemaVersionChange(t *testing.T) {
	recorder := &mikanCatalogWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Mikan-Schema-Version", "mikan-season-html-v2")
		_, _ = w.Write([]byte(`<div data-dayofweek="1"><ul>
<li><span data-bangumiid="12345"></span><a class="an-text" title="Mikan title"></a></li>
</ul></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadSeason(context.Background(), "2026-summer", domain.ReadHints{})
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("schema-version Mikan catalog failed: %+v err=%v", result, err)
	}
	if len(recorder.failures) != 1 || recorder.failures[0].Identity.ErrorCode != observabilityport.ErrorProviderParserSchemaMismatch {
		t.Fatalf("schema catalog warning = %+v", recorder.failures)
	}
	if !strings.Contains(string(recorder.failures[0].Details), "mikan-season-html-v2") {
		t.Fatalf("schema catalog warning details = %s", recorder.failures[0].Details)
	}
}

func TestMikanHTTPReaderRecoversRoutineOnNonEmptyResult(t *testing.T) {
	recorder := &mikanCatalogWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div data-dayofweek="1"><ul>
<li><span data-bangumiid="12345"></span><a class="an-text" title="Mikan title"></a></li>
</ul></div>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadSeason(context.Background(), "2026-summer", domain.ReadHints{})
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("non-empty Mikan catalog failed: %+v err=%v", result, err)
	}
	if len(recorder.failures) != 0 {
		t.Fatalf("non-empty result must not warn: %+v", recorder.failures)
	}
	// A successful read must restore the routine to success so a previous
	// parser warning does not stay visible forever.
	if len(recorder.routines) != 1 || recorder.routines[0].Identity.Outcome != observabilityport.RoutineOutcomeSuccess || recorder.routines[0].Identity.ResultCode != observabilityport.RoutineResultProviderRefreshCompleted {
		t.Fatalf("non-empty result routine must recover to success: %+v", recorder.routines)
	}
}

func TestMikanHTTPReaderRejectsSchemaMismatchAndDuplicateBangumiIDs(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing weekday containers", body: `<main><li><span data-bangumiid="1"></span></li></main>`},
		{name: "item without bangumi id", body: `<div data-dayofweek="1"><li><a class="an-text" title="missing id"></a></li></div>`},
		{name: "duplicate bangumi ids", body: `<div data-dayofweek="1"><li><span data-bangumiid="1"></span><a class="an-text" title="one"></a></li><li><span data-bangumiid="1"></span><a class="an-text" title="two"></a></li></div>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			_, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "2026-spring", domain.ReadHints{})
			if err == nil {
				t.Fatal("schema-invalid Mikan response was accepted")
			}
		})
	}
}

// The body is the real answer Mikan gave for the season that had just
// started: HTTP 200, only the shared SVG preamble, no containers.
func TestMikanHTTPReaderTreatsUnpopulatedSeasonAsValidEmptyWithoutWarning(t *testing.T) {
	body, err := os.ReadFile("testdata/mikan_season_unpopulated_20261002.html")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &mikanCatalogWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadSeason(context.Background(), "2026-autumn", domain.ReadHints{})
	if err != nil {
		t.Fatalf("unpopulated season must not be a read failure: %v", err)
	}
	if len(result.Entries) != 0 || result.ContractVersion != mikanSeasonContractVersion || result.NotModified {
		t.Fatalf("unexpected unpopulated result: %+v", result)
	}
	if len(recorder.failures) != 0 {
		t.Fatalf("unpopulated season must raise no warning: %+v", recorder.failures)
	}
}

func TestMikanHTTPReaderClassifiesFailuresWithStableCodes(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{name: "http status", status: http.StatusInternalServerError, body: "", wantCode: domain.ReadFailureHTTPStatus},
		{name: "foreign page without the season template", status: http.StatusOK, body: "<html><body>Just a moment...</body></html>", wantCode: domain.ReadFailureSchemaMismatch},
		{name: "item without bangumi id", status: http.StatusOK, body: `<div data-dayofweek="1"><li><a class="an-text" title="x"></a></li></div>`, wantCode: domain.ReadFailureSchemaMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			_, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSeason(context.Background(), "2026-autumn", domain.ReadHints{})
			var classified *domain.ReadError
			if !errors.As(err, &classified) || classified.Code != test.wantCode {
				t.Fatalf("want code %s, got %v", test.wantCode, err)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint, client := server.URL, server.Client()
		server.Close()
		_, err := (MikanHTTPReader{Endpoint: endpoint, Client: client}).ReadSeason(context.Background(), "2026-autumn", domain.ReadHints{})
		var classified *domain.ReadError
		if !errors.As(err, &classified) || classified.Code != domain.ReadFailureUnreachable {
			t.Fatalf("want unreachable, got %v", err)
		}
	})
}
