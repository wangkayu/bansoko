package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/redact"
)

func encodeFeedFacts(item acquisitiondomain.FeedItem) ([]byte, error) {
	return EncodeFeedFacts(item)
}

// EncodeFeedFacts serializes the public raw facts for one feed item. Exported
// so the one-shot path reuses the exact same shape as RSS refresh.
func EncodeFeedFacts(item acquisitiondomain.FeedItem) ([]byte, error) {
	facts := animedomain.RawReleaseFacts{Title: item.Title}
	if !item.Published.IsZero() {
		published := item.Published.UTC()
		facts.PublishedAt = &published
	}
	if item.EnclosureType != "" {
		enclosureType := item.EnclosureType
		facts.EnclosureType = &enclosureType
	}
	if item.EnclosureLength > 0 {
		enclosureLength := item.EnclosureLength
		facts.EnclosureLength = &enclosureLength
	}
	return json.Marshal(facts)
}

// encodeInternalFeedFacts keeps the opaque acquisition locator available to the
// execution handler without placing it in the public Release raw_facts field.
func encodeInternalFeedFacts(item acquisitiondomain.FeedItem) ([]byte, error) {
	return EncodeInternalFeedFacts(item)
}

// EncodeInternalFeedFacts serializes the internal acquisition facts for one
// feed item. Exported so the one-shot path reuses the same shape.
func EncodeInternalFeedFacts(item acquisitiondomain.FeedItem) ([]byte, error) {
	published := item.Published.UTC()
	var publishedPtr *time.Time
	if !item.Published.IsZero() {
		publishedPtr = &published
	}
	return json.Marshal(storedFeedFacts{
		GUID:               item.GUID,
		Title:              item.Title,
		Link:               item.Link,
		Description:        item.Description,
		Enclosure:          item.Enclosure,
		EnclosureType:      item.EnclosureType,
		EnclosureLength:    item.EnclosureLength,
		TorrentURL:         item.TorrentURL,
		TorrentInfoHash:    item.TorrentInfoHash,
		TorrentInfoHashV1:  item.TorrentInfoHashV1,
		TorrentInfoHashV2:  item.TorrentInfoHashV2,
		TorrentVersion:     item.TorrentVersion,
		TorrentFilename:    item.TorrentFilename,
		TorrentSizeBytes:   item.TorrentSizeBytes,
		TorrentErrorCode:   item.TorrentErrorCode,
		TorrentErrorReason: safeReason(item.TorrentErrorReason),
		DownloadURL:        item.DownloadURL(),
		Published:          publishedPtr,
		Provider:           item.Provider,
		ReleaseFiles:       encodeStoredReleaseFiles(item.ReleaseFiles),
	})
}

func encodeStoredReleaseFiles(files []acquisitiondomain.ReleaseFile) []storedReleaseFile {
	if len(files) == 0 {
		return nil
	}
	result := make([]storedReleaseFile, 0, len(files))
	for _, f := range files {
		result = append(result, storedReleaseFile{
			SourcePath:       f.SourcePath,
			Filename:         f.Filename,
			SizeBytes:        f.SizeBytes,
			MediaKind:        f.MediaKind,
			SourceHash:       f.SourceHash,
			Facts:            f.Facts,
			TargetPath:       f.TargetPath,
			TargetDiagnostic: f.TargetDiagnostic,
		})
	}
	return result
}

func decodeStoredReleaseFiles(files []storedReleaseFile) []acquisitiondomain.ReleaseFile {
	if len(files) == 0 {
		return nil
	}
	result := make([]acquisitiondomain.ReleaseFile, 0, len(files))
	for _, f := range files {
		result = append(result, acquisitiondomain.ReleaseFile{
			SourcePath:       f.SourcePath,
			Filename:         f.Filename,
			SizeBytes:        f.SizeBytes,
			MediaKind:        f.MediaKind,
			SourceHash:       f.SourceHash,
			Facts:            f.Facts,
			TargetPath:       f.TargetPath,
			TargetDiagnostic: f.TargetDiagnostic,
		})
	}
	return result
}

// verifiedTorrentMetadata is the complete internally verified group that may be
// reused when a transient enrichment failure removes the current file facts.
type verifiedTorrentMetadata struct {
	TorrentInfoHash   string
	TorrentInfoHashV1 string
	TorrentInfoHashV2 string
	TorrentVersion    string
	TorrentFilename   string
	TorrentSizeBytes  int64
	ReleaseFiles      []acquisitiondomain.ReleaseFile
}

func (facts storedFeedFacts) verifiedTorrentMetadata() (verifiedTorrentMetadata, bool) {
	if len(facts.ReleaseFiles) == 0 || strings.TrimSpace(facts.TorrentInfoHash) == "" || strings.TrimSpace(facts.TorrentFilename) == "" || facts.TorrentSizeBytes <= 0 {
		return verifiedTorrentMetadata{}, false
	}
	switch facts.TorrentVersion {
	case "v1":
		if strings.TrimSpace(facts.TorrentInfoHashV1) == "" || facts.TorrentInfoHashV2 != "" || facts.TorrentInfoHash != facts.TorrentInfoHashV1 {
			return verifiedTorrentMetadata{}, false
		}
	case "v2":
		if facts.TorrentInfoHashV1 != "" || strings.TrimSpace(facts.TorrentInfoHashV2) == "" || facts.TorrentInfoHash != facts.TorrentInfoHashV2 {
			return verifiedTorrentMetadata{}, false
		}
	case "hybrid":
		if strings.TrimSpace(facts.TorrentInfoHashV1) == "" || strings.TrimSpace(facts.TorrentInfoHashV2) == "" || facts.TorrentInfoHash != facts.TorrentInfoHashV1 {
			return verifiedTorrentMetadata{}, false
		}
	default:
		return verifiedTorrentMetadata{}, false
	}
	for _, file := range facts.ReleaseFiles {
		if strings.TrimSpace(file.SourcePath) == "" || strings.TrimSpace(file.Filename) == "" || file.SizeBytes == nil || *file.SizeBytes < 0 {
			return verifiedTorrentMetadata{}, false
		}
	}
	return verifiedTorrentMetadata{
		TorrentInfoHash:   facts.TorrentInfoHash,
		TorrentInfoHashV1: facts.TorrentInfoHashV1,
		TorrentInfoHashV2: facts.TorrentInfoHashV2,
		TorrentVersion:    facts.TorrentVersion,
		TorrentFilename:   facts.TorrentFilename,
		TorrentSizeBytes:  facts.TorrentSizeBytes,
		ReleaseFiles:      decodeStoredReleaseFiles(facts.ReleaseFiles),
	}, true
}

func torrentMetadataMatchesStoredFacts(item acquisitiondomain.FeedItem, input releaseTorrentInputRef) bool {
	metadata, ok := (storedFeedFacts{
		TorrentInfoHash:   item.TorrentInfoHash,
		TorrentInfoHashV1: item.TorrentInfoHashV1,
		TorrentInfoHashV2: item.TorrentInfoHashV2,
		TorrentVersion:    item.TorrentVersion,
		TorrentFilename:   item.TorrentFilename,
		TorrentSizeBytes:  item.TorrentSizeBytes,
		ReleaseFiles:      encodeStoredReleaseFiles(item.ReleaseFiles),
	}).verifiedTorrentMetadata()
	return ok && torrentMetadataMatchesInput(metadata, input) && !torrentIdentityConflicts(item, input)
}

func reuseVerifiedTorrentMetadata(oldInternalFacts []byte, current acquisitiondomain.FeedItem, input releaseTorrentInputRef) (acquisitiondomain.FeedItem, bool) {
	if current.TorrentErrorCode != "torrent_metadata_unavailable" || len(current.ReleaseFiles) != 0 || !isCompleteTorrentInputRefForReuse(input) {
		return current, false
	}
	var oldFacts storedFeedFacts
	if len(oldInternalFacts) == 0 || string(oldInternalFacts) == "null" || json.Unmarshal(oldInternalFacts, &oldFacts) != nil {
		return current, false
	}
	metadata, complete := oldFacts.verifiedTorrentMetadata()
	if !complete || !torrentMetadataMatchesInput(metadata, input) || torrentIdentityConflicts(current, input) {
		return current, false
	}
	current.TorrentInfoHash = metadata.TorrentInfoHash
	current.TorrentInfoHashV1 = metadata.TorrentInfoHashV1
	current.TorrentInfoHashV2 = metadata.TorrentInfoHashV2
	current.TorrentVersion = metadata.TorrentVersion
	current.TorrentFilename = metadata.TorrentFilename
	current.TorrentSizeBytes = metadata.TorrentSizeBytes
	current.ReleaseFiles = metadata.ReleaseFiles
	return current, true
}

func torrentIdentityConflicts(item acquisitiondomain.FeedItem, input releaseTorrentInputRef) bool {
	if value := strings.TrimSpace(item.TorrentVersion); value != "" && value != input.TorrentVersion {
		return true
	}
	if value := strings.TrimSpace(item.TorrentInfoHashV1); value != "" && value != input.InfoHashV1 {
		return true
	}
	if value := strings.TrimSpace(item.TorrentInfoHashV2); value != "" && value != input.InfoHashV2 {
		return true
	}
	preferredHash := input.InfoHashV1
	if preferredHash == "" {
		preferredHash = input.InfoHashV2
	}
	if value := strings.TrimSpace(item.TorrentInfoHash); value != "" && value != preferredHash {
		return true
	}
	if strings.TrimSpace(item.TorrentFilename) != "" {
		filename, err := torrentInputFilename(item.TorrentFilename, item.TorrentInfoHash)
		if err != nil || filename != input.Filename {
			return true
		}
	}
	sourceURL := item.TorrentMetadataURL()
	return sourceURL == "" || !torrentSourceMatches(sourceURL, input.SourceURLDigest)
}

// torrentSourceDigest is the stored torrent source identity: the digest of the
// URL with its credentials redacted (redact.URL). A private tracker's
// download URL carries the user's passkey or rsskey; rotating it changes no
// torrent, so it must not change the identity. The site's torrent id (UNIT3D
// /torrents/download/<id>.<key>, NexusPHP download.php?id=N) stays in the
// redacted form, so a different torrent still differs.
func torrentSourceDigest(sourceURL string) string {
	return stableDigest(redact.URL(strings.TrimSpace(sourceURL)))
}

// torrentSourceMatches compares a current torrent URL with a stored source
// identity. A row stored before credential-free identities holds the digest
// of the raw URL, which is still accepted.
func torrentSourceMatches(sourceURL, storedDigest string) bool {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" || storedDigest == "" {
		return false
	}
	return storedDigest == torrentSourceDigest(sourceURL) || storedDigest == stableDigest(sourceURL)
}

func encodeSourceFacts(item acquisitiondomain.FeedItem, groupName string) (kind, value string, encoded []byte, err error) {
	return EncodeSourceFacts(item, groupName)
}

// EncodeSourceFacts serializes the public source facts and returns the stable
// item key kind/value. Exported so the one-shot path reuses the same shape.
func EncodeSourceFacts(item acquisitiondomain.FeedItem, groupName string) (kind, value string, encoded []byte, err error) {
	kind, value = publicStableItemKey(item)
	encoded, err = json.Marshal(storedSourceFacts{
		GroupName:     strings.TrimSpace(groupName),
		IdentityKind:  kind,
		IdentityValue: value,
		AniBT:         storedAniBTTagsFromItem(item),
		Title:         storedTitleTagsFromItem(item),
	})
	return kind, value, encoded, err
}

func storedAniBTTagsFromItem(item acquisitiondomain.FeedItem) *StoredAniBTTags {
	return acquisitiondomain.StoredAniBTTagsFromSet(item.AniBTTags)
}

// storedTitleTagsFromItem freezes the title-parse tags: a set already read
// back from the Release (or frozen on a built item) is kept verbatim,
// otherwise the pinned parser derives it from the item title.
func storedTitleTagsFromItem(item acquisitiondomain.FeedItem) *StoredTitleTags {
	return acquisitiondomain.StoredTitleTagsFromSet(*FreezeTitleTags(item).TitleTags)
}

// FreezeTitleTags returns item with its title-parse tag set frozen: a set the
// item already carries is kept, otherwise the pinned parser reads the item
// title once. Paths that plan targets from an item before it is persisted
// (one-shot) call it when the item is built, so the plan
// and the persisted source facts carry the same set.
func FreezeTitleTags(item acquisitiondomain.FeedItem) acquisitiondomain.FeedItem {
	if item.TitleTags == nil {
		derived := reltitletags.Extract(item.Title)
		item.TitleTags = &derived
	}
	return item
}

// TitleTagSetFromStored rebuilds the frozen title tag set; nil stays nil.
func TitleTagSetFromStored(stored *StoredTitleTags) *acquisitiondomain.TitleTagSet {
	return acquisitiondomain.TitleTagSetFromStored(stored)
}

// AniBTTagSetFromStored rebuilds the frozen domain tag set; nil stays nil.
func AniBTTagSetFromStored(stored *StoredAniBTTags) *acquisitiondomain.AniBTTagSet {
	return acquisitiondomain.AniBTTagSetFromStored(stored)
}

// publicStableItemKey preserves human-provided GUIDs while digesting URL-based
// fallback identities. This keeps Release/source facts useful for dedupe without
// exposing a passkey-bearing enclosure or link in a public owner field.
func publicStableItemKey(item acquisitiondomain.FeedItem) (kind, value string) {
	return animedomain.PublicStableItemKey(item)
}

func encodeParsedFacts(result animedomain.PreviewReleaseResult) ([]byte, error) {
	return EncodeParsedFacts(result)
}

// EncodeParsedFacts serializes the parsed facts from an evaluation result.
// Exported so the one-shot path reuses the same shape.
func EncodeParsedFacts(result animedomain.PreviewReleaseResult) ([]byte, error) {
	if result.ParsedFacts == nil {
		return nil, nil
	}
	return json.Marshal(result.ParsedFacts)
}

func decodeFeedItem(rawFacts, sourceFacts, parsedFacts []byte, stableKey string) (acquisitiondomain.FeedItem, error) {
	return decodeFeedItemWithInternal(rawFacts, nil, sourceFacts, parsedFacts, stableKey)
}

// DecodeStoredFeedItem rebuilds the evaluation input for one persisted Release
// from its stored facts. Internal facts win when present; public raw/source
// facts are the fallback. It performs no I/O and is shared with the
// side-effect-free rules preview replay.
func DecodeStoredFeedItem(rawFacts, internalFacts, sourceFacts, parsedFacts []byte, stableKey string) (acquisitiondomain.FeedItem, error) {
	return decodeFeedItemWithInternal(rawFacts, internalFacts, sourceFacts, parsedFacts, stableKey)
}

func decodeFeedItemWithInternal(rawFacts, internalFacts, sourceFacts, parsedFacts []byte, stableKey string) (acquisitiondomain.FeedItem, error) {
	var stored storedFeedFacts
	facts := internalFacts
	if len(facts) == 0 || string(facts) == "null" {
		facts = rawFacts
	}
	if len(facts) > 0 && string(facts) != "null" {
		if err := json.Unmarshal(facts, &stored); err != nil {
			return acquisitiondomain.FeedItem{}, fmt.Errorf("decode stored feed facts: %w", err)
		}
	}
	item := acquisitiondomain.FeedItem{
		GUID:               stored.GUID,
		Title:              stored.Title,
		Link:               stored.Link,
		Description:        stored.Description,
		Enclosure:          stored.Enclosure,
		EnclosureType:      stored.EnclosureType,
		EnclosureLength:    stored.EnclosureLength,
		TorrentURL:         stored.TorrentURL,
		TorrentInfoHash:    stored.TorrentInfoHash,
		TorrentInfoHashV1:  stored.TorrentInfoHashV1,
		TorrentInfoHashV2:  stored.TorrentInfoHashV2,
		TorrentVersion:     stored.TorrentVersion,
		TorrentFilename:    stored.TorrentFilename,
		TorrentSizeBytes:   stored.TorrentSizeBytes,
		TorrentErrorCode:   stored.TorrentErrorCode,
		TorrentErrorReason: stored.TorrentErrorReason,
		Provider:           stored.Provider,
		ReleaseFiles:       decodeStoredReleaseFiles(stored.ReleaseFiles),
	}
	if stored.Published != nil {
		item.Published = stored.Published.UTC()
	}
	if item.DownloadURL() == "" && strings.TrimSpace(stored.DownloadURL) != "" {
		setDownloadURL(&item, stored.DownloadURL)
	}

	var source storedSourceFacts
	if len(sourceFacts) > 0 && string(sourceFacts) != "null" {
		if err := json.Unmarshal(sourceFacts, &source); err != nil {
			return acquisitiondomain.FeedItem{}, fmt.Errorf("decode stored source facts: %w", err)
		}
	}
	item.AniBTTags = AniBTTagSetFromStored(source.AniBT)
	item.TitleTags = TitleTagSetFromStored(source.Title)
	if !strings.HasPrefix(source.IdentityValue, "sha256:") {
		if item.GUID == "" && source.IdentityKind == "guid" {
			item.GUID = source.IdentityValue
		}
		if item.Enclosure == "" && source.IdentityKind == "enclosure" {
			item.Enclosure = source.IdentityValue
		}
		if item.Link == "" && source.IdentityKind == "link" {
			item.Link = source.IdentityValue
		}
	}
	if item.Title == "" {
		var fallback animedomain.RawReleaseFacts
		if len(rawFacts) > 0 {
			_ = json.Unmarshal(rawFacts, &fallback)
			item.Title = fallback.Title
			if fallback.PublishedAt != nil {
				item.Published = fallback.PublishedAt.UTC()
			}
			if item.EnclosureType == "" && fallback.EnclosureType != nil {
				item.EnclosureType = *fallback.EnclosureType
			}
			if item.EnclosureLength == 0 && fallback.EnclosureLength != nil {
				item.EnclosureLength = *fallback.EnclosureLength
			}
		}
	}
	if len(parsedFacts) > 0 && string(parsedFacts) != "null" {
		var parsed animedomain.ParsedReleaseFacts
		if err := json.Unmarshal(parsedFacts, &parsed); err != nil {
			return acquisitiondomain.FeedItem{}, fmt.Errorf("decode stored parsed facts: %w", err)
		}
		if item.Provider == "" && parsed.GroupName != nil {
			item.Provider = "stored"
		}
	}
	if item.GUID == "" && item.Enclosure == "" && item.Link == "" && stableKey != "" {
		// A legacy content-hash row has no dedicated content-hash field in the
		// FeedItem model. Keeping it as a GUID preserves a deterministic item
		// identity while evaluation uses the stored release key below.
		item.GUID = stableKey
	}
	if strings.TrimSpace(item.Title) == "" {
		return acquisitiondomain.FeedItem{}, errors.New("stored release has no feed title")
	}
	// A missing URL is tolerated through evaluation so filtered, stopped, and
	// metadata-held releases remain explainable. Accepted releases fail at the
	// task boundary because qB requires a concrete source effect.
	return item, nil
}

func setDownloadURL(item *acquisitiondomain.FeedItem, value string) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "magnet:") {
		// Magnets are not a download source: a stored one restores nothing,
		// so the release fails at the task boundary like any URL-less item.
		return
	}
	if parsed, err := url.Parse(value); err == nil && strings.HasSuffix(strings.ToLower(parsed.Path), ".torrent") {
		item.TorrentURL = value
		return
	}
	item.Link = value
}

func stableDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func digestStableKeys(keys []string) string {
	values := append([]string(nil), keys...)
	sort.Strings(values)
	return stableDigest(strings.Join(values, "\x00"))
}

// safeReason bounds operator-visible text and redacts URL credentials/query
// values before a reason reaches an owner row or SubscriptionLogEntry.
func safeReason(raw string) string {
	return SafeReason(raw)
}

// SafeReason bounds operator-visible text and redacts URL credentials/query
// values before a reason reaches an owner row or typed error. Exported so the
// one-shot path reuses the same redaction as RSS refresh.
func SafeReason(raw string) string {
	return boundedRedactedText(raw, 512)
}

func boundedRedactedText(raw string, maxRunes int) string {
	text, _ := redact.Apply(raw, maxRunes, redact.Mask, redact.Redacted)
	return text
}

func nullableJSON(raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
