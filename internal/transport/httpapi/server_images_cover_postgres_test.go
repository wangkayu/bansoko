package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	identitymetadataadapter "bansoko/internal/identitymetadata/adapter"
	identitymetadatapersistence "bansoko/internal/identitymetadata/adapter/persistence"
	"bansoko/internal/transport/httpapi/generated"
)

var coverPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x7}, 48)...)

// The cover route end to end over a real database: authentication, a registry
// that only knows keys the server itself registered, an upstream fetched once
// and then served from the durable cache, and no way to name another URL.
func TestImageCoverRouteOverDatabase(t *testing.T) {
	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(coverPNG)
	}))
	defer upstream.Close()

	var proxy *identitymetadataadapter.ConfiguredCoverProxy
	rig := newTestRig(t, "development", false, func(deps *Dependencies) {
		proxy = identitymetadataadapter.NewCoverProxy(
			stubConnectionLister{connections: []configurationdomain.Connection{{
				ID: "mikan-fixture", Kind: configurationdomain.ConnectionMikan, Endpoint: upstream.URL,
				State: configurationdomain.ConnectionConfigured,
			}}},
			nil, identitymetadatapersistence.NewProviderImageRepository(deps.DB))
		deps.ImageCovers = proxy
	})

	recorder, ownerBody := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner",
		map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner creation failed: %d %s", recorder.Code, string(ownerBody))
	}
	ownerResult := decodeBody[generated.LoginResult](t, ownerBody)
	if ownerResult.SessionToken == nil || *ownerResult.SessionToken == "" {
		t.Fatal("bootstrap owner returned no session token")
	}
	auth := map[string]string{"Authorization": "Bearer " + *ownerResult.SessionToken}

	coverURL := upstream.URL + "/images/Bangumi/1.jpg?width=400"
	paths := proxy.CoverPaths(context.Background(), identitymetadataadapter.CoverProviderMikan, []string{
		coverURL, "http://169.254.169.254/latest/meta-data", "https://evil.example/x.png",
	})
	path := paths[coverURL]
	if path == "" || len(paths) != 1 {
		t.Fatalf("registered paths = %v, want only the provider cover", paths)
	}

	if recorder, _ := rig.do(t, http.MethodGet, path, nil, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous cover read = %d, want 401", recorder.Code)
	}
	// An unregistered key is a 404 and never reaches the upstream.
	unknown := "/api/v1/images/cover/" + strings.Repeat("0", 64)
	if recorder, _ := rig.do(t, http.MethodGet, unknown, nil, auth); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown key = %d, want 404", recorder.Code)
	}
	if atomic.LoadInt32(&upstreamHits) != 0 {
		t.Fatal("an unregistered key reached the upstream")
	}
	// The route has no URL input: a url query parameter changes nothing.
	for i := 0; i < 3; i++ {
		recorder, body := rig.do(t, http.MethodGet, path+"?url=http://169.254.169.254/", nil, auth)
		if recorder.Code != http.StatusOK || !bytes.Equal(body, coverPNG) || recorder.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("cover read %d = %d %q", i, recorder.Code, recorder.Header().Get("Content-Type"))
		}
	}
	if got := atomic.LoadInt32(&upstreamHits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (the rest served from the durable cache)", got)
	}
	var cached int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM provider_image_cache WHERE image_bytes IS NOT NULL AND provider = 'mikan'`).Scan(&cached); err != nil || cached != 1 {
		t.Fatalf("cached rows = %d, %v", cached, err)
	}

	// A fresh proxy over the same database (a server restart) keeps serving
	// the cached image without the upstream.
	restarted := identitymetadataadapter.NewCoverProxy(nil, nil, identitymetadatapersistence.NewProviderImageRepository(rig.db))
	body, contentType, err := restarted.FetchCover(context.Background(), strings.TrimPrefix(path, identitymetadataadapter.CoverPathPrefix))
	if err != nil || contentType != "image/png" {
		t.Fatalf("restarted fetch = %q, %v", contentType, err)
	}
	body.Close()
	if got := atomic.LoadInt32(&upstreamHits); got != 1 {
		t.Fatalf("restarted fetch hit the upstream (%d)", got)
	}
}
