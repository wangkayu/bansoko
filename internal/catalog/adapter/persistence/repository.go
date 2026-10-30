// Package persistence implements the PostgreSQL persistence boundary for
// provider catalog and group snapshot retention.
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	generated "bansoko/internal/catalog/adapter/persistence/generated"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
)

const maxRetentionBatchSize = 500

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) transaction(scope txscope.Tx, batchSize int) (*sql.Tx, error) {
	if r == nil {
		return nil, errors.New("catalog retention repository is nil")
	}
	if batchSize < 1 || batchSize > maxRetentionBatchSize {
		return nil, fmt.Errorf("catalog retention batch size must be between 1 and %d", maxRetentionBatchSize)
	}
	tx := scope.SQLTx()
	if tx == nil {
		return nil, errors.New("catalog retention transaction is nil")
	}
	return tx, nil
}

func purgeRows(result sql.Result, operation string) (int64, error) {
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read %s deleted row count: %w", operation, err)
	}
	return count, nil
}

func (r *Repository) PurgeExpiredProviderCatalogSnapshots(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgeExpiredProviderCatalogSnapshots(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return purgeRows(result, "provider catalog snapshots")
}

func (r *Repository) PurgeExpiredProviderGroupSnapshots(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgeExpiredProviderGroupSnapshots(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return purgeRows(result, "provider group snapshots")
}

func (r *Repository) ReadLatestProviderFeedSnapshot(ctx context.Context, provider, entryRef, groupRef string) (catalogdomain.ProviderFeedSnapshot, error) {
	if r == nil || r.db == nil {
		return catalogdomain.ProviderFeedSnapshot{}, errors.New("catalog feed snapshot repository is nil")
	}
	queries := generated.New(r.db)
	row, err := queries.ReadLatestProviderFeedSnapshot(ctx, generated.ReadLatestProviderFeedSnapshotParams{
		Provider: provider, ProviderEntryRef: entryRef, ProviderGroupRef: groupRef,
	})
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	return r.readProviderFeedSnapshot(ctx, queries, row)
}

// ReadProviderFeedSnapshotByID reads one exact persisted snapshot identity. It
// never falls back to the latest version: a missing id stays sql.ErrNoRows so
// the application boundary can fail closed instead of silently rendering a
// different snapshot. Because it is the exact-identity read, it recomputes the
// payload digests and the whole-snapshot content hash and fails closed on any
// disagreement.
func (r *Repository) ReadProviderFeedSnapshotByID(ctx context.Context, id string) (catalogdomain.ProviderFeedSnapshot, error) {
	if r == nil || r.db == nil {
		return catalogdomain.ProviderFeedSnapshot{}, errors.New("catalog feed snapshot repository is nil")
	}
	return r.ReadProviderFeedSnapshotTx(ctx, r.db, id)
}

// ReadProviderFeedSnapshotTx preserves the exact reader inside a caller transaction.
func (r *Repository) ReadProviderFeedSnapshotTx(ctx context.Context, db generated.DBTX, id string) (catalogdomain.ProviderFeedSnapshot, error) {
	if db == nil {
		return catalogdomain.ProviderFeedSnapshot{}, errors.New("catalog feed transaction is nil")
	}
	queries := generated.New(db)
	row, err := queries.ReadProviderFeedSnapshotByID(ctx, id)
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	return r.readProviderFeedSnapshot(ctx, queries, generated.ReadLatestProviderFeedSnapshotRow(row))
}

func (r *Repository) readProviderFeedSnapshot(ctx context.Context, queries *generated.Queries, row generated.ReadLatestProviderFeedSnapshotRow) (catalogdomain.ProviderFeedSnapshot, error) {
	rows, err := queries.ReadProviderFeedSnapshotSeeds(ctx, row.ID)
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	seeds := make([]catalogdomain.ProviderFeedSnapshotSeed, 0, len(rows))
	for _, seed := range rows {
		fact := catalogdomain.ProviderFeedSnapshotSeed{
			StableSeedKey: seed.StableSeedKey, Locator: seed.Locator, LocatorDisplay: seed.LocatorDisplay, Title: seed.Title,
		}
		if seed.PublishedAt.Valid {
			value := seed.PublishedAt.Time
			fact.PublishedAt = &value
		}
		if seed.SizeBytes.Valid {
			value := seed.SizeBytes.Int64
			fact.SizeBytes = &value
		}
		if seed.InfoHash.Valid {
			value := seed.InfoHash.String
			fact.InfoHash = &value
		}
		if seed.TorrentFilename.Valid {
			value := seed.TorrentFilename.String
			fact.TorrentFilename = &value
		}
		fact.TorrentState = seed.TorrentState
		if seed.TorrentErrorCode.Valid {
			value := seed.TorrentErrorCode.String
			fact.TorrentErrorCode = &value
		}
		if seed.TorrentErrorReason.Valid {
			value := seed.TorrentErrorReason.String
			fact.TorrentErrorReason = &value
		}
		if seed.Payload != nil {
			fact.Payload = append([]byte(nil), seed.Payload...)
			if seed.BlobPayloadDigest.Valid {
				value := seed.BlobPayloadDigest.String
				fact.PayloadDigest = &value
			} else if seed.PayloadDigest.Valid {
				value := seed.PayloadDigest.String
				fact.PayloadDigest = &value
			}
		} else if seed.PayloadDigest.Valid {
			// The seed declares a payload digest but its payload bytes are absent:
			// keep the declared digest so VerifyContentHash fails closed instead of
			// silently treating the seed as payload-less.
			value := seed.PayloadDigest.String
			fact.PayloadDigest = &value
		}
		files, err := decodeFeedSeedFiles(seed.Files)
		if err != nil {
			return catalogdomain.ProviderFeedSnapshot{}, err
		}
		anibtTags, err := decodeFeedSeedAniBTTags(seed.AnibtTags.Valid, seed.AnibtTags.RawMessage)
		if err != nil {
			return catalogdomain.ProviderFeedSnapshot{}, err
		}
		fact.AniBTTags = anibtTags
		sourceSite, err := decodeFeedSeedSourceSite(seed.SourceSite.Valid, seed.SourceSite.RawMessage)
		if err != nil {
			return catalogdomain.ProviderFeedSnapshot{}, err
		}
		fact.SourceSite = sourceSite
		if seed.ManifestTorrentParserVersion.Valid {
			manifest := &catalogdomain.TorrentManifest{
				PayloadDigest:    seed.PayloadDigest.String,
				ParserVersion:    seed.ManifestTorrentParserVersion.String,
				TorrentVersion:   seed.TorrentVersion.String,
				TorrentName:      seed.ManifestTorrentName.String,
				TotalSizeBytes:   seed.ManifestTotalSizeBytes.Int64,
				ProjectionDigest: seed.ManifestProjectionDigest.String,
				Files:            files,
			}
			if seed.InfoHashV1.Valid {
				manifest.InfoHashV1 = seed.InfoHashV1.String
			}
			if seed.InfoHashV2.Valid {
				manifest.InfoHashV2 = seed.InfoHashV2.String
			}
			fact.Manifest = manifest
		}
		seeds = append(seeds, fact)
	}
	snapshot := catalogdomain.ProviderFeedSnapshot{
		ID: row.ID, Provider: row.Provider, ProviderEntryRef: row.ProviderEntryRef, ProviderGroupRef: row.ProviderGroupRef,
		Version: row.Version, FetchedAt: row.FetchedAt, FreshUntil: row.FreshUntil, RetentionUntil: row.RetentionUntil, ContentHash: row.ContentHash,
		HashSchema: row.HashSchema, SnapshotKind: catalogdomain.FeedSnapshotKind(row.SnapshotKind),
		SearchParserVersion: row.SearchParserVersion.String,
		FreshnessState:      catalogdomain.FeedSnapshotFreshness(row.Freshness), Seeds: seeds,
	}
	if row.ProjectionSchema.Valid {
		snapshot.ProjectionSchema = row.ProjectionSchema.String
	}
	// Every snapshot must reproduce its own declared content hash and payload
	// digests, and every available seed must still project a manifest the
	// parser-size contract accepts. Tampering with a seed fact, blob, manifest
	// or file row fails closed. The hash schema is pinned to the current value
	// by the table CHECK.
	if err := snapshot.VerifyContentHash(); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	if err := verifyProviderFeedSnapshotProjections(snapshot); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	return snapshot, nil
}

// feedSeedFile mirrors the file projection the publication stores in the seed
// row's files column. catalogdomain.TorrentFile carries no JSON tags of its own,
// so the wire names are declared here rather than imposed on the domain type.
type feedSeedFile struct {
	FileRef    string  `json:"file_ref"`
	Ordinal    int     `json:"ordinal"`
	SourcePath string  `json:"source_path"`
	Filename   string  `json:"filename"`
	SizeBytes  int64   `json:"size_bytes"`
	MediaKind  string  `json:"media_kind"`
	Language   *string `json:"language"`
}

// decodeFeedSeedFiles reads one seed's file projection from its JSONB column.
// The projection is a leaf of the seed's manifest, so it travels with the seed
// row and never needs a second query.
func decodeFeedSeedFiles(raw []byte) ([]catalogdomain.TorrentFile, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var stored []feedSeedFile
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode provider feed snapshot files: %w", err)
	}
	files := make([]catalogdomain.TorrentFile, 0, len(stored))
	for _, file := range stored {
		entry := catalogdomain.TorrentFile{
			FileRef: file.FileRef, Ordinal: file.Ordinal, SourcePath: file.SourcePath,
			Filename: file.Filename, SizeBytes: file.SizeBytes, MediaKind: file.MediaKind,
		}
		if file.Language != nil {
			entry.Language = *file.Language
		}
		files = append(files, entry)
	}
	return files, nil
}

// decodeFeedSeedAniBTTags reads one seed's frozen AniBT tag block from its
// JSONB column. SQL NULL is a seed observed without a tag set (a non-AniBT
// provider, or a snapshot written before the column existed) and stays nil; an
// object with an empty `tags` array is a tag-less AniBT item, which is a fact
// and not the same absence.
func decodeFeedSeedAniBTTags(valid bool, raw []byte) (*acquisitiondomain.StoredAniBTTags, error) {
	if !valid || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var stored acquisitiondomain.StoredAniBTTags
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode provider feed snapshot anibt tags: %w", err)
	}
	if stored.Tags == nil {
		stored.Tags = []string{}
	}
	return &stored, nil
}

func decodeFeedSeedSourceSite(valid bool, raw []byte) (*acquisitiondomain.SeedSourceSite, error) {
	if !valid || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var stored acquisitiondomain.SeedSourceSite
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode provider feed snapshot source site: %w", err)
	}
	if err := stored.Validate(); err != nil {
		return nil, fmt.Errorf("decode provider feed snapshot source site: %w", err)
	}
	return &stored, nil
}

// verifyProviderFeedSnapshotProjections fails closed when a current-schema
// snapshot's stored projection cannot be an exact derivation of its payload:
// the manifest is missing where the state says available, present where it says
// otherwise, or no longer covers its own file rows.
func verifyProviderFeedSnapshotProjections(snapshot catalogdomain.ProviderFeedSnapshot) error {
	for index, seed := range snapshot.Seeds {
		switch seed.NormalizedTorrentState() {
		case catalogdomain.TorrentAcquisitionAvailable:
			if seed.Manifest == nil {
				return fmt.Errorf("%w: seed %d is available without a persisted manifest", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, index)
			}
		case catalogdomain.TorrentAcquisitionUnavailable, catalogdomain.TorrentAcquisitionNotApplicable:
			if seed.Manifest != nil {
				return fmt.Errorf("%w: seed %d carries a manifest while classified %s", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, index, seed.NormalizedTorrentState())
			}
		default:
			return fmt.Errorf("%w: seed %d has an unsupported torrent state %q", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, index, seed.NormalizedTorrentState())
		}
	}
	return nil
}

func (r *Repository) PersistProviderFeedSnapshot(ctx context.Context, input catalogdomain.ProviderFeedSnapshotWrite) (catalogdomain.ProviderFeedSnapshot, error) {
	if input.Provider == "custom" {
		return catalogdomain.ProviderFeedSnapshot{}, errors.New("custom snapshot requires atomic source validator")
	}
	return r.persistFeedSnapshot(ctx, input, nil)
}

func (r *Repository) PersistCustomFeedSnapshot(ctx context.Context, input catalogdomain.ProviderFeedSnapshotWrite, validate func(context.Context, txscope.Tx) error) (catalogdomain.ProviderFeedSnapshot, error) {
	if input.Provider != "custom" || validate == nil {
		return catalogdomain.ProviderFeedSnapshot{}, errors.New("custom snapshot requires atomic source validator")
	}
	return r.persistFeedSnapshot(ctx, input, validate)
}

func (r *Repository) persistFeedSnapshot(ctx context.Context, input catalogdomain.ProviderFeedSnapshotWrite, validate func(context.Context, txscope.Tx) error) (catalogdomain.ProviderFeedSnapshot, error) {
	if r == nil || r.db == nil {
		return catalogdomain.ProviderFeedSnapshot{}, errors.New("catalog feed snapshot repository is nil")
	}
	if err := input.Validate(); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("begin provider feed snapshot transaction: %w", err)
	}
	defer tx.Rollback()
	if validate != nil {
		if err := validate(ctx, txscope.Wrap(tx)); err != nil {
			return catalogdomain.ProviderFeedSnapshot{}, err
		}
	}
	lockKey := "bansoko:provider-feed:" + input.Provider + ":" + input.ProviderEntryRef + ":" + input.ProviderGroupRef
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("lock provider feed snapshot: %w", err)
	}
	queries := generated.New(tx)
	version, err := queries.AllocateProviderFeedSnapshotVersion(ctx, generated.AllocateProviderFeedSnapshotVersionParams{
		Provider: input.Provider, ProviderEntryRef: input.ProviderEntryRef, ProviderGroupRef: input.ProviderGroupRef,
	})
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("allocate provider feed snapshot version: %w", err)
	}
	params, err := feedPublication(input, int64(version))
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	id, err := queries.PublishProviderFeedSnapshot(ctx, params)
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("publish provider feed snapshot: %w", err)
	}
	// The publication function seals a fixed 15-minute window; the caller's
	// fresh window (one RSS refresh interval) replaces it. fresh_until is one
	// of the lifecycle columns the snapshot immutability guard allows.
	if !input.FreshUntil.IsZero() {
		if _, err := tx.ExecContext(ctx, `UPDATE provider_feed_snapshots SET fresh_until = GREATEST(fetched_at, $2::timestamptz) WHERE id = $1`, id, input.FreshUntil); err != nil {
			return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("set provider feed snapshot fresh window: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("commit provider feed snapshot: %w", err)
	}
	// Return the snapshot this call just created, by its own id. Reading "latest"
	// after commit could return a concurrently persisted newer snapshot and
	// cross-wire two refresh callers.
	return r.ReadProviderFeedSnapshotByID(ctx, id)
}

// emptyToNil keeps an absent optional column absent instead of storing an
// empty string that would violate a bounded-text check.
func emptyToNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func (r *Repository) PurgeExpiredProviderFeedSnapshots(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgeExpiredProviderFeedSnapshots(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return purgeRows(result, "provider feed snapshots")
}
