package application

import (
	"reflect"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	torrentparser "bansoko/internal/acquisition/torrent"
	catalogdomain "bansoko/internal/catalog/domain"
)

// The snapshot builder is where a parsed torrent became a seed that could no
// longer describe its own file tree. These tests pin the projection the read
// model renders: an available torrent keeps its exact files, and an unusable
// one stays visible with a reason instead of disappearing.

func feedItemWithFiles(t *testing.T) acquisitiondomain.FeedItem {
	t.Helper()
	payload := []byte("d4:infod4:name8:show.mkvee")
	size := int64(1234)
	subtitleSize := int64(20)
	return acquisitiondomain.FeedItem{
		GUID:              "seed-guid-1",
		Title:             "[Group] Show - 01 [1080p]",
		Enclosure:         "https://provider.example.test/show-01.torrent",
		Published:         time.Date(2026, 7, 29, 16, 30, 0, 0, time.UTC),
		TorrentSizeBytes:  1254,
		TorrentPayload:    payload,
		TorrentFilename:   "show.mkv",
		TorrentVersion:    "v1",
		TorrentInfoHashV1: "0123456789abcdef0123456789abcdef01234567",
		TorrentInfoHash:   "0123456789abcdef0123456789abcdef01234567",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{SourcePath: "show/ep1.mkv", Filename: "ep1.mkv", SizeBytes: &size, MediaKind: "video"},
			{
				SourcePath: "show/Subs/ep1.zh-Hans.ass", Filename: "ep1.zh-Hans.ass", SizeBytes: &subtitleSize,
				MediaKind: "subtitle", Facts: map[string]any{"language": "zh-Hans"},
			},
		},
	}
}

func TestSnapshotBuilderProjectsExactTorrentTree(t *testing.T) {
	item := feedItemWithFiles(t)
	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 1 {
		t.Fatalf("seed count = %d, want 1", len(seeds))
	}
	seed := seeds[0]
	if seed.TorrentState != catalogdomain.TorrentAcquisitionAvailable {
		t.Fatalf("torrent state = %q, want %q", seed.TorrentState, catalogdomain.TorrentAcquisitionAvailable)
	}
	if seed.PayloadDigest == nil {
		t.Fatal("an available seed must carry its payload digest")
	}
	manifest := seed.Manifest
	if manifest == nil {
		t.Fatal("an available seed must carry its manifest")
	}
	if manifest.ParserVersion != torrentparser.ImplementationVersion {
		t.Fatalf("parser version = %q, want %q", manifest.ParserVersion, torrentparser.ImplementationVersion)
	}
	if manifest.FileCount() != len(item.ReleaseFiles) {
		t.Fatalf("projected %d files, want %d", manifest.FileCount(), len(item.ReleaseFiles))
	}
	if manifest.TotalSizeBytes != 1254 {
		t.Fatalf("total size = %d, want 1254", manifest.TotalSizeBytes)
	}
	for index, file := range manifest.Files {
		if file.Ordinal != index {
			t.Fatalf("file %d has ordinal %d", index, file.Ordinal)
		}
		if want := catalogdomain.TorrentFileRef(*seed.PayloadDigest, file.SourcePath); file.FileRef != want {
			t.Fatalf("file %d reference is not bound to the payload digest", index)
		}
	}
	if manifest.Files[1].Language != "zh-Hans" || manifest.Files[1].MediaKind != catalogdomain.TorrentFileMediaSubtitle {
		t.Fatalf("subtitle projection lost its parser facts: %+v", manifest.Files[1])
	}
	// The manifest must validate as an exact derivation, and the seed must hash
	// into a content identity that covers it.
	if err := manifest.Validate(); err != nil {
		t.Fatalf("projected manifest does not validate: %v", err)
	}
	if err := seed.Validate(); err != nil {
		t.Fatalf("projected seed does not validate: %v", err)
	}
}

func TestSnapshotBuilderKeepsUnavailableTorrentVisible(t *testing.T) {
	item := feedItemWithFiles(t)
	item.ReleaseFiles = nil
	item.TorrentPayload = nil
	item.TorrentErrorCode = "torrent_metadata_unavailable"
	item.TorrentErrorReason = "the torrent payload could not be parsed"

	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 1 {
		t.Fatalf("an unusable torrent must stay in its group: seed count = %d", len(seeds))
	}
	seed := seeds[0]
	if seed.TorrentState != catalogdomain.TorrentAcquisitionUnavailable {
		t.Fatalf("torrent state = %q, want %q", seed.TorrentState, catalogdomain.TorrentAcquisitionUnavailable)
	}
	if seed.Manifest != nil {
		t.Fatal("an unavailable torrent must not fabricate a manifest")
	}
	if seed.TorrentErrorCode == nil || *seed.TorrentErrorCode != "torrent_metadata_unavailable" {
		t.Fatalf("unavailable seed lost its bounded reason: %+v", seed.TorrentErrorCode)
	}
	if err := seed.Validate(); err != nil {
		t.Fatalf("unavailable seed does not validate: %v", err)
	}
}

func TestSnapshotBuilderWithoutTorrentAcquisitionStaysNotApplicable(t *testing.T) {
	item := feedItemWithFiles(t)
	item.ReleaseFiles = nil
	item.TorrentPayload = nil

	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].TorrentState != catalogdomain.TorrentAcquisitionNotApplicable {
		t.Fatalf("torrent state = %q, want %q", seeds[0].TorrentState, catalogdomain.TorrentAcquisitionNotApplicable)
	}
	if seeds[0].Manifest != nil {
		t.Fatal("a seed without torrent acquisition must not carry a manifest")
	}
}

// With preview no longer prefetching torrents, an item's v1/v2 hashes stay
// empty and the feed's own infohash field is the only identity the snapshot
// can publish. Without it the download gate would refuse every Mikan seed as
// strong_identity_missing, so this projection is the one place that keeps the
// unexpanded seed downloadable.
func TestSnapshotBuilderPublishesTheFeedInfoHashWhenNoTorrentWasParsed(t *testing.T) {
	item := feedItemWithFiles(t)
	item.TorrentPayload = nil
	item.ReleaseFiles = nil
	item.TorrentInfoHashV1 = ""
	item.TorrentInfoHashV2 = ""
	item.TorrentInfoHash = "0123456789abcdef0123456789abcdef01234567"

	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].InfoHash == nil {
		t.Fatal("a Mikan-style feed infohash must reach the snapshot seed")
	}
	if *seeds[0].InfoHash != item.TorrentInfoHash {
		t.Fatalf("published infohash = %q, want %q", *seeds[0].InfoHash, item.TorrentInfoHash)
	}
	if err := seeds[0].Validate(); err != nil {
		t.Fatalf("seed does not validate: %v", err)
	}
}

// AniBT's anibt:infohash is an opaque tag, not a hex infohash. The snapshot's
// info_hash column CHECK accepts only 40 or 64 lowercase hex characters, so an
// unvalidated fallback would fail the whole snapshot refresh.
func TestSnapshotBuilderDropsANonCanonicalFeedInfoHash(t *testing.T) {
	item := feedItemWithFiles(t)
	item.TorrentPayload = nil
	item.ReleaseFiles = nil
	item.TorrentInfoHashV1 = ""
	item.TorrentInfoHashV2 = ""
	item.TorrentInfoHash = "abc123def456"

	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatalf("an opaque provider tag must not fail the refresh: %v", err)
	}
	if seeds[0].InfoHash != nil {
		t.Fatalf("a non-canonical infohash was published: %q", *seeds[0].InfoHash)
	}
}

func TestSnapshotBuilderPrefersParsedHashesOverTheFeedField(t *testing.T) {
	parsedV1 := "0123456789abcdef0123456789abcdef01234567"
	parsedHashV2 := strings.Repeat("b", 64)
	for name, testCase := range map[string]struct {
		item acquisitiondomain.FeedItem
		want string
	}{
		"v1 wins over the feed field": {item: func() acquisitiondomain.FeedItem {
			item := feedItemWithFiles(t)
			item.TorrentInfoHashV1 = parsedV1
			item.TorrentInfoHashV2 = ""
			item.TorrentInfoHash = strings.Repeat("f", 40)
			return item
		}(), want: parsedV1},
		"v2 wins over v1": {item: func() acquisitiondomain.FeedItem {
			item := feedItemWithFiles(t)
			item.TorrentInfoHashV1 = parsedV1
			item.TorrentInfoHashV2 = parsedHashV2
			item.TorrentInfoHash = strings.Repeat("f", 40)
			return item
		}(), want: parsedHashV2},
	} {
		t.Run(name, func(t *testing.T) {
			seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{testCase.item})
			if err != nil {
				t.Fatal(err)
			}
			if seeds[0].InfoHash == nil || *seeds[0].InfoHash != testCase.want {
				t.Fatalf("published infohash = %v, want %q", seeds[0].InfoHash, testCase.want)
			}
		})
	}
}

// The snapshot prefers the feed's own field only in canonical form; an upper
// case v2 is normalized rather than dropped.
func TestSnapshotBuilderNormalizesAFeedInfoHashFromUrlExtraction(t *testing.T) {
	item := feedItemWithFiles(t)
	item.TorrentPayload = nil
	item.ReleaseFiles = nil
	item.TorrentInfoHashV1 = ""
	item.TorrentInfoHashV2 = ""
	item.TorrentInfoHash = strings.ToUpper(strings.Repeat("c", 64))

	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].InfoHash == nil || *seeds[0].InfoHash != strings.Repeat("c", 64) {
		t.Fatalf("published infohash = %v, want the lowercase form", seeds[0].InfoHash)
	}
}

// The snapshot is the only surviving carrier of an AniBT item's structured tag
// set: the tags arrive as namespaced XML in the item's transient Extensions, so
// a mapping that dropped them left every surface rebuilt from the snapshot
// unable to evaluate `anibt.*`. An item with no tag block (mikan, custom) stays
// nil, which is not the same fact as an AniBT item observed with no tags.
func TestSnapshotBuilderFreezesTheAniBTTagSet(t *testing.T) {
	item := feedItemWithFiles(t)
	item.Provider = "anibt"
	item.AniBTTags = &acquisitiondomain.AniBTTagSet{
		Schema: acquisitiondomain.AniBTTagSchemaV1,
		Tags:   []string{"resolution:1080p", "language:CHS"},
	}
	seeds, err := mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].AniBTTags == nil {
		t.Fatal("an AniBT item's tag set was dropped at snapshot mapping")
	}
	if seeds[0].AniBTTags.Schema != acquisitiondomain.AniBTTagSchemaV1 || !reflect.DeepEqual(seeds[0].AniBTTags.Tags, item.AniBTTags.Tags) {
		t.Fatalf("frozen tags = %+v, want the item's %+v", seeds[0].AniBTTags, item.AniBTTags)
	}

	// An AniBT item observed with no tags is a non-nil, empty set.
	item.AniBTTags = &acquisitiondomain.AniBTTagSet{Schema: acquisitiondomain.AniBTTagSchemaV1}
	seeds, err = mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].AniBTTags == nil || seeds[0].AniBTTags.Tags == nil || len(seeds[0].AniBTTags.Tags) != 0 {
		t.Fatalf("a tag-less AniBT item must persist an empty set, got %+v", seeds[0].AniBTTags)
	}

	// A provider without a tag block stays nil: absence, not an empty set.
	item.Provider = "mikan"
	item.AniBTTags = nil
	seeds, err = mapFeedItemsToSnapshotSeeds([]acquisitiondomain.FeedItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].AniBTTags != nil {
		t.Fatalf("a tag-less provider produced a tag block: %+v", seeds[0].AniBTTags)
	}
}
