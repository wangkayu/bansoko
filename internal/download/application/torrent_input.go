package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/acquisition/torrent"
)

const maxTorrentPayloadBytes = 20 << 20

type torrentInputPayload struct {
	Payload  []byte
	Filename string
}

func (s *Service) loadTorrentInput(ctx context.Context, task taskSnapshot, input frozenInput) (torrentInputPayload, error) {
	var payload []byte
	var digest, filename, parserVersion, torrentVersion, infoHashV1, infoHashV2 string
	err := s.db.QueryRowContext(ctx, `
SELECT payload, payload_digest, filename, parser_version, torrent_version,
       COALESCE(info_hash_v1, ''), COALESCE(info_hash_v2, '')
FROM release_torrent_inputs
WHERE id = $1 AND release_id = $2`, input.TorrentInputID, task.releaseID).Scan(
		&payload, &digest, &filename, &parserVersion, &torrentVersion, &infoHashV1, &infoHashV2)
	if errors.Is(err, sql.ErrNoRows) {
		return torrentInputPayload{}, errors.New("torrent_payload_missing")
	}
	if err != nil {
		return torrentInputPayload{}, fmt.Errorf("read torrent input: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxTorrentPayloadBytes || strings.TrimSpace(filename) == "" {
		return torrentInputPayload{}, errors.New("torrent_payload_invalid")
	}
	sum := sha256.Sum256(payload)
	actualDigest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != input.TorrentPayloadDigest || digest != actualDigest {
		return torrentInputPayload{}, errors.New("torrent_payload_digest_mismatch")
	}
	if filename != input.TorrentFilename {
		return torrentInputPayload{}, errors.New("torrent_payload_filename_mismatch")
	}
	if parserVersion != input.TorrentParserVersion || torrentVersion != input.TorrentVersion {
		return torrentInputPayload{}, errors.New("torrent_payload_version_mismatch")
	}
	metadata, err := torrent.Parse(payload)
	if err != nil {
		return torrentInputPayload{}, errors.New("torrent_payload_invalid")
	}
	if metadata.TorrentVersion != torrentVersion || metadata.InfoHashV1 != infoHashV1 || metadata.InfoHashV2 != infoHashV2 || input.InfoHashV1 != infoHashV1 || input.InfoHashV2 != infoHashV2 {
		return torrentInputPayload{}, errors.New("torrent_payload_hash_mismatch")
	}
	preferredHash := metadata.InfoHashV1
	if preferredHash == "" {
		preferredHash = metadata.InfoHashV2
	}
	if input.InfoHash != "" && input.InfoHash != preferredHash {
		return torrentInputPayload{}, errors.New("torrent_payload_hash_mismatch")
	}
	return torrentInputPayload{Payload: append([]byte(nil), payload...), Filename: filename}, nil
}
