package application

import (
	"context"
	"testing"

	"bansoko/internal/acquisition/torrent"
	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/testutil/animetest"
)

// This file holds the one-shot bucket's own torrent fixtures. They are copies
// of the ownership test doubles (fakeTorrentFetcher, parseTestTorrent) because
// a oneshot test may not use another bucket's test support; only the raw
// torrent bytes are shared, through animetest.

// oneShotFakeTorrentFetcher is a test double for TorrentPayloadFetcher. It
// returns a configured result or error for every call so PG integration tests
// never make real HTTP requests.
type oneShotFakeTorrentFetcher struct {
	payload animeapplication.TorrentPayload
	err     error
	calls   []string
}

func (f *oneShotFakeTorrentFetcher) FetchPayload(_ context.Context, torrentURL string) (animeapplication.TorrentPayload, error) {
	f.calls = append(f.calls, torrentURL)
	return f.payload, f.err
}

// parseOneShotTestTorrent parses the shared minimal test payload once so the
// fake fetcher returns a fully-populated torrent payload (bytes + metadata).
func parseOneShotTestTorrent(t *testing.T) animeapplication.TorrentPayload {
	t.Helper()
	data := animetest.ValidTestTorrentBytes()
	meta, err := torrent.Parse(data)
	if err != nil {
		t.Fatalf("parse test torrent: %v", err)
	}
	return animeapplication.TorrentPayload{Bytes: data, Metadata: meta}
}
