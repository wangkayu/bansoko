package httpapi

import (
	catalog "bansoko/internal/catalog/domain"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type customSnapshotTransportSpy struct {
	catalogCallSpy
	id                string
	revision, version int64
}

func (s *customSnapshotTransportSpy) FetchCustomSnapshot(_ context.Context, id string, revision, version int64) (catalog.ProviderRSSPreviewResult, error) {
	s.id = id
	s.revision = revision
	s.version = version
	return catalog.ProviderRSSPreviewResult{Provider: "custom", SnapshotID: "exact-snapshot", SnapshotVersion: 7, ContentHash: "sha256:" + strings.Repeat("a", 64)}, nil
}
func TestCustomSnapshotHTTPUsesOnlyExactCandidateRequest(t *testing.T) {
	app := &customSnapshotTransportSpy{}
	s := &Server{deps: Dependencies{Catalog: app}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/catalog/custom/feed-snapshots:fetch", strings.NewReader(`{"candidate_id":"candidate-a","revision":3,"expected_version":5}`))
	s.FetchCustomSnapshot(w, r)
	if w.Code != 200 || app.id != "candidate-a" || app.revision != 3 || app.version != 5 || !strings.Contains(w.Body.String(), `"snapshot_id":"exact-snapshot"`) {
		t.Fatalf("call=%+v status=%d body=%s", app, w.Code, w.Body.String())
	}
}
func TestCustomSnapshotHTTPRejectsFreeLocator(t *testing.T) {
	app := &customSnapshotTransportSpy{}
	s := &Server{deps: Dependencies{Catalog: app}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/catalog/custom/feed-snapshots:fetch", strings.NewReader(`{"candidate_id":"candidate-a","revision":3,"expected_version":5,"locator":"https://wrong.test/feed"}`))
	s.FetchCustomSnapshot(w, r)
	if w.Code != 400 || app.id != "" {
		t.Fatalf("status=%d body=%s call=%+v", w.Code, w.Body.String(), app)
	}
}
