package application

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

// Synthetic keys only.
func TestTorrentSourceMatchesIgnoresCredentialRotation(t *testing.T) {
	cases := []struct {
		name, stored, current string
		want                  bool
	}{
		{"unit3d rotated rsskey", "https://tracker.example.test/torrents/download/4211.aaaaaaaaaaaaaaaa", "https://tracker.example.test/torrents/download/4211.bbbbbbbbbbbbbbbb", true},
		{"unit3d other id", "https://tracker.example.test/torrents/download/4211.aaaaaaaaaaaaaaaa", "https://tracker.example.test/torrents/download/4212.aaaaaaaaaaaaaaaa", false},
		{"nexusphp rotated passkey", "https://pt.example.test/download.php?id=77&passkey=aaaaaaaa", "https://pt.example.test/download.php?id=77&passkey=bbbbbbbb", true},
		{"nexusphp other id", "https://pt.example.test/download.php?id=77&passkey=aaaaaaaa", "https://pt.example.test/download.php?id=78&passkey=aaaaaaaa", false},
		{"other host", "https://pt.example.test/download.php?id=77&passkey=aaaaaaaa", "https://pt2.example.test/download.php?id=77&passkey=aaaaaaaa", false},
		{"plain url", "https://downloads.example.test/show-01.torrent", "https://downloads.example.test/show-01.torrent", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := torrentSourceMatches(tc.current, torrentSourceDigest(tc.stored)); got != tc.want {
				t.Fatalf("torrentSourceMatches = %v, want %v", got, tc.want)
			}
		})
	}
	stored := "https://pt.example.test/download.php?id=77&passkey=aaaaaaaa"
	if strings.Contains(torrentSourceDigest(stored), "aaaaaaaa") || torrentSourceDigest(stored) == stableDigest(stored) {
		t.Fatal("stored identity must not be the raw URL digest")
	}
	if !torrentSourceMatches(stored, stableDigest(stored)) {
		t.Fatal("a legacy raw-URL digest must still match the same URL")
	}
	if torrentSourceMatches("", torrentSourceDigest(stored)) || torrentSourceMatches(stored, "") {
		t.Fatal("an empty URL or digest never matches")
	}
}

// The new-releases-only skip returns before any database read.
func TestTorrentMetadataReuserSkipsStartBaselineItems(t *testing.T) {
	baseline := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	service := &Service{}
	reuse := service.torrentMetadataReuser(context.Background(), sourceSnapshot{
		newReleasesOnly: true,
		startBaselineAt: sql.NullTime{Time: baseline, Valid: true},
	})
	for _, published := range []time.Time{{}, baseline.Add(-time.Hour)} {
		item := acquisitiondomain.FeedItem{GUID: "guid-old", Enclosure: "https://pt.example.test/download.php?id=1&passkey=x", Published: published}
		got, skip := reuse(item)
		if !skip || len(got.ReleaseFiles) != 0 || got.TorrentErrorCode != "" {
			t.Fatalf("published=%v: skip=%v item=%+v", published, skip, got)
		}
	}
}
