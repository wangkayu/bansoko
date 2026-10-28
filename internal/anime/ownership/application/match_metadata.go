package application

import (
	animeapplication "bansoko/internal/anime/application"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/metadata/backfill"
	"bansoko/internal/metadata/matcher"
	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

type metadataMatchTarget struct {
	title             string
	origin            string
	entryRef          sql.NullString
	state             sql.NullString
	bgmWorkID         sql.NullString
	tmdbWorkID        sql.NullString
	tmdbKind          sql.NullString
	tmdbSeason        sql.NullInt64
	totalEpisodes     sql.NullInt64
	mediaKindOverride sql.NullString
	year              sql.NullInt64
	season            sql.NullInt64
	version           sql.NullInt64
}

// MatchAnimeMetadata runs the configured matcher outside the write transaction,
// then fills only missing metadata fields after an active-subscription and CAS
// check. The Idempotency-Key dedupes repeated clicks / retries: a replay with
// the same key and request body returns the original result without re-running
// the provider match or touching metadata. Provider/configuration failures are
// stable response outcomes.
func (s *Service) MatchAnimeMetadata(ctx context.Context, managedAnimeID string, input MetadataMatchInput, idempotencyKey string) (MetadataMatchResult, error) {
	requestHash, err := animeapplication.CanonicalJSONHash(input)
	if err != nil {
		return MetadataMatchResult{}, fmt.Errorf("hash metadata match request: %w", err)
	}

	lockConn, err := s.db.Conn(ctx)
	if err != nil {
		return MetadataMatchResult{}, fmt.Errorf("acquire metadata match connection: %w", err)
	}
	defer lockConn.Close()
	lockKey := managedAnimeID + ":" + idempotencyKey
	if _, err := lockConn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return MetadataMatchResult{}, fmt.Errorf("lock metadata match idempotency: %w", err)
	}
	defer func() {
		_, _ = lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	}()

	// The advisory lock serializes the complete key across application
	// instances while leaving provider execution outside the owner write transaction.
	if replay, found, err := readMetadataMatchIdempotency(ctx, s.db, managedAnimeID, idempotencyKey, requestHash); err != nil {
		return MetadataMatchResult{}, err
	} else if found {
		return replay, nil
	}

	target, err := readMetadataMatchTarget(ctx, s.db, managedAnimeID)
	if err != nil {
		return MetadataMatchResult{}, err
	}
	if target.state.String != "active" {
		return MetadataMatchResult{}, &apperrors.Business{
			Status:  409,
			Code:    "SUBSCRIPTION_INACTIVE",
			Message: "Metadata matching requires an active subscription.",
		}
	}
	if !target.version.Valid {
		return MetadataMatchResult{}, &apperrors.NotFound{Resource: "anime_metadata", ID: managedAnimeID}
	}
	if target.version.Int64 != input.ExpectedVersion {
		return MetadataMatchResult{}, &apperrors.VersionConflict{Owner: "anime_metadata", CurrentVersion: target.version.Int64}
	}

	current := target.metadata(managedAnimeID)
	if current.TMDB != nil && !validMetadataMatchTMDB(current.TMDB) {
		return MetadataMatchResult{}, fmt.Errorf("stored metadata has invalid TMDB binding")
	}
	if metadataMatchComplete(current) {
		return s.commitMetadataMatch(ctx, managedAnimeID, input.ExpectedVersion, MetadataMatchOutcomeComplete, nil, "Metadata is already complete.", false, idempotencyKey, requestHash)
	}
	if s.metadataMatcher == nil {
		return s.commitMetadataMatch(ctx, managedAnimeID, input.ExpectedVersion, MetadataMatchOutcomeConfigError, nil, "Metadata matching is not configured.", false, idempotencyKey, requestHash)
	}

	entryRef := ""
	if target.entryRef.Valid {
		entryRef = target.entryRef.String
	}
	match, matchErr := s.metadataMatcher.Match(matcher.WithAffectedAnime(ctx, managedAnimeID), target.origin, entryRef, target.title,
		target.bgmWorkID.Valid,
		target.tmdbWorkID.Valid && target.tmdbKind.Valid && target.tmdbSeason.Valid)
	outcome := MetadataMatchOutcomeNoResult
	reason := "No metadata match was found."
	if matchErr != nil {
		if matcher.IsMatchErrorKind(matchErr, matcher.ErrorConfig) {
			outcome = MetadataMatchOutcomeConfigError
			reason = "Metadata matching configuration is unavailable."
		} else {
			outcome = MetadataMatchOutcomeProviderError
			reason = "A metadata provider could not be reached."
		}
	} else if match != nil {
		if match.TmdbKind != nil && !validMetadataMatchKind(*match.TmdbKind) {
			return MetadataMatchResult{}, fmt.Errorf("metadata matcher returned invalid TMDB media kind")
		}
		if match.TmdbSeason != nil && *match.TmdbSeason < 0 {
			return MetadataMatchResult{}, fmt.Errorf("metadata matcher returned invalid TMDB season")
		}
	}

	plan := backfill.Merge(storedMetadata(current), match)
	if matchErr == nil {
		switch backfill.Classify(plan, match) {
		case backfill.OutcomeComplete:
			outcome = MetadataMatchOutcomeComplete
			reason = "Metadata matched successfully."
		case backfill.OutcomePartial:
			outcome = MetadataMatchOutcomePartial
			reason = "Metadata matching found only part of the required identity."
		}
	}
	return s.commitMetadataMatch(ctx, managedAnimeID, input.ExpectedVersion, outcome, match, reason, matchErr == nil && plan.Changed(), idempotencyKey, requestHash)
}

// readMetadataMatchIdempotency replays a previous metadata_match response when
// the key + request hash match; a key reused with a different body is a typed
// conflict. found=false means no record exists yet.
func readMetadataMatchIdempotency(ctx context.Context, db *sql.DB, managedAnimeID, idempotencyKey, requestHash string) (MetadataMatchResult, bool, error) {
	var existingHash string
	var snapshot []byte
	err := db.QueryRowContext(ctx, `
SELECT request_hash, result_snapshot
FROM idempotency_records
WHERE kind = 'metadata_match' AND managed_anime_id = $1 AND idempotency_key = $2`, managedAnimeID, idempotencyKey).Scan(&existingHash, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return MetadataMatchResult{}, false, nil
	}
	if err != nil {
		return MetadataMatchResult{}, false, fmt.Errorf("read metadata match idempotency: %w", err)
	}
	if existingHash != requestHash {
		return MetadataMatchResult{}, false, animeapplication.MetadataMatchIdempotencyReusedError()
	}
	var result MetadataMatchResult
	if err := json.Unmarshal(snapshot, &result); err != nil {
		return MetadataMatchResult{}, false, fmt.Errorf("decode metadata match idempotency snapshot: %w", err)
	}
	return result, true, nil
}

// writeMetadataMatchIdempotencyTx persists the response in the owner transaction
// so a committed metadata mutation always has a replay record.
func writeMetadataMatchIdempotencyTx(ctx context.Context, tx *sql.Tx, managedAnimeID, idempotencyKey, requestHash string, result MetadataMatchResult) error {
	snapshot, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode metadata match idempotency: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
VALUES ('metadata_match', $1, $2, $3, $4)`, idempotencyKey, requestHash, managedAnimeID, snapshot); err != nil {
		return fmt.Errorf("record metadata match idempotency: %w", err)
	}
	return nil
}

func readMetadataMatchTarget(ctx context.Context, db *sql.DB, managedAnimeID string) (metadataMatchTarget, error) {
	var target metadataMatchTarget
	err := db.QueryRowContext(ctx, `
SELECT ma.title, s.origin, s.origin_entry_ref, s.state,
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season,
       m.total_episodes, m.media_kind_override, m.year, m.season, m.version
FROM managed_anime ma
JOIN subscriptions s ON s.managed_anime_id = ma.id
JOIN anime_metadata m ON m.managed_anime_id = ma.id
WHERE ma.id = $1`, managedAnimeID).Scan(
		&target.title, &target.origin, &target.entryRef, &target.state,
		&target.bgmWorkID, &target.tmdbWorkID, &target.tmdbKind, &target.tmdbSeason,
		&target.totalEpisodes, &target.mediaKindOverride, &target.year, &target.season, &target.version)
	if errors.Is(err, sql.ErrNoRows) {
		return metadataMatchTarget{}, &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return metadataMatchTarget{}, fmt.Errorf("read metadata match target: %w", err)
	}
	if !target.state.Valid {
		return metadataMatchTarget{}, &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
	}
	if !target.version.Valid {
		return metadataMatchTarget{}, &apperrors.NotFound{Resource: "anime_metadata", ID: managedAnimeID}
	}
	return target, nil
}

func (t metadataMatchTarget) metadata(managedAnimeID string) MetadataMatchMetadata {
	result := MetadataMatchMetadata{ManagedAnimeID: managedAnimeID, Version: t.version.Int64}
	if t.bgmWorkID.Valid {
		result.BGM = &MetadataMatchBGM{WorkID: t.bgmWorkID.String}
	}
	if t.tmdbWorkID.Valid || t.tmdbKind.Valid || t.tmdbSeason.Valid {
		if t.tmdbWorkID.Valid && t.tmdbKind.Valid && t.tmdbSeason.Valid {
			result.TMDB = &MetadataMatchTMDB{WorkID: t.tmdbWorkID.String, MediaKind: t.tmdbKind.String, Season: int(t.tmdbSeason.Int64)}
		}
	}
	if t.totalEpisodes.Valid {
		episodes := int(t.totalEpisodes.Int64)
		result.TotalEpisodes = &episodes
	}
	result.MediaKindOverride = animeapplication.NullableStringPointer(t.mediaKindOverride)
	if t.year.Valid {
		year := int(t.year.Int64)
		result.Year = &year
	}
	if t.season.Valid {
		season := int(t.season.Int64)
		result.Season = &season
	}
	return result
}

func validMetadataMatchKind(value string) bool {
	return value == "tv" || value == "movie"
}

func validMetadataMatchTMDB(binding *MetadataMatchTMDB) bool {
	if binding == nil || binding.WorkID == "" || binding.Season < 0 {
		return false
	}
	return validMetadataMatchKind(binding.MediaKind)
}

func metadataMatchComplete(metadata MetadataMatchMetadata) bool {
	return metadata.BGM != nil && metadata.BGM.WorkID != "" && validMetadataMatchTMDB(metadata.TMDB)
}

// storedMetadata projects the owner view onto the shared backfill input.
func storedMetadata(m MetadataMatchMetadata) backfill.Stored {
	stored := backfill.Stored{TotalEpisodes: m.TotalEpisodes, Year: m.Year, Season: m.Season, MediaKind: m.MediaKindOverride}
	if m.BGM != nil {
		stored.BGMWorkID = &m.BGM.WorkID
	}
	if m.TMDB != nil {
		stored.TMDBWorkID, stored.TMDBKind, stored.TMDBSeason = &m.TMDB.WorkID, &m.TMDB.MediaKind, &m.TMDB.Season
	}
	return stored
}

// mergeMetadata applies the shared fill-if-empty rule to the owner view.
func mergeMetadata(current MetadataMatchMetadata, match *matcher.MatchResult) MetadataMatchMetadata {
	merged := current
	m := backfill.Merge(storedMetadata(current), match).Merged
	if m.HasBGM() {
		merged.BGM = &MetadataMatchBGM{WorkID: *m.BGMWorkID}
	}
	if m.HasTMDB() {
		merged.TMDB = &MetadataMatchTMDB{WorkID: *m.TMDBWorkID, MediaKind: *m.TMDBKind, Season: *m.TMDBSeason}
	}
	merged.TotalEpisodes, merged.Year, merged.Season, merged.MediaKindOverride = m.TotalEpisodes, m.Year, m.Season, m.MediaKind
	return merged
}

func (s *Service) commitMetadataMatch(ctx context.Context, managedAnimeID string, expectedVersion int64, outcome MetadataMatchOutcome, match *matcher.MatchResult, reason string, write bool, idempotencyKey, requestHash string) (MetadataMatchResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MetadataMatchResult{}, fmt.Errorf("begin metadata match: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return MetadataMatchResult{}, err
	}

	if write {
		if err := lockMetadataReleaseEvaluations(ctx, tx, managedAnimeID); err != nil {
			return MetadataMatchResult{}, err
		}
	}

	var subscriptionID, title, state string
	if err := tx.QueryRowContext(ctx, `
	SELECT s.id, ma.title, s.state
	FROM subscriptions s
	JOIN managed_anime ma ON ma.id = s.managed_anime_id
	WHERE s.managed_anime_id = $1 FOR UPDATE`, managedAnimeID).Scan(&subscriptionID, &title, &state); errors.Is(err, sql.ErrNoRows) {
		return MetadataMatchResult{}, &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
	} else if err != nil {
		return MetadataMatchResult{}, fmt.Errorf("lock metadata subscription: %w", err)
	}
	if state != "active" {
		return MetadataMatchResult{}, &apperrors.Business{Status: 409, Code: "SUBSCRIPTION_INACTIVE", Message: "Metadata matching requires an active subscription."}
	}

	var target metadataMatchTarget
	if err := tx.QueryRowContext(ctx, `
SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season,
       total_episodes, media_kind_override, year, season, version
FROM anime_metadata WHERE managed_anime_id = $1 FOR UPDATE`, managedAnimeID).Scan(
		&target.bgmWorkID, &target.tmdbWorkID, &target.tmdbKind, &target.tmdbSeason,
		&target.totalEpisodes, &target.mediaKindOverride, &target.year, &target.season, &target.version); errors.Is(err, sql.ErrNoRows) {
		return MetadataMatchResult{}, &apperrors.NotFound{Resource: "anime_metadata", ID: managedAnimeID}
	} else if err != nil {
		return MetadataMatchResult{}, fmt.Errorf("lock anime metadata: %w", err)
	}
	current := target.metadata(managedAnimeID)
	if target.version.Int64 != expectedVersion {
		return MetadataMatchResult{}, &apperrors.VersionConflict{Owner: "anime_metadata", CurrentVersion: target.version.Int64}
	}

	if write {
		plan := backfill.Merge(storedMetadata(current), match)
		applied, err := backfill.ApplyTx(ctx, tx, managedAnimeID, expectedVersion, plan, match)
		if err != nil {
			return MetadataMatchResult{}, err
		}
		if !plan.Changed() {
			write = false
		} else if !applied {
			// CAS conflict: nothing was written.
			return MetadataMatchResult{}, &apperrors.VersionConflict{Owner: "anime_metadata", CurrentVersion: target.version.Int64}
		} else {
			merged := mergeMetadata(current, match)
			merged.Version = expectedVersion + 1
			current = merged
			if plan.Merged.Complete() {
				if err := s.scheduleMetadataReleaseEvaluation(ctx, tx, managedAnimeID); err != nil {
					return MetadataMatchResult{}, err
				}
			}
			if err := businesslog.WriteMetadataAppliedTx(ctx, txscope.Wrap(tx), s.activityWriter, subscriptionID, managedAnimeID, expectedVersion+1); err != nil {
				return MetadataMatchResult{}, fmt.Errorf("write metadata applied log: %w", err)
			}
		}
	}
	result := MetadataMatchResult{ManagedAnimeID: managedAnimeID, Outcome: outcome, Metadata: current}
	if match != nil {
		result.Match = &MetadataMatch{BGM: bindingFromBGM(match.BgmID), TMDB: bindingFromTMDB(match)}
	}
	if strings.TrimSpace(reason) != "" {
		reason = animeapplication.SafeReason(reason)
		result.Reason = &reason
	}
	if err := writeMetadataMatchIdempotencyTx(ctx, tx, managedAnimeID, idempotencyKey, requestHash, result); err != nil {
		return MetadataMatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MetadataMatchResult{}, fmt.Errorf("commit metadata match: %w", err)
	}
	if write {
		s.publishInvalidations(events.TargetSubscribed, events.TargetRSS)
	}
	s.recordMetadataMatch(ctx, managedAnimeID, outcome, reason)
	return result, nil
}

func (s *Service) recordMetadataMatch(ctx context.Context, managedAnimeID string, outcome MetadataMatchOutcome, reason string) {
	if s.observability == nil {
		return
	}
	resultCode := "metadata_match_no_result"
	recordOutcome := "warning"
	switch outcome {
	case MetadataMatchOutcomeComplete:
		resultCode = "metadata_match_completed"
		recordOutcome = "success"
	case MetadataMatchOutcomePartial:
		resultCode = "metadata_match_partial"
	case MetadataMatchOutcomeConfigError:
		resultCode = "metadata_match_config_error"
		recordOutcome = "failure"
	case MetadataMatchOutcomeProviderError:
		resultCode = "metadata_match_provider_error"
		recordOutcome = "failure"
	}
	identity := observabilityport.MetadataMatchRoutine(observabilityport.RoutineOutcome(recordOutcome), observabilityport.RoutineResultCode(resultCode))
	if err := s.observability.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: identity, ScopeID: managedAnimeID, ManagedAnimeID: managedAnimeID, Summary: animeapplication.SafeReason(reason),
	}); err != nil {
		// Observability is a bounded side channel and must never change the
		// metadata result already committed to the owner.
		return
	}
}

func bindingFromBGM(id *string) *MetadataMatchBGM {
	if id == nil {
		return nil
	}
	return &MetadataMatchBGM{WorkID: *id}
}

func bindingFromTMDB(match *matcher.MatchResult) *MetadataMatchTMDB {
	if match == nil || match.TmdbWorkID == nil || match.TmdbKind == nil || match.TmdbSeason == nil {
		return nil
	}
	return &MetadataMatchTMDB{WorkID: *match.TmdbWorkID, MediaKind: *match.TmdbKind, Season: *match.TmdbSeason}
}

func lockMetadataReleaseEvaluations(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	rows, err := workitems.LockMetadataEvaluationsTx(ctx, tx, managedAnimeID)
	if err != nil {
		return fmt.Errorf("lock release evaluation work items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			return fmt.Errorf("scan release evaluation work item: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate release evaluation work items: %w", err)
	}
	return nil
}

func (s *Service) scheduleMetadataReleaseEvaluation(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	rows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1 AND r.verdict = 'waiting_metadata'
  AND `+animeapplication.ReleaseReevaluationScopeSQL+`
  AND NOT EXISTS (
    SELECT 1 FROM work_items wi
    WHERE wi.owner_type = 'release' AND wi.owner_id = r.id
      AND wi.kind = 'release_evaluate' AND wi.state IN ('available', 'leased')
  )
FOR UPDATE OF r`, managedAnimeID)
	if err != nil {
		return fmt.Errorf("list held releases: %w", err)
	}
	type heldRelease struct {
		id      string
		version int64
	}
	var held []heldRelease
	for rows.Next() {
		var release heldRelease
		if err := rows.Scan(&release.id, &release.version); err != nil {
			rows.Close()
			return fmt.Errorf("scan held release: %w", err)
		}
		held = append(held, release)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate held releases: %w", err)
	}
	rows.Close()
	for _, release := range held {
		if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: release.id},
			Kind:                 workitems.KindReleaseEvaluate,
			IdempotencyKey:       "release_evaluate:" + release.id,
			InputSnapshot:        json.RawMessage(`{"trigger":"metadata_complete"}`),
			MaxAttempts:          animeapplication.DefaultMaxAttempts,
			ExpectedOwnerVersion: release.version,
		}); err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
			return fmt.Errorf("schedule release re-evaluation: %w", err)
		}
	}
	return nil
}
