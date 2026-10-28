package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/apperrors"
)

// workspaceReadOptions narrows a workspace read to what one consumer renders.
type workspaceReadOptions struct {
	// releasesPerSource > 0 reads only the newest N releases of each RSS source;
	// 0 reads them all.
	releasesPerSource int
	// varsEvidence reads each release's stored source facts and torrent input
	// hashes with the release row.
	varsEvidence bool
	// releasesOnly skips the sections the owner RSS list never renders
	// (downloads, artifacts, file operations, handoffs, targets, logs, episode
	// progress) and the per-release target sets.
	releasesOnly bool
	// fileIdentity reads each release's torrent payload digest so the RSS list
	// view can derive a file_ref for every row it lists.
	fileIdentity bool
}

// GetAnimeWorkspaceApplication returns a transport-neutral copy of the owner-chain read model.
func (s *Service) GetAnimeWorkspaceApplication(ctx context.Context, managedAnimeID string) (AnimeWorkspace, error) {
	return s.readAnimeWorkspace(ctx, managedAnimeID, workspaceReadOptions{})
}

func (s *Service) readAnimeWorkspace(ctx context.Context, managedAnimeID string, options workspaceReadOptions) (AnimeWorkspace, error) {
	var bgmWorkID, tmdbWorkID, tmdbMediaKind, mediaKindOverride sql.NullString
	var tmdbSeason, totalEpisodes, year, ownerSeason sql.NullInt32
	var titleOverride, providerTitle, originEntryRef, instanceLabel sql.NullString
	var retiredAt sql.NullTime
	var origin, state string
	var managedAnime ManagedAnime
	var subscription Subscription
	var metadata AnimeMetadata
	var startBaselineAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT a.id, a.title, a.title_override, a.provider_title, a.version,
       sub.id, sub.origin, sub.origin_entry_ref, sub.instance_label, sub.state, sub.version,
       a.lifecycle_state, a.fence_epoch, a.retired_at,
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season, m.media_kind_override,
       m.total_episodes, m.year, m.season, m.version, sub.new_releases_only, sub.start_baseline_at
FROM managed_anime a
JOIN subscriptions sub ON sub.managed_anime_id = a.id
JOIN anime_metadata m ON m.managed_anime_id = a.id
WHERE a.id = $1`, managedAnimeID).Scan(
		&managedAnime.ID, &managedAnime.Title, &titleOverride, &providerTitle, &managedAnime.Version,
		&subscription.ID, &origin, &originEntryRef, &instanceLabel, &state, &subscription.Version,
		&subscription.LifecycleState, &subscription.LifecycleEpoch, &retiredAt,
		&bgmWorkID, &tmdbWorkID, &tmdbMediaKind, &tmdbSeason, &mediaKindOverride, &totalEpisodes, &year, &ownerSeason, &metadata.Version, &subscription.NewReleasesOnly, &startBaselineAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AnimeWorkspace{}, &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return AnimeWorkspace{}, fmt.Errorf("read anime workspace: %w", err)
	}
	managedAnime.ID = managedAnimeID
	managedAnime.TitleOverride = nullableStringPointer(titleOverride)
	metadata.ManagedAnimeID = managedAnimeID
	metadata.TitleOverride = nullableStringPointer(titleOverride)
	metadata.BGM, metadata.TMDB = applicationBindingsFromRow(bgmWorkID, tmdbWorkID, tmdbMediaKind, tmdbSeason)
	metadata.MediaKindOverride = nullableStringPointer(mediaKindOverride)
	metadata.TotalEpisodes = nullableIntPointer(totalEpisodes)
	metadata.Year = nullableIntPointer(year)
	metadata.Season = nullableIntPointer(ownerSeason)
	var bgmID, tmdbID, tmdbKind string
	if metadata.BGM != nil {
		bgmID = metadata.BGM.WorkID
	}
	if metadata.TMDB != nil {
		tmdbID, tmdbKind = metadata.TMDB.WorkID, metadata.TMDB.MediaKind
	}
	if metadata.TitleSuggestions, err = s.readTitleSuggestions(ctx, bgmID, tmdbID, tmdbKind, providerTitle.String); err != nil {
		return AnimeWorkspace{}, fmt.Errorf("read title suggestions: %w", err)
	}
	subscription.ManagedAnimeID = managedAnimeID
	subscription.Origin, subscription.OriginEntryRef, subscription.InstanceLabel = origin, nullableStringPointer(originEntryRef), nullableStringPointer(instanceLabel)
	subscription.State = subscriptionStateFromDB(state)
	if startBaselineAt.Valid {
		subscription.StartBaselineAt = &startBaselineAt.Time
	}
	if retiredAt.Valid {
		subscription.RetiredAt = &retiredAt.Time
	}

	subgroups, err := s.readSubgroupsOwnerApplication(ctx, managedAnimeID, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	rules, err := s.readRulesOwnerApplication(ctx, managedAnimeID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	sources, err := s.readRSSSourcesApplication(ctx, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	runtimes, err := s.readRSSSourceRuntimesApplication(ctx, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	var (
		targets      []AcquisitionTarget
		observations []TargetObservation
		byRelease    map[string]releaseTargetSet
	)
	if !options.releasesOnly {
		targets, observations, byRelease, err = s.readAcquisitionTargetsApplication(ctx, subscription.ID)
		if err != nil {
			return AnimeWorkspace{}, err
		}
	}
	releases, err := s.readReleasesApplication(ctx, subscription.ID, releaseReadOptions{perSourceLimit: options.releasesPerSource, targets: byRelease, vars: options.varsEvidence, fileIdentity: options.fileIdentity})
	if err != nil {
		return AnimeWorkspace{}, err
	}
	if options.releasesOnly {
		return AnimeWorkspace{
			ManagedAnime: managedAnime, Metadata: metadata,
			MetadataHold: metadataHoldFromApplication(metadata.BGM, metadata.TMDB), Subscription: subscription,
			Subgroups: subgroups, Rules: rules, RSSSources: sources, RSSSourceRuntimes: runtimes,
			Releases: releases,
		}, nil
	}
	tasks, err := s.readDownloadTasksApplication(ctx, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	artifacts, err := s.readArtifactsApplication(ctx, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	operations, err := s.readFileOperationsApplication(ctx, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	placementHandoffs, err := s.readPlacementHandoffsApplication(ctx, subscription.ID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	logs, err := s.readSubscriptionLogsApplication(ctx, subscription.ID, managedAnimeID)
	if err != nil {
		return AnimeWorkspace{}, err
	}
	var progressEpisodes sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT ARRAY_TO_STRING(ARRAY_AGG(DISTINCT keys.episode ORDER BY keys.episode), ',')
FROM (
  SELECT substring(pc.observation_key FROM '^tv:(?:s[0-9]+:)?e([0-9]+)$')::int AS episode
  FROM placements p JOIN placement_coverage pc ON pc.placement_id = p.id
  WHERE p.managed_anime_id = $1 AND p.state = 'active' AND p.verification_state = 'verified'
    AND p.replacement_eligible AND pc.observation_key ~ '^tv:(?:s[0-9]+:)?e[0-9]+$'
) keys`, managedAnimeID).Scan(&progressEpisodes); err != nil {
		return AnimeWorkspace{}, fmt.Errorf("read anime episode progress: %w", err)
	}
	var episodeProgress *EpisodeProgress
	if value := buildApplicationEpisodeProgress(progressEpisodes, metadata.TotalEpisodes, nil); value != nil {
		episodeProgress = value
	}
	return AnimeWorkspace{
		ManagedAnime: managedAnime, Metadata: metadata,
		MetadataHold: metadataHoldFromApplication(metadata.BGM, metadata.TMDB), Subscription: subscription,
		Subgroups: subgroups, Rules: rules, RSSSources: sources, RSSSourceRuntimes: runtimes,
		Releases: releases, DownloadTasks: tasks, Artifacts: artifacts, FileOperations: operations,
		PlacementHandoffs:  placementHandoffs,
		AcquisitionTargets: &targets, TargetObservations: &observations, Logs: logs, EpisodeProgress: episodeProgress,
	}, nil
}
