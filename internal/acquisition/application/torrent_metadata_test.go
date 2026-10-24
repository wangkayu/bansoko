package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Lazy seed expansion reports a state instead of an error, so it must be able to
// tell the fetch outcomes apart without reading English message text. These
// tests pin one sentinel per outcome; a new bare error here would silently
// reclassify a user-visible reason.

// torrentReaderForTest pins the reader to one test server while the request URL
// stays a public address, so the SSRF guard is exercised rather than bypassed.
func torrentReaderForTest(t *testing.T, server *httptest.Server) HTTPTorrentMetadataReader {
	t.Helper()
	serverAddress := server.Listener.Addr().String()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	}
	return HTTPTorrentMetadataReader{Client: &http.Client{Transport: transport}}
}

func TestFetchPayloadClassifiesHTTPStatus(t *testing.T) {
	for name, testCase := range map[string]struct {
		status int
		want   error
	}{
		"missing": {status: http.StatusNotFound, want: ErrTorrentPayloadMissing},
		"refused": {status: http.StatusTooManyRequests, want: ErrTorrentPayloadFetch},
		"server":  {status: http.StatusInternalServerError, want: ErrTorrentPayloadFetch},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no", testCase.status)
			}))
			defer server.Close()

			_, err := torrentReaderForTest(t, server).FetchPayload(context.Background(), "http://203.0.113.10/release.torrent")
			if !errors.Is(err, testCase.want) {
				t.Fatalf("HTTP %d = %v, want %v", testCase.status, err, testCase.want)
			}
		})
	}
}

func TestFetchPayloadClassifiesUnusableBody(t *testing.T) {
	for name, body := range map[string]string{
		"not bencode": "this is not a torrent",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()

			_, err := torrentReaderForTest(t, server).FetchPayload(context.Background(), "http://203.0.113.10/release.torrent")
			if !errors.Is(err, ErrTorrentPayloadInvalid) {
				t.Fatalf("body %q = %v, want %v", body, err, ErrTorrentPayloadInvalid)
			}
		})
	}
}

func TestFetchPayloadRejectsOversizedBodyAsInvalid(t *testing.T) {
	// The limit is read from the reader, so a small limit proves the size branch
	// without shipping a 20 MiB fixture.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("d4:infod4:name8:show.mkv6:lengthi12345eee"))
	}))
	defer server.Close()

	reader := torrentReaderForTest(t, server)
	reader.MaxSize = 8
	_, err := reader.FetchPayload(context.Background(), "http://203.0.113.10/release.torrent")
	if !errors.Is(err, ErrTorrentPayloadInvalid) {
		t.Fatalf("oversized body = %v, want %v", err, ErrTorrentPayloadInvalid)
	}
}

func TestFetchPayloadRejectsNonAbsoluteLocatorAsFetchFailure(t *testing.T) {
	reader := HTTPTorrentMetadataReader{}
	_, err := reader.FetchPayload(context.Background(), "/relative/release.torrent")
	if !errors.Is(err, ErrTorrentPayloadFetch) {
		t.Fatalf("relative locator = %v, want %v", err, ErrTorrentPayloadFetch)
	}
}

func TestFetchPayloadReturnsParsedBytesOnSuccess(t *testing.T) {
	data := []byte("d4:infod4:name8:show.mkv6:lengthi12345eee")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	payload, err := torrentReaderForTest(t, server).FetchPayload(context.Background(), "http://203.0.113.10/release.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Bytes) != string(data) {
		t.Fatalf("payload bytes = %q, want the exact body", payload.Bytes)
	}
	if payload.Metadata.InfoHashV1 == "" || payload.Metadata.TorrentName != "show.mkv" {
		t.Fatalf("payload metadata = %+v", payload.Metadata)
	}
}
