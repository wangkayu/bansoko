package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// SourceTorrentFetcher fetches one .torrent of a source through the source's
// own connection and network route. The explicit Release retry uses it for a
// Release whose torrent a refresh deliberately never fetched (a start_baseline
// item under new-releases-only, or an item the feed-layer filter rejected), and
// the automatic evaluation for a feed-filtered item that a rules change now
// lets through. A SourceFetcher without it leaves such a Release to the
// ordinary evaluation, which reports the missing file list.
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
	if snapshot.downloadTaskID.Valid {
		return false, nil
	}
	item, err := decodeFeedItemWithInternal(snapshot.rawFacts, snapshot.internalFacts, snapshot.sourceFacts, snapshot.parsedFacts, snapshot.stableItemKey)
	if err != nil {
		return false, nil
	}
	fetch, err := s.fetchMissingReleaseTorrent(ctx, snapshot, item)
	if err != nil || fetch == nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin retry torrent input: %w", err)
	}
	defer tx.Rollback()
	if _, err := storeReleaseTorrentFetchTx(ctx, tx, snapshot, fetch); err != nil {
		if errors.Is(err, errReleaseVersionConflict) {
			// Another writer moved the Release: evaluate what it holds now.
			return true, nil
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit retry torrent input: %w", err)
	}
	return true, nil
}

// ensureEvaluationTorrentInput fetches, once, the torrent a refresh skipped
// because the feed-layer filter rejected the item (torrentMetadataReuser),
// when an automatic evaluation now finds that the item passes that filter (a
// rules change). The evaluation then judges it with its file list, exactly as
// if the refresh had fetched it. It fetches nothing for an item the filter
// still rejects, an item whose fetch was already attempted (a recorded
// torrent_metadata_unavailable is the next refresh's to retry), an item the
// new-releases-only gate covers, or an evaluation that cannot accept the
// Release anyway (a provider group that is not selected, a subscription that
// does not admit work). On a fetch it advances the claim to the Release's new
// version and returns the reloaded snapshot.
func (s *Service) ensureEvaluationTorrentInput(ctx context.Context, claim workitems.Claim, snapshot releaseSnapshot) (workitems.Claim, releaseSnapshot, error) {
	if snapshot.downloadTaskID.Valid || !snapshot.admitsWork(snapshot.subscriptionState) || snapshot.parserPolicyError != nil {
		return claim, snapshot, nil
	}
	item, err := decodeFeedItemWithInternal(snapshot.rawFacts, snapshot.internalFacts, snapshot.sourceFacts, snapshot.parsedFacts, snapshot.stableItemKey)
	if err != nil || len(item.ReleaseFiles) != 0 || item.TorrentErrorCode != "" {
		return claim, snapshot, nil
	}
	if gated, gateErr := startBaselineApplies(snapshot, item.Published); gated || gateErr != nil {
		return claim, snapshot, nil
	}
	if code, _ := animedomain.FeedFilterReason(item, snapshot.effective); code != "" {
		return claim, snapshot, nil
	}
	selected, err := sourceScopeSelected(ctx, s.db, snapshot.sourceID, false)
	if err != nil || !selected {
		return claim, snapshot, err
	}
	fetch, err := s.fetchMissingReleaseTorrent(ctx, snapshot, item)
	if err != nil || fetch == nil {
		return claim, snapshot, err
	}
	advanced, err := s.workItems.MutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("evaluation torrent input transaction is nil")
		}
		return storeReleaseTorrentFetchTx(ctx, tx, snapshot, fetch)
	})
	if err != nil {
		return claim, snapshot, err
	}
	reloaded, err := s.loadReleaseSnapshot(ctx, snapshot.releaseID)
	if err != nil {
		return claim, snapshot, err
	}
	return advanced, reloaded, nil
}

// releaseTorrentFetch is one fetch of a Release's missing torrent: the item
// with the fetched file list (or the recorded fetch failure) and its encoded
// internal facts.
type releaseTorrentFetch struct {
	item          acquisitiondomain.FeedItem
	internalFacts []byte
	fetched       bool
}

// fetchMissingReleaseTorrent fetches the torrent of a Release that has no file
// list and no stored torrent input through the source's own connection. It
// returns nil when there is nothing to fetch: the fetcher cannot fetch one
// torrent, the item already holds a file list or names no torrent URL, or an
// input is stored (the evaluation recovers that one itself).
func (s *Service) fetchMissingReleaseTorrent(ctx context.Context, snapshot releaseSnapshot, item acquisitiondomain.FeedItem) (*releaseTorrentFetch, error) {
	fetcher, ok := s.sourceFetcher.(SourceTorrentFetcher)
	if !ok || len(item.ReleaseFiles) != 0 {
		return nil, nil
	}
	torrentURL := strings.TrimSpace(item.TorrentMetadataURL())
	if torrentURL == "" {
		return nil, nil
	}
	var stored int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_torrent_inputs WHERE release_id = $1`, snapshot.releaseID).Scan(&stored); err != nil {
		return nil, fmt.Errorf("read release torrent input: %w", err)
	}
	if stored > 0 {
		return nil, nil
	}
	source, err := s.loadSourceLocator(ctx, snapshot.sourceID)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("encode fetched torrent facts: %w", err)
	}
	return &releaseTorrentFetch{item: item, internalFacts: internalFacts, fetched: fetchErr == nil}, nil
}

// storeReleaseTorrentFetchTx stores one fetch on the Release at the version
// the snapshot read: its internal facts, and the torrent input when the fetch
// succeeded. A Release another writer moved (or that was admitted) reports
// errReleaseVersionConflict.
func storeReleaseTorrentFetchTx(ctx context.Context, tx *sql.Tx, snapshot releaseSnapshot, fetch *releaseTorrentFetch) (int64, error) {
	var version int64
	err := tx.QueryRowContext(ctx, `
UPDATE releases
SET internal_facts = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $3 AND download_task_id IS NULL
RETURNING version`, snapshot.releaseID, fetch.internalFacts, snapshot.releaseVersion).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: release %s moved before its torrent was stored", errReleaseVersionConflict, snapshot.releaseID)
	}
	if err != nil {
		return 0, fmt.Errorf("store fetched torrent facts: %w", err)
	}
	if fetch.fetched {
		if err := persistReleaseTorrentInput(ctx, tx, snapshot.releaseID, fetch.item); err != nil {
			return 0, err
		}
		// The torrent identity write can advance the Release version again.
		if err := tx.QueryRowContext(ctx, `SELECT version FROM releases WHERE id = $1`, snapshot.releaseID).Scan(&version); err != nil {
			return 0, fmt.Errorf("read release version after torrent input: %w", err)
		}
	}
	return version, nil
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
