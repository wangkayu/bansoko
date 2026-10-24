package torrent

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"bansoko/internal/filekinds"
)

func TestParseSingleFileTorrent(t *testing.T) {
	data := []byte("d4:infod4:name8:show.mkv6:lengthi12345eee")
	metadata, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TorrentName != "show.mkv" || metadata.TorrentVersion != "v1" || metadata.TotalSize != 12345 || len(metadata.Files) != 1 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.Files[0].Path != "show.mkv" || metadata.Files[0].MediaKind != "video" {
		t.Fatalf("unexpected file tree: %+v", metadata.Files)
	}
	hash := sha1.Sum([]byte("d4:name8:show.mkv6:lengthi12345ee"))
	if metadata.InfoHashV1 != hex.EncodeToString(hash[:]) {
		t.Fatalf("info hash v1 = %s, want %s", metadata.InfoHashV1, hex.EncodeToString(hash[:]))
	}
	if metadata.InfoHashV2 != "" {
		t.Fatalf("v1-only torrent should not have v2 hash, got %s", metadata.InfoHashV2)
	}
}

func TestParseMultiFileTorrentAndSubtitleLanguage(t *testing.T) {
	data := []byte("d4:infod5:filesld6:lengthi10e4:pathl7:ep1.mkveed6:lengthi20e4:pathl15:ep1.zh-Hans.ass" + "eee" + "4:name7:Show S1ee")
	metadata, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TotalSize != 30 || len(metadata.Files) != 2 {
		t.Fatalf("unexpected multi-file metadata: %+v", metadata)
	}
	if metadata.Files[0].Path != "Show S1/ep1.mkv" || metadata.Files[1].Path != "Show S1/ep1.zh-Hans.ass" {
		t.Fatalf("unexpected file paths: %+v", metadata.Files)
	}
	if metadata.Files[1].MediaKind != "subtitle" || metadata.Files[1].Language != "zh-Hans" {
		t.Fatalf("subtitle facts were not normalized: %+v", metadata.Files[1])
	}
}

func TestParseMultiFileTorrentProjectionIsExact(t *testing.T) {
	// A real release torrent shape: one video, one nested subtitle directory,
	// one extra non-media file. Every projection fact the read model will show
	// (path, name, size, media kind, subtitle language) must be exact.
	data := multiFileTorrentBencode("Show S1", []torrentFileFixture{
		{Path: []string{"ep1.mkv"}, Length: 1234},
		{Path: []string{"Subs", "ep1.zh-Hans.ass"}, Length: 20},
		{Path: []string{"cover.jpg"}, Length: 7},
	})
	metadata, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TorrentVersion != "v1" || metadata.TotalSize != 1261 {
		t.Fatalf("unexpected metadata totals: %+v", metadata)
	}
	want := []File{
		{Path: "Show S1/ep1.mkv", Name: "ep1.mkv", SizeBytes: 1234, MediaKind: "video"},
		{Path: "Show S1/Subs/ep1.zh-Hans.ass", Name: "ep1.zh-Hans.ass", SizeBytes: 20, MediaKind: "subtitle", Language: "zh-Hans"},
		{Path: "Show S1/cover.jpg", Name: "cover.jpg", SizeBytes: 7, MediaKind: "other"},
	}
	if len(metadata.Files) != len(want) {
		t.Fatalf("file count = %d, want %d (%+v)", len(metadata.Files), len(want), metadata.Files)
	}
	for index, expected := range want {
		if metadata.Files[index] != expected {
			t.Fatalf("file %d = %+v, want %+v", index, metadata.Files[index], expected)
		}
	}
}

// The manifest path of a first-level file is exactly torrent.name/<filename>
// for a multi-file torrent and torrent.name itself for a single-file torrent;
// every deeper file is nested. filekinds.FirstLevel reads that from the path.
func TestManifestPathsCarryTheFirstLevelDepth(t *testing.T) {
	multi, err := Parse(multiFileTorrentBencode("Show S1", []torrentFileFixture{
		{Path: []string{"ep1.mkv"}, Length: 1},
		{Path: []string{"SPs", "ncop.mkv"}, Length: 1},
		{Path: []string{"a", "b", "c.mkv"}, Length: 1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []bool{true, false, false} {
		if got := filekinds.FirstLevel(multi.Files[index].Path); got != want {
			t.Fatalf("%s first level = %v, want %v", multi.Files[index].Path, got, want)
		}
	}
	single, err := Parse([]byte("d4:infod4:name8:show.mkv6:lengthi1eee"))
	if err != nil {
		t.Fatal(err)
	}
	if !filekinds.FirstLevel(single.Files[0].Path) {
		t.Fatalf("single-file torrent file %q must be first level", single.Files[0].Path)
	}
}

// torrentFileFixture is one entry of a bencoded v1 multi-file torrent.
type torrentFileFixture struct {
	Path   []string
	Length int64
}

// multiFileTorrentBencode assembles a minimal but structurally valid v1
// multi-file torrent. It exists so tree/projection assertions do not depend on
// hand-counted bencode string prefixes.
func multiFileTorrentBencode(torrentName string, files []torrentFileFixture) []byte {
	var builder strings.Builder
	builder.WriteString("d4:info")
	builder.WriteString("d4:name" + strconv.Itoa(len(torrentName)) + ":" + torrentName)
	builder.WriteString("5:filesl")
	for _, file := range files {
		builder.WriteString("d6:lengthi" + strconv.FormatInt(file.Length, 10) + "e4:pathl")
		for _, part := range file.Path {
			builder.WriteString(strconv.Itoa(len(part)) + ":" + part)
		}
		builder.WriteString("ee")
	}
	builder.WriteString("e") // close files list
	builder.WriteString("e") // close info dictionary
	builder.WriteString("e") // close root dictionary
	return []byte(builder.String())
}

func TestParseRejectsUnsafeTorrentPath(t *testing.T) {
	data := []byte("d4:infod5:filesld6:lengthi10e4:pathl2:..eee4:name4:Showee")
	if _, err := Parse(data); err == nil {
		t.Fatal("unsafe torrent path was accepted")
	}
}

func TestParseRejectsExcessiveBencodeNesting(t *testing.T) {
	payload := "d4:info" + strings.Repeat("l", maxNestingDepth+1) + "e" + strings.Repeat("e", maxNestingDepth+1)
	if _, err := Parse([]byte(payload)); err == nil || !strings.Contains(err.Error(), "nesting exceeds limit") {
		t.Fatalf("expected nesting limit error, got %v", err)
	}
}

func TestParseV2FileTree(t *testing.T) {
	data := []byte("d4:info" +
		"d9:file tree" +
		"d7:Show S1" +
		"d7:ep1.mkv" +
		"d0:d6:lengthi7ee" +
		"e" + // ep1.mkv directory
		"e" + // Show S1 directory
		"e" + // file tree
		"4:name7:Show S1" +
		"e" + // info
		"e") // root
	metadata, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TorrentVersion != "v2" || metadata.TotalSize != 7 || len(metadata.Files) != 1 {
		t.Fatalf("unexpected v2 metadata: %+v", metadata)
	}
	if metadata.Files[0].Path != "Show S1/ep1.mkv" {
		t.Fatalf("unexpected v2 file path: %+v", metadata.Files[0])
	}
	// v2-only torrent must have SHA-256 hash, empty SHA-1
	if metadata.InfoHashV1 != "" {
		t.Fatalf("v2-only torrent should not have v1 hash, got %s", metadata.InfoHashV1)
	}
	if metadata.InfoHashV2 == "" {
		t.Fatal("v2-only torrent should have a v2 (SHA-256) hash")
	}
	expectedInfoHashV2 := sha256.Sum256([]byte("d9:file tree" +
		"d7:Show S1" +
		"d7:ep1.mkv" +
		"d0:d6:lengthi7ee" +
		"e" + // ep1.mkv directory
		"e" + // Show S1 directory
		"e" + // file tree
		"4:name7:Show S1" +
		"e")) // close info dict
	if metadata.InfoHashV2 != hex.EncodeToString(expectedInfoHashV2[:]) {
		t.Fatalf("v2 hash = %s, want %s", metadata.InfoHashV2, hex.EncodeToString(expectedInfoHashV2[:]))
	}
}
