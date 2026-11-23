package httpapi

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(h http.Handler, accept string, method string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/x", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

func TestCompressJSONLargeBodyRoundTrips(t *testing.T) {
	body := `{"v":"` + strings.Repeat("abcdef", 2000) + `"}`
	rec := serve(compressJSON(jsonHandler(body)), "br, gzip;q=0.8", http.MethodGet, nil)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip, got %q", rec.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("missing Vary")
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != body {
		t.Fatalf("decoded body differs")
	}
	if rec.Body.Len() >= len(body)/2 {
		t.Fatalf("not compressed: %d of %d", rec.Body.Len(), len(body))
	}
}

func TestCompressJSONPassThroughCases(t *testing.T) {
	big := `{"v":"` + strings.Repeat("abcdef", 2000) + `"}`
	cases := []struct {
		name, accept, method string
		hdr                  map[string]string
		h                    http.Handler
	}{
		{"no accept", "", http.MethodGet, nil, jsonHandler(big)},
		{"identity", "identity", http.MethodGet, nil, jsonHandler(big)},
		{"gzip q0", "gzip;q=0", http.MethodGet, nil, jsonHandler(big)},
		{"range", "gzip", http.MethodGet, map[string]string{"Range": "bytes=0-9"}, jsonHandler(big)},
		{"head", "gzip", http.MethodHead, nil, jsonHandler(big)},
		{"small", "gzip", http.MethodGet, nil, jsonHandler(`{"ok":true}`)},
		{"sse", "gzip", http.MethodGet, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Repeat("data: x\n\n", 500))
		})},
		{"already encoded", "gzip", http.MethodGet, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, big)
		})},
	}
	for _, c := range cases {
		rec := serve(compressJSON(c.h), c.accept, c.method, c.hdr)
		if ce := rec.Header().Get("Content-Encoding"); ce == "gzip" {
			t.Errorf("%s: unexpectedly gzipped", c.name)
		}
	}
}

func TestCompressJSONSmallBodyKeepsLengthAndStatus(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	rec := serve(compressJSON(h), "gzip", http.MethodGet, nil)
	if rec.Code != http.StatusCreated || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("status/body changed: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "11" {
		t.Fatalf("content-length = %q", rec.Header().Get("Content-Length"))
	}
}

func TestCompressJSONNoContent(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec := serve(compressJSON(h), "gzip", http.MethodGet, nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("204 altered: %d", rec.Code)
	}
}

func TestCompressJSONNotModifiedKeepsETag(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"snap-1"`)
		w.WriteHeader(http.StatusNotModified)
	})
	rec := serve(compressJSON(h), "gzip", http.MethodGet, nil)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("304 altered: %d", rec.Code)
	}
	if rec.Header().Get("ETag") != `"snap-1"` {
		t.Fatalf("etag changed: %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("304 compressed")
	}
}

func TestCompressJSONCompressedBodyKeepsETag(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"snap-2"`)
		_, _ = io.WriteString(w, `{"v":"`+strings.Repeat("abcdef", 2000)+`"}`)
	})
	rec := serve(compressJSON(h), "gzip", http.MethodGet, nil)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("expected gzip")
	}
	if rec.Header().Get("ETag") != `"snap-2"` {
		t.Fatalf("etag changed: %q", rec.Header().Get("ETag"))
	}
}

func TestCompressJSONBinaryDownloadNotCompressed(t *testing.T) {
	body := bytes.Repeat([]byte{0x1f, 0x8b, 0x42, 0x00}, 4096)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	rec := serve(compressJSON(h), "gzip", http.MethodGet, nil)
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("binary download compressed")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatal("binary body altered")
	}
}

// streamRecorder records exactly what reaches the underlying writer, and when.
type streamRecorder struct {
	header   http.Header
	body     bytes.Buffer
	flushes  int
	upstream bool
}

func (r *streamRecorder) Header() http.Header         { return r.header }
func (r *streamRecorder) WriteHeader(status int)      { r.upstream = true }
func (r *streamRecorder) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *streamRecorder) Flush()                      { r.flushes++ }

func TestCompressJSONSSEFirstEventFlushesImmediately(t *testing.T) {
	rec := &streamRecorder{header: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("compress wrapper must keep http.Flusher")
			return
		}
		_, _ = w.Write([]byte(": connected\n\n"))
		flusher.Flush()
		// The initial comment must already be on the wire, not held back
		// waiting for the compression decision.
		if rec.body.String() != ": connected\n\n" {
			t.Errorf("first event buffered: %q", rec.body.String())
		}
		if rec.flushes == 0 {
			t.Error("flush not forwarded to the underlying writer")
		}
		_, _ = w.Write([]byte("data: x\n\n"))
		flusher.Flush()
	}))
	h.ServeHTTP(rec, req)
	if got := rec.body.String(); got != ": connected\n\ndata: x\n\n" {
		t.Fatalf("stream body = %q", got)
	}
	if rec.header.Get("Content-Encoding") == "gzip" {
		t.Fatal("SSE compressed")
	}
}

// workspaceSample builds a deterministic ~280 KB JSON document shaped like a
// full anime workspace: many similar but non-identical release and log rows.
func workspaceSample() []byte {
	var b strings.Builder
	b.WriteString(`{"releases":[`)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"rel-%04d","title":"[Subgroup %02d] Some Anime Title With A Long Name - %02d [1080p][HEVC][AAC][%08x]","status":"downloaded","size":%d}`, i, i%17, i%24, i*2654435761, 400000000+i)
	}
	b.WriteString(`],"logs":[`)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"at":"2026-11-23T10:%02d:%02dZ","level":"info","message":"release rel-%04d matched rule and entered the download queue","seq":%d}`, i/60, i%60, i, i)
	}
	b.WriteString(`],"file_operations":[],"download_tasks":[]}`)
	return []byte(b.String())
}

func TestCompressJSONWorkspaceSampleRatio(t *testing.T) {
	body := workspaceSample()
	if len(body) < 200_000 {
		t.Fatalf("sample too small: %d", len(body))
	}
	rec := serve(compressJSON(jsonHandler(string(body))), "gzip", http.MethodGet, nil)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("expected gzip")
	}
	ratio := float64(rec.Body.Len()) / float64(len(body))
	t.Logf("workspace sample: raw=%d gzip=%d reduction=%.1f%%", len(body), rec.Body.Len(), (1-ratio)*100)
	if ratio > 0.30 {
		t.Fatalf("reduction below 70%%: raw=%d gzip=%d", len(body), rec.Body.Len())
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, body) {
		t.Fatal("round trip differs")
	}
}

func BenchmarkCompressJSONWorkspace(b *testing.B) {
	body := string(workspaceSample())
	h := compressJSON(jsonHandler(body))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
