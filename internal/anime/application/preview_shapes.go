package application

import (
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// This file holds the provider-feed preview vocabulary the oneshot admission
// path produces and the read models render. It lives in the kernel because
// both buckets read it, and neither may reference the other.

// SeedPreviewFact is one seed fact from the exact persisted provider feed
// snapshot identity a caller selected.
type SeedPreviewFact struct {
	DownloadURL           string
	ExpectedPayloadDigest string
	InfoHash              string
	PublishedAt           time.Time
	SizeBytes             *int64
	SnapshotContentHash   string
	SnapshotID            string
	SnapshotVersion       int64
	StableSeedKey         string
	Title                 string
	TorrentFilename       string
	// TorrentState is one of the catalog TorrentAcquisition* values and
	// Manifest is the seed's derived file projection, present exactly when the
	// state is available. The manifest is the authority for the files the
	// preview renders, so a multi-file torrent is evaluated file by file.
	TorrentState       string
	TorrentErrorCode   string
	TorrentErrorReason string
	Manifest           *animecatalogdomain.TorrentManifest
	// AniBTTags is the frozen AniBT structured tag set the snapshot recorded,
	// in the persisted (public) block shape. nil means the snapshot carries no
	// tag set; a preview that rebuilt the item without it would evaluate
	// `anibt.*` against an empty set.
	AniBTTags  *acquisitiondomain.StoredAniBTTags
	SourceSite *acquisitiondomain.SeedSourceSite
	// LocatorDisplay is the redacted display form of the seed's source
	// location. The raw locator is deliberately not part of this fact set.
	LocatorDisplay string
}

// SeedPreviewSnapshot is one exact persisted snapshot identity with the seed
// facts the read model and admission render from.
type SeedPreviewSnapshot struct {
	Provider         string
	ProviderEntryRef string
	ProviderGroupRef string
	SnapshotID       string
	SnapshotVersion  int64
	ContentHash      string
	Freshness        string
	Seeds            []SeedPreviewFact
}
