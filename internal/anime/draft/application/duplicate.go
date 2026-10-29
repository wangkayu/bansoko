package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// DuplicateSubscriptionInput names the display-only label of the copy.
type DuplicateSubscriptionInput struct {
	InstanceLabel string
}

// DuplicateAnimeSubscription creates a new, independent pending subscription
// from the source instance's current saved configuration, in one transaction
// and with the owner set Subscribe creates. Configuration is copied (source
// entry or custom feeds, group selection, rule/tag-filter/naming/parser/file
// effect sections, the policy axes, file-maintenance settings, metadata
// bindings, title override and per-source episode offsets); state, history,
// releases, download work, files, the managed file state and logs are not. The copy is always pending. Overlap with the source is not
// detected. Replaying the key with the same source
// and label returns the first result; a retired source is refused.
func (s *Service) DuplicateAnimeSubscription(ctx context.Context, sourceAnimeID string, input DuplicateSubscriptionInput, idempotencyKey string) (SubscribeResult, error) {
	if s == nil || s.db == nil {
		return SubscribeResult{}, errors.New("anime service database is nil")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return SubscribeResult{}, err
	}
	sourceAnimeID = strings.TrimSpace(sourceAnimeID)
	if sourceAnimeID == "" {
		return SubscribeResult{}, &apperrors.NotFound{Resource: "managed_anime", ID: sourceAnimeID}
	}
	label := strings.TrimSpace(input.InstanceLabel)
	if label == "" || utf8.RuneCountInString(label) > 256 || strings.ContainsFunc(label, isControlRune) {
		return SubscribeResult{}, draftInvalidError([]animedomain.FieldViolation{{Field: "instance_label", Code: "DRAFT_INVALID", Message: "instance_label is required for a copy: non-empty, at most 256 characters, no control characters"}})
	}
	requestHash, err := canonicalJSONHash(struct {
		Kind          string `json:"kind"`
		Source        string `json:"source_managed_anime_id"`
		InstanceLabel string `json:"instance_label"`
	}{Kind: "subscription_duplicate", Source: sourceAnimeID, InstanceLabel: label})
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("hash duplicate request: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("begin duplicate transaction: %w", err)
	}
	defer tx.Rollback()

	// The key shares Subscribe's idempotency namespace: a key used for a
	// subscribe and then for a copy (or the reverse) is a reused key.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, idempotencyKey); err != nil {
		return SubscribeResult{}, fmt.Errorf("lock duplicate idempotency key: %w", err)
	}
	var existingHash string
	var existingSnapshot []byte
	var existingManagedAnimeID sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT request_hash, result_snapshot, managed_anime_id
FROM idempotency_records
WHERE kind = 'subscribe' AND idempotency_key = $1
FOR UPDATE`, idempotencyKey).Scan(&existingHash, &existingSnapshot, &existingManagedAnimeID)
	if err == nil {
		if existingHash != requestHash {
			return SubscribeResult{}, idempotencyKeyReusedError()
		}
		var result SubscribeResult
		if err := json.Unmarshal(existingSnapshot, &result); err != nil {
			return SubscribeResult{}, fmt.Errorf("decode duplicate idempotency snapshot: %w", err)
		}
		if !existingManagedAnimeID.Valid || existingManagedAnimeID.String != result.ManagedAnime.ID || result.Subscription.ManagedAnimeID != result.ManagedAnime.ID {
			return SubscribeResult{}, idempotencyKeyReusedError()
		}
		sanitizeRSSSourceDisplays(result.RssSources)
		if err := tx.Commit(); err != nil {
			return SubscribeResult{}, fmt.Errorf("commit duplicate replay: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SubscribeResult{}, fmt.Errorf("read duplicate idempotency: %w", err)
	}

	// The shared lifecycle lock blocks retirement and every owner edit (they
	// take the lifecycle row FOR UPDATE first), so the copy reads one
	// consistent saved configuration.
	var (
		sourceTitle, lifecycle   string
		titleOverride, provTitle sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
SELECT title, title_override, provider_title, lifecycle_state
FROM managed_anime WHERE id = $1 FOR SHARE`, sourceAnimeID).Scan(&sourceTitle, &titleOverride, &provTitle, &lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return SubscribeResult{}, &apperrors.NotFound{Resource: "managed_anime", ID: sourceAnimeID}
	}
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("lock source managed anime: %w", err)
	}
	if lifecycle != "active" {
		return SubscribeResult{}, animeapplication.ManagedAnimeRetiredError()
	}
	var sourceSubscriptionID, origin string
	var originEntryRef sql.NullString
	var newReleasesOnly bool
	err = tx.QueryRowContext(ctx, `
SELECT id, origin, origin_entry_ref, new_releases_only FROM subscriptions WHERE managed_anime_id = $1`, sourceAnimeID).Scan(&sourceSubscriptionID, &origin, &originEntryRef, &newReleasesOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return SubscribeResult{}, &apperrors.NotFound{Resource: "subscription", ID: sourceAnimeID}
	}
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("read source subscription: %w", err)
	}
	if origin == string(animedomain.SubscriptionOriginOneshot) {
		return SubscribeResult{}, &apperrors.Business{Status: 422, Code: "ORIGIN_NOT_SUBSCRIBABLE", Message: "A one-shot acquisition cannot be copied into a subscription."}
	}

	metadataDraft, err := loadSourceMetadataDraftTx(ctx, tx, sourceAnimeID)
	if err != nil {
		return SubscribeResult{}, err
	}
	sources, err := loadSourceRSSSourceDraftsTx(ctx, tx, sourceSubscriptionID)
	if err != nil {
		return SubscribeResult{}, err
	}

	// The copy carries the source's fixed title; a source row written before
	// the title was fixed (NULL) is fixed to its base title.
	if !titleOverride.Valid || strings.TrimSpace(titleOverride.String) == "" {
		titleOverride = sql.NullString{String: sourceTitle, Valid: true}
	}
	animeID := uuid.NewString()
	subscriptionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO managed_anime(id, title, title_override, provider_title)
VALUES ($1, $2, $3, $4)`, animeID, sourceTitle, nullString(titleOverride), nullString(provTitle)); err != nil {
		return SubscribeResult{}, fmt.Errorf("create managed anime: %w", err)
	}
	// The copy is a new instance: it keeps the new-releases-only option, and its
	// baseline is its own creation time.
	startBaselineAt, err := insertSubscriptionTx(ctx, tx, subscriptionID, animeID, origin, nullString(originEntryRef), label, string(subscriptiondomain.SubscriptionPending), newReleasesOnly)
	if err != nil {
		return SubscribeResult{}, err
	}
	metadata, err := s.provisioner.InitializeAnimeOwnerRowsTx(ctx, tx, animeID, metadataDraft)
	if err != nil {
		return SubscribeResult{}, err
	}
	// The merged business row is the one configuration source of the policy
	// axes and the frozen business; its values are copied and its owner
	// versions restart at 1 like any new owner.
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_business_configs AS dst
SET parser_kind = src.parser_kind,
    parser_source_default_version = src.parser_source_default_version,
    parser_version = CASE WHEN src.parser_version IS NULL THEN NULL ELSE 1 END,
    admission_mode = src.admission_mode, quality_profile = src.quality_profile,
    replacement_source_default_version = src.replacement_source_default_version,
    replacement_version = CASE WHEN src.replacement_version IS NULL THEN NULL ELSE 1 END,
    unclassified_route = src.unclassified_route,
    file_effect_source_default_version = src.file_effect_source_default_version,
    file_effect_version = CASE WHEN src.file_effect_version IS NULL THEN NULL ELSE 1 END,
    candidate_id = src.candidate_id, revision = src.revision,
    business_snapshot = src.business_snapshot, business_digest = src.business_digest,
    business_version = CASE WHEN src.business_snapshot IS NULL THEN 0 ELSE 1 END,
    updated_at = NOW()
FROM anime_business_configs AS src
WHERE dst.managed_anime_id = $1 AND src.managed_anime_id = $2`, animeID, sourceAnimeID); err != nil {
		return SubscribeResult{}, fmt.Errorf("copy business config: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
SELECT $1, owner_type, section_name, schema_version, value
FROM anime_config_overrides WHERE managed_anime_id = $2`, animeID, sourceAnimeID); err != nil {
		return SubscribeResult{}, fmt.Errorf("copy config overrides: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_group_selections(
    id, subscription_id, provider, group_ref, display_name, selection_mode, manual_selected,
    effective_selected, effective_score, available, availability_generation)
SELECT gen_random_uuid()::text, $1, provider, group_ref, display_name, selection_mode, manual_selected,
       effective_selected, effective_score, available, availability_generation
FROM provider_group_selections WHERE subscription_id = $2`, subscriptionID, sourceSubscriptionID); err != nil {
		return SubscribeResult{}, fmt.Errorf("copy group selections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_group_scope_generations(subscription_id, provider, generation)
SELECT $1, provider, generation
FROM provider_group_scope_generations WHERE subscription_id = $2`, subscriptionID, sourceSubscriptionID); err != nil {
		return SubscribeResult{}, fmt.Errorf("copy group scope generations: %w", err)
	}
	// The per-anime file-maintenance settings (scan and alignment policy) are
	// configuration: copied as a new version-1 row, editable on the copy like
	// any instance override. The managed state those settings drive
	// (placements, companions, scan generations and observations, alignment,
	// offset changes, cleanup and replacement rows) belongs to the source's
	// files and is never copied.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO filemaintenance_policies(scope_key, enabled, scan_interval_seconds, alignment_enabled, safety, version, updated_at)
SELECT 'anime:' || $1, enabled, scan_interval_seconds, alignment_enabled, safety, 1, NOW()
FROM filemaintenance_policies WHERE scope_key = 'anime:' || $2`, animeID, sourceAnimeID); err != nil {
		return SubscribeResult{}, fmt.Errorf("copy file maintenance settings: %w", err)
	}
	copied := make([]RSSSource, 0, len(sources))
	for _, sourceDraft := range sources {
		source, err := insertRSSSource(ctx, tx, subscriptionID, sourceDraft)
		if err != nil {
			return SubscribeResult{}, err
		}
		// A fresh runtime: no cursor, no baseline, nothing scheduled. The
		// pending copy's first start is a normal first start.
		if _, err := tx.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, source.ID); err != nil {
			return SubscribeResult{}, fmt.Errorf("create rss source runtime: %w", err)
		}
		copied = append(copied, source)
	}
	versions := ownedConfigVersions()

	result := SubscribeResult{
		ManagedAnime: ManagedAnime{ID: animeID, Title: sourceTitle, TitleOverride: stringPtr(titleOverride), Version: 1},
		Subscription: Subscription{
			ID:              subscriptionID,
			ManagedAnimeID:  animeID,
			Origin:          origin,
			OriginEntryRef:  stringPtr(originEntryRef),
			InstanceLabel:   &label,
			State:           subscriptiondomain.SubscriptionPending,
			LifecycleState:  "active",
			LifecycleEpoch:  1,
			Version:         1,
			NewReleasesOnly: newReleasesOnly,
			StartBaselineAt: startBaselineAt,
		},
		Metadata:                metadata,
		RssSources:              copied,
		EffectiveConfigVersions: versions,
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("encode duplicate result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
VALUES ('subscribe', $1, $2, $3, $4)`, idempotencyKey, requestHash, animeID, snapshot); err != nil {
		return SubscribeResult{}, fmt.Errorf("record duplicate idempotency: %w", err)
	}
	if err := writeSubscriptionCreatedLog(ctx, tx, s.activityWriter, subscriptionID, animeID); err != nil {
		return SubscribeResult{}, err
	}
	// The copy is pending, so it admits no started notice (its first start
	// does), exactly like a pending Subscribe.
	if s.observability != nil && !metadataCompleteApplication(metadata) {
		if err := s.observability.UpsertRoutineTx(ctx, txscope.Wrap(tx), observabilityport.RoutineRequest{
			Identity: observabilityport.MetadataMatchRoutine(observabilityport.RoutineOutcomeWarning, observabilityport.RoutineResultMetadataPending),
			ScopeID:  animeID, ManagedAnimeID: animeID, Summary: "Waiting for complete metadata.",
		}); err != nil {
			return SubscribeResult{}, fmt.Errorf("record metadata hold: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SubscribeResult{}, fmt.Errorf("commit duplicate transaction: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetCatalog)
	return result, nil
}

func isControlRune(r rune) bool { return r < 0x20 || r == 0x7f }

func nullString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func loadSourceMetadataDraftTx(ctx context.Context, tx *sql.Tx, animeID string) (animedomain.AnimeMetadataDraft, error) {
	var (
		bgm, tmdb, tmdbKind, kindOverride sql.NullString
		season, total, year, ownerSeason  sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind, total_episodes, year, season
FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&bgm, &tmdb, &tmdbKind, &season, &kindOverride, &total, &year, &ownerSeason)
	if errors.Is(err, sql.ErrNoRows) {
		return animedomain.AnimeMetadataDraft{}, nil
	}
	if err != nil {
		return animedomain.AnimeMetadataDraft{}, fmt.Errorf("read source metadata: %w", err)
	}
	var draft animedomain.AnimeMetadataDraft
	if bgm.Valid {
		draft.Bgm = &animedomain.BGMBinding{WorkId: bgm.String}
	}
	if tmdb.Valid && tmdbKind.Valid && season.Valid {
		draft.Tmdb = &animedomain.TMDBBinding{WorkId: tmdb.String, MediaKind: animedomain.TMDBMediaKind(tmdbKind.String), Season: int(season.Int64)}
	}
	draft.MediaKind = stringPtr(kindOverride)
	if total.Valid {
		value := int(total.Int64)
		draft.TotalEpisodes = &value
	}
	if year.Valid {
		value := int(year.Int64)
		draft.Year = &value
	}
	if ownerSeason.Valid {
		value := int(ownerSeason.Int64)
		draft.Season = &value
	}
	return draft, nil
}

func loadSourceRSSSourceDraftsTx(ctx context.Context, tx *sql.Tx, subscriptionID string) ([]animedomain.RSSSourceDraft, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT source_kind, locator, group_name, title, episode_offset, anibt_aux_mode, provenance,
       http_feed_connection_ref, network_route_ref
FROM rss_sources WHERE subscription_id = $1
ORDER BY created_at, id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read source rss sources: %w", err)
	}
	defer rows.Close()
	var drafts []animedomain.RSSSourceDraft
	for rows.Next() {
		var (
			draft                   animedomain.RSSSourceDraft
			kind                    string
			auxMode, connRef, route sql.NullString
			provenance              []byte
		)
		if err := rows.Scan(&kind, &draft.Locator, &draft.GroupName, &draft.Title, &draft.EpisodeOffset, &auxMode, &provenance, &connRef, &route); err != nil {
			return nil, fmt.Errorf("scan source rss source: %w", err)
		}
		draft.SourceKind = animedomain.SourceKind(kind)
		if auxMode.Valid {
			mode := animedomain.RSSSourceDraftAnibtAuxMode(auxMode.String)
			draft.AnibtAuxMode = &mode
		}
		if len(provenance) > 0 {
			var value animedomain.ProviderProvenance
			if err := json.Unmarshal(provenance, &value); err != nil {
				return nil, fmt.Errorf("decode source rss provenance: %w", err)
			}
			draft.ProviderProvenance = &value
		}
		draft.HttpFeedConnectionRef = stringPtr(connRef)
		draft.NetworkRouteRef = stringPtr(route)
		drafts = append(drafts, draft)
	}
	return drafts, rows.Err()
}

// ownedConfigVersions reports the effective source version of a new
// instance's sections: every section is a value the instance owns from
// creation (owner version 1); none resolves against a Global row.
func ownedConfigVersions() EffectiveConfigVersions {
	return EffectiveConfigVersions{Subgroups: 1, Parser: 1, ReleaseFilter: 1, FileFilter: 1, Naming: 1, FileEffect: 1}
}
