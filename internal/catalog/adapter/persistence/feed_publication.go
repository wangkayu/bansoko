package persistence

import (
	"database/sql"
	"encoding/json"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	generated "bansoko/internal/catalog/adapter/persistence/generated"
	catalogdomain "bansoko/internal/catalog/domain"
	"github.com/google/uuid"
)

// Publication declarations mirror the narrow owner entry's exact fields. Raw
// payloads stay BYTEA[], never base64/hex in the JSON metadata or public DTOs.
// Each seed carries its own manifest and file projection as nested leaves: the
// snapshot is one seed row, not a three-table graph.
type feedSeedDeclaration struct {
	ID                 string                   `json:"id"`
	StableSeedKey      string                   `json:"stable_seed_key"`
	Locator            string                   `json:"locator"`
	LocatorDisplay     string                   `json:"locator_display"`
	Title              string                   `json:"title"`
	PublishedAt        *time.Time               `json:"published_at"`
	SizeBytes          *int64                   `json:"size_bytes"`
	InfoHash           *string                  `json:"info_hash"`
	PayloadDigest      *string                  `json:"payload_digest"`
	TorrentFilename    *string                  `json:"torrent_filename"`
	TorrentState       string                   `json:"torrent_state"`
	TorrentErrorCode   *string                  `json:"torrent_error_code"`
	TorrentErrorReason *string                  `json:"torrent_error_reason"`
	TorrentVersion     *string                  `json:"torrent_version"`
	InfoHashV1         *string                  `json:"info_hash_v1"`
	InfoHashV2         *string                  `json:"info_hash_v2"`
	PayloadIndex       *int                     `json:"payload_index"`
	Manifest           *feedManifestDeclaration `json:"manifest"`
	Files              []feedFileDeclaration    `json:"files"`
	// AniBTTags is always present so the owner function's closed key set holds;
	// a nil block marshals as JSON null and is persisted as SQL NULL.
	AniBTTags  *acquisitiondomain.StoredAniBTTags `json:"anibt_tags"`
	SourceSite *acquisitiondomain.SeedSourceSite  `json:"source_site"`
}

type feedManifestDeclaration struct {
	PayloadDigest    string `json:"payload_digest"`
	ParserVersion    string `json:"torrent_parser_version"`
	TorrentName      string `json:"torrent_name"`
	TotalSizeBytes   int64  `json:"total_size_bytes"`
	FileCount        int    `json:"file_count"`
	ProjectionDigest string `json:"projection_digest"`
}

type feedFileDeclaration struct {
	FileRef    string  `json:"file_ref"`
	Ordinal    int     `json:"ordinal"`
	SourcePath string  `json:"source_path"`
	Filename   string  `json:"filename"`
	SizeBytes  int64   `json:"size_bytes"`
	MediaKind  string  `json:"media_kind"`
	Language   *string `json:"language"`
}

// publicationSnapshotKind is the kind written for one publication. An empty
// kind is the ordinary provider feed: every snapshot written before the
// column existed was a published feed or a custom feed, and those callers
// never set it.
func publicationSnapshotKind(input catalogdomain.ProviderFeedSnapshotWrite) string {
	return string(input.NormalizedKind())
}

func feedPublication(input catalogdomain.ProviderFeedSnapshotWrite, version int64) (generated.PublishProviderFeedSnapshotParams, error) {
	params := generated.PublishProviderFeedSnapshotParams{ID: uuid.NewString(), Provider: input.Provider, ProviderEntryRef: input.ProviderEntryRef,
		ProviderGroupRef: input.ProviderGroupRef, ExpectedVersion: version, FetchedAt: input.FetchedAt, ContentHash: input.ContentHash, Payloads: make([][]byte, 0),
		SnapshotKind: publicationSnapshotKind(input),
		SearchParserVersion: sql.NullString{
			String: input.SearchParserVersion, Valid: input.SearchParserVersion != "",
		}}
	seeds := make([]feedSeedDeclaration, 0, len(input.Seeds))
	// One capacity pass keeps Files non-nil (an absent manifest marshals as an
	// empty array, never null) without reading a nil manifest.
	fileCount := 0
	for _, seed := range input.Seeds {
		if seed.Manifest != nil {
			fileCount = len(seed.Manifest.Files)
		}
	}
	for _, seed := range input.Seeds {
		d := feedSeedDeclaration{ID: uuid.NewString(), StableSeedKey: seed.StableSeedKey, Locator: seed.Locator, LocatorDisplay: seed.LocatorDisplay,
			Title: seed.Title, PublishedAt: seed.PublishedAt, SizeBytes: seed.SizeBytes, InfoHash: seed.InfoHash, PayloadDigest: seed.PayloadDigest,
			TorrentFilename: seed.TorrentFilename, TorrentState: seed.NormalizedTorrentState(), TorrentErrorCode: seed.TorrentErrorCode, TorrentErrorReason: seed.TorrentErrorReason,
			Files: make([]feedFileDeclaration, 0, fileCount), AniBTTags: seed.AniBTTags, SourceSite: seed.SourceSite}
		if len(seed.Payload) > 0 {
			params.Payloads = append(params.Payloads, seed.Payload)
			index := len(params.Payloads)
			d.PayloadIndex = &index
		}
		if m := seed.Manifest; m != nil {
			d.TorrentVersion = &m.TorrentVersion
			d.InfoHashV1 = emptyToNil(m.InfoHashV1)
			d.InfoHashV2 = emptyToNil(m.InfoHashV2)
			d.Manifest = &feedManifestDeclaration{PayloadDigest: m.PayloadDigest, ParserVersion: m.ParserVersion,
				TorrentName: m.TorrentName, TotalSizeBytes: m.TotalSizeBytes, FileCount: len(m.Files), ProjectionDigest: m.ProjectionDigest}
			for _, f := range m.Files {
				d.Files = append(d.Files, feedFileDeclaration{FileRef: f.FileRef, Ordinal: f.Ordinal,
					SourcePath: f.SourcePath, Filename: f.Filename, SizeBytes: f.SizeBytes, MediaKind: f.MediaKind, Language: emptyToNil(f.Language)})
			}
		}
		seeds = append(seeds, d)
	}
	var err error
	if params.Seeds, err = json.Marshal(seeds); err != nil {
		return params, err
	}
	return params, nil
}
