package application

import (
	"fmt"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
)

// A fetched torrent whose file list its release row cannot store, or which
// lists more files than the file filter reads, is kept as a torrent_too_large
// release without its file list and payload; an ordinary batch is untouched.
func TestRefuseOversizedTorrentMarksTheReleaseTooLarge(t *testing.T) {
	item := func(files int, dir string) acquisitiondomain.FeedItem {
		value := acquisitiondomain.FeedItem{Title: "Detective Conan", TorrentPayload: []byte("d4:infodee")}
		size := int64(1)
		for index := 0; index < files; index++ {
			path := fmt.Sprintf("%sDetective Conan Frame %05d.png", dir, index)
			value.ReleaseFiles = append(value.ReleaseFiles, acquisitiondomain.ReleaseFile{SourcePath: path, Filename: path, SizeBytes: &size, MediaKind: "other", SourceHash: strings.Repeat("a", 40)})
		}
		return value
	}
	batch := refuseOversizedTorrent(item(619, "Detective Conan/"))
	if batch.TorrentErrorCode != "" || len(batch.ReleaseFiles) != 619 || len(batch.TorrentPayload) == 0 {
		t.Fatalf("a 619-file batch was refused: %q with %d files", batch.TorrentErrorCode, len(batch.ReleaseFiles))
	}
	for name, oversized := range map[string]acquisitiondomain.FeedItem{
		"over the file budget":  item(animedomain.MaxFilterInventoryEntries+1, ""),
		"over the stored bound": item(6000, strings.Repeat("Detective Conan Season 01 [Ma10p_1080p]/", 3)),
	} {
		refused := refuseOversizedTorrent(oversized)
		if refused.TorrentErrorCode != "torrent_too_large" || refused.TorrentErrorReason == "" || refused.ReleaseFiles != nil || refused.TorrentPayload != nil {
			t.Fatalf("%s: code %q, %d files, %d payload bytes", name, refused.TorrentErrorCode, len(refused.ReleaseFiles), len(refused.TorrentPayload))
		}
		raw, err := encodeInternalFeedFacts(refused)
		if err != nil || len(raw) > releaseInternalFactsRawLimit {
			t.Fatalf("%s: refused facts are %d bytes (%v)", name, len(raw), err)
		}
		var evaluation animedomain.PreviewReleaseResult
		markTorrentUnavailable(&evaluation, refused)
		if evaluation.Verdict != "parse_failed" || evaluation.ReasonCode != "torrent_too_large" {
			t.Fatalf("%s: evaluation = %s/%s", name, evaluation.Verdict, evaluation.ReasonCode)
		}
	}
	unavailable := item(animedomain.MaxFilterInventoryEntries+1, "")
	unavailable.TorrentErrorCode = "torrent_metadata_unavailable"
	if kept := refuseOversizedTorrent(unavailable); kept.TorrentErrorCode != "torrent_metadata_unavailable" {
		t.Fatalf("an earlier torrent error was replaced by %q", kept.TorrentErrorCode)
	}
}
