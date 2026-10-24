package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lib/pq"

	catalogdomain "bansoko/internal/catalog/domain"
)

// seedTorrentCacheColumns is the exact read shape of one cached locator. It is
// listed once so a read can never silently drift from the insert below.
const seedTorrentCacheColumns = `locator, payload, payload_digest, payload_size_bytes, torrent_version,
	info_hash_v1, info_hash_v2, torrent_name, total_size_bytes, file_count,
	projection_digest, parser_version, files, fetched_at`

// ReadSeedTorrentCache returns one locator's cached bytes and manifest. A cache
// miss is (zero, false, nil): absence is an expected outcome of lazy expansion,
// not an error, and the caller decides to fetch. A stored row that no longer
// reproduces its own digest or projection is a hard error: the cache is
// content-addressed, so corruption must fail closed instead of feeding a
// downloader bytes the database can no longer attest.
func (r *Repository) ReadSeedTorrentCache(ctx context.Context, locator string) (catalogdomain.SeedTorrentCache, bool, error) {
	if r == nil || r.db == nil {
		return catalogdomain.SeedTorrentCache{}, false, errors.New("catalog repository is not configured")
	}
	row := r.db.QueryRowContext(ctx, `SELECT `+seedTorrentCacheColumns+` FROM seed_torrent_cache WHERE locator = $1`, locator)
	entry, err := scanSeedTorrentCache(row)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogdomain.SeedTorrentCache{}, false, nil
	}
	if err != nil {
		return catalogdomain.SeedTorrentCache{}, false, fmt.Errorf("read seed torrent cache: %w", err)
	}
	return entry, true, nil
}

// UpsertSeedTorrentCache stores one verified locator payload. The row is
// replaced rather than appended: locator is the content key and the cache holds
// no evidence that must be preserved, so a re-fetch after a removal simply
// overwrites the same key.
func (r *Repository) UpsertSeedTorrentCache(ctx context.Context, entry catalogdomain.SeedTorrentCache) error {
	if r == nil || r.db == nil {
		return errors.New("catalog repository is not configured")
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	files, err := encodeSeedTorrentCacheFiles(entry.Manifest.Files)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO seed_torrent_cache(locator, payload, payload_digest, payload_size_bytes, torrent_version,
	info_hash_v1, info_hash_v2, torrent_name, total_size_bytes, file_count, projection_digest,
	parser_version, files, fetched_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, CURRENT_TIMESTAMP)
ON CONFLICT (locator) DO UPDATE SET
	payload = EXCLUDED.payload,
	payload_digest = EXCLUDED.payload_digest,
	payload_size_bytes = EXCLUDED.payload_size_bytes,
	torrent_version = EXCLUDED.torrent_version,
	info_hash_v1 = EXCLUDED.info_hash_v1,
	info_hash_v2 = EXCLUDED.info_hash_v2,
	torrent_name = EXCLUDED.torrent_name,
	total_size_bytes = EXCLUDED.total_size_bytes,
	file_count = EXCLUDED.file_count,
	projection_digest = EXCLUDED.projection_digest,
	parser_version = EXCLUDED.parser_version,
	files = EXCLUDED.files,
	fetched_at = EXCLUDED.fetched_at,
	updated_at = CURRENT_TIMESTAMP`,
		entry.Locator, entry.Payload, entry.PayloadDigest, int64(len(entry.Payload)), entry.Manifest.TorrentVersion,
		emptyToNil(entry.Manifest.InfoHashV1), emptyToNil(entry.Manifest.InfoHashV2), entry.Manifest.TorrentName,
		entry.Manifest.TotalSizeBytes, len(entry.Manifest.Files), entry.Manifest.ProjectionDigest,
		entry.Manifest.ParserVersion, files, entry.FetchedAt)
	if err != nil {
		return fmt.Errorf("upsert seed torrent cache: %w", err)
	}
	return nil
}

// ReadSeedTorrentCaches returns every cached row among the given locators in
// one query. The read model renders a whole group of seeds at once and must not
// issue one query per seed, so the batch read is part of the cache's own
// boundary. An empty locator list is not a query at all: a group whose seeds
// were all already expanded by their snapshot never touches the database. A
// locator with no row is simply absent from the result, which is what lets a
// caller tell "never expanded" from "expanded" without treating absence as an
// error.
func (r *Repository) ReadSeedTorrentCaches(ctx context.Context, locators []string) (map[string]catalogdomain.SeedTorrentCache, error) {
	if len(locators) == 0 {
		return map[string]catalogdomain.SeedTorrentCache{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("catalog repository is not configured")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+seedTorrentCacheColumns+` FROM seed_torrent_cache WHERE locator = ANY($1)`, pq.Array(locators))
	if err != nil {
		return nil, fmt.Errorf("read seed torrent cache batch: %w", err)
	}
	defer rows.Close()
	entries := make(map[string]catalogdomain.SeedTorrentCache, len(locators))
	for rows.Next() {
		entry, err := scanSeedTorrentCache(rows)
		if err != nil {
			return nil, fmt.Errorf("read seed torrent cache batch: %w", err)
		}
		entries[entry.Locator] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read seed torrent cache batch: %w", err)
	}
	return entries, nil
}

// scanSeedTorrentCache rebuilds one row into the domain type and validates it,
// so a tampered or truncated row fails at the boundary instead of returning an
// incoherent manifest.
func scanSeedTorrentCache(row rowScanner) (catalogdomain.SeedTorrentCache, error) {
	var (
		entry                  catalogdomain.SeedTorrentCache
		payloadSize, totalSize int64
		fileCount              int
		infoHashV1, infoHashV2 sql.NullString
		filesRaw               []byte
	)
	if err := row.Scan(
		&entry.Locator, &entry.Payload, &entry.PayloadDigest, &payloadSize, &entry.Manifest.TorrentVersion,
		&infoHashV1, &infoHashV2, &entry.Manifest.TorrentName, &totalSize, &fileCount,
		&entry.Manifest.ProjectionDigest, &entry.Manifest.ParserVersion, &filesRaw, &entry.FetchedAt,
	); err != nil {
		return catalogdomain.SeedTorrentCache{}, err
	}
	files, err := decodeFeedSeedFiles(filesRaw)
	if err != nil {
		return catalogdomain.SeedTorrentCache{}, err
	}
	entry.Manifest.Files = files
	// The manifest is bound to the stored bytes, not to a separately stored
	// digest column, so the projection can always be proven against the payload.
	entry.Manifest.PayloadDigest = entry.PayloadDigest
	entry.Manifest.TotalSizeBytes = totalSize
	if infoHashV1.Valid {
		entry.Manifest.InfoHashV1 = infoHashV1.String
	}
	if infoHashV2.Valid {
		entry.Manifest.InfoHashV2 = infoHashV2.String
	}
	if err := entry.Validate(); err != nil {
		return catalogdomain.SeedTorrentCache{}, err
	}
	return entry, nil
}

// encodeSeedTorrentCacheFiles writes the file projection with the same wire
// names the snapshot seed row uses, so both tables describe a file identically.
func encodeSeedTorrentCacheFiles(files []catalogdomain.TorrentFile) ([]byte, error) {
	stored := make([]feedSeedFile, 0, len(files))
	for _, file := range files {
		entry := feedSeedFile{
			FileRef: file.FileRef, Ordinal: file.Ordinal, SourcePath: file.SourcePath,
			Filename: file.Filename, SizeBytes: file.SizeBytes, MediaKind: file.MediaKind,
		}
		if file.Language != "" {
			language := file.Language
			entry.Language = &language
		}
		stored = append(stored, entry)
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("encode seed torrent cache files: %w", err)
	}
	return raw, nil
}
