package application

import (
	animeapplication "bansoko/internal/anime/application"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

type RSSSourceOffsetPatchInput struct {
	ExpectedVersion int64
	EpisodeOffset   int
}

type RSSSourceAniBTModePatchInput struct {
	ExpectedVersion int64
	AnibtAuxMode    *animedomain.RSSSourceDraftAnibtAuxMode
}

// PatchRSSSourceOffset replaces the RSS link's episode offset with
// expected-version CAS against rss_sources.version. 0 means no offset; there
// is no inheritance. A changed offset admits one migration generation scoped to
// this link only.
func (s *Service) PatchRSSSourceOffset(ctx context.Context, managedAnimeID, rssSourceID string, patch RSSSourceOffsetPatchInput) (RSSSource, error) {
	if !animedomain.EpisodeOffsetInRange(patch.EpisodeOffset) {
		return RSSSource{}, &apperrors.BadRequest{Code: "OFFSET_OUT_OF_RANGE", Message: animedomain.EpisodeOffsetRangeMessage() + "."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RSSSource{}, fmt.Errorf("begin rss source offset patch: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return RSSSource{}, err
	}

	var version int64
	var oldOffset int
	err = tx.QueryRowContext(ctx, `
SELECT src.version, src.episode_offset
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE src.id = $1 AND sub.managed_anime_id = $2
FOR UPDATE OF src`, rssSourceID, managedAnimeID).Scan(&version, &oldOffset)
	if errors.Is(err, sql.ErrNoRows) {
		return RSSSource{}, &apperrors.NotFound{Resource: "rss_source", ID: rssSourceID}
	}
	if err != nil {
		return RSSSource{}, fmt.Errorf("lock rss source: %w", err)
	}
	if version != patch.ExpectedVersion {
		return RSSSource{}, &apperrors.VersionConflict{Owner: "rss_source", CurrentVersion: version}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE rss_sources SET episode_offset = $2, version = version + 1, updated_at = NOW()
WHERE id = $1`, rssSourceID, patch.EpisodeOffset); err != nil {
		return RSSSource{}, fmt.Errorf("save rss source offset: %w", err)
	}
	if oldOffset != patch.EpisodeOffset {
		if s.offsetMigrationAdmission == nil {
			return RSSSource{}, errors.New("rss link offset change requires offset migration admission")
		}
		if _, _, err := s.offsetMigrationAdmission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), managedAnimeID, rssSourceID, version+1, oldOffset, patch.EpisodeOffset); err != nil {
			return RSSSource{}, fmt.Errorf("admit rss source offset migration: %w", err)
		}
	}
	if err := s.scheduleUnsubmittedReleaseEvaluation(ctx, tx, `
SELECT r.id, r.version
FROM releases r
WHERE r.rss_source_id = $1 AND `+animeapplication.ReleaseReevaluationScopeSQL+`
FOR UPDATE OF r`, rssSourceID, "offset_patch"); err != nil {
		return RSSSource{}, err
	}

	var subscriptionID, sourceKind, locatorDisplay, groupName, title sql.NullString
	var episodeOffset int
	var provenance []byte
	err = tx.QueryRowContext(ctx, `
SELECT subscription_id, source_kind, locator_display, group_name, title, episode_offset, provenance, version
FROM rss_sources
WHERE id = $1`, rssSourceID).Scan(&subscriptionID, &sourceKind, &locatorDisplay, &groupName, &title, &episodeOffset, &provenance, &version)
	if err != nil {
		return RSSSource{}, fmt.Errorf("read rss source after patch: %w", err)
	}
	// Older embedders may construct the anime service without the optional
	// central Activity writer. Preserve offset owner compatibility in that
	// degraded wiring; the supported server composition root injects it.
	if s.activityWriter != nil {
		if err := businesslog.WriteRSSSourceOffsetPolicyChangedTx(ctx, txscope.Wrap(tx), s.activityWriter,
			subscriptionID.String, managedAnimeID, rssSourceID, groupName.String, version); err != nil {
			return RSSSource{}, fmt.Errorf("write rss source offset policy activity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return RSSSource{}, fmt.Errorf("commit rss source offset patch: %w", err)
	}
	s.publishInvalidations(events.TargetRSS)

	result := RSSSource{
		ID:             rssSourceID,
		SubscriptionID: subscriptionID.String,
		SourceKind:     animedomain.SourceKind(sourceKind.String),
		LocatorDisplay: animedomain.SanitizeLocatorDisplay(locatorDisplay.String),
		GroupName:      groupName.String,
		Title:          title.String,
		EpisodeOffset:  episodeOffset,
		Version:        version,
	}
	if provenance != nil {
		var value animedomain.ProviderProvenance
		if err := json.Unmarshal(provenance, &value); err != nil {
			return RSSSource{}, fmt.Errorf("decode rss source provenance: %w", err)
		}
		result.ProviderProvenance = &value
	}
	return result, nil
}

// RSSRefreshAccepted is the transport-neutral result of scheduling a source refresh.
type RSSRefreshAccepted struct {
	Accepted   bool
	WorkItemID string
}

// RefreshRSSSource schedules one manual rss_refresh WorkItem for the source
// runtime. A live item conflict is surfaced as a typed 409.
func (s *Service) RefreshRSSSource(ctx context.Context, rssSourceID, idempotencyKey string) (RSSRefreshAccepted, error) {
	if err := animeapplication.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return RSSRefreshAccepted{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RSSRefreshAccepted{}, fmt.Errorf("begin rss source refresh: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleForSourceTx(ctx, tx, rssSourceID); err != nil {
		return RSSRefreshAccepted{}, err
	}

	var runtimeVersion int64
	var subscriptionID, subscriptionState, sourceKind, lifecycleState string
	var provider, providerGroupRef sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT rt.version, src.subscription_id, sub.state, src.source_kind, src.provider, src.provider_group_ref, lifecycle.lifecycle_state
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime lifecycle ON lifecycle.id = sub.managed_anime_id
JOIN rss_source_runtime rt ON rt.rss_source_id = src.id
WHERE src.id = $1
FOR UPDATE OF src, sub, rt`, rssSourceID).Scan(
		&runtimeVersion, &subscriptionID, &subscriptionState, &sourceKind, &provider, &providerGroupRef, &lifecycleState)
	if errors.Is(err, sql.ErrNoRows) {
		return RSSRefreshAccepted{}, &apperrors.NotFound{Resource: "rss_source_runtime", ID: rssSourceID}
	}
	if err != nil {
		return RSSRefreshAccepted{}, fmt.Errorf("read rss source refresh eligibility: %w", err)
	}
	if lifecycleState == "retired" {
		return RSSRefreshAccepted{}, animeapplication.ManagedAnimeRetiredError()
	}
	selectionExists := false
	providerSelected := false
	if sourceKind == "provider_group" {
		err = tx.QueryRowContext(ctx, `
SELECT effective_selected
FROM provider_group_selections
WHERE subscription_id = $1 AND provider = $2 AND group_ref = $3
FOR UPDATE`, subscriptionID, provider.String, providerGroupRef.String).Scan(&providerSelected)
		selectionExists = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return RSSRefreshAccepted{}, fmt.Errorf("lock provider selection for refresh eligibility: %w", err)
		}
	}
	ineligibility := subscriptiondomain.EvaluateSourceRefreshEligibility(subscriptiondomain.SourceRefreshEligibilityFacts{
		SubscriptionState:       subscriptiondomain.SubscriptionState(subscriptionState),
		ProviderSource:          sourceKind == "provider_group",
		ProviderSelectionExists: selectionExists,
		ProviderSelected:        providerSelected,
	})
	// A stopped Subscription blocks new automatic work only; an explicit
	// manual refresh is allowed for it (shared spec). The provider selection
	// gate still applies because it reflects user configuration, not state.
	// A pending (never started) Subscription does no work, manual included:
	// its RSS page reads the feed on demand without storing anything.
	if ineligibility == subscriptiondomain.SourceRefreshSubscriptionPending {
		return RSSRefreshAccepted{}, animeapplication.SubscriptionNotStartedError()
	}
	if ineligibility == subscriptiondomain.SourceRefreshPolicyNotSelected {
		return RSSRefreshAccepted{}, &apperrors.Business{Status: 409, Code: "RSS_SOURCE_NOT_SELECTED", Message: "The provider source is not currently selected."}
	}

	workItemID, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: rssSourceID},
		Kind:                 workitems.KindRSSRefresh,
		IdempotencyKey:       "rss_refresh:" + rssSourceID,
		InputSnapshot:        json.RawMessage(`{"trigger":"manual"}`),
		MaxAttempts:          animeapplication.DefaultMaxAttempts,
		ExpectedOwnerVersion: runtimeVersion,
	})
	if err != nil {
		if errors.Is(err, workitems.ErrLiveItemConflict) {
			return RSSRefreshAccepted{}, &apperrors.Business{Status: 409, Code: "LIVE_REFRESH_EXISTS", Message: "A refresh is already in progress for this source."}
		}
		return RSSRefreshAccepted{}, fmt.Errorf("schedule manual refresh: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RSSRefreshAccepted{}, fmt.Errorf("commit rss source refresh: %w", err)
	}
	s.publishInvalidations(events.TargetRSS)
	return RSSRefreshAccepted{
		Accepted:   true,
		WorkItemID: workItemID,
	}, nil
}

// PatchRSSSourceAniBTMode replaces the per-source AniBT auxiliary merge mode
// with expected-version CAS. Only AniBT provider sources accept a mode; nil
// clears it.
func (s *Service) PatchRSSSourceAniBTMode(ctx context.Context, managedAnimeID, rssSourceID string, patch RSSSourceAniBTModePatchInput) (RSSSource, error) {
	if patch.AnibtAuxMode != nil {
		switch *patch.AnibtAuxMode {
		case animedomain.RSSSourceDraftAnibtAuxModeDisabled,
			animedomain.RSSSourceDraftAnibtAuxModeFillMissing,
			animedomain.RSSSourceDraftAnibtAuxModeOverride:
		default:
			return RSSSource{}, &apperrors.BadRequest{Code: "ANIbtMODE_INVALID", Message: "anibt_aux_mode must be disabled, fill_missing, or override"}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RSSSource{}, fmt.Errorf("begin rss source anibt mode patch: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return RSSSource{}, err
	}

	var version int64
	var sourceKind, provenance string
	var provenanceRaw []byte
	err = tx.QueryRowContext(ctx, `
SELECT src.version, src.source_kind, COALESCE(src.provenance, '{}'::jsonb)::text
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE src.id = $1 AND sub.managed_anime_id = $2
FOR UPDATE OF src`, rssSourceID, managedAnimeID).Scan(&version, &sourceKind, &provenance)
	if errors.Is(err, sql.ErrNoRows) {
		return RSSSource{}, &apperrors.NotFound{Resource: "rss_source", ID: rssSourceID}
	}
	if err != nil {
		return RSSSource{}, fmt.Errorf("lock rss source: %w", err)
	}
	if version != patch.ExpectedVersion {
		return RSSSource{}, &apperrors.VersionConflict{Owner: "rss_source", CurrentVersion: version}
	}
	if sourceKind != string(animedomain.SourceKindProviderGroup) {
		return RSSSource{}, &apperrors.BadRequest{Code: "ANIbtMODE_SOURCE_INVALID", Message: "anibt_aux_mode is only valid for provider group sources"}
	}
	provenanceRaw = []byte(provenance)
	var prov animedomain.ProviderProvenance
	if err := json.Unmarshal(provenanceRaw, &prov); err != nil || prov.Provider != animedomain.ProviderKindAnibt {
		return RSSSource{}, &apperrors.BadRequest{Code: "ANIbtMODE_SOURCE_INVALID", Message: "anibt_aux_mode is only valid for AniBT provider sources"}
	}
	var modeValue any
	if patch.AnibtAuxMode != nil {
		modeValue = string(*patch.AnibtAuxMode)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE rss_sources SET anibt_aux_mode = $2, version = version + 1, updated_at = NOW()
WHERE id = $1`, rssSourceID, modeValue); err != nil {
		return RSSSource{}, fmt.Errorf("save rss source anibt mode: %w", err)
	}
	// The aux mode changes the media-fact input digest and therefore the
	// resolved identity of every unsubmitted release; re-evaluate them in the
	// same transaction so a fresh snapshot (with the new mode) is frozen.
	if err := s.scheduleUnsubmittedReleaseEvaluation(ctx, tx, `
SELECT r.id, r.version
FROM releases r
WHERE r.rss_source_id = $1 AND `+animeapplication.ReleaseReevaluationScopeSQL+`
FOR UPDATE OF r`, rssSourceID, "anibt_mode_patch"); err != nil {
		return RSSSource{}, err
	}
	var subscriptionID, locatorDisplay, groupName, title sql.NullString
	var episodeOffset int
	err = tx.QueryRowContext(ctx, `
SELECT subscription_id, source_kind, locator_display, group_name, title, episode_offset, version
FROM rss_sources
WHERE id = $1`, rssSourceID).Scan(&subscriptionID, &sourceKind, &locatorDisplay, &groupName, &title, &episodeOffset, &version)
	if err != nil {
		return RSSSource{}, fmt.Errorf("read rss source after patch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RSSSource{}, fmt.Errorf("commit rss source anibt mode patch: %w", err)
	}
	s.publishInvalidations(events.TargetRSS)

	result := RSSSource{
		ID:             rssSourceID,
		SubscriptionID: subscriptionID.String,
		SourceKind:     animedomain.SourceKind(sourceKind),
		LocatorDisplay: animedomain.SanitizeLocatorDisplay(locatorDisplay.String),
		GroupName:      groupName.String,
		Title:          title.String,
		EpisodeOffset:  episodeOffset,
		Version:        version,
	}
	if patch.AnibtAuxMode != nil {
		mode := *patch.AnibtAuxMode
		result.AnibtAuxMode = &mode
	}
	return result, nil
}
