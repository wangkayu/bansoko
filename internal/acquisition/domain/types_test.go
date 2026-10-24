package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/attrrules"
)

func TestParseEpisodeUsesRegexAndOffset(t *testing.T) {
	result, err := ParseEpisode("[Group] Show - 03 [1080p]", `- (\d{2}) `, 1)
	if err != nil || result.EpisodeNumber != 4 || result.RawCapture != "03" {
		t.Fatalf("unexpected parser result: %+v, err=%v", result, err)
	}
}

func TestParseEpisodeUsesDefaultPatternForMikanTitles(t *testing.T) {
	tests := []struct {
		title   string
		episode int
	}{
		{"[示例字幕组][进击的巨人/进击的巨人][01][GB][1080P][MP4]", 1},
		{"[Group] Show - 12 [1080p]", 12},
		{"[Group] Show S01E03 [1080p]", 3},
		{"[Group] Show 第04话 [1080p]", 4},
		// "<anime title> N - EE": the trailing bare number after a dash is the
		// episode; the season ordinal "N" must not win (real AniBT release:
		// "[SampleGroup] 进击的巨人 3 - 06 [1080P]..." parsed as episode 3
		// until the negative lookahead was added).
		{"[SampleGroup] 进击的巨人 3 - 06 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", 6},
		{"[SampleGroup] 进击的巨人 3 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT]", 1},
	}
	for _, test := range tests {
		result, err := ParseEpisode(test.title, "", 0)
		if err != nil || result.EpisodeNumber != test.episode || result.Pattern == "" {
			t.Errorf("default parser for %q = %+v, err=%v", test.title, result, err)
		}
	}
}

func TestParseEpisodeRejectsEmptyTitle(t *testing.T) {
	if _, err := ParseEpisode("", "", 0); err == nil {
		t.Fatal("expected empty title to be rejected")
	}
}

func TestParseEpisodeHandlesVersionSuffix(t *testing.T) {
	// ParseEpisode strips "v2" suffix: "01v2" -> 1
	result, err := ParseEpisode("[Group] Show - 01v2 [1080p]", `- (\d{2})v\d+`, 0)
	if err != nil || result.EpisodeNumber != 1 || result.RawCapture != "01" {
		t.Fatalf("version suffix not stripped: %+v, err=%v", result, err)
	}
}

func TestParseEpisodeHandlesDecimalEpisode(t *testing.T) {
	// Decimal episodes like 7.5 are supported.
	result, err := ParseEpisode("[Group] Show - 07.5 [1080p]", `- ([0-9]{1,3}\.[0-9]+)`, 0)
	if err != nil || result.EpisodeNumber != 7 || result.RawCapture != "07.5" {
		t.Fatalf("decimal episode not parsed: %+v, err=%v", result, err)
	}
}

func TestParseEpisodeExactKeepsDecimals(t *testing.T) {
	// The exact path captures the fraction as-is; the resolver refuses it.
	result, err := ParseEpisodeExact("[Group] Show - 07.5 [1080p]", `- ([0-9]{1,3}\.[0-9]+)`, 0)
	if err != nil {
		t.Fatalf("ParseEpisodeExact error: %v", err)
	}
	if string(result.EpisodeNumber) != "7.5" {
		t.Fatalf("EpisodeNumber=%q, want 7.5 (decimal preserved)", result.EpisodeNumber)
	}
	if result.RawCapture != "07.5" || result.Confidence != "certain" {
		t.Fatalf("raw=%q confidence=%q", result.RawCapture, result.Confidence)
	}
}

func TestParseEpisodeExactOffsetIsIntegerOnly(t *testing.T) {
	result, err := ParseEpisodeExact("[Group] Show - 07 [1080p]", `- ([0-9]{1,3})`, 2)
	if err != nil || string(result.EpisodeNumber) != "9" {
		t.Fatalf("EpisodeNumber=%q err=%v, want 9", result.EpisodeNumber, err)
	}
	// A fraction is evidence only; it takes no offset.
	if _, err := ParseEpisodeExact("[Group] Show - 07.5 [1080p]", `- ([0-9]{1,3}\.[0-9]+)`, 2); err == nil {
		t.Fatal("a fractional capture accepted an offset")
	}
}

func TestParseEpisodeExactDefaultPatterns(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"[SampleGroup] 进击的巨人 3 - 06 [1080P][Baha][WEB-DL][AAC AVC][CHT]", "6"},
		{"Show S2E12 [1080p]", "12"},
		{"[Group] Episode 12.5 [1080p]", "12.5"},
		{"Show - 07.5", "7.5"},
	}
	for _, tt := range tests {
		result, err := ParseEpisodeExact(tt.title, "", 0)
		if err != nil {
			t.Fatalf("%q: ParseEpisodeExact error: %v", tt.title, err)
		}
		if string(result.EpisodeNumber) != tt.want {
			t.Fatalf("%q: EpisodeNumber=%q, want %q", tt.title, result.EpisodeNumber, tt.want)
		}
	}
}

func testRules() attrrules.Rules {
	return attrrules.Rules{Version: 1, Resolution: []string{"2160p", "1440p", "1080p", "720p", "480p"}, Quality: []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"}, Language: []string{"chs", "cht", "sc", "tc", "jpn", "eng"}, SpecialMarkers: []string{"special", "ncop", "nced"}, GroupBrackets: []string{"[", "]"}}
}
func TestParseReleaseFactsUsesConfiguredGroupBrackets(t *testing.T) {
	rules := testRules()
	rules.GroupBrackets = []string{"<", ">"}
	parsed := ParseReleaseFacts("<Configured Group> Show 1080p", rules)
	if parsed.GroupName != "Configured Group" {
		t.Fatalf("group=%q, want configured bracket group", parsed.GroupName)
	}
	if parsed.Confidence != "certain" {
		t.Fatalf("confidence=%q, want certain", parsed.Confidence)
	}
	parsed = ParseReleaseFacts("[Hardcoded Group] Show 1080p", rules)
	if parsed.GroupName != "" {
		t.Fatalf("group=%q, parser used hard-coded brackets", parsed.GroupName)
	}
}

func TestParseReleaseFactsFailsClosedForInvalidGroupBrackets(t *testing.T) {
	for _, brackets := range [][]string{nil, {}, {"["}, {"[", "]", "extra"}, {"", "]"}, {"[", ""}} {
		rules := testRules()
		rules.GroupBrackets = brackets
		parsed := ParseReleaseFacts("[Group] Show 1080p", rules)
		if parsed.GroupName != "" {
			t.Fatalf("brackets=%v: group=%q, want empty", brackets, parsed.GroupName)
		}
		if parsed.Confidence != "uncertain" {
			t.Fatalf("brackets=%v: confidence=%q, want uncertain", brackets, parsed.Confidence)
		}
	}
}
func TestParseFileDisplayFacts(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     FileDisplayFacts
	}{
		{
			name:     "full badges with revision and language",
			filename: "[Nekomoe kissaten][Sakamoto Days][01v2][1080p][CHS].mkv",
			want:     FileDisplayFacts{Revision: "v2", Resolution: "1080p", Language: "chs"},
		},
		{
			name:     "decimal episode keeps its revision",
			filename: "[Group] Show - 07.5v3 [720p].mkv",
			want:     FileDisplayFacts{Revision: "v3", Resolution: "720p"},
		},
		{
			name:     "subtitle file carries language only",
			filename: "[Group] Show - 12 [CHT].ass",
			want:     FileDisplayFacts{Language: "cht"},
		},
		{
			name:     "v0 is not a revision",
			filename: "[Group] Show - 01v0 [1080p].mkv",
			want:     FileDisplayFacts{Resolution: "1080p"},
		},
		{
			name:     "a codec tag is not a revision",
			filename: "[Group] Show - 01 [x265v2k][1080p].mkv",
			want:     FileDisplayFacts{Resolution: "1080p"},
		},
		{
			name:     "no episode yields only resolution",
			filename: "[Group] Show [1080p].mkv",
			want:     FileDisplayFacts{Resolution: "1080p"},
		},
		{
			name:     "unparseable filename yields nothing",
			filename: "menu.png",
			want:     FileDisplayFacts{},
		},
		{
			name:     "empty filename yields nothing",
			filename: "   ",
			want:     FileDisplayFacts{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseFileDisplayFacts(test.filename, testRules()); got != test.want {
				t.Fatalf("ParseFileDisplayFacts(%q) = %+v, want %+v", test.filename, got, test.want)
			}
		})
	}
}

func TestFeedItemDownloadURLPrefersTorrentURL(t *testing.T) {
	item := FeedItem{Link: "https://mikan.test/Home/Episode/1", Enclosure: "https://mikan.test/release/1", TorrentURL: "https://mikan.test/Download/1.torrent"}
	if got := item.DownloadURL(); got != item.TorrentURL {
		t.Fatalf("download URL should prefer the normalized torrent URL: %q", got)
	}
	item.TorrentURL = ""
	if got := item.DownloadURL(); got != item.Enclosure {
		t.Fatalf("download URL should use the enclosure when no torrent URL is known: %q", got)
	}
	item.Enclosure = ""
	if got := item.DownloadURL(); got != item.Link {
		t.Fatalf("download URL should fall back to the release link: %q", got)
	}
}

func TestParseFeedPreservesV1RSSFieldsAndMikanTorrentNamespace(t *testing.T) {
	items, err := ParseFeed([]byte(`<?xml version="1.0"?>
<rss xmlns:torrent="https://mikan.example/torrent">
  <channel><item>
    <guid>release-1</guid>
    <title>[Group] Show - 01</title>
    <link>https://mikan.example/Home/Episode/release-1</link>
    <description><![CDATA[release description]]></description>
    <enclosure url="https://mikan.example/Download/release-1.torrent" type="application/x-bittorrent" length="12345"/>
    <torrent:pubDate>2025-10-08T12:26:51.417</torrent:pubDate>
    <torrent:link>https://mikan.example/Download/release-1.torrent</torrent:link>
  </item></channel>
</rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	item := items[0]
	if item.Description != "release description" || item.EnclosureType != "application/x-bittorrent" || item.EnclosureLength != 12345 {
		t.Fatalf("RSS fields were not preserved: %+v", item)
	}
	want := time.Date(2025, 10, 8, 12, 26, 51, 417000000, time.UTC)
	if !item.Published.Equal(want) {
		t.Fatalf("Mikan namespace pubDate = %s, want %s", item.Published, want)
	}
	if item.DownloadURL() != item.Enclosure {
		t.Fatalf("enclosure must remain the download URL: %+v", item)
	}
}

func TestParseFeedAcceptsAniBTTorrentFactsWithoutEnclosure(t *testing.T) {
	items, err := ParseFeed([]byte(`<rss xmlns:torrent="https://anibt.example/torrent"><channel><item>
<guid>release-2</guid><title>Show - 02</title><link>https://anibt.example/release/2</link>
<torrent:link>https://anibt.example/torrent/2.torrent</torrent:link>
<torrent:infohash>ABCDEF</torrent:infohash><torrent:magneturi>magnet:?xt=urn:btih:ABCDEF</torrent:magneturi>
<torrent:filename>show-02.mkv</torrent:filename><torrent:contentLength>99</torrent:contentLength>
</item></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DownloadURL() != "https://anibt.example/torrent/2.torrent" {
		t.Fatalf("torrent link was not normalized: %+v", items)
	}
	item := items[0]
	if item.TorrentInfoHash != "ABCDEF" || item.TorrentFilename != "show-02.mkv" || item.TorrentSizeBytes != 99 {
		t.Fatalf("AniBT torrent facts were not preserved: %+v", item)
	}
}

func TestParseFeedDetailedPrefersTorrentEnclosure(t *testing.T) {
	result, err := ParseFeedDetailed([]byte(`<rss><channel><item>
<guid>multi-enclosure</guid><title>Show - 03</title>
<link>https://example.test/release/3</link>
<enclosure url="https://example.test/release/3" type="text/html"/>
<enclosure url="https://example.test/download/3.torrent" type="application/octet-stream"/>
</item></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != FeedFormatRSS || len(result.Items) != 1 || result.Items[0].DownloadURL() != "https://example.test/download/3.torrent" {
		t.Fatalf("torrent enclosure was not preferred: %+v", result)
	}
}

func TestParseFeedDetailedAcceptsRSSOneRootItems(t *testing.T) {
	result, err := ParseFeedDetailed([]byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><item>
<title>Show - 03</title><link>https://example.test/3.torrent</link>
</item></rdf:RDF>`))
	if err != nil || result.Format != FeedFormatRSS || len(result.Items) != 1 {
		t.Fatalf("RSS 1.0 root items were not normalized: %+v err=%v", result, err)
	}
}

func TestParseFeedDetailedPrefersAtomEnclosureOverAlternate(t *testing.T) {
	result, err := ParseFeedDetailed([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry>
<id>atom-1</id><title>Show - 04</title>
<link rel="alternate" href="https://example.test/release/4"/>
<link rel="enclosure" type="application/x-bittorrent" href="https://example.test/download/4.torrent"/>
<updated>2026-07-10T00:00:00Z</updated>
</entry></feed>`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != FeedFormatAtom || len(result.Items) != 1 {
		t.Fatalf("unexpected Atom result: %+v", result)
	}
	item := result.Items[0]
	if item.Link != "https://example.test/release/4" || item.DownloadURL() != "https://example.test/download/4.torrent" || item.TorrentURL == "" {
		t.Fatalf("Atom enclosure/alternate normalization is wrong: %+v", item)
	}
}

// Magnets are not supported. A magnet-only item is
// skipped with magnet_only, decided by the URL scheme: an enclosure typed
// application/x-bittorrent whose URL is a magnet is still a magnet.
func TestParseFeedDetailedSkipsMagnetOnlyItem(t *testing.T) {
	for name, feed := range map[string]string{
		"link": `<rss><channel><item>
<title>Show - 05</title><link>magnet:?xt=urn:btih:abcdef</link>
</item></channel></rss>`,
		"nested": `<rss xmlns:torrent="https://example.test/torrent"><channel><item>
<title>Show - 05b</title><torrent:magneturi>magnet:?xt=urn:btih:nested</torrent:magneturi>
</item></channel></rss>`,
		"enclosure typed as torrent": `<rss><channel><item>
<title>Show - 06</title><link>https://example.test/show-06.html</link><enclosure url="magnet:?xt=urn:btih:123456" type="application/x-bittorrent" length="1"/>
</item></channel></rss>`,
		"atom": `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>a</id><title>Show - 07</title>
<link rel="enclosure" type="application/x-bittorrent" href="magnet:?xt=urn:btih:abc"/></entry></feed>`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParseFeedDetailed([]byte(feed))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Reason != FeedSkipMagnetOnly {
				t.Fatalf("magnet-only item was not skipped as magnet_only: %+v", result)
			}
			var failure *FeedFailure
			if err := RequireUsableItems(result); !errors.As(err, &failure) || failure.Code != FeedFailureNoUsableItems || !strings.Contains(err.Error(), "magnet_only=1") {
				t.Fatalf("magnet-only feed must fail with a summary, got %v", err)
			}
		})
	}
}

// An item with both a .torrent and a magnet keeps the .torrent and drops the
// magnet, whichever the feed ranks first.
func TestParseFeedDetailedPrefersTorrentOverMagnet(t *testing.T) {
	result, err := ParseFeedDetailed([]byte(`<rss xmlns:torrent="https://example.test/torrent"><channel><item>
<title>Show - 08</title><link>magnet:?xt=urn:btih:abc</link>
<enclosure url="magnet:?xt=urn:btih:abc" type="application/x-bittorrent"/>
<enclosure url="https://example.test/8.torrent" type="application/x-bittorrent"/>
<torrent:magneturi>magnet:?xt=urn:btih:abc</torrent:magneturi>
</item></channel></rss>`))
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("torrent item with a magnet was not accepted: %+v err=%v", result, err)
	}
	item := result.Items[0]
	if item.DownloadURL() != "https://example.test/8.torrent" || item.TorrentURL != "https://example.test/8.torrent" {
		t.Fatalf("the .torrent must be the download: %+v", item)
	}
	for _, value := range []string{item.Link, item.Enclosure, item.GUID} {
		if strings.HasPrefix(value, "magnet:") {
			t.Fatalf("a magnet leaked into the item: %+v", item)
		}
	}
}

func TestParseFeedDetailedDistinguishesEmptyAndSkippedItems(t *testing.T) {
	empty, err := ParseFeedDetailed([]byte(`<rss><channel/></rss>`))
	if err != nil || len(empty.Items) != 0 || len(empty.Skipped) != 0 {
		t.Fatalf("valid empty RSS should be a zero-result: %+v err=%v", empty, err)
	}
	skipped, err := ParseFeedDetailed([]byte(`<rss><channel><item><title>missing download</title><link>https://example.test/release/1</link></item><item><link>https://example.test/2.torrent</link></item></channel></rss>`))
	if err != nil || len(skipped.Items) != 0 || len(skipped.Skipped) != 2 || len(skipped.Warnings) == 0 {
		t.Fatalf("item-level skips were not diagnosed: %+v err=%v", skipped, err)
	}
	if skipped.Skipped[0].Reason != "download_url_missing" || skipped.Skipped[1].Reason != "title_missing" {
		t.Fatalf("skip reasons are not stable: %+v", skipped.Skipped)
	}
	if _, err := ParseFeed([]byte(`<rss><channel><item><title>missing download</title></item></channel></rss>`)); err == nil {
		t.Fatal("compact ParseFeed should preserve no-usable-item failure")
	}
}

func TestParseFeedDetailedRejectsUnsupportedRoot(t *testing.T) {
	if _, err := ParseFeedDetailed([]byte(`<html><body>not a feed</body></html>`)); err == nil {
		t.Fatal("non-feed XML root must be rejected")
	}
}

func TestEvaluateCustomRSSUsesOneLogicalGroupAndRSSMax(t *testing.T) {
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	decision, err := Evaluate([]ReleaseCandidate{
		{ID: "a", StableID: "a", Title: "show 01", Group: "Custom", Episode: 1, Published: day},
		{ID: "b", StableID: "b", Title: "show 01 v2", Group: "Custom", Episode: 1, Published: day.Add(time.Hour)},
		{ID: "c", StableID: "c", Title: "show 02", Group: "Custom", Episode: 2, Published: day.Add(2 * time.Hour)},
	}, SelectionPolicy{Mode: ModeAll, RSSMax: 2})
	if err != nil || len(decision.Candidates) != 3 || decision.Explanation["custom_group_count"] != 1 {
		t.Fatalf("unexpected decision: %+v, err=%v", decision, err)
	}
}

func TestEvaluateIgnoresPerEpisodeRSSMaxAndKeepsFullFeed(t *testing.T) {
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	decision, err := Evaluate([]ReleaseCandidate{
		{ID: "1a", StableID: "1a", Group: "A", Episode: 1, Published: day},
		{ID: "1b", StableID: "1b", Group: "A", Episode: 1, Published: day.Add(time.Minute)},
		{ID: "1c", StableID: "1c", Group: "A", Episode: 1, Published: day.Add(2 * time.Minute)},
		{ID: "2a", StableID: "2a", Group: "A", Episode: 2, Published: day.Add(3 * time.Minute)},
		{ID: "2b", StableID: "2b", Group: "A", Episode: 2, Published: day.Add(4 * time.Minute)},
	}, SelectionPolicy{Mode: ModeAll, RSSMax: 2})
	if err != nil || len(decision.Candidates) != 5 {
		t.Fatalf("full-feed evaluation must keep every filtered release: %+v, err=%v", decision, err)
	}
	if decision.Explanation["selection_scope"] != "full_feed" || decision.Explanation["rssmax_ignored"] != true {
		t.Fatalf("full-feed evidence is missing: %+v", decision.Explanation)
	}
}

func TestEvaluateRejectsRetiredPerGroupMode(t *testing.T) {
	_, err := Evaluate([]ReleaseCandidate{
		{ID: "1a", StableID: "1a", Group: "Custom", Episode: 1},
		{ID: "1b", StableID: "1b", Group: "Custom", Episode: 1},
		{ID: "2a", StableID: "2a", Group: "Custom", Episode: 2},
	}, SelectionPolicy{Mode: ModePerGroup, RSSMax: 3})
	if err == nil {
		t.Fatal("retired per_group mode must be rejected")
	}
}

func TestEvaluateAppliesTypedQualityAndLanguageFilters(t *testing.T) {
	decision, err := Evaluate([]ReleaseCandidate{
		{ID: "good", StableID: "good", Group: "Custom", Quality: "1080p", Language: "chs", Episode: 1},
		{ID: "bad-quality", StableID: "bad-quality", Group: "Custom", Quality: "720p", Language: "chs", Episode: 1},
		{ID: "bad-language", StableID: "bad-language", Group: "Custom", Quality: "1080p", Language: "eng", Episode: 1},
	}, SelectionPolicy{Mode: ModeAll, RSSMax: 3, QualityInclude: []string{"1080p"}, LanguageInclude: []string{"chs"}})
	if err != nil || len(decision.Candidates) != 1 || decision.Candidates[0].ID != "good" {
		t.Fatalf("typed quality/language filters were not applied: %+v, err=%v", decision, err)
	}
	filters, ok := decision.Explanation["filters"].(map[string]any)
	if !ok || filters["quality_include"] == nil || filters["language_include"] == nil {
		t.Fatalf("filter explanation missing: %+v", decision.Explanation)
	}
}

func TestParseReleaseRevision(t *testing.T) {
	cases := map[string]string{
		"[SampleGroup] 进击的巨人 - 01v2 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]": "v2",
		"[示例字幕组&示例协作组] 进击的巨人 / 进击的巨人 [03V3][WebRip 1080p]":            "v3",
		"[Group] Title [v2][1080p]":           "v2",
		"[Group] 标题 第01话v2 [1080p]":           "v2",
		"[Group] Title - 01 [1080p]":          "",
		"[Group] Title - 01v0 [1080p]":        "",
		"[Group] Title - 12.5v2 [1080p]":      "v2",
		"[Group] Title S2 - 01 [HEVC x265v2]": "",
	}
	for title, want := range cases {
		if got := ParseReleaseRevision(title); got != want {
			t.Errorf("ParseReleaseRevision(%q) = %q want %q", title, got, want)
		}
	}
	if facts := ParseReleaseFacts("[Group] Title - 01v2 [1080p]", testRules()); facts.Revision != "v2" {
		t.Fatalf("ParseReleaseFacts revision = %q", facts.Revision)
	}
}
