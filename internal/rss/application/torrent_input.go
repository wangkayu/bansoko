package application

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/persistence/txscope"
)

const torrentInputParserVersion = "torrent-bencode-v1"

// TorrentInputParserVersion is the exported identity of the torrent input
// parser version. The HTTP parse endpoint reports it through
// transport/httpapi so the parse result matches persisted torrent input rows.
const TorrentInputParserVersion = torrentInputParserVersion

var errTorrentPayloadMissing = errors.New("torrent_payload_missing")
var errTorrentPayloadInvalid = errors.New("torrent_payload_invalid")

// torrentTooLargeOutcome is the release outcome of an acceptance refused for
// size (terminal.TorrentTooLargeCode): the torrent lists more files than its
// frozen download record can hold, so the release is not downloaded and the
// evaluation itself still completes.
func torrentTooLargeOutcome() (verdict, reasonCode, reasonDetail string) {
	return "acquisition_failed", "torrent_too_large", "the torrent lists too many files to freeze its download record"
}

// releaseInternalFactsRawLimit is the raw JSON bound of a release's internal
// facts, which carry its parsed file list: releases.internal_facts is bound
// to pg_column_size <= 2097152, and the raw bound keeps the same one eighth of
// headroom over the jsonb datum the frozen task documents keep.
const releaseInternalFactsRawLimit = 2<<20 - (2<<20)/8

// refuseOversizedTorrent marks a fetched torrent whose file list its release
// row cannot store, or which lists more files than the file filter reads, as
// torrent_too_large: the file list and payload are dropped, so the release is
// stored and evaluated (parse_failed with that reason) instead of failing the
// refresh of its whole feed.
func refuseOversizedTorrent(item acquisitiondomain.FeedItem) acquisitiondomain.FeedItem {
	if item.TorrentErrorCode != "" || len(item.ReleaseFiles) == 0 {
		return item
	}
	tooLarge := len(item.ReleaseFiles) > animedomain.MaxFilterInventoryEntries
	if !tooLarge {
		raw, err := encodeInternalFeedFacts(item)
		tooLarge = err == nil && len(raw) > releaseInternalFactsRawLimit
	}
	if !tooLarge {
		return item
	}
	_, item.TorrentErrorCode, item.TorrentErrorReason = torrentTooLargeOutcome()
	item.ReleaseFiles = nil
	item.TorrentPayload = nil
	return item
}

// persistReleaseTorrentInput stores only program-fetched and bencode-validated
// bytes. A URL, magnet, or provider hint is never a substitute for this input.
func persistReleaseTorrentInput(ctx context.Context, tx *sql.Tx, releaseID string, item acquisitiondomain.FeedItem) error {
	if len(item.TorrentPayload) == 0 {
		return nil
	}
	filename, err := torrentInputFilename(item.TorrentFilename, item.TorrentInfoHash)
	if err != nil {
		return err
	}
	version := strings.TrimSpace(item.TorrentVersion)
	if version != "v1" && version != "v2" && version != "hybrid" {
		return errors.New("validated torrent input has an unsupported torrent version")
	}
	sourceURL := item.TorrentMetadataURL()
	if sourceURL == "" {
		return errors.New("validated torrent input has no torrent source URL")
	}
	payload := append([]byte(nil), item.TorrentPayload...)
	metadata, err := torrent.Parse(payload)
	if err != nil {
		return fmt.Errorf("validate torrent payload: %w", err)
	}
	if err := validateTorrentFacts(metadata, item, filename, version); err != nil {
		return err
	}
	return persistTorrentInputRaw(ctx, tx, releaseID, payload, metadata, sourceURL, filename)
}

// PersistTorrentInput stores one program-fetched and bencode-validated torrent
// payload. It is shared by the RSS refresh path and the one-shot
// path so both guarantee the same torrent_input invariant. The caller must have
// already bencode-parsed the payload and validated the metadata against its
// source facts. The payload is copied so the caller retains its own bytes.
func PersistTorrentInput(ctx context.Context, scope txscope.Tx, releaseID string, payload []byte, metadata torrent.Metadata, sourceURL, filename string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("persist torrent input transaction is nil")
	}
	return persistTorrentInputRaw(ctx, tx, releaseID, payload, metadata, sourceURL, filename)
}

func persistTorrentInputRaw(ctx context.Context, tx *sql.Tx, releaseID string, payload []byte, metadata torrent.Metadata, sourceURL, filename string) error {
	if len(payload) == 0 {
		return errors.New("torrent payload is empty")
	}
	version := strings.TrimSpace(metadata.TorrentVersion)
	if version != "v1" && version != "v2" && version != "hybrid" {
		return errors.New("validated torrent input has an unsupported torrent version")
	}
	if strings.TrimSpace(sourceURL) == "" {
		return errors.New("validated torrent input has no torrent source URL")
	}
	if strings.TrimSpace(filename) == "" {
		return errors.New("validated torrent input has no filename")
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO release_torrent_inputs(
    id, release_id, payload, payload_digest, source_url_digest, filename,
    parser_version, torrent_version, info_hash_v1, info_hash_v2, payload_size_bytes
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''), $11)
ON CONFLICT (release_id) DO UPDATE
SET payload = EXCLUDED.payload,
    payload_digest = EXCLUDED.payload_digest,
    source_url_digest = EXCLUDED.source_url_digest,
    filename = EXCLUDED.filename,
    parser_version = EXCLUDED.parser_version,
    torrent_version = EXCLUDED.torrent_version,
    info_hash_v1 = EXCLUDED.info_hash_v1,
    info_hash_v2 = EXCLUDED.info_hash_v2,
    payload_size_bytes = EXCLUDED.payload_size_bytes,
    updated_at = CURRENT_TIMESTAMP
WHERE NOT EXISTS (SELECT 1 FROM download_tasks task WHERE task.release_id = EXCLUDED.release_id)`,
		newID(), releaseID, payload, stableDigest(string(payload)), torrentSourceDigest(sourceURL), filename,
		torrentInputParserVersion, version, metadata.InfoHashV1, metadata.InfoHashV2, len(payload))
	if err != nil {
		return fmt.Errorf("persist release torrent input: %w", err)
	}
	return nil
}

func validateTorrentFacts(metadata torrent.Metadata, item acquisitiondomain.FeedItem, filename, version string) error {
	if metadata.TorrentVersion != version {
		return fmt.Errorf("torrent payload version mismatch: payload=%s item=%s", metadata.TorrentVersion, version)
	}
	if strings.TrimSpace(item.TorrentInfoHashV1) != metadata.InfoHashV1 {
		return fmt.Errorf("torrent payload v1 infohash mismatch")
	}
	if strings.TrimSpace(item.TorrentInfoHashV2) != metadata.InfoHashV2 {
		return fmt.Errorf("torrent payload v2 infohash mismatch")
	}
	if item.TorrentInfoHash != "" {
		expected := metadata.InfoHashV1
		if expected == "" {
			expected = metadata.InfoHashV2
		}
		if strings.TrimSpace(item.TorrentInfoHash) != expected {
			return errors.New("torrent payload infohash mismatch")
		}
	}
	if strings.TrimSpace(filename) == "" {
		return errors.New("torrent input filename is empty")
	}
	// Feed-declared size is only a hint: real feeds (e.g. mikan contentLength)
	// routinely differ from the actual content size by small amounts, so
	// equality is not a validity signal. The fetched payload is authoritative;
	// a declared infohash (above) is what proves we fetched the right torrent.
	if len(item.ReleaseFiles) != 0 {
		if len(item.ReleaseFiles) != len(metadata.Files) {
			return errors.New("torrent payload file count mismatch")
		}
		for index, file := range metadata.Files {
			actual := item.ReleaseFiles[index]
			if actual.SourcePath != file.Path || actual.Filename != file.Name || actual.SizeBytes == nil || *actual.SizeBytes != file.SizeBytes {
				return fmt.Errorf("torrent payload file facts mismatch at index %d", index)
			}
		}
	}
	return nil
}

// torrentInputFilename derives a safe single path component from the feed or
// torrent display name. The name is provenance/display metadata, not the final
// file target (naming rules render that), so a name that is not already a safe
// component is sanitized rather than rejected: real feeds routinely embed path
// separators or control characters in display titles (e.g. "Show A / 第二季"),
// and qBittorrent revalidates the upload component at submission time.
func torrentInputFilename(value, fallbackHash string) (string, error) {
	return TorrentInputFilename(value, fallbackHash)
}

// TorrentInputFilename derives a safe single path component from the feed or
// torrent display name. Exported so the one-shot path reuses the same
// sanitization as RSS refresh.
func TorrentInputFilename(value, fallbackHash string) (string, error) {
	base := sanitizeFilenameComponent(value)
	if base == "" {
		base = sanitizeFilenameComponent(fallbackHash)
	}
	if base == "" {
		base = "release"
	}
	if !strings.HasSuffix(strings.ToLower(base), ".torrent") {
		base += ".torrent"
	}
	return base, nil
}

// sanitizeFilenameComponent keeps at most 503 UTF-8 bytes of a single path
// component, replacing path separators and control characters so the value can
// never escape into a directory. It returns "" when nothing safe remains.
func sanitizeFilenameComponent(value string) string {
	name := strings.TrimSpace(value)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	base := filepath.Base(name)
	if base == "." || base == ".." {
		return ""
	}
	if len(base) <= 503 {
		return base
	}
	// Keep a UTF-8-safe prefix of the maximum component length.
	trimmed := base[:503]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

type releaseTorrentInputRef struct {
	ID              string
	PayloadDigest   string
	SourceURLDigest string
	Filename        string
	ParserVersion   string
	TorrentVersion  string
	InfoHashV1      string
	InfoHashV2      string
}

func loadReleaseTorrentInputRef(ctx context.Context, tx *sql.Tx, releaseID string) (releaseTorrentInputRef, error) {
	var input releaseTorrentInputRef
	err := tx.QueryRowContext(ctx, `
SELECT id, payload_digest, source_url_digest, filename, parser_version, torrent_version,
       COALESCE(info_hash_v1, ''), COALESCE(info_hash_v2, '')
FROM release_torrent_inputs
WHERE release_id = $1
FOR UPDATE`, releaseID).Scan(&input.ID, &input.PayloadDigest, &input.SourceURLDigest, &input.Filename, &input.ParserVersion, &input.TorrentVersion, &input.InfoHashV1, &input.InfoHashV2)
	if errors.Is(err, sql.ErrNoRows) {
		return releaseTorrentInputRef{}, errTorrentPayloadMissing
	}
	if err != nil {
		return releaseTorrentInputRef{}, fmt.Errorf("read release torrent input: %w", err)
	}
	if !isUsableTorrentInputRef(input) {
		return releaseTorrentInputRef{}, errTorrentPayloadInvalid
	}
	return input, nil
}

// isUsableTorrentInputRef preserves the existing task-boundary contract for
// historical and manual releases. Transient metadata reuse applies the stricter
// completeness and identity checks below.
func isUsableTorrentInputRef(input releaseTorrentInputRef) bool {
	return strings.TrimSpace(input.ID) != "" && strings.TrimSpace(input.PayloadDigest) != "" && strings.TrimSpace(input.Filename) != "" &&
		strings.TrimSpace(input.ParserVersion) != "" && (input.TorrentVersion == "v1" || input.TorrentVersion == "v2" || input.TorrentVersion == "hybrid") &&
		(input.TorrentVersion != "v1" || strings.TrimSpace(input.InfoHashV1) != "") &&
		(input.TorrentVersion != "v2" || strings.TrimSpace(input.InfoHashV2) != "") &&
		(input.TorrentVersion != "hybrid" || (strings.TrimSpace(input.InfoHashV1) != "" && strings.TrimSpace(input.InfoHashV2) != ""))
}

func isCompleteTorrentInputRefForReuse(input releaseTorrentInputRef) bool {
	if !isUsableTorrentInputRef(input) || !isSHA256Digest(input.PayloadDigest) || !isSHA256Digest(input.SourceURLDigest) || input.ParserVersion != torrentInputParserVersion {
		return false
	}
	switch input.TorrentVersion {
	case "v1":
		return isHexHash(input.InfoHashV1, 20) && input.InfoHashV2 == ""
	case "v2":
		return input.InfoHashV1 == "" && isHexHash(input.InfoHashV2, 32)
	case "hybrid":
		return isHexHash(input.InfoHashV1, 20) && isHexHash(input.InfoHashV2, 32)
	default:
		return false
	}
}

func isSHA256Digest(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) && isHexHash(strings.TrimPrefix(value, prefix), 32)
}

func isHexHash(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size
}

func torrentMetadataMatchesInput(metadata verifiedTorrentMetadata, input releaseTorrentInputRef) bool {
	filename, err := torrentInputFilename(metadata.TorrentFilename, metadata.TorrentInfoHash)
	if err != nil || !isCompleteTorrentInputRefForReuse(input) || filename != input.Filename || metadata.TorrentVersion != input.TorrentVersion {
		return false
	}
	return metadata.TorrentInfoHashV1 == input.InfoHashV1 && metadata.TorrentInfoHashV2 == input.InfoHashV2
}
