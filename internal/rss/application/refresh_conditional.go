package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// feedValidators are the response validators persisted for the next
// conditional request. Empty values clear the stored validators.
type feedValidators struct {
	etag         string
	lastModified string
}

// completeFeedValidators keeps the response validators only when every item of
// the answer carries complete torrent metadata. A 304 skips all item work, so a
// stored validator must never hide an item whose .torrent still has to be
// fetched again (torrent_metadata_unavailable is retried by the next full read).
func completeFeedValidators(exchange *acquisitionapplication.FeedExchange, feed []feedEvaluation) feedValidators {
	if exchange == nil {
		return feedValidators{}
	}
	for _, evaluation := range feed {
		if evaluation.item.TorrentErrorCode != "" {
			return feedValidators{}
		}
	}
	return feedValidators{etag: exchange.ResponseETag, lastModified: exchange.ResponseLastModified}
}

// refreshNotModifiedWriter records a 304 answer: the source was checked and is
// unchanged, so only the refresh and success times move. Cursor, last-good
// facts, validators and releases stay as they are.
func (s *Service) refreshNotModifiedWriter(claim workitems.Claim, snapshot sourceSnapshot, effect *circuitEffect) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("RSS refresh owner writer transaction is nil")
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `
UPDATE rss_source_runtime
SET last_refresh_at = CURRENT_TIMESTAMP, last_success_at = CURRENT_TIMESTAMP,
    health = 'ready', reason_code = NULL, reason_detail = NULL,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE rss_source_id = $1 AND version = $2
RETURNING version`, snapshot.sourceID, claim.ExpectedOwnerVersion).Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("RSS source runtime version conflict")
			}
			return 0, fmt.Errorf("update RSS not-modified runtime: %w", err)
		}
		if err := resetRefreshFailuresTx(ctx, tx, snapshot, effect); err != nil {
			return 0, err
		}
		return version, nil
	}
}

// torrentMetadataReuser returns the pre-fetch hook that keeps a refresh from
// downloading a .torrent again for a release this source already holds:
//
//   - an admitted release (download task or terminal verdict) is immutable, so
//     its metadata is never needed again;
//
//   - a release whose stored torrent input was fetched from the same URL and
//     whose stored metadata verifies against it is rebuilt from that metadata,
//     exactly as a fresh download would describe it.
//
//   - an item the subscription's new-releases-only gate covers (published before the
//     start baseline, or without a publish time) is never fetched: its
//     Release takes start_baseline from the publish time alone, and an
//     explicit retry fetches the torrent then (retryTorrentInput).
//
//   - an item the feed-layer filter rejects (animedomain.FeedFilterReason:
//     the AniBT tag row, the title tag row and the keyword filter) is not
//     fetched when the source holds no torrent input for it: that filter
//     reads the feed's own facts only, so its Release is filtered with the
//     same reason whether or not the torrent was read. Should a later rules
//     change let it pass, its evaluation fetches the torrent then
//     (ensureEvaluationTorrentInput), and an explicit retry does too.
//
// The URL match ignores the credentials in the URL (torrentSourceMatches), so
// a rotated passkey or rsskey does not refetch every release. Any read error
// or mismatch falls back to fetching.
func (s *Service) torrentMetadataReuser(ctx context.Context, snapshot sourceSnapshot) func(acquisitiondomain.FeedItem) (acquisitiondomain.FeedItem, bool) {
	return func(item acquisitiondomain.FeedItem) (acquisitiondomain.FeedItem, bool) {
		if snapshot.newReleasesOnly && snapshot.startBaselineAt.Valid && StartBaselineCovers(snapshot.startBaselineAt.Time, item.Published) {
			return item, true
		}
		_, stableKey := publicStableItemKey(item)
		var releaseID, verdict string
		var downloadTask sql.NullString
		var internalFacts []byte
		// feedFiltered skips the fetch of an item the feed-layer filter
		// rejects; it is consulted only where the source holds no torrent
		// input to keep, so a stored file list is never replaced by none.
		feedFiltered := func() bool {
			code, _ := animedomain.FeedFilterReason(item, snapshot.effective)
			return code != ""
		}
		err := s.db.QueryRowContext(ctx, `
SELECT id, verdict, download_task_id, internal_facts
FROM releases WHERE rss_source_id = $1 AND stable_item_key = $2`, snapshot.sourceID, stableKey).Scan(&releaseID, &verdict, &downloadTask, &internalFacts)
		if errors.Is(err, sql.ErrNoRows) {
			return item, feedFiltered()
		}
		if err != nil {
			return item, false
		}
		if downloadTask.Valid || isTerminalReleaseVerdict(verdict) {
			return item, true
		}
		var input releaseTorrentInputRef
		if err := s.db.QueryRowContext(ctx, `
SELECT id, payload_digest, source_url_digest, filename, parser_version, torrent_version,
       COALESCE(info_hash_v1, ''), COALESCE(info_hash_v2, '')
FROM release_torrent_inputs WHERE release_id = $1`, releaseID).Scan(&input.ID, &input.PayloadDigest, &input.SourceURLDigest, &input.Filename, &input.ParserVersion, &input.TorrentVersion, &input.InfoHashV1, &input.InfoHashV2); err != nil {
			if errors.Is(err, sql.ErrNoRows) && !releaseFactsHoldFiles(internalFacts) {
				return item, feedFiltered()
			}
			return item, false
		}
		if !torrentSourceMatches(item.TorrentMetadataURL(), input.SourceURLDigest) {
			return item, false
		}
		candidate := item
		candidate.TorrentErrorCode = "torrent_metadata_unavailable"
		reused, ok := reuseVerifiedTorrentMetadata(internalFacts, candidate, input)
		if !ok {
			return item, false
		}
		reused.TorrentErrorCode, reused.TorrentErrorReason = "", ""
		// Mirror the download path: a feed without a declared length takes
		// the torrent's total size.
		if reused.EnclosureLength == 0 {
			reused.EnclosureLength = reused.TorrentSizeBytes
		}
		return reused, true
	}
}

// releaseFactsHoldFiles reports whether a stored Release's internal facts
// carry a torrent file list. Undecodable facts count as holding one, so the
// pre-fetch hook falls back to fetching rather than replacing them.
func releaseFactsHoldFiles(internalFacts []byte) bool {
	if len(internalFacts) == 0 || string(internalFacts) == "null" {
		return false
	}
	var stored storedFeedFacts
	if err := json.Unmarshal(internalFacts, &stored); err != nil {
		return true
	}
	return len(stored.ReleaseFiles) > 0
}
