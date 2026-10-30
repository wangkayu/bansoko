package application

import (
	"reflect"
	"testing"

	"bansoko/internal/download/qbittorrent"
)

func TestManagementCleanupCandidatesComeOnlyFromMatchingRecords(t *testing.T) {
	index := newTorrentRecordIndex()
	index.records = []torrentRecord{
		{infoHash: "ABCDEF", externalID: "tag:bansoko-known", stableTag: "bansoko-known"},
		{infoHash: "999999", externalID: "tag:bansoko-other", stableTag: "bansoko-other"},
		{externalID: "tag:bansoko-tag-only", stableTag: "bansoko-tag-only"},
		{infoHash: "abcdef", externalID: "hash:abcdef", stableTag: "user-tag"},
	}

	target := qbittorrent.TorrentInfo{Hash: "abcdef", Tags: "bansoko-unrelated, user-tag"}
	if got, want := index.systemTags(target), []string{"bansoko-known"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hash-matched candidates = %v, want %v", got, want)
	}

	tagged := qbittorrent.TorrentInfo{Hash: "unrecorded", Tags: "bansoko-tag-only, bansoko-unrelated"}
	if got, want := index.systemTags(tagged), []string{"bansoko-tag-only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tag-matched candidates = %v, want %v", got, want)
	}

	unmatched := qbittorrent.TorrentInfo{Hash: "unrecorded", Tags: "bansoko-unrelated"}
	if got := index.systemTags(unmatched); len(got) != 0 {
		t.Fatalf("an arbitrary torrent tag became a cleanup candidate: %v", got)
	}
}
