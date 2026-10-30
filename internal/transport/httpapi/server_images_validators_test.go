package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

// Both image routes are fetched through the session as a blob on every mount,
// so the browser's HTTP cache is the only thing between a reload and the
// network. These tests pin what makes that cache useful: a strong validator
// over the exact bytes, a 304 that carries no body, and a cache directive that
// matches what the URL can promise (a content-addressed cover may be called
// immutable; a subject-id poster may not).

type fakePosterService struct {
	body        []byte
	contentType string
	err         error
	calls       []string
}

func (f *fakePosterService) FetchPoster(_ context.Context, bgmID string) (io.ReadCloser, string, error) {
	f.calls = append(f.calls, bgmID)
	if f.err != nil {
		return nil, "", f.err
	}
	return io.NopCloser(strings.NewReader(string(f.body))), f.contentType, nil
}

func posterServer(posters ImagePosterService) *Server {
	return &Server{deps: Dependencies{SkipAuth: true, RuntimeLogger: observabilityport.NopRuntimeLogger{}, ImagePosters: posters}}
}

func getImage(t *testing.T, server *Server, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestReadImagePosterServesAValidatorAndAnswersRevalidation(t *testing.T) {
	posters := &fakePosterService{body: []byte("poster-bytes"), contentType: "image/jpeg"}
	server := posterServer(posters)

	first := getImage(t, server, "/api/v1/images/poster/12", "")
	if first.Code != http.StatusOK || first.Body.String() != "poster-bytes" {
		t.Fatalf("first read = %d %q", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("ETag = %q, want a strong quoted validator", etag)
	}
	// public would let a shared cache keep a session-authenticated response, and
	// immutable would promise a subject-id path can never change.
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Fatalf("poster Cache-Control = %q", got)
	}
	if got := first.Header().Get("Content-Length"); got != "12" {
		t.Fatalf("poster Content-Length = %q, want the buffered length", got)
	}

	for _, header := range []string{etag, "W/" + etag, `"stale", ` + etag, "*"} {
		recorder := getImage(t, server, "/api/v1/images/poster/12", header)
		if recorder.Code != http.StatusNotModified || recorder.Body.Len() != 0 {
			t.Fatalf("If-None-Match %q answered %d %q, want 304 with no body", header, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("ETag") != etag || recorder.Header().Get("Cache-Control") != "private, max-age=86400" {
			t.Fatalf("304 for %q dropped the cache metadata: %v", header, recorder.Header())
		}
	}

	// A replaced poster is different bytes behind the same path: the validator
	// changes, so the client must download again instead of trusting its copy.
	posters.body = []byte("replaced-poster")
	replaced := getImage(t, server, "/api/v1/images/poster/12", etag)
	if replaced.Code != http.StatusOK || replaced.Body.String() != "replaced-poster" {
		t.Fatalf("replaced poster = %d %q", replaced.Code, replaced.Body.String())
	}
	if replaced.Header().Get("ETag") == etag {
		t.Fatal("a replaced poster kept the previous validator")
	}

	// A body outside the proxy's own bound is refused rather than served.
	posters.body = []byte(strings.Repeat("x", maxProxiedImageBytes+1))
	if oversized := getImage(t, server, "/api/v1/images/poster/12", ""); oversized.Code != http.StatusInternalServerError {
		t.Fatalf("oversized poster status = %d", oversized.Code)
	}
}

func TestReadImageCoverServesAValidatorAndAnswersRevalidation(t *testing.T) {
	server := coverServer(&fakeCoverService{})

	first := getImage(t, server, "/api/v1/images/cover/"+testCoverKey, "")
	if first.Code != http.StatusOK || first.Body.String() != "png-bytes" {
		t.Fatalf("first cover read = %d %q", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("cover response carries no validator")
	}
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=86400, immutable" {
		t.Fatalf("cover Cache-Control = %q", got)
	}
	again := getImage(t, server, "/api/v1/images/cover/"+testCoverKey, etag)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("cover revalidation = %d %q", again.Code, again.Body.String())
	}
}

// An image that never arrives, or arrives empty, keeps each route's existing
// failure answer: neither may reply 200 with nothing behind it.
func TestReadImageRoutesRefuseAnUnusableImage(t *testing.T) {
	failing := errors.New("upstream truncated")
	if recorder := getImage(t, posterServer(&fakePosterService{err: failing}), "/api/v1/images/poster/12", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("failed poster fetch status = %d", recorder.Code)
	}
	empty := posterServer(&fakePosterService{contentType: "image/jpeg"})
	if recorder := getImage(t, empty, "/api/v1/images/poster/12", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("empty poster status = %d", recorder.Code)
	}
}
