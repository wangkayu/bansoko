package domain

import (
	"strings"
	"testing"
)

// Mikan's RSS publishes no infohash element; the identity lives in the
// enclosure path. These tests pin the fill-only extraction so a lazy expansion
// has a candidate identity to verify the fetched bytes against, without ever
// changing what AniBT's anibt:infohash or a provider element already said.

const mikanHashV1 = "0123456789abcdef0123456789abcdef01234567"
const mikanHashV2 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseFeedExtractsMikanInfoHashFromEnclosureURL(t *testing.T) {
	items, err := ParseFeed([]byte(`<rss><channel><item>
<guid>mikan-1</guid><title>[Group] Show - 01</title>
<link>https://mikanani.me/Home/Episode/` + mikanHashV1 + `</link>
<enclosure url="https://mikanani.me/Download/20261017/` + mikanHashV1 + `.torrent" type="application/x-bittorrent" length="12345"/>
</item></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	if item := items[0]; item.TorrentInfoHash != mikanHashV1 {
		t.Fatalf("TorrentInfoHash = %q, want %q", item.TorrentInfoHash, mikanHashV1)
	}
	// Reading an identity off the URL does not make the URL a torrent blob in a
	// different way: the enclosure is still the metadata URL the expand path
	// fetches, exactly as before lazy expansion existed.
	if item := items[0]; item.TorrentMetadataURL() != item.Enclosure {
		t.Fatalf("TorrentMetadataURL = %q, want the enclosure %q", item.TorrentMetadataURL(), item.Enclosure)
	}
}

func TestParseFeedExtractsHashV2AndLowercasesURLHash(t *testing.T) {
	items, err := ParseFeed([]byte(`<rss><channel><item>
<guid>mikan-2</guid><title>[Group] Show - 02</title>
<enclosure url="https://mikanani.me/Download/20261017/` + "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF" + `.torrent" type="application/x-bittorrent" length="12345"/>
</item></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TorrentInfoHash != mikanHashV2 {
		t.Fatalf("uppercase v2 URL hash was not normalized: %+v", items)
	}
}

func TestParseFeedLeavesAniBTOpaqueTorrentURLWithoutInfoHash(t *testing.T) {
	items, err := ParseFeed([]byte(`<rss xmlns:torrent="https://anibt.example/torrent"><channel><item>
<guid>anibt-1</guid><title>Show - 03</title><link>https://anibt.example/release/3</link>
<torrent:link>https://anibt.example/api/torrent/rel_abc123def456.torrent</torrent:link>
</item></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	// rel_abc123def456 is not canonical hex, so the URL contributes nothing and
	// the opaque AniBT tag is never asked to satisfy the hex shape.
	if item := items[0]; item.TorrentInfoHash != "" {
		t.Fatalf("opaque AniBT torrent URL produced an infohash: %q", item.TorrentInfoHash)
	}
}

func TestParseFeedPrefersXMLInfoHashOverURLHash(t *testing.T) {
	items, err := ParseFeed([]byte(`<rss xmlns:torrent="https://example.test/torrent"><channel><item>
<guid>mixed-1</guid><title>Show - 04</title>
<torrent:link>https://mikanani.me/Download/20261017/` + mikanHashV1 + `.torrent</torrent:link>
<torrent:infohash>abc123def456</torrent:infohash>
</item></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TorrentInfoHash != "abc123def456" {
		t.Fatalf("the XML identity must win over the URL: %+v", items)
	}
}

func TestParseAtomEntryExtractsInfoHashFromEnclosureURL(t *testing.T) {
	result, err := ParseFeedDetailed([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry>
<id>atom-mikan-1</id><title>Show - 05</title>
<link rel="enclosure" type="application/x-bittorrent" href="https://mikanani.me/Download/20261017/` + mikanHashV1 + `.torrent"/>
</entry></feed>`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != FeedFormatAtom || len(result.Items) != 1 {
		t.Fatalf("unexpected Atom result: %+v", result)
	}
	if item := result.Items[0]; item.TorrentInfoHash != mikanHashV1 {
		t.Fatalf("Atom TorrentInfoHash = %q, want %q", item.TorrentInfoHash, mikanHashV1)
	}
}

// IsCanonicalInfoHash is the single rule the RSS layer and the catalog layer
// both rely on: the former to accept a URL-derived identity, the latter to
// decide whether a feed's own infohash may reach the snapshot's info_hash
// column, whose CHECK accepts only these two shapes. A second copy of the
// rule is how one layer ends up admitting what the other rejects.
func TestCanonicalInfoHashAcceptsOnlyCanonicalForms(t *testing.T) {
	for name, testCase := range map[string]struct {
		value string
		want  bool
	}{
		"v1":                   {value: mikanHashV1, want: true},
		"v2":                   {value: mikanHashV2, want: true},
		"uppercase v2":         {value: strings.ToUpper(mikanHashV2), want: false},
		"anibt opaque tag":     {value: "abc123def456", want: false},
		"short hex":            {value: mikanHashV1[:39], want: false},
		"non-hex of v1 length": {value: strings.Repeat("z", 40), want: false},
		"empty":                {value: "", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsCanonicalInfoHash(testCase.value); got != testCase.want {
				t.Fatalf("IsCanonicalInfoHash(%q) = %v, want %v", testCase.value, got, testCase.want)
			}
		})
	}
}
