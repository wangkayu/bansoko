package application

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/anime/animeconfig"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/anime/metadatabinding"
	"bansoko/internal/parserpolicy"
)

func (s *Service) loadSourceSnapshot(ctx context.Context, sourceID string) (sourceSnapshot, error) {
	var snapshot sourceSnapshot
	var sourceKind, subscriptionState string
	var httpFeedConnection, networkRoute, provider, providerEntryRef, providerGroupRef sql.NullString
	var bgmWorkID, tmdbWorkID, tmdbMediaKind, mediaKindOverride sql.NullString
	var tmdbSeason sql.NullInt64
	var cursorJSON, lastGoodJSON []byte
	var policyKind sql.NullString
	var policyVersion sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
SELECT src.id, src.subscription_id, sub.managed_anime_id, src.source_kind,
       src.locator, src.locator_display, src.http_feed_connection_ref, src.network_route_ref,
       src.provider, src.provider_entry_ref, src.provider_group_ref,
       src.group_name, src.title, src.episode_offset, src.version,
 sub.state, selection.id IS NOT NULL, COALESCE(selection.effective_selected AND selection.available, FALSE),
       rt.version, rt.cursor, rt.last_good_facts, COALESCE(rt.etag, ''), COALESCE(rt.last_modified, ''), rt.consecutive_failures,
       (sub.resumed_at IS NOT NULL AND rt.created_at < sub.resumed_at AND rt.resume_baseline_at IS DISTINCT FROM sub.resumed_at),
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season, m.media_kind_override,
       m.version, COALESCE(rules.rules_version, 1),
       pp.parser_kind, pp.parser_version,
       sub.new_releases_only, sub.start_baseline_at
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN rss_source_runtime rt ON rt.rss_source_id = src.id
JOIN anime_metadata m ON m.managed_anime_id = sub.managed_anime_id
LEFT JOIN provider_group_selections selection
  ON selection.subscription_id = src.subscription_id
 AND selection.provider = src.provider
 AND selection.group_ref = src.provider_group_ref
LEFT JOIN anime_business_configs rules
  ON rules.managed_anime_id = sub.managed_anime_id
LEFT JOIN anime_business_configs pp
  ON pp.managed_anime_id = sub.managed_anime_id
WHERE src.id = $1`, sourceID).Scan(
		&snapshot.sourceID, &snapshot.subscriptionID, &snapshot.managedAnimeID, &sourceKind,
		&snapshot.locator, &snapshot.locatorDisplay, &httpFeedConnection, &networkRoute,
		&provider, &providerEntryRef, &providerGroupRef,
		&snapshot.groupName, &snapshot.title, &snapshot.episodeOffset, &snapshot.sourceVersion,
		&subscriptionState, &snapshot.selectionExists, &snapshot.providerSelected,
		&snapshot.runtimeVersion, &cursorJSON, &lastGoodJSON, &snapshot.etag, &snapshot.lastModified, &snapshot.consecutiveFailures,
		&snapshot.resumeBaselinePending,
		&bgmWorkID, &tmdbWorkID, &tmdbMediaKind, &tmdbSeason, &mediaKindOverride,
		&snapshot.metadataVersion, &snapshot.rulesVersion,
		&policyKind, &policyVersion,
		&snapshot.newReleasesOnly, &snapshot.startBaselineAt); err != nil {
		if err == sql.ErrNoRows {
			return sourceSnapshot{}, fmt.Errorf("RSS source %q not found: %w", sourceID, err)
		}
		return sourceSnapshot{}, fmt.Errorf("read RSS source snapshot: %w", err)
	}
	policy, err := instanceParserPolicyFromColumns(snapshot.managedAnimeID, policyKind, policyVersion)
	if err != nil {
		return sourceSnapshot{}, err
	}
	snapshot.parserKind = string(policy.Policy.Kind)
	snapshot.parserVersion = policy.Version
	snapshot.sourceKind = animedomain.SourceKind(sourceKind)
	snapshot.httpFeedConnection = httpFeedConnection.String
	snapshot.networkRoute = networkRoute.String
	snapshot.provider = provider.String
	snapshot.providerEntryRef = providerEntryRef.String
	snapshot.providerGroupRef = providerGroupRef.String
	snapshot.subscriptionState = subscriptionState
	if snapshot.resumeBaselinePending {
		// The baseline refresh after a resume must read the whole feed window:
		// a conditional 304 would record nothing, and releases the retention
		// janitor purged while stopped would then return on a later refresh.
		snapshot.etag, snapshot.lastModified = "", ""
	}
	snapshot.cursorJSON = append([]byte(nil), cursorJSON...)
	snapshot.lastGoodJSON = append([]byte(nil), lastGoodJSON...)

	bgm, tmdb, err := metadataBindingsFromColumns(bgmWorkID, tmdbWorkID, tmdbMediaKind, tmdbSeason, snapshot.managedAnimeID)
	if err != nil {
		return sourceSnapshot{}, err
	}
	snapshot.metadataHold, err = metadataHoldFromBindings(bgm, tmdb)
	if err != nil {
		return sourceSnapshot{}, fmt.Errorf("map metadata hold: %w", err)
	}
	snapshot.mediaKind, snapshot.mediaSeason, snapshot.mediaSeasonBound = frozenMediaBinding(tmdb, nullableStringPtr(mediaKindOverride))

	effective, err := s.loadEffectiveConfig(ctx, snapshot.managedAnimeID)
	if err != nil {
		return sourceSnapshot{}, err
	}
	snapshot.effective = effective
	snapshot.profileVersion = effective.Versions[animedomain.SectionParser]
	return snapshot, nil
}

// instanceParserPolicyFromColumns projects the LEFT JOIN columns of
// anime_business_configs into the validated frozen policy. A missing row
// (NULL columns) and an unusable row both raise the shared typed integrity
// failure: no production path substitutes the code default for a missing
// instance row.
func instanceParserPolicyFromColumns(managedAnimeID string, kind sql.NullString, version sql.NullInt64) (parserpolicy.Instance, error) {
	if !kind.Valid || !version.Valid {
		return parserpolicy.Instance{}, &parserpolicy.InstancePolicyRequiredError{ManagedAnimeID: managedAnimeID}
	}
	return parserpolicy.InstanceFromRow(managedAnimeID, kind.String, 0, version.Int64)
}

func metadataBindingsFromColumns(bgmWorkID, tmdbWorkID, tmdbMediaKind sql.NullString, tmdbSeason sql.NullInt64, managedAnimeID string) (*animedomain.BGMBinding, *animedomain.TMDBBinding, error) {
	return metadatabinding.BindingsFromColumns(bgmWorkID, tmdbWorkID, tmdbMediaKind, tmdbSeason, managedAnimeID)
}

func metadataHoldFromBindings(bgm *animedomain.BGMBinding, tmdb *animedomain.TMDBBinding) (animedomain.MetadataHold, error) {
	return animedomain.MetadataHoldFromBindings(bgm, tmdb), nil
}

// frozenMediaBinding projects the TMDB metadata binding and the owner
// media-kind override into the frozen media kind/season context one release
// identity resolves with. The override wins when set; otherwise a missing
// binding is the historical TV default (season unset) and a movie binding
// short-circuits the resolver to the canonical movie:0 identity. The bound
// season participates in the TV coverage key; a bound season 0 keeps the
// historical default of season 1 (SeasonBound stays false on the automatic
// path; only explicit continuous-SP recognition opts into preserving 0).
//
// The projection itself lives in anime/domain so the saved-revision preview and
// the one-shot admission resolve the identical binding; this wrapper only keeps
// the local call sites unchanged.
func frozenMediaBinding(tmdb *animedomain.TMDBBinding, mediaKindOverride *string) (mediaKind string, season int, seasonBound bool) {
	return animedomain.FrozenMediaBinding(tmdb, mediaKindOverride)
}

// nullableStringPtr projects a nullable text column into the optional owner
// value the domain projection takes (nil means unset).
func nullableStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	trimmed := value.String
	return &trimmed
}
func (s *Service) loadEffectiveConfig(ctx context.Context, managedAnimeID string) (animedomain.EffectiveConfig, error) {
	return loadEffectiveConfig(ctx, s.db, managedAnimeID)
}

func loadResolvedConfig(ctx context.Context, q queryer, managedAnimeID string) (animedomain.ResolvedSections, animedomain.EffectiveConfig, error) {
	resolved, live, err := animeconfig.LoadResolvedWithLive(ctx, q, managedAnimeID)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	effective, err := animedomain.EffectiveConfigFromSections(resolved)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	live.Apply(&effective)
	return resolved, effective, nil
}

// loadEffectiveConfig resolves one instance's effective rules against saved
// Global values. The implementation lives in the shared animeconfig package so
// RSS evaluation and Artifact per-file recognition cannot drift apart.
func loadEffectiveConfig(ctx context.Context, q queryer, managedAnimeID string) (animedomain.EffectiveConfig, error) {
	_, effective, err := loadResolvedConfig(ctx, q, managedAnimeID)
	return effective, err
}
