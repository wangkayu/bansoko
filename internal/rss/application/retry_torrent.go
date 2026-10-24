package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	acquisitionapplication "bansoko/internal/acquisition/application"
	animedomain "bansoko/internal/anime/domain"
)

// SourceTorrentFetcher fetches one .torrent of a source through the source's
// own connection and network route. The explicit Release retry uses it for a
// Release whose torrent a refresh deliberately never fetched (a start_baseline
// item under new-releases-only). A SourceFetcher without it leaves such a Release to
// the ordinary evaluation, which reports the missing file list.
type SourceTorrentFetcher interface {
	FetchSourceTorrent(ctx context.Context, source SourceLocator, torrentURL string) (acquisitionapplication.TorrentPayload, error)
}

// ensureRetryTorrentInput fetches, once, the torrent of a Release that has no
// file list and no stored torrent input, so the explicit retry evaluates it
// through every rule like any fetched item. The fetched bytes are stored as
// the Release's torrent input and its facts carry the file list; a failed
// fetch is stored as torrent_metadata_unavailable, which the evaluation turns
// into the ordinary visible parse failure. It reports whether the Release row
// changed, in which case the caller reloads its snapshot.
func (s *Service) ensureRetryTorrentInput(ctx context.Context, snapshot releaseSnapshot) (bool, error) {
	fetcher, ok := s.sourceFetcher.(SourceTorrentFetcher)
	if !ok || snapshot.downloadTaskID.Valid {
		return false, nil
	}
	item, err := decodeFeedItemWithInternal(snapshot.rawFacts, snapshot.internalFacts, snapshot.sourceFacts, snapshot.parsedFacts, snapshot.stableItemKey)
	if err != nil || len(item.ReleaseFiles) != 0 {
		return false, nil
	}
	torrentURL := strings.TrimSpace(item.TorrentMetadataURL())
	if torrentURL == "" {
		return false, nil
	}
	var stored int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_torrent_inputs WHERE release_id = $1`, snapshot.releaseID).Scan(&stored); err != nil {
		return false, fmt.Errorf("read release torrent input: %w", err)
	}
	if stored > 0 {
		// A stored input is recovered by the evaluation itself.
		return false, nil
	}
	source, err := s.loadSourceLocator(ctx, snapshot.sourceID)
	if err != nil {
		return false, err
	}
	payload, fetchErr := fetcher.FetchSourceTorrent(ctx, source, torrentURL)
	if fetchErr != nil {
		item.TorrentErrorCode = "torrent_metadata_unavailable"
		item.TorrentErrorReason = safeReason(fetchErr.Error())
	} else {
		acquisitionapplication.ApplyTorrentPayload(&item, payload)
		item.TorrentErrorCode, item.TorrentErrorReason = "", ""
	}
	internalFacts, err := encodeInternalFeedFacts(item)
	if err != nil {
		return false, fmt.Errorf("encode retry torrent facts: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin retry torrent input: %w", err)
	}
	defer tx.Rollback()
	var version int64
	err = tx.QueryRowContext(ctx, `
UPDATE releases
SET internal_facts = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $3 AND download_task_id IS NULL
RETURNING version`, snapshot.releaseID, internalFacts, snapshot.releaseVersion).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		// Another writer moved the Release: evaluate what it holds now.
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("store retry torrent facts: %w", err)
	}
	if fetchErr == nil {
		if err := persistReleaseTorrentInput(ctx, tx, snapshot.releaseID, item); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit retry torrent input: %w", err)
	}
	return true, nil
}

func (s *Service) loadSourceLocator(ctx context.Context, sourceID string) (SourceLocator, error) {
	var kind string
	var connection, route, provider, entryRef, groupRef sql.NullString
	source := SourceLocator{}
	if err := s.db.QueryRowContext(ctx, `
SELECT source_kind, locator, http_feed_connection_ref, network_route_ref, provider, provider_entry_ref, provider_group_ref
FROM rss_sources WHERE id = $1`, sourceID).Scan(&kind, &source.URL, &connection, &route, &provider, &entryRef, &groupRef); err != nil {
		return SourceLocator{}, fmt.Errorf("read RSS source locator: %w", err)
	}
	source.Kind = animedomain.SourceKind(kind)
	source.HTTPFeedConnection = connection.String
	source.NetworkRoute = route.String
	source.Provider = provider.String
	source.ProviderEntryRef = entryRef.String
	source.ProviderGroupRef = groupRef.String
	return source, nil
}
