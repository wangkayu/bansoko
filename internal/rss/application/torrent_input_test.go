package application

import (
	"strings"
	"testing"
	"unicode/utf8"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
)

func TestValidateTorrentFactsAcceptsParserFacts(t *testing.T) {
	payload := []byte("d4:infod4:name15:show-01.torrent6:lengthi1eee")
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	item := acquisitiondomain.FeedItem{
		TorrentFilename:   metadata.TorrentName,
		TorrentInfoHash:   metadata.InfoHashV1,
		TorrentInfoHashV1: metadata.InfoHashV1,
		TorrentVersion:    metadata.TorrentVersion,
		TorrentSizeBytes:  metadata.TotalSize,
	}
	if err := validateTorrentFacts(metadata, item, "show-01.torrent", metadata.TorrentVersion); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTorrentFactsRejectsHashMismatch(t *testing.T) {
	payload := []byte("d4:infod4:name15:show-01.torrent6:lengthi1eee")
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	item := acquisitiondomain.FeedItem{
		TorrentInfoHashV1: "wrong",
		TorrentVersion:    metadata.TorrentVersion,
	}
	if err := validateTorrentFacts(metadata, item, "show-01.torrent", metadata.TorrentVersion); err == nil {
		t.Fatal("hash mismatch was accepted")
	}
}

func TestValidateTorrentFactsRejectsMalformedPayloadBeforePersistence(t *testing.T) {
	if _, err := torrent.Parse([]byte("not-a-torrent")); err == nil {
		t.Fatal("malformed payload was accepted by parser")
	}
}

// Real feeds declare approximate sizes (mikan contentLength is routinely off
// by a few KiB from the actual content). The fetched payload is authoritative,
// so a declared-size difference must not reject the release.
func TestValidateTorrentFactsAcceptsFeedDeclaredSizeDifference(t *testing.T) {
	payload := []byte("d4:infod4:name15:show-01.torrent6:lengthi1eee")
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	item := acquisitiondomain.FeedItem{
		TorrentFilename:   metadata.TorrentName,
		TorrentInfoHash:   metadata.InfoHashV1,
		TorrentInfoHashV1: metadata.InfoHashV1,
		TorrentVersion:    metadata.TorrentVersion,
		TorrentSizeBytes:  metadata.TotalSize + 41150, // feed-declared hint, off like mikan
	}
	if err := validateTorrentFacts(metadata, item, "show-01.torrent", metadata.TorrentVersion); err != nil {
		t.Fatalf("feed-declared size difference rejected the release: %v", err)
	}
}

// Real feeds embed path separators in display titles (e.g. anibt
// "[ANi] Ushiro no Shoumen Kamuisan / 從後面來的神威先生 - 05 ..."). The stored
// name is provenance/display metadata — qBittorrent revalidates the upload
// component at submission time — so the name is sanitized, never a release gate.
func TestTorrentInputFilenameSanitizesPathSeparators(t *testing.T) {
	name, err := torrentInputFilename("[ANi] Ushiro no Shoumen Kamuisan /  從後面來的神威先生 [年齡限制版] - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", "16961e8d6bf220d78d0700322a2f0875496c617f")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(name, "/\\") {
		t.Fatalf("name still contains a path separator: %q", name)
	}
	if !strings.HasSuffix(name, ".torrent") {
		t.Fatalf("name does not end in .torrent: %q", name)
	}
	if !strings.Contains(name, "Ushiro no Shoumen Kamuisan _") {
		t.Fatalf("separator was not replaced with an underscore: %q", name)
	}
}

func TestTorrentInputFilenameSanitizesControlCharacters(t *testing.T) {
	name, err := torrentInputFilename("show\x00\r\nname", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(name, "\x00\r\n") {
		t.Fatalf("name still contains control characters: %q", name)
	}
}

func TestTorrentInputFilenameFallsBackToHashAndKeepsSafeNames(t *testing.T) {
	name, err := torrentInputFilename("", "16961e8d6bf220d78d0700322a2f0875496c617f")
	if err != nil {
		t.Fatal(err)
	}
	if name != "16961e8d6bf220d78d0700322a2f0875496c617f.torrent" {
		t.Fatalf("expected hash fallback, got %q", name)
	}
	name, err = torrentInputFilename("show-01.torrent", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if name != "show-01.torrent" {
		t.Fatalf("safe name was changed: %q", name)
	}
}

func TestTorrentInputFilenameTruncatesLongNames(t *testing.T) {
	long := strings.Repeat("名", 300) // 900 UTF-8 bytes
	name, err := torrentInputFilename(long, "")
	if err != nil {
		t.Fatal(err)
	}
	// The component is capped at 503 bytes before the .torrent suffix is
	// appended, matching the historical component limit.
	if len(name) > 511 {
		t.Fatalf("name exceeds the component-plus-suffix limit: %d", len(name))
	}
	if !utf8.ValidString(name) {
		t.Fatal("truncated name splits a UTF-8 sequence")
	}
	if !strings.HasSuffix(name, ".torrent") {
		t.Fatalf("truncated name does not end in .torrent: %q", name)
	}
}

func TestTorrentInputFilenameTraversalOnlyFallsBackToRelease(t *testing.T) {
	name, err := torrentInputFilename("..", "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "release.torrent" {
		t.Fatalf("expected release fallback, got %q", name)
	}
}
