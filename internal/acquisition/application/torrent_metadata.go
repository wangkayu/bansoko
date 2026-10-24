package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/redact"
)

// TorrentPayload is one bounded, bencode-validated torrent input. Payload is
// intentionally in-process only until RSS persists it in its dedicated blob
// relation; it must never be encoded in ordinary release JSON facts.
type TorrentPayload struct {
	Bytes    []byte
	Metadata torrent.Metadata
}

// TorrentMetadataReader is shared by Mikan, AniBT and custom RSS adapters.
// Providers only supply a source URL; the bencode parser remains provider
// independent and returns immutable package/file facts.
type TorrentMetadataReader interface {
	Fetch(context.Context, string) (torrent.Metadata, error)
}

// TorrentPayloadReader lets callers retain the same verified bytes that were
// parsed into metadata, so no later qB submission has to re-fetch a source URL.
type TorrentPayloadReader interface {
	FetchPayload(context.Context, string) (TorrentPayload, error)
}

// HTTPTorrentMetadataReader is an optional network adapter. It uses the same
// SSRF-safe transport rules as RSS and is intentionally opt-in so fixture
// feeds do not trigger an unexpected second HTTP request.
type HTTPTorrentMetadataReader struct {
	Client            *http.Client
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
	MaxSize           int64
}

func (f HTTPTorrentMetadataReader) Fetch(ctx context.Context, sourceURL string) (torrent.Metadata, error) {
	result, err := f.FetchPayload(ctx, sourceURL)
	if err != nil {
		return torrent.Metadata{}, err
	}
	return result.Metadata, nil
}

// The fetch outcomes are typed so a caller that must report a state instead of
// an error (lazy seed expansion) can tell a missing torrent from a transport
// failure from a payload the parser refused, without matching message text.
var (
	// ErrTorrentPayloadMissing means the source answered 404.
	ErrTorrentPayloadMissing = errors.New("torrent source is missing")
	// ErrTorrentPayloadFetch means the locator could not be reached or was
	// refused before a body was read.
	ErrTorrentPayloadFetch = errors.New("torrent source is unreachable")
	// ErrTorrentPayloadInvalid means a body was read but is not an acceptable
	// torrent: corrupt bencode, trailing bytes, or over the size limit.
	ErrTorrentPayloadInvalid = errors.New("torrent payload is not a valid torrent")
)

// FetchPayload performs one bounded, SSRF-safe HTTP request, parses that exact
// bencoded body, and returns both facts and bytes for release-owned persistence.
func (f HTTPTorrentMetadataReader) FetchPayload(ctx context.Context, sourceURL string) (TorrentPayload, error) {
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return TorrentPayload{}, fmt.Errorf("%w: torrent URL must be an absolute HTTP URL without credentials", ErrTorrentPayloadFetch)
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	copyClient := *client
	transport, err := (HTTPRSSFetcher{Client: &copyClient, Resolver: f.Resolver, AllowPrivateHosts: f.AllowPrivateHosts}).secureTransport(copyClient.Transport)
	if err != nil {
		return TorrentPayload{}, fmt.Errorf("%w: %v", ErrTorrentPayloadFetch, err)
	}
	copyClient.Transport = transport
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("torrent source exceeded redirect limit")
		}
		redirect := request.URL
		if (redirect.Scheme != "http" && redirect.Scheme != "https") || redirect.Hostname() == "" || redirect.User != nil || redirect.Fragment != "" {
			return errors.New("torrent redirect rejected")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return TorrentPayload{}, fmt.Errorf("%w: create torrent request: %v", ErrTorrentPayloadFetch, redact.URLError(err))
	}
	request.Header.Set("Accept", "application/x-bittorrent, application/octet-stream, */*;q=0.1")
	request.Header.Set("User-Agent", browserDisguiseUserAgent)
	response, err := copyClient.Do(request)
	if err != nil {
		return TorrentPayload{}, fmt.Errorf("%w: fetch torrent metadata: %v", ErrTorrentPayloadFetch, redact.URLError(err))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusNotFound {
			return TorrentPayload{}, fmt.Errorf("%w: torrent source returned HTTP %d", ErrTorrentPayloadMissing, response.StatusCode)
		}
		return TorrentPayload{}, fmt.Errorf("%w: torrent source returned HTTP %d", ErrTorrentPayloadFetch, response.StatusCode)
	}
	maxSize := f.MaxSize
	if maxSize <= 0 {
		maxSize = 20 << 20
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return TorrentPayload{}, fmt.Errorf("%w: read torrent metadata: %v", ErrTorrentPayloadFetch, err)
	}
	if int64(len(payload)) > maxSize {
		return TorrentPayload{}, fmt.Errorf("%w: torrent metadata exceeds the configured size limit", ErrTorrentPayloadInvalid)
	}
	metadata, err := torrent.Parse(payload)
	if err != nil {
		return TorrentPayload{}, fmt.Errorf("%w: %v", ErrTorrentPayloadInvalid, err)
	}
	return TorrentPayload{Bytes: append([]byte(nil), payload...), Metadata: metadata}, nil
}

func enrichFeedItemsWithTorrentMetadata(ctx context.Context, items []domain.FeedItem, reader TorrentMetadataReader) ([]domain.FeedItem, error) {
	if reader == nil {
		return items, nil
	}
	payloadReader, hasPayloadReader := reader.(TorrentPayloadReader)
	// One refresh attempt may list several releases that share the same torrent
	// locator (quality variants of one package, for example). The exact same
	// bytes back every one of them, so fetch each unique locator at most once
	// and remember the failure just as well — a repeated dead locator must not
	// cost another bounded request. The cache is local to this call, which is
	// the group attempt boundary.
	type attempt struct {
		payload TorrentPayload
		err     error
	}
	attempts := make(map[string]attempt)
	for index := range items {
		item := &items[index]

		metadataURL := item.TorrentMetadataURL()
		if metadataURL == "" {
			// Magnet URIs and release-page links are left as source facts;
			// qBittorrent resolves them at submission time.
			continue
		}
		if reuseTorrentMetadata(ctx, item) {
			// The consumer already holds verified metadata for this item
			// (or the item is admitted and immutable): no second download.
			continue
		}

		cached, seen := attempts[metadataURL]
		if !seen {
			if hasPayloadReader {
				result, err := payloadReader.FetchPayload(ctx, metadataURL)
				cached = attempt{payload: result, err: err}
			} else {
				metadata, err := reader.Fetch(ctx, metadataURL)
				cached = attempt{payload: TorrentPayload{Metadata: metadata}, err: err}
			}
			attempts[metadataURL] = cached
		}
		if cached.err != nil {
			markTorrentItemError(item, cached.err)
			continue
		}
		applyTorrentPayload(item, cached.payload, true)
	}
	return items, nil
}

func markTorrentItemError(item *domain.FeedItem, cause error) {
	item.TorrentErrorCode = "torrent_metadata_unavailable"
	// The reason is shown in the RSS list and stored with the seed: a torrent
	// URL inside it never keeps its key.
	item.TorrentErrorReason = redact.URLsInText(strings.TrimSpace(cause.Error()))
	if len(item.TorrentErrorReason) > 512 {
		item.TorrentErrorReason = strings.ToValidUTF8(item.TorrentErrorReason[:512], "")
	}
	if item.TorrentErrorReason == "" {
		item.TorrentErrorReason = "torrent metadata could not be fetched or parsed"
	}
}

// ApplyTorrentMetadata writes parsed torrent metadata onto a feed item exactly
// as a fetch does: the torrent identity, the authoritative total size and the
// per-file manifest. The payload bytes stay with their owner.
func ApplyTorrentMetadata(item *domain.FeedItem, metadata torrent.Metadata) {
	applyTorrentPayload(item, TorrentPayload{Metadata: metadata}, false)
}

// ApplyTorrentPayload writes a fetched payload onto a feed item exactly as a
// feed refresh does, keeping the verified bytes for torrent input storage.
func ApplyTorrentPayload(item *domain.FeedItem, payload TorrentPayload) {
	applyTorrentPayload(item, payload, true)
}

func applyTorrentPayload(item *domain.FeedItem, payload TorrentPayload, ownBytes bool) {
	metadata := payload.Metadata
	item.TorrentInfoHashV1 = metadata.InfoHashV1
	item.TorrentInfoHashV2 = metadata.InfoHashV2
	item.TorrentVersion = metadata.TorrentVersion
	if item.TorrentInfoHash == "" {
		if metadata.InfoHashV1 != "" {
			item.TorrentInfoHash = metadata.InfoHashV1
		} else {
			item.TorrentInfoHash = metadata.InfoHashV2
		}
	}
	if item.TorrentFilename == "" {
		item.TorrentFilename = metadata.TorrentName
	}
	// The fetched payload is authoritative: feeds routinely declare slightly
	// off sizes (e.g. mikan contentLength), so the parsed total wins even when
	// the feed supplied one.
	item.TorrentSizeBytes = metadata.TotalSize
	if item.EnclosureLength == 0 {
		item.EnclosureLength = metadata.TotalSize
	}
	if ownBytes {
		item.TorrentPayload = append([]byte(nil), payload.Bytes...)
	}
	item.ReleaseFiles = make([]domain.ReleaseFile, 0, len(metadata.Files))
	for _, file := range metadata.Files {
		fileFacts := map[string]any{
			"torrent_info_hash_v1": metadata.InfoHashV1,
			"torrent_info_hash_v2": metadata.InfoHashV2,
			"torrent_name":         metadata.TorrentName,
			"torrent_version":      metadata.TorrentVersion,
		}
		if file.Language != "" {
			fileFacts["language"] = file.Language
		}
		sourceHash := metadata.InfoHashV1
		if sourceHash == "" {
			sourceHash = metadata.InfoHashV2
		}
		item.ReleaseFiles = append(item.ReleaseFiles, domain.ReleaseFile{
			SourcePath: file.Path,
			Filename:   file.Name,
			SizeBytes:  valuePtr(file.SizeBytes),
			MediaKind:  file.MediaKind,
			SourceHash: sourceHash,
			Facts:      fileFacts,
		})
	}
}

func valuePtr(value int64) *int64 {
	return &value
}
