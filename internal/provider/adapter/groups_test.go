package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
)

type mikanParserWarningRecorder struct {
	failures []observabilityport.DiagnosticFailureRequest
	routines []observabilityport.RoutineRequest
}

func (r *mikanParserWarningRecorder) UpsertRoutine(_ context.Context, req observabilityport.RoutineRequest) error {
	r.routines = append(r.routines, req)
	return nil
}

func (r *mikanParserWarningRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}

func (*mikanParserWarningRecorder) RecordDiagnosticSuccess(context.Context, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func (*mikanParserWarningRecorder) UpsertRoutineTx(context.Context, txscope.Tx, observabilityport.RoutineRequest) error {
	return nil
}

func (*mikanParserWarningRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, observabilityport.DiagnosticFailureRequest) error {
	return nil
}

func (*mikanParserWarningRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func TestAniBTGroupReaderUsesStructuredAnimeGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/anime/groups" || r.URL.Query().Get("bgmId") != "543360" {
			t.Fatalf("unexpected AniBT request: %s", r.URL.String())
		}
		w.Header().Set("ETag", `W/"groups-v1"`)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":543360,"animeId":"anime","title":{"primary":"Title"},"total":2,"groups":[{"slug":"kitauji-sub","name":"Kitauji","status":"active","totalReleases":10,"lastUpdatedAt":1},{"slug":"sample-secondary","name":"SampleSecondary","status":"active","totalReleases":5,"lastUpdatedAt":1}]}}`))
	}))
	defer server.Close()

	result, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client(), Now: func() time.Time { return time.Unix(10, 0) }}).ReadGroups(context.Background(), "543360")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || result.Groups[0].Ref.Key() != "anibt:kitauji-sub" || result.Groups[0].FeedLocator.CatalogEntryRef != "543360" {
		t.Fatalf("unexpected AniBT groups: %+v", result)
	}
	if result.SchemaVersion != aniBTGroupSchemaVersion || result.SourceVersion == "" {
		t.Fatalf("AniBT provenance was not retained: %+v", result)
	}
}

func TestMikanGroupReaderExtractsRealSubgroupIDAndTextFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Home/Bangumi/3706" {
			t.Fatalf("unexpected Mikan request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`<html><body>
<div class="subgroup-text" id="page-anchor-1">
  <a href="/Home/PublishGroup/223">SampleGroup</a>
  <a href="/RSS/Bangumi?bangumiId=3706&amp;subgroupid=370" class="mikan-rss">RSS</a>
</div>
<div class="subgroup-text" id="page-anchor-2">
  Raw / Unknown
  <a href="/RSS/Bangumi?bangumiId=3706&amp;subgroupid=202" class="mikan-rss">RSS</a>
</div>
</body></html>`))
	}))
	defer server.Close()

	result, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client(), Now: func() time.Time { return time.Unix(10, 0) }}).ReadGroups(context.Background(), "3706")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || result.Groups[0].Ref.ExternalID != "370" || result.Groups[0].DisplayName != "SampleGroup" {
		t.Fatalf("unexpected Mikan groups: %+v", result.Groups)
	}
	if result.Groups[1].Ref.ExternalID != "202" || result.Groups[1].DisplayName != "Raw / Unknown" {
		t.Fatalf("Mikan text fallback failed: %+v", result.Groups[1])
	}
	for _, group := range result.Groups {
		if group.Ref.Provider != providerdomain.ProviderMikan || group.FeedLocator.Provider != providerdomain.ProviderMikan {
			t.Fatalf("Mikan namespace was lost: %+v", group)
		}
	}
}

func TestGroupReadersDistinguishValidEmptyFromMalformedResponse(t *testing.T) {
	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div class="bangumi-title">No groups yet</div></body></html>`))
	}))
	defer emptyServer.Close()
	empty, err := (MikanGroupReader{Endpoint: emptyServer.URL, Client: emptyServer.Client()}).ReadGroups(context.Background(), "1")
	if err != nil || empty.Groups == nil || len(empty.Groups) != 0 {
		t.Fatalf("valid empty Mikan response rejected: %+v err=%v", empty, err)
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div class="subgroup-text"><a href="/RSS/Bangumi?bangumiId=1&amp;subgroupid=bad">RSS</a></div>`))
	}))
	defer badServer.Close()
	if _, err := (MikanGroupReader{Endpoint: badServer.URL, Client: badServer.Client()}).ReadGroups(context.Background(), "1"); err == nil {
		t.Fatal("malformed Mikan subgroup response accepted as empty")
	}
}

func TestMikanGroupReaderEmptyResultWithTitleAnchorIsNotFlagged(t *testing.T) {
	recorder := &mikanParserWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div class="bangumi-title">No groups yet</div></body></html>`))
	}))
	defer server.Close()

	result, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadGroups(context.Background(), "1")
	if err != nil || len(result.Groups) != 0 {
		t.Fatalf("legitimate empty Mikan result rejected: %+v err=%v", result, err)
	}
	if len(recorder.failures) != 0 || len(recorder.routines) != 0 {
		t.Fatalf("legitimate empty result flagged as parser warning: failures=%+v routines=%+v", recorder.failures, recorder.routines)
	}
}

func TestMikanGroupReaderEmptyResultWithoutTitleAnchorIsFlagged(t *testing.T) {
	recorder := &mikanParserWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div class="unrelated">no title, no groups</div></body></html>`))
	}))
	defer server.Close()

	result, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadGroups(context.Background(), "1")
	if err != nil || len(result.Groups) != 0 {
		t.Fatalf("structure-broken empty Mikan result rejected: %+v err=%v", result, err)
	}
	if len(recorder.failures) != 1 || !isMikanParserFault(recorder.failures[0]) {
		t.Fatalf("missing-anchor empty result should flag provider_parser_empty: %+v", recorder.failures)
	}
	if len(recorder.routines) != 1 || recorder.routines[0].Identity.Outcome != observabilityport.RoutineOutcomeWarning || recorder.routines[0].Identity.ResultCode != observabilityport.RoutineResultProviderParserWarning {
		t.Fatalf("missing-anchor empty result routine = %+v", recorder.routines)
	}
}

func TestMikanGroupReaderWithGroupsDoesNotFlagEmptyWarning(t *testing.T) {
	recorder := &mikanParserWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div class="bangumi-title">Has groups</div>
<div class="subgroup-text">
  <a href="/Home/PublishGroup/223">SampleGroup</a>
  <a href="/RSS/Bangumi?bangumiId=1&amp;subgroupid=370">RSS</a>
</div></body></html>`))
	}))
	defer server.Close()

	result, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadGroups(context.Background(), "1")
	if err != nil || len(result.Groups) != 1 {
		t.Fatalf("non-empty Mikan result failed: %+v err=%v", result, err)
	}
	for _, failure := range recorder.failures {
		if isMikanParserFault(failure) {
			t.Fatalf("non-empty result flagged provider_parser_empty: %+v", recorder.failures)
		}
	}
	// A successful read must restore the routine to success so a previous
	// parser warning does not stay visible forever.
	recovered := false
	for _, routine := range recorder.routines {
		if routine.Identity.Outcome != observabilityport.RoutineOutcomeSuccess || routine.Identity.ResultCode != observabilityport.RoutineResultProviderRefreshCompleted {
			t.Fatalf("non-empty result routine must recover to success: %+v", recorder.routines)
		}
		recovered = true
	}
	if !recovered || len(recorder.routines) == 0 {
		t.Fatalf("non-empty result routine must be recorded as success: %+v", recorder.routines)
	}
}

func TestMikanGroupReaderRecordsWarningForSchemaVersionChange(t *testing.T) {
	recorder := &mikanParserWarningRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Mikan-Schema-Version", "mikan-detail-groups-v2")
		_, _ = w.Write([]byte(`<div class="subgroup-text">
<a href="/Home/PublishGroup/223">SampleGroup</a>
<a href="/RSS/Bangumi?bangumiId=1&amp;subgroupid=370">RSS</a>
</div>`))
	}))
	defer server.Close()

	result, err := (MikanGroupReader{Endpoint: server.URL, Client: server.Client(), Recorder: recorder}).ReadGroups(context.Background(), "1")
	if err != nil || len(result.Groups) != 1 {
		t.Fatalf("schema-version Mikan result failed: %+v err=%v", result, err)
	}
	if len(recorder.failures) != 1 || !isMikanParserFault(recorder.failures[0]) {
		t.Fatalf("schema parser warning = %+v", recorder.failures)
	}
	if string(recorder.routines[0].Details) == "" || !containsWarningDetail(string(recorder.routines[0].Details), "mikan-detail-groups-v2") {
		t.Fatalf("schema warning details = %s", recorder.routines[0].Details)
	}
}

func containsWarningDetail(details, value string) bool {
	return len(details) > 0 && strings.Contains(details, value)
}

func TestAniBTGroupReaderAcceptsValidEmptyResponse(t *testing.T) {
	observedAt := time.Unix(20, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `W/"empty-v1"`)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"bgmId":1,"total":0,"groups":[]}}`))
	}))
	defer server.Close()
	result, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client(), Now: func() time.Time { return observedAt }}).ReadGroups(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != providerdomain.ProviderAniBT || result.CatalogEntryRef != "1" || result.Groups == nil || len(result.Groups) != 0 {
		t.Fatalf("unexpected valid empty AniBT result: %+v", result)
	}
	if result.SchemaVersion != aniBTGroupSchemaVersion || result.SourceVersion != `W/"empty-v1"` || !result.ObservedAt.Equal(observedAt) {
		t.Fatalf("valid empty AniBT provenance mismatch: %+v", result)
	}
}

func TestAniBTGroupReaderRejectsSchemaMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"bgmId":1,"total":2,"groups":[]}}`))
	}))
	defer server.Close()
	if _, err := (AniBTGroupReader{Endpoint: server.URL, Client: server.Client()}).ReadGroups(context.Background(), "1"); err == nil {
		t.Fatal("AniBT total mismatch accepted as valid empty snapshot")
	}
}

func isMikanParserFault(request observabilityport.DiagnosticFailureRequest) bool {
	return request.Condition == (observabilityport.Condition{Kind: observabilityport.KindSite, Variant: observabilityport.VariantUnreadable}) &&
		request.TargetID == observabilityport.SiteMikanParser
}
