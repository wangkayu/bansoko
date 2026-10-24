package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// The expand endpoint is the only route that may fetch a torrent during a list
// read, so its transport contract is explicit: the identity comes from the path
// plus the exact snapshot the body names, an unexpandable torrent is a state
// with a bounded reason (200), and a snapshot the server can no longer prove
// stays a typed failure — never a manifest from another snapshot.

type seedExpansionCallSpy struct {
	catalogCallSpy
	input catalogapplication.EnsureSeedTorrentInput
	calls int
	fact  catalogapplication.SeedTorrentFact
	err   error
}

func (s *seedExpansionCallSpy) EnsureSeedTorrent(_ context.Context, input catalogapplication.EnsureSeedTorrentInput) (catalogapplication.SeedTorrentFact, error) {
	s.calls++
	s.input = input
	return s.fact, s.err
}

func seedExpansionRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/provider-feed-snapshots/snap-1/seeds/seed-1/expand", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// A successful expansion answers 200 with the state, the manifest the row
// renders and whether the provider was touched at all.
func TestExpandSnapshotSeedReturnsTheManifestAndCachedFlag(t *testing.T) {
	files := []catalogdomain.TorrentFile{{Ordinal: 0, SourcePath: "Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: 1000, MediaKind: "video"}}
	spy := &seedExpansionCallSpy{fact: catalogapplication.SeedTorrentFact{
		State:    catalogapplication.SeedTorrentStateAvailable,
		Cached:   true,
		Manifest: &catalogdomain.TorrentManifest{PayloadDigest: "sha256:" + strings.Repeat("a", 64), ParserVersion: "parser-v1", TorrentVersion: "v1", TorrentName: "Show S1", TotalSizeBytes: 1000, Files: files},
	}}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()

	server.ExpandSnapshotSeed(recorder, seedExpansionRequest(t, `{"provider":"mikan","provider_entry_ref":"123","provider_group_ref":"456","snapshot_version":3,"content_hash":"sha256:deadbeef"}`), "snap-1", "seed-1")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if spy.calls != 1 || spy.input.SnapshotID != "snap-1" || spy.input.StableSeedKey != "seed-1" {
		t.Fatalf("calls = %d input = %+v, want the path identity", spy.calls, spy.input)
	}
	if spy.input.Provider != "mikan" || spy.input.ProviderEntryRef != "123" || spy.input.ProviderGroupRef != "456" ||
		spy.input.SnapshotVersion != 3 || spy.input.ContentHash != "sha256:deadbeef" {
		t.Fatalf("input = %+v, want the body's exact snapshot identity", spy.input)
	}
	var result generated.SnapshotSeedExpandResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.State != generated.SnapshotSeedExpandResultStateAvailable || !result.Cached {
		t.Fatalf("result = %+v, want an available cached expansion", result)
	}
	if result.Torrent.Manifest == nil || result.Torrent.Manifest.TorrentName != "Show S1" {
		t.Fatalf("torrent = %+v, want the manifest the row renders", result.Torrent)
	}
	if result.ErrorCode != nil || result.ErrorReason != nil {
		t.Fatalf("an available expansion has no reason to give: %+v", result)
	}
}

// An upstream that cannot be reached, parsed or found is an answer about an
// external resource, not a server fault: the state and the bounded code travel
// on the result and on the nested torrent, and no manifest is claimed.
func TestExpandSnapshotSeedReportsUnavailableTorrentsAsAState(t *testing.T) {
	reason := "the torrent payload could not be parsed"
	spy := &seedExpansionCallSpy{fact: catalogapplication.SeedTorrentFact{
		State: catalogapplication.SeedTorrentStateUnavailable, ErrorCode: catalogapplication.SeedTorrentErrorParseFailed, ErrorReason: reason,
	}}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()

	server.ExpandSnapshotSeed(recorder, seedExpansionRequest(t, `{"provider":"mikan","provider_entry_ref":"123","provider_group_ref":"456","snapshot_version":3,"content_hash":"sha256:deadbeef"}`), "snap-1", "seed-1")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", recorder.Code, recorder.Body.String())
	}
	var result generated.SnapshotSeedExpandResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.State != generated.SnapshotSeedExpandResultStateUnavailable || result.Torrent.State != generated.RSSListTorrentStateStateUnavailable {
		t.Fatalf("result = %+v, want unavailable in both places", result)
	}
	if result.ErrorCode == nil || *result.ErrorCode != catalogapplication.SeedTorrentErrorParseFailed || result.Torrent.ErrorCode == nil || *result.Torrent.ErrorCode != catalogapplication.SeedTorrentErrorParseFailed {
		t.Fatalf("error code = %+v / %+v, want %q", result.ErrorCode, result.Torrent.ErrorCode, catalogapplication.SeedTorrentErrorParseFailed)
	}
	if result.ErrorReason == nil || *result.ErrorReason != reason {
		t.Fatalf("error reason = %v, want the bounded reason", result.ErrorReason)
	}
	if result.Torrent.Manifest != nil {
		t.Fatalf("an unavailable expansion must not claim a manifest: %+v", result.Torrent.Manifest)
	}
}

// An identity the server cannot prove is a typed failure, not a state: the
// client refreshes instead of rendering structure from another snapshot.
func TestExpandSnapshotSeedKeepsADriftedIdentityATypedFailure(t *testing.T) {
	spy := &seedExpansionCallSpy{err: &apperrors.Business{Status: http.StatusConflict, Code: "FEED_SNAPSHOT_REFRESH_REQUIRED", Message: "the snapshot is no longer retained"}}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()

	server.ExpandSnapshotSeed(recorder, seedExpansionRequest(t, `{"provider":"mikan","provider_entry_ref":"123","provider_group_ref":"456","snapshot_version":3,"content_hash":"sha256:deadbeef"}`), "snap-1", "seed-1")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "FEED_SNAPSHOT_REFRESH_REQUIRED") {
		t.Fatalf("body = %s, want the typed code", recorder.Body.String())
	}
}

// The body is a closed shape: an unknown field is rejected before any expansion
// is attempted, so a client cannot smuggle a second identity past the path.
func TestExpandSnapshotSeedRejectsAnUnknownBodyField(t *testing.T) {
	spy := &seedExpansionCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()

	server.ExpandSnapshotSeed(recorder, seedExpansionRequest(t, `{"provider":"mikan","provider_entry_ref":"123","provider_group_ref":"456","snapshot_version":3,"content_hash":"sha256:deadbeef","snapshot_id":"snap-2"}`), "snap-1", "seed-1")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", recorder.Code, recorder.Body.String())
	}
	if spy.calls != 0 {
		t.Fatalf("expansion calls = %d, want none for an invalid body", spy.calls)
	}
}
