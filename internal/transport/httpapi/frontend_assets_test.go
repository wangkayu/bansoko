package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFrontendAssetEncodingPreferences(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   []string
	}{
		{"", []string{}},
		{"br, gzip", []string{"br", "gzip"}},
		{"gzip;q=1, br;q=0.5", []string{"gzip", "br"}},
		{"br;q=0, gzip", []string{"gzip"}},
		{"*;q=1, gzip;q=0", []string{"br"}},
		{"br;q=NaN, gzip;q=invalid", []string{}},
	} {
		if got := preferredAssetEncodings(tt.header); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: got %v, want %v", tt.header, got, tt.want)
		}
	}
}

func TestFrontendPrecompressedAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("console.log('asset');\n"), 100)
	name := "assets/app-Abcd1234.js"
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{name: body, name + ".gz": compressed.Bytes(), "index.html": []byte("<!doctype html>"), "sw.js": []byte("// worker")} {
		if err := os.WriteFile(filepath.Join(dir, path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := frontendHandler(dir)
	request := func(method, path, encoding, byteRange string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Accept-Encoding", encoding)
		if byteRange != "" {
			r.Header.Set("Range", byteRange)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, encoding := range []string{"gzip", "br, gzip"} {
		w := request(http.MethodGet, "/"+name, encoding, "")
		if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("compressed: %d %v", w.Code, w.Header())
		}
		reader, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("decoded content mismatch: %v", err)
		}
		if !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatal(w.Header())
		}
		if w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatal(w.Header())
		}
	}
	w := request(http.MethodGet, "/"+name, "gzip;q=0", "")
	if w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatal("identity fallback changed")
	}
	w = request(http.MethodHead, "/"+name, "gzip", "")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("HEAD changed")
	}
	w = request(http.MethodGet, "/"+name, "gzip", "bytes=0-6")
	if w.Code != 206 || w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), body[:7]) {
		t.Fatal("range changed")
	}
	w = request(http.MethodGet, "/assets/missing-Abcd1234.js", "gzip", "")
	if w.Code != 404 || strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("cached missing asset")
	}
	w = request(http.MethodGet, "/", "gzip", "")
	if w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("Content-Security-Policy") != DocumentContentSecurityPolicy {
		t.Fatal("HTML behavior changed")
	}
	w = request(http.MethodGet, "/sw.js", "gzip", "")
	if strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("worker must revalidate")
	}
}
