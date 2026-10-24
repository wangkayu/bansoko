package application

import (
	"testing"

	"bansoko/internal/download/qbittorrent"
)

// TestQBTorrentOriginIsDecidedByRecordsAndFailsClosed pins how the downloader
// manager decides who created a qB task: the correlation tag decides before the
// infohash, disagreeing records and a bare bansoko tag are unknown (never
// guessed into a segment), and only a task with neither is external.
func TestQBTorrentOriginIsDecidedByRecordsAndFailsClosed(t *testing.T) {
	index := newTorrentRecordIndex()
	index.origins.add("", "tag:bansoko-sub", "bansoko-sub", "subscription")
	index.origins.add("aaaa", "hash:aaaa", "", "subscription")
	index.origins.add("bbbb", "tag:bansoko-one", "bansoko-one", "one_shot")
	// A one-shot adopted the subscription's task by infohash.
	index.origins.add("cccc", "tag:bansoko-sub2", "bansoko-sub2", "subscription")
	index.origins.add("cccc", "hash:cccc", "", "one_shot")
	// Two records claim the same tag with different kinds.
	index.origins.add("", "tag:bansoko-both", "", "subscription")
	index.origins.add("", "tag:bansoko-both", "", "one_shot")
	// Two records share an infohash with no tag on the task.
	index.origins.add("dddd", "", "", "subscription")
	index.origins.add("dddd", "", "", "one_shot")

	cases := []struct {
		name string
		item qbittorrent.TorrentInfo
		want string
	}{
		{"tag of a subscription", qbittorrent.TorrentInfo{Hash: "ffff", Tags: "anime, bansoko-sub"}, "subscription"},
		{"infohash only", qbittorrent.TorrentInfo{Hash: "AAAA"}, "subscription"},
		{"tag of a one-shot", qbittorrent.TorrentInfo{Hash: "bbbb", Tags: "bansoko-one"}, "one_shot"},
		{"tag decides before an adopting infohash", qbittorrent.TorrentInfo{Hash: "cccc", Tags: "bansoko-sub2"}, "subscription"},
		{"records disagree on the tag", qbittorrent.TorrentInfo{Hash: "eeee", Tags: "bansoko-both"}, "unknown"},
		{"records disagree on the infohash", qbittorrent.TorrentInfo{Hash: "dddd"}, "unknown"},
		{"bansoko tag without a record", qbittorrent.TorrentInfo{Hash: "9999", Tags: "bansoko-gone"}, "unknown"},
		{"no record, no bansoko tag", qbittorrent.TorrentInfo{Hash: "8888", Tags: "manual"}, "external"},
		{"no tags at all", qbittorrent.TorrentInfo{Hash: "7777"}, "external"},
	}
	for _, tc := range cases {
		if got := index.origin(tc.item); got != tc.want {
			t.Errorf("%s: origin = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAcquisitionOwnerKindCoversEveryEntryKind(t *testing.T) {
	for entry, want := range map[string]string{"automatic": "subscription", "subscription_add": "subscription", "one_shot": "one_shot"} {
		if got, ok := acquisitionOwnerKind(entry); !ok || got != want {
			t.Errorf("%s: %q %v", entry, got, ok)
		}
	}
	if _, ok := acquisitionOwnerKind("agent"); ok {
		t.Error("an unmapped entry kind must not get an owner kind")
	}
}
