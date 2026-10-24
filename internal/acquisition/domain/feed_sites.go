package domain

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FeedSiteID names one adapter of the declared feed-site list.
type FeedSiteID string

const (
	FeedSiteMikan      FeedSiteID = "mikan"
	FeedSiteAniBT      FeedSiteID = "anibt"
	FeedSiteNyaa       FeedSiteID = "nyaa"
	FeedSiteACGRip     FeedSiteID = "acgrip"
	FeedSiteBangumiMoe FeedSiteID = "bangumi_moe"
	FeedSiteMioBT      FeedSiteID = "miobt"
	FeedSiteUNIT3D     FeedSiteID = "unit3d"
)

// FeedSiteMatch says how a custom feed is routed to an adapter: by the URL host
// (a declared site), or by the feed's own content signature (a tracker
// architecture such as UNIT3D, whose hosts vary per installation).
type FeedSiteMatch string

const (
	FeedSiteMatchHost    FeedSiteMatch = "host"
	FeedSiteMatchContent FeedSiteMatch = "content"
)

// FeedIdentitySource says where an adapter's items get their content identity:
// from the feed itself (an infohash element, a hash in the URL), or only after
// lazy expansion fetches and parses the torrent.
type FeedIdentitySource string

const (
	FeedIdentityFromFeed       FeedIdentitySource = "feed"
	FeedIdentityAfterExpansion FeedIdentitySource = "expansion"
)

// FeedSite is one entry of the declared support list. This list is the one
// declaration of which sites have an adapter: the API, the UI and the agent
// guide all read it. A host outside it is parsed by the generic RSS/Atom
// adapter and carries no guarantee.
type FeedSite struct {
	ID       FeedSiteID
	Name     string
	Hosts    []string
	Identity FeedIdentitySource
	// Match is host for a declared site and content for an architecture
	// detected from the feed itself; a content entry declares no hosts.
	Match FeedSiteMatch
}

type feedAdapter struct {
	site FeedSite
	// adapt applies the site's quirks to the generically normalized item and
	// reports whether the raw item still has the shape the adapter reads.
	adapt func(raw rssItem, item *FeedItem) bool
	// detect, set only on content-matched entries, decides from the whole
	// feed whether it is this architecture's feed.
	detect func(items []rssItem) bool
}

// feedAdapters is the declared list, in display order.
var feedAdapters = []feedAdapter{
	{site: FeedSite{ID: FeedSiteMikan, Name: "Mikan", Hosts: []string{"mikanani.me", "mikanime.tv"}, Identity: FeedIdentityFromFeed, Match: FeedSiteMatchHost}, adapt: adaptMikanItem},
	{site: FeedSite{ID: FeedSiteAniBT, Name: "AniBT", Hosts: []string{"anibt.net"}, Identity: FeedIdentityFromFeed, Match: FeedSiteMatchHost}, adapt: adaptAniBTItem},
	{site: FeedSite{ID: FeedSiteNyaa, Name: "nyaa", Hosts: []string{"nyaa.si"}, Identity: FeedIdentityFromFeed, Match: FeedSiteMatchHost}, adapt: adaptNyaaItem},
	{site: FeedSite{ID: FeedSiteACGRip, Name: "ACG.RIP", Hosts: []string{"acg.rip"}, Identity: FeedIdentityAfterExpansion, Match: FeedSiteMatchHost}, adapt: adaptACGRipItem},
	{site: FeedSite{ID: FeedSiteBangumiMoe, Name: "bangumi.moe", Hosts: []string{"bangumi.moe"}, Identity: FeedIdentityAfterExpansion, Match: FeedSiteMatchHost}, adapt: adaptBangumiMoeItem},
	{site: FeedSite{ID: FeedSiteMioBT, Name: "MioBT", Hosts: []string{"miobt.com", "comicat.org", "kisssub.org"}, Identity: FeedIdentityFromFeed, Match: FeedSiteMatchHost}, adapt: adaptMioBTItem},
	{site: FeedSite{ID: FeedSiteUNIT3D, Name: "UNIT3D", Hosts: []string{}, Identity: FeedIdentityAfterExpansion, Match: FeedSiteMatchContent}, adapt: adaptUNIT3DItem, detect: detectUNIT3DFeed},
}

// FeedSites returns a copy of the declared support list.
func FeedSites() []FeedSite {
	sites := make([]FeedSite, 0, len(feedAdapters))
	for _, adapter := range feedAdapters {
		site := adapter.site
		site.Hosts = append(make([]string, 0, len(adapter.site.Hosts)), adapter.site.Hosts...)
		sites = append(sites, site)
	}
	return sites
}

// FeedSiteForHost matches a host against the list: the declared host itself or
// any subdomain of it (www.miobt.com, sukebei.nyaa.si).
func FeedSiteForHost(host string) (FeedSite, bool) {
	adapter := adapterForHost(host)
	if adapter == nil {
		return FeedSite{}, false
	}
	return adapter.site, true
}

// FeedSiteForURL matches the host of an absolute feed URL.
func FeedSiteForURL(raw string) (FeedSite, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return FeedSite{}, false
	}
	return FeedSiteForHost(parsed.Hostname())
}

func adapterForHost(host string) *feedAdapter {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil
	}
	for index := range feedAdapters {
		for _, declared := range feedAdapters[index].site.Hosts {
			if host == declared || strings.HasSuffix(host, "."+declared) {
				return &feedAdapters[index]
			}
		}
	}
	return nil
}

func adapterByID(id FeedSiteID) *feedAdapter {
	for index := range feedAdapters {
		if feedAdapters[index].site.ID == id {
			return &feedAdapters[index]
		}
	}
	return nil
}

// ParseFeedForURL is the one feed pipeline entry for a URL-addressed feed: the
// adapter is chosen by the URL host; for a host outside the list the feed's
// content signature may select an architecture adapter (UNIT3D), and anything
// else falls to the generic adapter (result.Site is nil). A listed host or a
// detected architecture never falls back.
func ParseFeedForURL(feedURL string, data []byte) (FeedParseResult, error) {
	parsed, err := url.Parse(strings.TrimSpace(feedURL))
	if err != nil {
		return ParseFeedDetailed(data)
	}
	return parseFeedWith(adapterForHost(parsed.Hostname()), data)
}

// ParseSiteFeed parses with one named adapter. Provider sources use it: their
// site is known from the provider kind, whatever endpoint the connection
// configures.
func ParseSiteFeed(id FeedSiteID, data []byte) (FeedParseResult, error) {
	adapter := adapterByID(id)
	if adapter == nil {
		return FeedParseResult{}, fmt.Errorf("feed site %q is not declared", id)
	}
	return parseFeedWith(adapter, data)
}

// Feed-level failure codes.
const (
	// FeedFailureNoUsableItems: the feed had items and every one was skipped.
	FeedFailureNoUsableItems = "feed_no_usable_items"
	// FeedFailureSiteFormatChanged: a listed site's feed no longer matches its
	// adapter. It never falls back to the generic adapter.
	FeedFailureSiteFormatChanged = "feed_site_format_changed"
)

// FeedFailure is a feed that parsed as XML but yields nothing usable. Code is
// one of the FeedFailure* codes; the error text is a plain summary.
type FeedFailure struct {
	Code    string
	Site    FeedSiteID
	Total   int
	Skipped []FeedItemSkip
	Detail  string
}

func (f *FeedFailure) Error() string {
	summary := summarizeFeedSkips(f.Skipped)
	switch f.Code {
	case FeedFailureSiteFormatChanged:
		name := string(f.Site)
		if adapter := adapterByID(f.Site); adapter != nil {
			name = adapter.site.Name
		}
		message := name + " feed format has changed"
		if f.Detail != "" {
			message += " (" + f.Detail + ")"
		}
		if summary != "" {
			message += fmt.Sprintf(" (skipped %d of %d item(s): %s)", len(f.Skipped), f.Total, summary)
		}
		return message
	default:
		return fmt.Sprintf("RSS feed contains no usable items (skipped %d of %d item(s): %s)", len(f.Skipped), f.Total, summary)
	}
}

// RequireUsableItems is the shared zero-usable rule: an empty feed is a valid
// zero-result (AniBT returns an empty channel for a show with no releases),
// but a feed whose every item was skipped fails with a summary of why.
func RequireUsableItems(result FeedParseResult) error {
	if len(result.Items) > 0 || len(result.Skipped) == 0 {
		return nil
	}
	failure := &FeedFailure{Code: FeedFailureNoUsableItems, Total: len(result.Skipped), Skipped: result.Skipped}
	if result.Site != nil {
		failure.Site = result.Site.ID
	}
	return failure
}

func summarizeFeedSkips(skipped []FeedItemSkip) string {
	counts := make(map[string]int)
	for _, item := range skipped {
		reason := strings.TrimSpace(item.Reason)
		if reason == "" {
			reason = "unknown"
		}
		counts[reason]++
	}
	reasons := make([]string, 0, len(counts))
	for reason, count := range counts {
		reasons = append(reasons, fmt.Sprintf("%s=%d", reason, count))
	}
	sort.Strings(reasons)
	return strings.Join(reasons, ", ")
}

// detectContentAdapter returns the content-matched adapter whose signature the
// whole feed carries, or nil. Only feeds no host adapter claimed reach it.
func detectContentAdapter(items []rssItem) *feedAdapter {
	for index := range feedAdapters {
		if feedAdapters[index].detect != nil && feedAdapters[index].detect(items) {
			return &feedAdapters[index]
		}
	}
	return nil
}

// --- adapters -----------------------------------------------------------------

// mikanFeedZone is the zone Mikan's torrent:pubDate is written in: the value
// carries no offset and the site publishes China time.
var mikanFeedZone = time.FixedZone("UTC+8", 8*60*60)

// adaptMikanItem: the enclosure is /Download/<date>/<infohash>.torrent (the
// generic parser already lifts the hash from the file name); torrent:pubDate
// has no timezone and means +0800.
func adaptMikanItem(raw rssItem, item *FeedItem) bool {
	if strings.TrimSpace(raw.PubDate) == "" {
		if value := collectTorrentFields(raw.Children).pubDate; value != "" {
			if published, ok := parseZonelessTime(value, mikanFeedZone); ok {
				item.Published = published
			}
		}
	}
	return looksLikeTorrentURL(item.TorrentURL)
}

// adaptAniBTItem: anibt:torrentUrl (or the enclosure) is the .torrent;
// torrent/infohash is the feed identity. The provenance facts and structured
// tags are added by the provider fetcher.
func adaptAniBTItem(_ rssItem, item *FeedItem) bool {
	return looksLikeTorrentURL(item.TorrentURL)
}

// adaptNyaaItem: <link> is /download/<id>.torrent, nyaa:infoHash the identity,
// nyaa:size a human string such as "1.4 GiB".
func adaptNyaaItem(raw rssItem, item *FeedItem) bool {
	hash := ""
	sizeText := ""
	for _, element := range raw.Children {
		switch strings.ToLower(element.XMLName.Local) {
		case "infohash":
			hash = strings.ToLower(strings.TrimSpace(element.Text))
		case "size":
			sizeText = strings.TrimSpace(element.Text)
		}
	}
	if !IsCanonicalInfoHash(hash) {
		return false
	}
	item.TorrentInfoHash = hash
	if size, ok := parseHumanSize(sizeText); ok {
		item.TorrentSizeBytes = size
		item.EnclosureLength = size
	} else {
		item.TorrentSizeBytes = 0
		item.EnclosureLength = 0
	}
	return looksLikeTorrentURL(item.TorrentURL)
}

// adaptACGRipItem: the enclosure is /t/<id>.torrent; the numeric id is not a
// hash, so the identity comes from the expanded torrent.
func adaptACGRipItem(_ rssItem, item *FeedItem) bool {
	return looksLikeTorrentURL(item.TorrentURL)
}

// adaptBangumiMoeItem: the enclosure is /download/torrent/<ObjectId>/<title>.torrent
// with an unencoded path (spaces, brackets, CJK) and length="0". The path is
// percent-encoded; the 24-hex ObjectId is not a hash, so the identity comes
// from the expanded torrent.
func adaptBangumiMoeItem(raw rssItem, item *FeedItem) bool {
	enclosure := selectRSS(raw.Enclosures)
	parsed, err := url.Parse(strings.TrimSpace(enclosure.URL))
	if err != nil || !strings.HasPrefix(parsed.Path, "/download/torrent/") {
		return false
	}
	// url.Parse keeps the decoded path; String() re-encodes it.
	parsed.RawPath = ""
	encoded := parsed.String()
	if !isHTTPURL(encoded) {
		return false
	}
	item.Enclosure = encoded
	item.TorrentURL = encoded
	item.TorrentInfoHash = ""
	if item.GUID == strings.TrimSpace(enclosure.URL) {
		item.GUID = encoded
	}
	return true
}

// adaptMioBTItem covers miobt, comicat and kisssub (one backend). The enclosure
// is http://v2.uploadbt.com/?r=down&hash=<infohash>: a .torrent download
// without the suffix, identified by its hash parameter.
func adaptMioBTItem(raw rssItem, item *FeedItem) bool {
	enclosure := selectRSS(raw.Enclosures)
	value := strings.TrimSpace(enclosure.URL)
	parsed, err := url.Parse(value)
	if err != nil || !isHTTPURL(value) {
		return false
	}
	hash := strings.ToLower(strings.TrimSpace(parsed.Query().Get("hash")))
	if !IsCanonicalInfoHash(hash) {
		return false
	}
	item.Enclosure = value
	item.TorrentURL = value
	item.TorrentInfoHash = hash
	return true
}

// unit3dDownloadPath is UNIT3D's per-user torrent download path:
// /torrents/download/<torrent id>.<rsskey>. The key is the user's credential.
var unit3dDownloadPath = regexp.MustCompile(`^/torrents/download/([0-9]+)\.([0-9A-Fa-f]{32})$`)

// unit3dTorrentID returns the torrent id of a UNIT3D download URL.
func unit3dTorrentID(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if !isHTTPURL(value) {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" {
		return "", false
	}
	match := unit3dDownloadPath.FindStringSubmatch(parsed.Path)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// unit3dSize returns the text of the item's size element in a torrents
// namespace (UNIT3D writes <size xmlns="https://<site>/torrents">313.99 MiB</size>).
func unit3dSize(raw rssItem) (string, bool) {
	for _, element := range raw.Children {
		if strings.EqualFold(element.XMLName.Local, "size") && strings.HasSuffix(strings.TrimRight(strings.ToLower(element.XMLName.Space), "/"), "/torrents") {
			return strings.TrimSpace(element.Text), true
		}
	}
	return "", false
}

// detectUNIT3DFeed is the feed-level UNIT3D signature: no item carries an
// enclosure, and at least one item has a /torrents/download/<id>.<key> link
// together with the torrents-namespace size element. Items that then do not
// conform are judged by adaptUNIT3DItem, never by a fall back to generic.
func detectUNIT3DFeed(items []rssItem) bool {
	signed := false
	for _, raw := range items {
		if len(raw.Enclosures) > 0 {
			return false
		}
		if _, ok := unit3dTorrentID(raw.Link); !ok {
			continue
		}
		if _, ok := unit3dSize(raw); ok {
			signed = true
		}
	}
	return signed
}

// adaptUNIT3DItem: <link> is the .torrent download (no suffix, no enclosure),
// <guid> the site's torrent id (not a hash: the identity comes from the
// expanded torrent), the torrents-namespace size a human string.
func adaptUNIT3DItem(raw rssItem, item *FeedItem) bool {
	if len(raw.Enclosures) > 0 {
		return false
	}
	link := strings.TrimSpace(raw.Link)
	id, ok := unit3dTorrentID(link)
	if !ok || strings.TrimSpace(raw.GUID) != id {
		return false
	}
	sizeText, ok := unit3dSize(raw)
	if !ok {
		return false
	}
	size, ok := parseHumanSize(sizeText)
	if !ok {
		return false
	}
	item.GUID = id
	item.Link = link
	item.TorrentURL = link
	item.TorrentInfoHash = ""
	item.TorrentSizeBytes = size
	item.EnclosureLength = size
	return true
}

func parseZonelessTime(value string, zone *time.Location) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, zone); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseHumanSize reads sizes such as "1.4 GiB", "700 MiB" or "512 KB". Binary
// and decimal suffixes are both read as powers of 1024, which is how these
// sites compute them. An unreadable value is reported as absent, never zero.
func parseHumanSize(value string) (int64, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 || len(fields) > 2 {
		return 0, false
	}
	number, unit := fields[0], ""
	if len(fields) == 2 {
		unit = fields[1]
	} else {
		cut := strings.IndexFunc(number, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if cut > 0 {
			number, unit = number[:cut], number[cut:]
		}
	}
	amount, err := strconv.ParseFloat(number, 64)
	if err != nil || amount < 0 || math.IsInf(amount, 0) || math.IsNaN(amount) {
		return 0, false
	}
	multipliers := map[string]float64{
		"": 1, "b": 1, "bytes": 1,
		"kib": 1 << 10, "kb": 1 << 10,
		"mib": 1 << 20, "mb": 1 << 20,
		"gib": 1 << 30, "gb": 1 << 30,
		"tib": 1 << 40, "tb": 1 << 40,
	}
	multiplier, ok := multipliers[strings.ToLower(unit)]
	if !ok {
		return 0, false
	}
	bytes := amount * multiplier
	if bytes <= 0 || bytes > math.MaxInt64/2 {
		return 0, false
	}
	return int64(math.Round(bytes)), true
}
