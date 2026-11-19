package domain

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/episodeidentity"
)

type SeedSourceSite struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s SeedSourceSite) Validate() error {
	if strings.TrimSpace(s.ID) == "" || len(s.ID) > 256 || strings.TrimSpace(s.Name) == "" || len(s.Name) > 256 {
		return errors.New("seed source site id and name are required and bounded")
	}
	if s.Type != "public" && s.Type != "private" && s.Type != "semi-private" {
		return errors.New("seed source site type must be public, private or semi-private")
	}
	return nil
}

// MaxSeedCategories is the per-seed category limit of the persisted feed snapshot.
const MaxSeedCategories = 32

// SeedCategory is one tracker category of a Jackett search result: the
// torznab `category` attribute id resolved to its name through the indexer
// caps. It is display evidence on the seed's second line, never a filter, a
// grouping key or an identity input.
type SeedCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c SeedCategory) Validate() error {
	if strings.TrimSpace(c.ID) == "" || len(c.ID) > 64 || strings.TrimSpace(c.Name) == "" || len(c.Name) > 256 {
		return errors.New("seed category id and name are required and bounded")
	}
	return nil
}

type FeedItem struct {
	GUID string
	// SourceSite attributes one result to the tracker that Jackett aggregated.
	// It is independent of the subtitle group parsed from the release title.
	SourceSite *SeedSourceSite `json:"-"`
	Title      string
	Link       string
	// DetailsURL is an explicit page supplied by an adapter, never DownloadURL.
	DetailsURL        string
	Description       string
	Enclosure         string
	EnclosureType     string
	EnclosureLength   int64
	TorrentURL        string
	TorrentInfoHash   string
	TorrentInfoHashV1 string
	TorrentInfoHashV2 string
	TorrentVersion    string
	TorrentFilename   string
	TorrentSizeBytes  int64
	// TorrentPayload is the verified bencoded input fetched and parsed by this
	// process. It deliberately never enters persisted JSON facts or public DTOs.
	TorrentPayload []byte `json:"-"`
	// TorrentError records a bounded item-level acquisition problem. Feed-level
	// transport/XML failures remain returned as errors; this field lets one bad
	// torrent remain visible without blocking other feed items.
	TorrentErrorCode   string
	TorrentErrorReason string
	Published          time.Time
	Provider           string
	SourceFacts        []SourceProvidedFact
	ReleaseFiles       []ReleaseFile
	// Extensions keeps the namespaced leaf elements of an RSS item so provider
	// adapters can read provider-owned fields after format-level normalization.
	// It is transient parse input and never enters persisted or public JSON.
	Extensions []FeedExtensionField `json:"-"`
	// AniBTTags is the frozen AniBT structured tag set. It is nil for releases
	// that did not come from an AniBT source; an AniBT release always carries a
	// (possibly empty) set. Persistence is owned by the Release source facts.
	AniBTTags *AniBTTagSet `json:"-"`
	// TitleTags is the frozen title-parse tag set read back from the Release
	// source facts. It is nil for a freshly fetched item (the tags are derived
	// from Title by the pinned parser) and for a release persisted before the
	// title row existed.
	TitleTags *TitleTagSet `json:"-"`
	// The four swarm/billing facts below are tracker-reported attributes of a
	// Jackett search result. Every one stays absent when the tracker did not
	// report it: an absent billing factor is never normalized to 1, and
	// Leechers is derived at parse time as max(peers-seeders, 0), only when the
	// tracker published both counts. They are display evidence, not filter or
	// identity inputs.
	Seeders        *int64   `json:"-"`
	Leechers       *int64   `json:"-"`
	DownloadFactor *float64 `json:"-"`
	UploadFactor   *float64 `json:"-"`
	// SeedCategoryIDs are the raw torznab category attribute values of a
	// Jackett item. They are transient parse input (like Extensions): the
	// indexer caps resolve them into SeedCategories before the item is mapped,
	// and the raw ids are never persisted on their own.
	SeedCategoryIDs []string `json:"-"`
	// SeedCategories are the caps-resolved categories of a Jackett item. A nil
	// or empty list means no resolvable category (the tracker published none,
	// or its caps could not be read); it never fails the item.
	SeedCategories []SeedCategory `json:"-"`
}

// DownloadURL returns the item's download source: the normalized .torrent URL
// first, then the selected enclosure, then the release link. Magnet URIs are
// never a download source: the parser drops them, so
// none can reach this method.
func (item FeedItem) DownloadURL() string {
	if value := strings.TrimSpace(item.TorrentURL); value != "" {
		return value
	}
	if value := strings.TrimSpace(item.Enclosure); value != "" {
		return value
	}
	return strings.TrimSpace(item.Link)
}

// TorrentMetadataURL returns only an explicit HTTP(S) torrent blob URL. It is
// intentionally narrower than DownloadURL: a release-page link is not a
// bencoded torrent file.
func (item FeedItem) TorrentMetadataURL() string {
	if value := strings.TrimSpace(item.TorrentURL); isHTTPURL(value) {
		return value
	}
	enclosure := strings.TrimSpace(item.Enclosure)
	if isHTTPURL(enclosure) && (isTorrentMIME(item.EnclosureType) || looksLikeTorrentURL(enclosure)) {
		return enclosure
	}
	return ""
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == ""
}

type ReleaseExclusion struct {
	ID             string
	SubscriptionID string
	ReleaseID      string
	Provider       string
	EvidenceHash   string
	Reason         string
	Scope          string
	ActorID        string
	PolicyVersion  string
	ExpiresAt      *time.Time
	State          string
	Version        int64
}

type SourceProvidedFact struct {
	Provider        string
	FieldName       string
	RawValue        string
	NormalizedValue map[string]any
	ObservedAt      time.Time
	SchemaVersion   string
}

type ReleaseFile struct {
	SourcePath       string
	Filename         string
	SizeBytes        *int64
	MediaKind        string
	SourceHash       string
	Facts            map[string]any
	TargetPath       *string
	TargetDiagnostic *TargetDiagnostic
}

type TargetDiagnostic struct {
	Code       string
	Message    string
	SourcePath string
}

type rssDocument struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	GUID        string         `xml:"guid"`
	Title       string         `xml:"title"`
	Link        string         `xml:"link"`
	Comments    string         `xml:"comments"`
	Description string         `xml:"description"`
	PubDate     string         `xml:"pubDate"`
	Enclosures  []rssEnclosure `xml:"enclosure"`
	Children    []rssElement   `xml:",any"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}

type atomDocument struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID      string     `xml:"id"`
	Title   string     `xml:"title"`
	Updated string     `xml:"updated"`
	Created string     `xml:"published"`
	Links   []atomLink `xml:"link"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

// rssElement deliberately keeps unknown namespace elements instead of
// binding to a provider prefix. Mikan uses torrent:pubDate while AniBT uses
// torrent:link/infohash/magneturi/filename; encoding/xml exposes both with
// the same local names regardless of the chosen prefix or namespace URI.
type rssElement struct {
	XMLName  xml.Name
	Text     string       `xml:",chardata"`
	Children []rssElement `xml:",any"`
}

type FeedFormat string

const (
	FeedFormatRSS  FeedFormat = "rss"
	FeedFormatAtom FeedFormat = "atom"
)

// FeedItemSkip keeps item-level incompatibilities visible without making a
// malformed item invalidate the rest of an otherwise parseable feed. Reason is
// one of the FeedSkip* codes.
type FeedItemSkip struct {
	Index  int
	GUID   string
	Title  string
	Reason string
}

// Item skip reasons. They are stable codes; the UI owns their wording.
const (
	FeedSkipTitleMissing = "title_missing"
	// FeedSkipDownloadURLMissing: no HTTP(S) .torrent download on the item.
	FeedSkipDownloadURLMissing = "download_url_missing"
	// FeedSkipMagnetOnly: the item's only download is a magnet link, which is
	// not supported. Decided by the URL scheme, never by the MIME type.
	FeedSkipMagnetOnly = "magnet_only"
	// FeedSkipSiteFormatMismatch: a listed site's item no longer has the
	// shape its adapter reads.
	FeedSkipSiteFormatMismatch = "site_format_mismatch"
)

type FeedParseResult struct {
	Format FeedFormat
	// Site is the declared adapter that parsed the feed; nil means the feed's
	// host is not on the list and the generic RSS/Atom adapter parsed it.
	Site     *FeedSite
	Items    []FeedItem
	Skipped  []FeedItemSkip
	Warnings []string
}

// ParseFeed is the compact generic API: ParseFeedDetailed plus the shared
// no-usable-item rule. A structurally valid empty feed is a successful
// zero-result; a feed whose every item was skipped is a *FeedFailure.
func ParseFeed(data []byte) ([]FeedItem, error) {
	result, err := ParseFeedDetailed(data)
	if err != nil {
		return nil, err
	}
	if err := RequireUsableItems(result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

// ParseFeedDetailed parses with the generic RSS 2.x/RSS 1.0/Atom adapter,
// the one used for hosts outside the declared list.
func ParseFeedDetailed(data []byte) (FeedParseResult, error) {
	return parseFeedWith(nil, data)
}

func parseFeedWith(adapter *feedAdapter, data []byte) (FeedParseResult, error) {
	if len(data) == 0 || len(data) > 10<<20 {
		return FeedParseResult{}, errors.New("RSS payload is empty or exceeds 10 MiB")
	}
	var root struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(data, &root); err != nil {
		return FeedParseResult{}, fmt.Errorf("parse feed XML: %w", err)
	}
	var site *FeedSite
	if adapter != nil {
		declared := adapter.site
		site = &declared
	}
	rootName := strings.ToLower(strings.TrimSpace(root.XMLName.Local))
	switch {
	case rootName == "rss" || rootName == "rdf":
		return parseRSSDocument(adapter, site, data)
	case rootName == "feed" && adapter == nil:
		return parseAtomDocument(data)
	case adapter != nil:
		// Every listed site publishes RSS. Any other root on a listed host is a
		// changed site, never a reason to try the generic adapter instead.
		return FeedParseResult{}, &FeedFailure{Code: FeedFailureSiteFormatChanged, Site: adapter.site.ID, Detail: fmt.Sprintf("root element %q", root.XMLName.Local)}
	default:
		return FeedParseResult{}, fmt.Errorf("unsupported feed root element %q", root.XMLName.Local)
	}
}

func parseRSSDocument(adapter *feedAdapter, site *FeedSite, data []byte) (FeedParseResult, error) {
	var rss rssDocument
	if err := xml.Unmarshal(data, &rss); err != nil {
		return FeedParseResult{}, fmt.Errorf("parse RSS XML: %w", err)
	}
	allItems := make([]rssItem, 0, len(rss.Channel.Items)+len(rss.Items))
	allItems = append(allItems, rss.Channel.Items...)
	allItems = append(allItems, rss.Items...)
	if adapter == nil {
		// No host adapter claimed the feed: a content signature may still
		// name its architecture (UNIT3D hosts vary per installation).
		if detected := detectContentAdapter(allItems); detected != nil {
			adapter = detected
			declared := detected.site
			site = &declared
		}
	}
	result := FeedParseResult{Format: FeedFormatRSS, Site: site, Items: make([]FeedItem, 0, len(allItems)), Skipped: make([]FeedItemSkip, 0)}
	for index, raw := range allItems {
		parsed, sawMagnet := normalizeRSSItem(raw)
		conforms := true
		if adapter != nil {
			conforms = adapter.adapt(raw, &parsed)
		}
		if reason := feedItemSkipReason(parsed, sawMagnet, conforms); reason != "" {
			result.Skipped = append(result.Skipped, FeedItemSkip{Index: index, GUID: parsed.GUID, Title: parsed.Title, Reason: reason})
			continue
		}
		result.Items = append(result.Items, parsed)
	}
	if len(result.Skipped) > 0 {
		result.Warnings = append(result.Warnings, "items_skipped")
	}
	if adapter != nil && len(result.Items) == 0 {
		for _, skip := range result.Skipped {
			if skip.Reason == FeedSkipSiteFormatMismatch {
				return FeedParseResult{}, &FeedFailure{Code: FeedFailureSiteFormatChanged, Site: adapter.site.ID, Total: len(allItems), Skipped: result.Skipped}
			}
		}
	}
	return result, nil
}

func parseAtomDocument(data []byte) (FeedParseResult, error) {
	var atom atomDocument
	if err := xml.Unmarshal(data, &atom); err != nil {
		return FeedParseResult{}, fmt.Errorf("parse Atom XML: %w", err)
	}
	result := FeedParseResult{Format: FeedFormatAtom, Items: make([]FeedItem, 0, len(atom.Entries)), Skipped: make([]FeedItemSkip, 0)}
	for index, entry := range atom.Entries {
		parsed, sawMagnet := normalizeAtomEntry(entry)
		if reason := feedItemSkipReason(parsed, sawMagnet, true); reason != "" {
			result.Skipped = append(result.Skipped, FeedItemSkip{Index: index, GUID: parsed.GUID, Title: parsed.Title, Reason: reason})
			continue
		}
		result.Items = append(result.Items, parsed)
	}
	if len(result.Skipped) > 0 {
		result.Warnings = append(result.Warnings, "items_skipped")
	}
	return result, nil
}

// feedItemSkipReason is the one acceptance rule every adapter shares: a title,
// an HTTP(S) .torrent download, and (for a listed site) the site's own shape.
// The identity is then always obtainable: from the feed when it publishes an
// infohash, otherwise from the fetched torrent at expansion time, so a missing
// feed infohash is never a skip.
func feedItemSkipReason(item FeedItem, sawMagnet, conforms bool) string {
	if strings.TrimSpace(item.Title) == "" {
		return FeedSkipTitleMissing
	}
	hasTorrent := isHTTPURL(item.TorrentURL)
	if !hasTorrent && sawMagnet {
		return FeedSkipMagnetOnly
	}
	if !conforms {
		return FeedSkipSiteFormatMismatch
	}
	if !hasTorrent {
		return FeedSkipDownloadURLMissing
	}
	return ""
}

func normalizeAtomEntry(entry atomEntry) (FeedItem, bool) {
	title := strings.TrimSpace(entry.Title)
	var selected atomLink
	selectedRank := -1
	var fallback string
	sawMagnet := false
	for _, candidate := range entry.Links {
		href := strings.TrimSpace(candidate.Href)
		if href == "" {
			continue
		}
		if isMagnetURL(href) {
			sawMagnet = true
			continue
		}
		rel := strings.ToLower(strings.TrimSpace(candidate.Rel))
		if fallback == "" && (rel == "" || rel == "alternate") {
			fallback = href
		}
		rank := atomLinkRank(candidate)
		if rank > selectedRank {
			selected = atomLink{Rel: candidate.Rel, Type: candidate.Type, Href: href}
			selectedRank = rank
		}
	}
	download := ""
	if selectedRank >= 80 {
		download = strings.TrimSpace(selected.Href)
	}
	link := fallback
	if link == "" {
		link = download
	}
	torrentURL := ""
	if isTorrentDownload(download, selected.Type) {
		torrentURL = download
	}
	// Atom entries carry no provider identity element, so a .torrent URL on the
	// selected enclosure is the only available candidate identity.
	infoHash := infoHashFromTorrentURL(torrentURL)
	guid := strings.TrimSpace(entry.ID)
	if guid == "" {
		guid = firstNonEmpty(download, link)
	}
	details := fallback
	if !isHTTPURL(details) || download == "" || details == download || isTorrentDownload(details, "") {
		details = ""
	}
	return FeedItem{
		GUID: guid, Title: title, Link: link, DetailsURL: details, Enclosure: download,
		EnclosureType: strings.TrimSpace(selected.Type), TorrentURL: torrentURL,
		TorrentInfoHash: infoHash, Published: parseTime(firstNonEmpty(entry.Updated, entry.Created)),
	}, sawMagnet
}

func atomLinkRank(link atomLink) int {
	href := strings.TrimSpace(link.Href)
	if href == "" || isMagnetURL(href) {
		return -1
	}
	rel := strings.ToLower(strings.TrimSpace(link.Rel))
	typeName := strings.ToLower(strings.TrimSpace(link.Type))
	torrent := isTorrentMIME(typeName) || looksLikeTorrentURL(href)
	if rel == "enclosure" && torrent {
		return 120
	}
	if torrent {
		return 80
	}
	if rel == "alternate" || rel == "" {
		return 10
	}
	return 1
}

// isTorrentDownload reports whether value is an HTTP(S) torrent download: a
// .torrent path, or an enclosure the feed typed as a torrent. A magnet URI is
// never one, whatever MIME type the feed claims for it.
func isTorrentDownload(value, mimeType string) bool {
	if !isHTTPURL(value) {
		return false
	}
	return looksLikeTorrentURL(value) || isTorrentMIME(mimeType)
}

// normalizeRSSItem is the generic RSS adapter. It reports whether the item
// offered a magnet link anywhere, which is only used to name the skip reason:
// magnets are never kept.
func normalizeRSSItem(item rssItem) (FeedItem, bool) {
	sawMagnet := false
	for _, enclosure := range item.Enclosures {
		if isMagnetURL(enclosure.URL) {
			sawMagnet = true
		}
	}
	enclosure := selectRSS(item.Enclosures)
	link := strings.TrimSpace(item.Link)
	if isMagnetURL(link) {
		sawMagnet = true
		link = ""
	}
	if link == "" {
		link = strings.TrimSpace(enclosure.URL)
	}
	length := parseNonNegativeInt64(enclosure.Length)
	torrent := collectTorrentFields(item.Children)
	sawMagnet = sawMagnet || torrent.magnet
	if torrent.url == "" && isTorrentDownload(enclosure.URL, enclosure.Type) {
		torrent.url = strings.TrimSpace(enclosure.URL)
	}
	if torrent.url == "" && isTorrentDownload(link, "") {
		torrent.url = link
	}
	// An XML infohash element wins; a URL contributes only when the feed
	// published no identity element of its own (Mikan's case).
	if torrent.infoHash == "" {
		torrent.infoHash = infoHashFromTorrentURL(torrent.url)
	}
	if torrent.size > 0 {
		length = torrent.size
	}
	// RSS link denotes the item page only when an independent torrent location
	// exists. Never reuse the normalizer's enclosure fallback as a page.
	details := strings.TrimSpace(item.Comments)
	if !isHTTPURL(details) {
		details = strings.TrimSpace(item.Link)
		if !isHTTPURL(details) || torrent.url == "" || details == torrent.url || isTorrentDownload(details, "") {
			details = ""
		}
	}
	guid := strings.TrimSpace(item.GUID)
	if guid == "" {
		guid = firstNonEmpty(strings.TrimSpace(enclosure.URL), strings.TrimSpace(torrent.url), link)
	}
	return FeedItem{
		GUID:             guid,
		Title:            strings.TrimSpace(item.Title),
		Link:             link,
		DetailsURL:       details,
		Description:      strings.TrimSpace(item.Description),
		Enclosure:        strings.TrimSpace(enclosure.URL),
		EnclosureType:    strings.TrimSpace(enclosure.Type),
		EnclosureLength:  length,
		TorrentURL:       torrent.url,
		TorrentInfoHash:  torrent.infoHash,
		TorrentFilename:  torrent.filename,
		TorrentSizeBytes: torrent.size,
		Published:        parseTime(firstNonEmpty(item.PubDate, torrent.pubDate)),
		Extensions:       collectExtensionFields(item.Children),
	}, sawMagnet
}

// selectRSS picks the best non-magnet enclosure. Magnet enclosures are never
// selected: they are not a download source here.
func selectRSS(items []rssEnclosure) rssEnclosure {
	var selected rssEnclosure
	selectedRank := -1
	for _, enclosure := range items {
		rank := rssEnclosureRank(enclosure)
		if rank > selectedRank {
			selected = enclosure
			selectedRank = rank
		}
	}
	return selected
}

func rssEnclosureRank(enclosure rssEnclosure) int {
	value := strings.TrimSpace(enclosure.URL)
	if value == "" || isMagnetURL(value) {
		return -1
	}
	if isTorrentMIME(enclosure.Type) {
		return 90
	}
	if looksLikeTorrentURL(value) {
		return 80
	}
	if strings.EqualFold(strings.TrimSpace(enclosure.Type), "application/octet-stream") {
		return 40
	}
	return 10
}

func isTorrentMIME(value string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0])) {
	case "application/x-bittorrent", "application/bittorrent", "application/x-torrent":
		return true
	default:
		return false
	}
}

func isMagnetURL(value string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.SplitN(value, ":", 2)[0]), "magnet")
}

func looksLikeTorrentURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isMagnetURL(value) {
		return false
	}
	if parsed, err := url.Parse(value); err == nil {
		return strings.HasSuffix(strings.ToLower(parsed.Path), ".torrent") || strings.Contains(strings.ToLower(parsed.RawQuery), ".torrent")
	}
	return strings.Contains(strings.ToLower(value), ".torrent")
}

// infoHashFromTorrentURL reads a strong identity off a .torrent URL's last path
// segment. Mikan publishes the infohash only there (its RSS has no infohash
// element), while AniBT uses the anibt:infohash element. This is a candidate
// identity only: the fetched bytes are re-verified against the parser's own
// hashes, so a URL can never attest its own content.
func infoHashFromTorrentURL(value string) string {
	if !looksLikeTorrentURL(value) {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	candidate := strings.TrimSuffix(strings.ToLower(path.Base(parsed.Path)), ".torrent")
	if !IsCanonicalInfoHash(candidate) {
		return ""
	}
	return candidate
}

// IsCanonicalInfoHash accepts the v1 (40 hex) and v2 (64 hex) lowercase forms
// every persisted infohash column accepts, so the RSS layer and the catalog
// layer share one rule instead of each deciding for itself.
//
// AniBT's anibt:infohash is deliberately NOT passed through it: that provider's
// tag is an opaque tag value, not this shape, and the snapshot's info_hash
// column CHECK would reject it.
func IsCanonicalInfoHash(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC822Z,
		time.RFC822,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05.999",
		"2006-01-02T15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

type torrentFields struct {
	pubDate  string
	url      string
	infoHash string
	// magnet records that the item declared a magnet link; the value itself
	// is discarded.
	magnet   bool
	filename string
	size     int64
}

func collectTorrentFields(elements []rssElement) torrentFields {
	var result torrentFields
	var visit func([]rssElement)
	visit = func(items []rssElement) {
		for _, element := range items {
			name := strings.ToLower(strings.TrimSpace(element.XMLName.Local))
			value := strings.TrimSpace(element.Text)
			switch name {
			case "pubdate":
				if result.pubDate == "" {
					result.pubDate = value
				}
			case "link", "torrentlink", "torrenturl":
				if isMagnetURL(value) {
					result.magnet = true
				} else if result.url == "" && isHTTPURL(value) && looksLikeTorrentURL(value) {
					result.url = value
				}
			case "infohash":
				if result.infoHash == "" {
					result.infoHash = value
				}
			case "magneturi", "magnet":
				if value != "" {
					result.magnet = true
				}
			case "filename", "name":
				if result.filename == "" {
					result.filename = value
				}
			case "contentlength", "length", "size", "sizebytes":
				if result.size == 0 {
					result.size = parseNonNegativeInt64(value)
				}
			}
			visit(element.Children)
		}
	}
	visit(elements)
	return result
}

func parseNonNegativeInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type EpisodeResult struct {
	EpisodeNumber int
	RawCapture    string
	Pattern       string
	Offset        int
	Confidence    string
	Evidence      string
}

// EpisodeResultExact is the exact episode parse result. It keeps a captured
// fraction (e.g. "12.5") untouched as evidence; the episodeidentity resolver
// refuses it. The legacy EpisodeResult truncates fractions to integers; new
// consumers must use the exact variant.
type EpisodeResultExact struct {
	EpisodeNumber episodeidentity.EpisodeNumber
	RawCapture    string
	Pattern       string
	Offset        int
	Confidence    string
	Evidence      string
}

// DefaultEpisodePattern is the primary seed pattern.
// It covers SxxExx, EP/第X话, bracket, and bare-number forms
// in a single regex. Tests and persisted parser results reference this
// constant as the canonical "default" pattern identity.
const DefaultEpisodePattern = `S[0-9]{1,2}\s*E0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?|(?:EP?|Episode|第)\s*0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?(?:\s*[话話集])?|[\[【(]\s*0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?\s*[\]】)]|(?:^|[\s._-])0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\s._\-\]】])`

// DashEpisodePattern prefers the dash-spaced episode in "<anime title> N - EE"
// titles ("[示例字幕组] 进击的巨人 ... 3 - 06"): the bare-number default
// pattern would capture the season ordinal "3" instead of 6. It is a
// DEFAULT pattern-list member (first), not a code pre-check; users can configure their own patterns and drop it.
const DashEpisodePattern = `\s-\s*0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\s._\-\]】])`

// DefaultEpisodePatterns is the ordered fallback list used when no parser
// patterns are configured. It tries four patterns in order. Ranges and
// lists such as "09~16" are classified by the parser before any pattern runs
// and are never read as their first number.
var DefaultEpisodePatterns = []string{
	// Dash-spaced: "<title> N - EE" — the number after a spaced dash is the
	// episode, not the season ordinal "N".
	DashEpisodePattern,
	// SxxExx form.
	`S[0-9]{1,2}\s*E0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?`,
	// EP / Episode / 第X话 form.
	`(?:EP?|Episode|第)\s*0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?(?:\s*[话話集])?`,
	// Bracket form: [01] or （01） or 【01】.
	`[\[【(]\s*0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?\s*[\]】)]`,
	// Bare number: " - 01 " or "_01_" or " 01$".
	`(?:^|[\s._-])0*([0-9]{1,3}(?:\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\s._\-\]】)])`,
}

func ParseEpisode(title, pattern string, offset int) (EpisodeResult, error) {
	if strings.TrimSpace(title) == "" {
		return EpisodeResult{}, errors.New("episode title is required")
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		// The default episode resolution is the ordered pattern list (the
		// list carries the dash-spaced member and the batch range). Keep
		// DefaultEpisodePattern for explicit callers that want the single
		// legacy pattern.
		var lastErr error
		for _, candidate := range DefaultEpisodePatterns {
			result, err := ParseEpisode(title, candidate, offset)
			if err == nil {
				return result, nil
			}
			lastErr = err
		}
		return EpisodeResult{}, lastErr
	}
	result, err := parseEpisodeWithPattern(title, pattern, offset)
	if err != nil {
		return EpisodeResult{}, err
	}
	// Legacy compatibility: the historical result truncates decimal episodes
	// to integers. The exact path is ParseEpisodeExact.
	integerPart, _, _ := strings.Cut(string(result.EpisodeNumber), ".")
	episode, _ := strconv.Atoi(integerPart)
	return EpisodeResult{
		EpisodeNumber: episode,
		RawCapture:    result.RawCapture,
		Pattern:       result.Pattern,
		Offset:        result.Offset,
		Confidence:    result.Confidence,
		Evidence:      result.Evidence,
	}, nil
}

// ParseEpisodeExact resolves an episode exactly like ParseEpisode but keeps a
// captured fraction (e.g. "12.5") untouched instead of truncating it; an
// offset applies to integers only. It is the evidence
// producer for the episodeidentity resolver path; parser results never apply
// an offset for downstream resolution — callers pass offset 0 and let the
// resolver apply the effective offset exactly once.
func ParseEpisodeExact(title, pattern string, offset int) (EpisodeResultExact, error) {
	if strings.TrimSpace(title) == "" {
		return EpisodeResultExact{}, errors.New("episode title is required")
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		var lastErr error
		for _, candidate := range DefaultEpisodePatterns {
			result, err := ParseEpisodeExact(title, candidate, offset)
			if err == nil {
				return result, nil
			}
			lastErr = err
		}
		return EpisodeResultExact{}, lastErr
	}
	return parseEpisodeWithPattern(title, pattern, offset)
}

// parseEpisodeWithPattern compiles one pattern, extracts the first non-empty
// numeric capture, and finalizes it. It is the shared engine for ParseEpisode
// (legacy integer truncation) and ParseEpisodeExact (exact capture).
func parseEpisodeWithPattern(title, pattern string, offset int) (EpisodeResultExact, error) {
	if len(pattern) > 512 || offset < -1000 || offset > 1000 {
		return EpisodeResultExact{}, errors.New("episode pattern or offset is outside the allowed bounds")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return EpisodeResultExact{}, fmt.Errorf("compile episode pattern: %w", err)
	}
	matches := re.FindStringSubmatch(title)
	if len(matches) < 2 {
		return EpisodeResultExact{}, errors.New("episode pattern must capture a numeric episode")
	}
	// With alternation patterns (e.g. DefaultEpisodePattern), only one branch
	// captures; the others are empty. Pick the first non-empty capture group.
	value := ""
	for _, group := range matches[1:] {
		if strings.TrimSpace(group) != "" {
			value = strings.TrimSpace(group)
			break
		}
	}
	if value == "" {
		return EpisodeResultExact{}, errors.New("episode pattern matched but no capture group was populated")
	}
	return finalizeEpisodeExact(title, value, pattern, offset)
}

// finalizeEpisodeExact normalizes the raw capture (version suffix strip,
// canonicalization, offset) into the episode result. A captured fraction
// survives untouched and takes no offset.
func finalizeEpisodeExact(title, value, pattern string, offset int) (EpisodeResultExact, error) {
	// Strip version suffix (e.g. "01v2" -> "01").
	if idx := strings.Index(strings.ToLower(value), "v"); idx > 0 {
		value = value[:idx]
	}
	canonical, err := episodeidentity.CanonicalEpisode(value)
	if err != nil {
		return EpisodeResultExact{}, errors.New("episode capture is not a bounded number")
	}
	episode, err := canonical.AddOffset(offset)
	if err != nil {
		return EpisodeResultExact{}, errors.New("episode after offset is outside the allowed bounds")
	}
	return EpisodeResultExact{EpisodeNumber: episode, RawCapture: value, Pattern: pattern, Offset: offset, Confidence: "certain", Evidence: title}, nil
}
