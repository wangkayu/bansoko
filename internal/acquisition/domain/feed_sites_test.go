package domain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures are trimmed real feed samples. Each test pins the adapter's title,
// download URL and identity rules on the real shape, selected by the host a
// custom source would be configured with.

func readFeedFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "feeds", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parseFixtureForURL(t *testing.T, feedURL, fixture string, wantSite FeedSiteID) FeedParseResult {
	t.Helper()
	result, err := ParseFeedForURL(feedURL, readFeedFixture(t, fixture))
	if err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	if result.Site == nil || result.Site.ID != wantSite {
		t.Fatalf("%s: adapter = %+v, want %s", fixture, result.Site, wantSite)
	}
	if len(result.Skipped) != 0 || len(result.Items) == 0 {
		t.Fatalf("%s: every recorded item must be accepted: items=%d skipped=%+v", fixture, len(result.Items), result.Skipped)
	}
	for _, item := range result.Items {
		if strings.TrimSpace(item.Title) == "" || !isHTTPURL(item.TorrentURL) || item.DownloadURL() != item.TorrentURL || item.TorrentMetadataURL() != item.TorrentURL {
			t.Fatalf("%s: item lacks a title or an HTTP .torrent download: %+v", fixture, item)
		}
	}
	return result
}

func TestFeedSiteMikanFixture(t *testing.T) {
	result := parseFixtureForURL(t, "https://mikanani.me/RSS/Classic", "mikan.xml", FeedSiteMikan)
	first := result.Items[0]
	if first.TorrentURL != "https://mikanani.me/Download/20261019/084c615f82f1e9577ce3bf115069cf3c59111065.torrent" || first.TorrentInfoHash != "084c615f82f1e9577ce3bf115069cf3c59111065" {
		t.Fatalf("Mikan identity comes from the .torrent file name: %+v", first)
	}
	if first.EnclosureLength != 381052512 {
		t.Fatalf("Mikan size = %d", first.EnclosureLength)
	}
	// torrent:pubDate 2026-10-19T19:13:00 carries no zone and means +0800.
	if want := time.Date(2026, 10, 19, 11, 13, 0, 0, time.UTC); !first.Published.Equal(want) {
		t.Fatalf("Mikan published = %s, want %s", first.Published, want)
	}
	// The generic adapter reads the same zoneless value as UTC; only the
	// Mikan adapter knows the site's zone.
	generic, err := ParseFeedDetailed(readFeedFixture(t, "mikan.xml"))
	if err != nil || generic.Site != nil || generic.Items[0].Published.Equal(first.Published) {
		t.Fatalf("generic parse should not apply the Mikan zone: %+v err=%v", generic.Site, err)
	}
	// The mirror host uses the same adapter.
	if site, ok := FeedSiteForURL("https://mikanime.tv/RSS/Bangumi?bangumiId=1"); !ok || site.ID != FeedSiteMikan {
		t.Fatalf("mirror host not matched: %+v", site)
	}
}

func TestFeedSiteAniBTFixture(t *testing.T) {
	result := parseFixtureForURL(t, "https://anibt.net/rss/anime.xml?bgmId=698032", "anibt.xml", FeedSiteAniBT)
	first := result.Items[0]
	if first.TorrentURL != "https://anibt.net/api/torrent/rel_gXq43BXIlSAT.torrent" || first.TorrentInfoHash != "44c9a9119c7694330b79d5e39a191a194eabea27" || first.GUID != "rel_gXq43BXIlSAT" {
		t.Fatalf("AniBT identity comes from torrent/infohash: %+v", first)
	}
	if first.TorrentSizeBytes != 114029245 {
		t.Fatalf("AniBT size = %d", first.TorrentSizeBytes)
	}
	// An empty channel is a valid zero-result (a show with no releases), not
	// a changed format.
	empty, err := ParseSiteFeed(FeedSiteAniBT, []byte(`<rss version="2.0"><channel><title>x</title></channel></rss>`))
	if err != nil || len(empty.Items) != 0 || RequireUsableItems(empty) != nil {
		t.Fatalf("empty AniBT channel must be valid: %+v err=%v", empty, err)
	}
}

func TestFeedSiteNyaaFixture(t *testing.T) {
	result := parseFixtureForURL(t, "https://nyaa.si/?page=rss&q=1080p", "nyaa.xml", FeedSiteNyaa)
	first := result.Items[0]
	if first.TorrentURL != "https://nyaa.si/download/2176137.torrent" || first.TorrentInfoHash != "a8cbeb9b0be4650c4817565af4002688602b9e58" || first.GUID != "https://nyaa.si/view/2176137" {
		t.Fatalf("nyaa identity comes from nyaa:infoHash: %+v", first)
	}
	// "1.4 GiB" is read as bytes.
	if want := int64(1503238554); first.TorrentSizeBytes != want {
		t.Fatalf("nyaa size = %d, want %d", first.TorrentSizeBytes, want)
	}
	// pubDate ends in -0000.
	if want := time.Date(2026, 10, 19, 11, 28, 3, 0, time.UTC); !first.Published.Equal(want) {
		t.Fatalf("nyaa published = %s", first.Published)
	}
	if site, ok := FeedSiteForHost("sukebei.nyaa.si"); !ok || site.ID != FeedSiteNyaa {
		t.Fatal("a subdomain of a listed host uses its adapter")
	}
}

func TestFeedSiteACGRipFixture(t *testing.T) {
	result := parseFixtureForURL(t, "https://acg.rip/.xml?term=1080p", "acgrip.xml", FeedSiteACGRip)
	first := result.Items[0]
	if first.TorrentURL != "https://acg.rip/t/365651.torrent" || first.TorrentSizeBytes != 381096960 {
		t.Fatalf("acg.rip item: %+v", first)
	}
	// The numeric id is not a hash: identity only after expansion.
	for _, item := range result.Items {
		if item.TorrentInfoHash != "" {
			t.Fatalf("acg.rip must not invent a feed identity: %+v", item)
		}
	}
}

func TestFeedSiteBangumiMoeFixture(t *testing.T) {
	result := parseFixtureForURL(t, "https://bangumi.moe/rss/latest", "bangumi-moe.xml", FeedSiteBangumiMoe)
	first := result.Items[0]
	want := "https://bangumi.moe/download/torrent/6ad5fbef992853e4c9cc6736/%5BSampleGroup%5D%20%E8%BF%9B%E5%87%BB%E7%9A%84%E5%B7%A8%E4%BA%BA%20-%2001%20%5BWebRip%201080p%20HEVC-10bit%20AAC%20ASSx2%5D.torrent"
	if first.TorrentURL != want || first.Enclosure != want {
		t.Fatalf("bangumi.moe download URL must be percent-encoded:\n got %s\nwant %s", first.TorrentURL, want)
	}
	for _, item := range result.Items {
		if strings.ContainsAny(item.TorrentURL, " []") || item.TorrentInfoHash != "" || item.TorrentSizeBytes != 0 || item.EnclosureLength != 0 {
			t.Fatalf("bangumi.moe item: encoded URL, no feed identity, no size (length=0): %+v", item)
		}
	}
}

func TestFeedSiteMioBTFamilyFixtures(t *testing.T) {
	for host, fixture := range map[string]string{
		"https://www.miobt.com/rss-1080p.xml":  "miobt.xml",
		"http://comicat.org/rss-1080p.xml":     "comicat.xml",
		"http://www.kisssub.org/rss-1080p.xml": "kisssub.xml",
	} {
		t.Run(fixture, func(t *testing.T) {
			result := parseFixtureForURL(t, host, fixture, FeedSiteMioBT)
			for _, item := range result.Items {
				if !strings.HasPrefix(item.TorrentURL, "http://v2.uploadbt.com/?r=down&hash=") || !IsCanonicalInfoHash(item.TorrentInfoHash) || !strings.HasSuffix(item.TorrentURL, item.TorrentInfoHash) {
					t.Fatalf("MioBT identity comes from hash= and the enclosure is the .torrent: %+v", item)
				}
			}
		})
	}
}

// acgnx is not listed: it is parsed generically, and every item is a magnet
// typed application/x-bittorrent, so the feed fails with the magnet summary.
func TestFeedSiteUnlistedMagnetOnlyFixture(t *testing.T) {
	result, err := ParseFeedForURL("https://share.acgnx.se/rss.xml", readFeedFixture(t, "acgnx-magnet-only.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Site != nil || len(result.Items) != 0 || len(result.Skipped) != 3 {
		t.Fatalf("acgnx must be generic and fully skipped: %+v", result)
	}
	for _, skip := range result.Skipped {
		if skip.Reason != FeedSkipMagnetOnly {
			t.Fatalf("skip reason = %q, want magnet_only", skip.Reason)
		}
	}
	var failure *FeedFailure
	if err := RequireUsableItems(result); !errors.As(err, &failure) || failure.Code != FeedFailureNoUsableItems || failure.Total != 3 {
		t.Fatalf("acgnx must fail with no usable items, got %v", err)
	}
}

// An unlisted host is parsed generically and is not labelled supported.
func TestFeedSiteUnlistedHostParsesGenerically(t *testing.T) {
	result, err := ParseFeedForURL("https://feeds.example.test/rss.xml", readFeedFixture(t, "acgrip.xml"))
	if err != nil || result.Site != nil || len(result.Items) != 4 {
		t.Fatalf("unlisted host must use the generic adapter: site=%+v items=%d err=%v", result.Site, len(result.Items), err)
	}
	if _, ok := FeedSiteForHost("notnyaa.si"); ok {
		t.Fatal("a host that merely ends with a listed name is not that site")
	}
}

// A listed host whose feed no longer has its shape fails as a changed format
// and never falls back to the generic adapter, even when the generic adapter
// could read the items.
func TestFeedSiteListedHostWithChangedFormatFails(t *testing.T) {
	for name, testCase := range map[string]struct {
		url  string
		feed []byte
	}{
		// acg.rip items carry no nyaa:infoHash.
		"nyaa serving another shape": {url: "https://nyaa.si/?page=rss", feed: readFeedFixture(t, "acgrip.xml")},
		// The enclosure lost its hash= parameter.
		"miobt without hash":    {url: "https://www.miobt.com/rss.xml", feed: []byte(`<rss><channel><item><title>x - 01</title><enclosure url="http://v2.uploadbt.com/?r=down&amp;id=1" type="application/x-bittorrent"/></item></channel></rss>`)},
		"atom on a listed host": {url: "https://acg.rip/.xml", feed: []byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>1</id><title>x</title><link rel="enclosure" href="https://acg.rip/t/1.torrent"/></entry></feed>`)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFeedForURL(testCase.url, testCase.feed)
			var failure *FeedFailure
			if !errors.As(err, &failure) || failure.Code != FeedFailureSiteFormatChanged {
				t.Fatalf("want a site format change, got %v", err)
			}
		})
	}
}

func TestFeedSitesListIsTheDeclaration(t *testing.T) {
	sites := FeedSites()
	ids := make([]string, 0, len(sites))
	for _, site := range sites {
		ids = append(ids, string(site.ID))
		// A host entry declares hosts; a content entry declares none.
		if site.Name == "" || (site.Match == FeedSiteMatchHost) != (len(site.Hosts) > 0) || (site.Match != FeedSiteMatchHost && site.Match != FeedSiteMatchContent) {
			t.Fatalf("incomplete site: %+v", site)
		}
	}
	if got := strings.Join(ids, ","); got != "mikan,anibt,nyaa,acgrip,bangumi_moe,miobt,unit3d" {
		t.Fatalf("declared list = %s", got)
	}
	sites[0].Hosts[0] = "changed"
	if FeedSites()[0].Hosts[0] != "mikanani.me" {
		t.Fatal("FeedSites must return a copy")
	}
}

func TestParseHumanSize(t *testing.T) {
	for value, want := range map[string]int64{"1.4 GiB": 1503238554, "700 MiB": 734003200, "512KiB": 524288, "12 B": 12} {
		if got, ok := parseHumanSize(value); !ok || got != want {
			t.Fatalf("parseHumanSize(%q) = %d,%v want %d", value, got, ok, want)
		}
	}
	for _, value := range []string{"", "big", "1.4 GiB extra", "-1 MiB", "3 parsecs"} {
		if _, ok := parseHumanSize(value); ok {
			t.Fatalf("parseHumanSize(%q) must be absent", value)
		}
	}
}

// UNIT3D is detected by the feed's content, whatever host serves it: the
// /torrents/download/<id>.<key> link is the .torrent, <guid> the site id (not
// a hash, so the identity comes from expansion), size the namespaced element.
func TestFeedSiteUNIT3DDetectedByContent(t *testing.T) {
	for _, feedURL := range []string{"https://tracker.example.test/rss/1.00000000000000000000000000000000", "https://other.example.test/rss?rsskey=00000000000000000000000000000000"} {
		result := parseFixtureForURL(t, feedURL, "unit3d.xml", FeedSiteUNIT3D)
		first := result.Items[0]
		if first.TorrentURL != "https://tracker.example.test/torrents/download/29577.0123456789abcdef0123456789abcdef" || first.GUID != "29577" || first.TorrentInfoHash != "" {
			t.Fatalf("UNIT3D item: %+v", first)
		}
		if first.TorrentSizeBytes != 329242378 || first.EnclosureLength != first.TorrentSizeBytes {
			t.Fatalf("UNIT3D size = %d", first.TorrentSizeBytes)
		}
		if want := time.Date(2026, 10, 20, 14, 31, 47, 0, time.UTC); !first.Published.Equal(want) {
			t.Fatalf("UNIT3D published = %s", first.Published)
		}
	}
	// The generic entry point detects it too; a listed host never does.
	if generic, err := ParseFeedDetailed(readFeedFixture(t, "unit3d.xml")); err != nil || generic.Site == nil || generic.Site.ID != FeedSiteUNIT3D {
		t.Fatalf("generic entry must detect UNIT3D: %+v err=%v", generic.Site, err)
	}
	if _, err := ParseFeedForURL("https://nyaa.si/?page=rss", readFeedFixture(t, "unit3d.xml")); err == nil {
		t.Fatal("a listed host keeps its own adapter")
	}
	if _, ok := FeedSiteForHost("tracker.example.test"); ok {
		t.Fatal("a content-matched architecture is never matched by host")
	}
}

const unit3dItemTemplate = `<item><title>Show - 01</title><size xmlns="https://t.example.test/torrents">%s</size><link>%s</link><guid>%s</guid></item>`

func unit3dFeed(items ...string) []byte {
	return []byte(`<rss version="2.0"><channel>` + strings.Join(items, "") + `</channel></rss>`)
}

func unit3dItem(size, link, guid string) string {
	return fmt.Sprintf(unit3dItemTemplate, size, link, guid)
}

const unit3dTestLink = "https://t.example.test/torrents/download/7.0123456789abcdef0123456789abcdef"

// A detected UNIT3D feed whose items no longer conform fails as a changed
// format; it never falls back to the generic adapter. A feed without the
// signature keeps the generic behaviour.
func TestFeedSiteUNIT3DStrictness(t *testing.T) {
	// One item conforms, one has lost its size: the second is a mismatch.
	result, err := ParseFeedForURL("https://t.example.test/rss", unit3dFeed(
		unit3dItem("1 GiB", unit3dTestLink, "7"),
		`<item><title>Show - 02</title><link>https://t.example.test/torrents/download/8.0123456789abcdef0123456789abcdef</link><guid>8</guid></item>`,
	))
	if err != nil || result.Site == nil || result.Site.ID != FeedSiteUNIT3D || len(result.Items) != 1 || len(result.Skipped) != 1 || result.Skipped[0].Reason != FeedSkipSiteFormatMismatch {
		t.Fatalf("partial conformance: %+v err=%v", result, err)
	}
	for name, feed := range map[string][]byte{
		"guid is not the link id": unit3dFeed(unit3dItem("1 GiB", unit3dTestLink, "8")),
		"unreadable size":         unit3dFeed(unit3dItem("big", unit3dTestLink, "7")),
		"signed item plus items without the shape": unit3dFeed(
			unit3dItem("big", unit3dTestLink, "7"),
			`<item><title>Show - 02</title><link>https://t.example.test/torrents/8</link><guid>8</guid></item>`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFeedForURL("https://t.example.test/rss", feed)
			var failure *FeedFailure
			if !errors.As(err, &failure) || failure.Code != FeedFailureSiteFormatChanged || failure.Site != FeedSiteUNIT3D {
				t.Fatalf("want a UNIT3D format change, got %v", err)
			}
			if !strings.Contains(err.Error(), "UNIT3D feed format has changed") {
				t.Fatalf("error text: %v", err)
			}
		})
	}
	// No signature: an enclosure anywhere, no namespaced size, or a link of
	// another shape keeps the generic adapter and its skip reasons.
	for name, feed := range map[string][]byte{
		"enclosure present":   unit3dFeed(`<item><title>Show - 01</title><size xmlns="https://t.example.test/torrents">1 GiB</size><link>` + unit3dTestLink + `</link><guid>7</guid><enclosure url="https://t.example.test/a.torrent" type="application/x-bittorrent"/></item>`),
		"size not namespaced": unit3dFeed(`<item><title>Show - 01</title><size>1 GiB</size><link>` + unit3dTestLink + `</link><guid>7</guid></item>`),
		"short key":           unit3dFeed(unit3dItem("1 GiB", "https://t.example.test/torrents/download/7.abc", "7")),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParseFeedForURL("https://t.example.test/rss", feed)
			if err != nil || result.Site != nil {
				t.Fatalf("want the generic adapter: %+v err=%v", result.Site, err)
			}
		})
	}
}

// NexusPHP needs no adapter: its enclosure is typed application/x-bittorrent
// with the size as length, so the generic adapter accepts it.
func TestFeedSiteNexusPHPParsesGenerically(t *testing.T) {
	result, err := ParseFeedForURL("https://pt.example.test/torrentrss.php?rows=10&passkey=fedcba9876543210fedcba9876543210", readFeedFixture(t, "nexusphp.xml"))
	if err != nil || result.Site != nil || len(result.Items) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("NexusPHP: site=%+v items=%d skipped=%+v err=%v", result.Site, len(result.Items), result.Skipped, err)
	}
	item := result.Items[0]
	if item.TorrentURL != "https://pt.example.test/download.php?id=1234&passkey=fedcba9876543210fedcba9876543210&https=1" || item.DownloadURL() != item.TorrentURL || item.TorrentMetadataURL() != item.TorrentURL {
		t.Fatalf("NexusPHP download: %+v", item)
	}
	if item.EnclosureLength != 734003200 || item.Title != "[Example Group] Example Show - 03 [1080p][CHS]" {
		t.Fatalf("NexusPHP item: %+v", item)
	}
}
