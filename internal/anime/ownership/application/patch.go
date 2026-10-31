package application

import (
	"bansoko/internal/anime/animeconfig"
	animeapplication "bansoko/internal/anime/application"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	automationapp "bansoko/internal/automation/application"
	"bansoko/internal/events"
	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	selectionapp "bansoko/internal/selection/application"
	selectiondomain "bansoko/internal/selection/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// SubgroupsPatchInput is the transport-neutral complete subgroups replacement.
type SubgroupsPatchInput struct {
	ExpectedVersion int64
	GroupActions    []GroupAction
	Subgroups       SubgroupsSection
}

type GroupAction struct {
	Action   GroupActionType
	GroupRef string
	Provider string
}

type GroupActionType string

const (
	GroupActionForceInclude GroupActionType = "force_include"
	GroupActionForceExclude GroupActionType = "force_exclude"
	GroupActionRestoreAuto  GroupActionType = "restore_auto"
)

func (s *Service) PatchAnimeSubgroupsApplication(ctx context.Context, managedAnimeID string, input SubgroupsPatchInput) (SubgroupsOwnerResult, error) {
	if violations := animedomain.ValidateSubgroupsSection(domainSubgroupsSection(input.Subgroups)); len(violations) > 0 {
		return SubgroupsOwnerResult{}, animeapplication.DraftInvalidError(violations)
	}
	if err := applyGroupActions(&input.Subgroups, input.GroupActions); err != nil {
		return SubgroupsOwnerResult{}, &apperrors.Business{
			Status:  422,
			Code:    "GROUP_ACTION_INVALID",
			Message: err.Error(),
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubgroupsOwnerResult{}, fmt.Errorf("begin subgroups patch: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return SubgroupsOwnerResult{}, err
	}
	var subscriptionID, animeTitle string
	var ownerVersion int64
	err = tx.QueryRowContext(ctx, `
SELECT o.subgroups_version, sub.id, ma.title
FROM anime_business_configs o
JOIN subscriptions sub ON sub.managed_anime_id = o.managed_anime_id
JOIN managed_anime ma ON ma.id = o.managed_anime_id
WHERE o.managed_anime_id = $1
FOR UPDATE OF sub, o`, managedAnimeID).Scan(&ownerVersion, &subscriptionID, &animeTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return SubgroupsOwnerResult{}, &apperrors.NotFound{Resource: "subgroups_owner", ID: managedAnimeID}
	}
	if err != nil {
		return SubgroupsOwnerResult{}, fmt.Errorf("lock subgroups owner: %w", err)
	}
	if ownerVersion != input.ExpectedVersion {
		return SubgroupsOwnerResult{}, &apperrors.VersionConflict{Owner: "subgroups", CurrentVersion: ownerVersion}
	}
	subgroupsOverride, overrideViolations := animedomain.SubgroupsSectionOverride(domainSubgroupsSection(input.Subgroups))
	if len(overrideViolations) > 0 {
		return SubgroupsOwnerResult{}, animeapplication.DraftInvalidError(overrideViolations)
	}
	var previousSchema sql.NullString
	var previousValue []byte
	err = tx.QueryRowContext(ctx, `
SELECT schema_version, value
FROM anime_config_overrides
WHERE managed_anime_id = $1 AND section_name = 'subgroups'`, managedAnimeID).Scan(&previousSchema, &previousValue)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SubgroupsOwnerResult{}, fmt.Errorf("read current subgroups override: %w", err)
	}
	selectionChanged := subgroupsOverrideChanged(previousSchema, previousValue, subgroupsOverride)
	if err := animeapplication.UpsertOverride(ctx, tx, managedAnimeID, "subgroups", animedomain.SectionSubgroups, subgroupsOverride); err != nil {
		return SubgroupsOwnerResult{}, err
	}
	if err := syncExistingProviderSelections(ctx, tx, subscriptionID, input.Subgroups, ownerVersion+1); err != nil {
		return SubgroupsOwnerResult{}, err
	}
	recomputer := automationapp.Dispatcher{DB: s.db, PolicyReader: selectionapp.NewService(s.db)}
	if err := recomputer.RecomputeSubscriptionSelections(ctx, txscope.Wrap(tx), subscriptionID, managedAnimeID); err != nil {
		return SubgroupsOwnerResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_business_configs SET subgroups_version = subgroups_version + 1, updated_at = NOW()
WHERE managed_anime_id = $1`, managedAnimeID); err != nil {
		return SubgroupsOwnerResult{}, fmt.Errorf("bump subgroups owner version: %w", err)
	}
	// Ruling 3: the single merged row is the one configuration source, so the
	// frozen business snapshot is rewritten in the same transaction as the patch.
	if _, err := animeconfig.SyncInstanceBusinessSnapshotTx(ctx, tx, managedAnimeID); err != nil {
		return SubgroupsOwnerResult{}, err
	}
	if selectionChanged {
		if err := businesslog.WriteGroupChangedTx(ctx, txscope.Wrap(tx), s.activityWriter, subscriptionID, managedAnimeID, ownerVersion+1); err != nil {
			return SubgroupsOwnerResult{}, fmt.Errorf("write group changed log: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SubgroupsOwnerResult{}, fmt.Errorf("commit subgroups patch: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetRSS)
	result, err := s.readSubgroupsOwnerApplication(ctx, managedAnimeID, subscriptionID)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	return result, nil
}

// subgroupsOverrideChanged reports whether the next owner value differs from
// the stored one.
func subgroupsOverrideChanged(previousSchema sql.NullString, previousValue []byte, next animedomain.OverrideValue) bool {
	return sectionOverrideChanged(animedomain.SectionSubgroups, previousSchema, previousValue, next)
}

// changedRulesSectionsTx compares the next rules overrides with the stored
// rows and returns the changed section names in rules section order (an
// absent optional section is not compared: the patch keeps it).
func changedRulesSectionsTx(ctx context.Context, tx *sql.Tx, managedAnimeID string, overrides map[string]animedomain.OverrideValue) ([]string, error) {
	sections := append(append([]string{}, animeapplication.RulesSectionOrder...), animedomain.SectionTagFilters)
	changed := []string{}
	for _, section := range sections {
		next, present := overrides[section]
		if !present {
			continue
		}
		var previousSchema sql.NullString
		var previousValue []byte
		err := tx.QueryRowContext(ctx, `
SELECT schema_version, value
FROM anime_config_overrides
WHERE managed_anime_id = $1 AND section_name = $2`, managedAnimeID, section).Scan(&previousSchema, &previousValue)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read current %s override: %w", section, err)
		}
		if sectionOverrideChanged(section, previousSchema, previousValue, next) {
			changed = append(changed, section)
		}
	}
	return changed, nil
}

// rulesRenderChanged reports whether a changed section can alter a rendered
// path: only the naming rules reach the maintenance render.
func rulesRenderChanged(changedSections []string) bool {
	for _, section := range changedSections {
		if section == animedomain.SectionNamingRules {
			return true
		}
	}
	return false
}

// sectionOverrideChanged reports whether the next owner value of a section
// differs from the stored one (schema version or decoded JSON value).
func sectionOverrideChanged(section string, previousSchema sql.NullString, previousValue []byte, next animedomain.OverrideValue) bool {
	if !previousSchema.Valid || len(previousValue) == 0 {
		return true
	}
	if previousSchema.String != animedomain.SectionSchemaVersions[section] {
		return true
	}
	var previousJSON, nextJSON any
	if json.Unmarshal(previousValue, &previousJSON) != nil || json.Unmarshal(next.Value, &nextJSON) != nil {
		return true
	}
	return !reflect.DeepEqual(previousJSON, nextJSON)
}

// unsubmittedReleasesSQL locks the anime's releases that have no download task
// yet and are not terminal: the releases a re-evaluation covers.
const unsubmittedReleasesSQL = `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1 AND ` + animeapplication.ReleaseReevaluationScopeSQL + `
FOR UPDATE OF r`

// PatchAnimeRules replaces the rules owner sections with expected-version CAS
// and reports how many new Release evaluation WorkItems were scheduled.
func (s *Service) PatchAnimeRules(ctx context.Context, managedAnimeID string, patch RulesPatchInput) (RulesPatchResult, error) {
	overrides, violations := animedomain.RulesSectionOverrides(patch.Sections)
	if len(violations) > 0 {
		return RulesPatchResult{}, animeapplication.DraftInvalidError(violations)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RulesPatchResult{}, fmt.Errorf("begin rules patch: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return RulesPatchResult{}, err
	}
	var ownerVersion int64
	var subscriptionID sql.NullString
	var animeTitle string
	err = tx.QueryRowContext(ctx, `
SELECT o.rules_version, s.id, ma.title
FROM anime_business_configs o
JOIN managed_anime ma ON ma.id = o.managed_anime_id
LEFT JOIN subscriptions s ON s.managed_anime_id = o.managed_anime_id
WHERE o.managed_anime_id = $1
FOR UPDATE OF o, ma`, managedAnimeID).Scan(&ownerVersion, &subscriptionID, &animeTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return RulesPatchResult{}, &apperrors.NotFound{Resource: "rules_owner", ID: managedAnimeID}
	}
	if err != nil {
		return RulesPatchResult{}, fmt.Errorf("lock rules owner: %w", err)
	}
	if ownerVersion != patch.ExpectedVersion {
		return RulesPatchResult{}, &apperrors.VersionConflict{Owner: "rules", CurrentVersion: ownerVersion}
	}
	changedSections, err := changedRulesSectionsTx(ctx, tx, managedAnimeID, overrides)
	if err != nil {
		return RulesPatchResult{}, err
	}
	for _, section := range animeapplication.RulesSectionOrder {
		if err := animeapplication.UpsertOverride(ctx, tx, managedAnimeID, "rules", section, overrides[section]); err != nil {
			return RulesPatchResult{}, err
		}
	}
	// tag_filters is optional on the wire; an absent section keeps the stored
	// value instead of resetting it.
	for _, optional := range []string{animedomain.SectionTagFilters} {
		if override, present := overrides[optional]; present {
			if err := animeapplication.UpsertOverride(ctx, tx, managedAnimeID, "rules", optional, override); err != nil {
				return RulesPatchResult{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_business_configs SET rules_version = rules_version + 1, updated_at = NOW()
WHERE managed_anime_id = $1`, managedAnimeID); err != nil {
		return RulesPatchResult{}, fmt.Errorf("bump rules owner version: %w", err)
	}
	// Ruling 3: the single merged row is the one configuration source, so the
	// frozen business snapshot is rewritten in the same transaction as the patch.
	if _, err := animeconfig.SyncInstanceBusinessSnapshotTx(ctx, tx, managedAnimeID); err != nil {
		return RulesPatchResult{}, err
	}
	scheduled, err := s.scheduleUnsubmittedReleaseEvaluationCount(ctx, tx, unsubmittedReleasesSQL, managedAnimeID, "rules_patch")
	if err != nil {
		return RulesPatchResult{}, err
	}
	// A save that changed no section is a no-change success: no log entry.
	if subscriptionID.Valid && len(changedSections) > 0 {
		if err := businesslog.WriteRulesPolicyChangedTx(ctx, txscope.Wrap(tx), s.activityWriter, subscriptionID.String, managedAnimeID, ownerVersion+1, changedSections); err != nil {
			return RulesPatchResult{}, fmt.Errorf("write rules policy changed log: %w", err)
		}
	}
	// A naming change may complete (or break) the render an import waits for.
	if err := animeapplication.WakeConfigWaitingPlansTx(ctx, tx, managedAnimeID); err != nil {
		return RulesPatchResult{}, err
	}
	// The naming templates are the rules a render reads: a changed template
	// requests the scan that re-renders the subscription's files.
	if subscriptionID.Valid && rulesRenderChanged(changedSections) {
		if err := animeapplication.RequestConfigScanTx(ctx, tx, managedAnimeID); err != nil {
			return RulesPatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RulesPatchResult{}, fmt.Errorf("commit rules patch: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetRSS)
	rules, err := s.readRulesOwnerApplication(ctx, managedAnimeID)
	if err != nil {
		return RulesPatchResult{}, err
	}
	return RulesPatchResult{
		Rules: rules,
		Reevaluation: RulesPatchReevaluation{
			ManagedAnimeID: managedAnimeID,
			Accepted:       true,
			ScheduledCount: scheduled,
		},
	}, nil
}

func (s *Service) readRulesOwnerApplication(ctx context.Context, managedAnimeID string) (RulesOwner, error) {
	var ownerVersion int64
	if err := s.db.QueryRowContext(ctx, `SELECT rules_version FROM anime_business_configs WHERE managed_anime_id = $1`, managedAnimeID).Scan(&ownerVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RulesOwner{}, &apperrors.NotFound{Resource: "rules_owner", ID: managedAnimeID}
		}
		return RulesOwner{}, fmt.Errorf("read rules owner: %w", err)
	}
	subgroupsOverride, rulesOverrides, err := s.readOverrides(ctx, managedAnimeID)
	if err != nil {
		return RulesOwner{}, err
	}
	effective, err := animedomain.ResolveEffective(subgroupsOverride, rulesOverrides)
	if err != nil {
		return RulesOwner{}, err
	}
	var hasAniBTSource bool
	if err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM rss_sources src
    JOIN subscriptions sub ON sub.id = src.subscription_id
    WHERE sub.managed_anime_id = $1 AND src.provider = 'anibt')`, managedAnimeID).Scan(&hasAniBTSource); err != nil {
		return RulesOwner{}, fmt.Errorf("read AniBT source presence: %w", err)
	}
	return RulesOwner{
		ManagedAnimeID:      managedAnimeID,
		Sections:            rulesSectionsFromDomainOverrides(rulesOverrides),
		Effective:           effective.Rules,
		EffectiveTagFilters: effective.TagFilters.Rules,
		HasAniBTSource:      hasAniBTSource,
		Version:             ownerVersion,
	}, nil
}

func rulesSectionsFromDomainOverrides(overrides map[string]animedomain.OverrideValue) animedomain.RulesSections {
	section := func(name string) json.RawMessage {
		override := overrides[name]
		result := make(json.RawMessage, 0, len(override.Value)+len(`{"mode":"replace","value":}`))
		result = append(result, `{"mode":"replace","value":`...)
		result = append(result, override.Value...)
		result = append(result, '}')
		return result
	}
	return animedomain.RulesSections{
		FileEffect:    section(animedomain.SectionFileEffect),
		FileFilter:    section(animedomain.SectionFileRules),
		Naming:        section(animedomain.SectionNamingRules),
		Parser:        section(animedomain.SectionParser),
		ReleaseFilter: section(animedomain.SectionReleaseRules),
		TagFilters:    section(animedomain.SectionTagFilters),
	}
}

// PatchAnimeSubscription saves the unique subscription state with CAS. The
// target state is active or stopped; pending is only ever a creation state. A
// pending -> active patch is the first start: it is a normal new subscription
// (no resume baseline), so it schedules the initial refresh of every eligible
// source and admits the started notice. pending -> stopped is refused. A
// resume schedules refresh work only for custom sources; provider sources run
// the existing group discovery after the committed state transition. A
// stopped -> active transition also dispatches a fresh replacement reevaluation
// epoch after commit; the stop side only bumps the Subscription version, which
// invalidates old admitted replacement decisions at the next settle revalidation.
func (s *Service) PatchAnimeSubscription(ctx context.Context, managedAnimeID string, patch SubscriptionPatchInput) (Subscription, error) {
	if patch.State != subscriptiondomain.SubscriptionActive && patch.State != subscriptiondomain.SubscriptionStopped {
		return Subscription{}, animeapplication.DraftInvalidError([]animedomain.FieldViolation{{Field: "state", Code: "DRAFT_INVALID", Message: "state must be active or stopped"}})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, fmt.Errorf("begin subscription patch: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return Subscription{}, err
	}
	var subscriptionID string
	var version int64
	var currentState string
	var origin string
	var originEntryRef, instanceLabel, entrySeason sql.NullString
	var createdAt time.Time
	var animeTitle, lifecycleState string
	var lifecycleEpoch int64
	var retiredAt, startBaselineAt sql.NullTime
	var newReleasesOnly bool
	err = tx.QueryRowContext(ctx, `
SELECT s.id, s.origin, s.origin_entry_ref, s.instance_label, s.entry_season, s.created_at, s.state, s.version, ma.title,
       ma.lifecycle_state, ma.fence_epoch, ma.retired_at, s.new_releases_only, s.start_baseline_at
FROM subscriptions s
JOIN managed_anime ma ON ma.id = s.managed_anime_id
WHERE s.managed_anime_id = $1
FOR UPDATE OF s`, managedAnimeID).Scan(&subscriptionID, &origin, &originEntryRef, &instanceLabel, &entrySeason, &createdAt, &currentState, &version, &animeTitle, &lifecycleState, &lifecycleEpoch, &retiredAt, &newReleasesOnly, &startBaselineAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("lock subscription: %w", err)
	}
	if version != patch.ExpectedVersion {
		return Subscription{}, &apperrors.VersionConflict{Owner: "subscription", CurrentVersion: version}
	}
	if lifecycleState == "retired" {
		return Subscription{}, animeapplication.ManagedAnimeRetiredError()
	}
	started := currentState == string(subscriptiondomain.SubscriptionPending)
	if started && patch.State != subscriptiondomain.SubscriptionActive {
		return Subscription{}, animeapplication.SubscriptionNotStartedError()
	}
	// A stopped -> active transition stamps resumed_at: each source's first
	// successful refresh after it records the feed window as the baseline
	// (no discovery notice, no evaluation), so history the retention janitor
	// purged while the subscription was stopped is never discovered again.
	resumed := patch.State == subscriptiondomain.SubscriptionActive && currentState == string(subscriptiondomain.SubscriptionStopped)
	if _, err := tx.ExecContext(ctx, `
UPDATE subscriptions SET state = $2, version = version + 1, updated_at = NOW(),
    resumed_at = CASE WHEN $3 THEN NOW() ELSE resumed_at END
WHERE id = $1`, subscriptionID, string(patch.State), resumed); err != nil {
		return Subscription{}, fmt.Errorf("save subscription: %w", err)
	}
	if started {
		// The first start schedules what a subscribe that starts at once
		// schedules: one initial refresh per eligible source (custom, or a
		// selected available provider group). Provider discovery runs after
		// commit and schedules the groups it adds.
		if err := s.scheduleStartRefreshesTx(ctx, tx, subscriptionID); err != nil {
			return Subscription{}, err
		}
	}
	if resumed {
		rows, err := tx.QueryContext(ctx, `
SELECT src.id, rt.version
FROM rss_sources src
JOIN rss_source_runtime rt ON rt.rss_source_id = src.id
WHERE src.subscription_id = $1 AND src.source_kind = 'custom_rss'
  AND NOT EXISTS (
    SELECT 1 FROM work_items wi
    WHERE wi.owner_type = 'rss_source_runtime' AND wi.owner_id = src.id
      AND wi.kind = 'rss_refresh' AND wi.state IN ('available', 'leased')
  )`, subscriptionID)
		if err != nil {
			return Subscription{}, fmt.Errorf("list custom sources: %w", err)
		}
		type customSource struct {
			id             string
			runtimeVersion int64
		}
		var customSources []customSource
		for rows.Next() {
			var source customSource
			if err := rows.Scan(&source.id, &source.runtimeVersion); err != nil {
				rows.Close()
				return Subscription{}, fmt.Errorf("scan custom source: %w", err)
			}
			customSources = append(customSources, source)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return Subscription{}, err
		}
		rows.Close()
		for _, source := range customSources {
			if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
				Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: source.id},
				Kind:                 workitems.KindRSSRefresh,
				IdempotencyKey:       "rss_refresh:" + source.id,
				InputSnapshot:        json.RawMessage(`{"trigger":"subscription_resumed"}`),
				MaxAttempts:          animeapplication.DefaultMaxAttempts,
				ExpectedOwnerVersion: source.runtimeVersion,
			}); err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
				return Subscription{}, fmt.Errorf("schedule refresh on resume: %w", err)
			}
		}
		// Imports that waited for configuration while the subscription was
		// stopped check it again.
		if err := animeapplication.WakeConfigWaitingPlansTx(ctx, tx, managedAnimeID); err != nil {
			return Subscription{}, err
		}
	}
	// Record the business main-line entry for an actual state transition;
	// same-state saves bump the CAS version but are not new transitions.
	if currentState != string(patch.State) {
		transition, action := businesslog.SubscriptionTransitionResumed, plugintargetport.SubscriptionResumed
		switch {
		case started:
			transition, action = businesslog.SubscriptionTransitionStarted, plugintargetport.SubscriptionStarted
		case patch.State == subscriptiondomain.SubscriptionStopped:
			transition, action = businesslog.SubscriptionTransitionStopped, plugintargetport.SubscriptionStopped
		}
		if err := businesslog.WriteSubscriptionStateTx(ctx, txscope.Wrap(tx), s.activityWriter, subscriptionID, managedAnimeID,
			transition, version+1); err != nil {
			return Subscription{}, fmt.Errorf("write subscription state log: %w", err)
		}
		if err := s.admitSubscriptionChangeTx(ctx, tx, subscriptionID, version+1, managedAnimeID, animeTitle, action); err != nil {
			return Subscription{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, fmt.Errorf("commit subscription patch: %w", err)
	}
	// Resume dispatch after commit: a fresh reevaluation epoch for the anime.
	// The trigger merges into the one live evaluate WorkItem per anime and
	// records any failure as an observable async orchestration failure; the
	// resumed owner truth is already committed and never rolled back. Stop
	// dispatches nothing: the bumped Subscription version makes old admitted
	// decisions stale at the next settle revalidation (zero new effects).
	if started || (currentState == string(subscriptiondomain.SubscriptionStopped) &&
		patch.State == subscriptiondomain.SubscriptionActive) {
		s.discovery.DiscoverAfterCommit(ctx, managedAnimeID, origin)
	}
	if currentState == string(subscriptiondomain.SubscriptionStopped) &&
		patch.State == subscriptiondomain.SubscriptionActive && s.replacement != nil {
		if err := s.replacement.TriggerSubscriptionResumed(ctx, managedAnimeID); err != nil {
			// recorded by the trigger; the resume response stays a success.
		}
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetCatalog)
	result := Subscription{
		ID:             subscriptionID,
		ManagedAnimeID: managedAnimeID,
		Origin:         origin,
		State:          patch.State,
		LifecycleState: lifecycleState,
		LifecycleEpoch: lifecycleEpoch,
		Version:        version + 1,
		// The option is frozen from here on; the start never changes it.
		NewReleasesOnly: newReleasesOnly,
	}
	if startBaselineAt.Valid {
		result.StartBaselineAt = &startBaselineAt.Time
	}
	if retiredAt.Valid {
		result.RetiredAt = &retiredAt.Time
	}
	if originEntryRef.Valid {
		result.OriginEntryRef = &originEntryRef.String
	}
	if instanceLabel.Valid {
		result.InstanceLabel = &instanceLabel.String
	}
	if entrySeason.Valid {
		result.EntrySeason = &entrySeason.String
	}
	result.CreatedAt = createdAt
	return result, nil
}

// scheduleStartRefreshesTx enqueues the initial rss_refresh of every source a
// first start makes eligible: custom sources and provider groups that are
// selected and available. A source with a live refresh keeps it.
func (s *Service) scheduleStartRefreshesTx(ctx context.Context, tx *sql.Tx, subscriptionID string) error {
	rows, err := tx.QueryContext(ctx, `
SELECT src.id, rt.version
FROM rss_sources src
JOIN rss_source_runtime rt ON rt.rss_source_id = src.id
WHERE src.subscription_id = $1
  AND (
    src.source_kind = 'custom_rss'
    OR EXISTS (
      SELECT 1 FROM provider_group_selections selection
      WHERE selection.subscription_id = src.subscription_id
        AND selection.provider = src.provider
        AND selection.group_ref = src.provider_group_ref
        AND selection.effective_selected AND selection.available
    )
  )
ORDER BY src.id`, subscriptionID)
	if err != nil {
		return fmt.Errorf("list sources to start: %w", err)
	}
	type startSource struct {
		id             string
		runtimeVersion int64
	}
	var sources []startSource
	for rows.Next() {
		var source startSource
		if err := rows.Scan(&source.id, &source.runtimeVersion); err != nil {
			rows.Close()
			return fmt.Errorf("scan source to start: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, source := range sources {
		if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: source.id},
			Kind:                 workitems.KindRSSRefresh,
			IdempotencyKey:       "rss_refresh:" + source.id,
			InputSnapshot:        json.RawMessage(`{"trigger":"subscription_started"}`),
			MaxAttempts:          animeapplication.DefaultMaxAttempts,
			ExpectedOwnerVersion: source.runtimeVersion,
		}); err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
			return fmt.Errorf("schedule refresh on start: %w", err)
		}
	}
	return nil
}

func syncExistingProviderSelections(ctx context.Context, tx *sql.Tx, subscriptionID string, section SubgroupsSection, version int64) error {
	normalized, err := animedomain.SubgroupsSectionWithEffectiveSelections(domainSubgroupsSection(section))
	if err != nil {
		return err
	}
	for _, selection := range providerSelectionsFromDomainSection(normalized, subscriptionID, version) {
		if _, err := tx.ExecContext(ctx, `
UPDATE provider_group_selections
SET display_name = $4, selection_mode = $5, manual_selected = $6,
    effective_selected = $7, effective_score = $8,
    version = version + 1, updated_at = NOW()
WHERE subscription_id = $1 AND provider = $2 AND group_ref = $3`,
			subscriptionID, string(selection.Provider), selection.GroupRef, selection.DisplayName,
			string(selection.SelectionMode), selection.ManualSelected, selection.EffectiveSelected,
			selection.EffectiveScore); err != nil {
			return fmt.Errorf("sync provider group selection %s: %w", applicationSubgroupEntryKey(selection.Provider, selection.GroupRef), err)
		}
	}
	return nil
}

func (s *Service) scheduleUnsubmittedReleaseEvaluation(ctx context.Context, tx *sql.Tx, query string, scopeID, trigger string) error {
	_, err := s.scheduleUnsubmittedReleaseEvaluationCount(ctx, tx, query, scopeID, trigger)
	return err
}

func (s *Service) scheduleUnsubmittedReleaseEvaluationCount(ctx context.Context, tx *sql.Tx, query, scopeID, trigger string) (int, error) {
	rows, err := tx.QueryContext(ctx, query, scopeID)
	if err != nil {
		return 0, fmt.Errorf("list unsubmitted releases: %w", err)
	}
	type releaseTarget struct {
		id      string
		version int64
	}
	var releases []releaseTarget
	for rows.Next() {
		var release releaseTarget
		if err := rows.Scan(&release.id, &release.version); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan unsubmitted release: %w", err)
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate unsubmitted releases: %w", err)
	}
	rows.Close()
	snapshot, err := json.Marshal(map[string]string{"trigger": trigger})
	if err != nil {
		return 0, fmt.Errorf("encode release evaluation trigger: %w", err)
	}
	scheduled := 0
	for _, release := range releases {
		_, created, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: release.id},
			Kind:                 workitems.KindReleaseEvaluate,
			IdempotencyKey:       "release_evaluate:" + release.id,
			InputSnapshot:        snapshot,
			MaxAttempts:          animeapplication.DefaultMaxAttempts,
			ExpectedOwnerVersion: release.version,
		})
		if err != nil {
			return 0, fmt.Errorf("schedule release re-evaluation: %w", err)
		}
		if created {
			scheduled++
		}
	}
	return scheduled, nil
}

// ReleaseReevaluationResult reports the work admitted for existing releases.
type ReleaseReevaluationResult struct {
	ManagedAnimeID string
	Accepted       bool
	ScheduledCount int
}

// ReevaluateAnimeReleasesApplication schedules evaluation for all unsubmitted
// Releases owned by one anime. Submitted download work keeps its frozen input.
func (s *Service) ReevaluateAnimeReleasesApplication(ctx context.Context, managedAnimeID string) (ReleaseReevaluationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReleaseReevaluationResult{}, fmt.Errorf("begin release re-evaluation: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return ReleaseReevaluationResult{}, err
	}
	scheduled, err := s.scheduleUnsubmittedReleaseEvaluationCount(ctx, tx, unsubmittedReleasesSQL, managedAnimeID, "manual_reevaluate")
	if err != nil {
		return ReleaseReevaluationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReleaseReevaluationResult{}, fmt.Errorf("commit release re-evaluation: %w", err)
	}
	s.publishInvalidations(events.TargetRSS)
	return ReleaseReevaluationResult{
		ManagedAnimeID: managedAnimeID,
		Accepted:       true,
		ScheduledCount: scheduled,
	}, nil
}

func domainSubgroupsSection(value SubgroupsSection) animedomain.SubgroupsSection {
	result := animedomain.SubgroupsSection{MaxRss: value.MaxRss}
	if value.Entries != nil {
		result.Entries = make([]animedomain.SubgroupScoreEntry, len(value.Entries))
	}
	for i, entry := range value.Entries {
		result.Entries[i] = animedomain.SubgroupScoreEntry{
			DisplayName:       entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected, GroupRef: entry.GroupRef,
			ManualSelected: cloneBoolValue(entry.ManualSelected),
			Provider:       animedomain.ProviderKind(entry.Provider), Score: entry.Score,
			SelectionMode: animedomain.SelectionMode(entry.SelectionMode),
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &animedomain.ProviderNameKeywordRules{
			Anibt: domainNameKeywordRules(value.NameKeywords.Anibt),
			Mikan: domainNameKeywordRules(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &animedomain.ProviderGroupDefaults{
			Anibt: domainProviderGroupDefault(value.ProviderDefaults.Anibt),
			Mikan: domainProviderGroupDefault(value.ProviderDefaults.Mikan),
		}
	}
	return result
}

func domainNameKeywordRules(value *[]NameKeywordRule) *[]animedomain.NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]animedomain.NameKeywordRule, len(*value))
	for i, rule := range *value {
		result[i] = animedomain.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score}
	}
	return &result
}

func domainProviderGroupDefault(value *ProviderGroupDefault) *animedomain.ProviderGroupDefault {
	if value == nil {
		return nil
	}
	result := &animedomain.ProviderGroupDefault{
		Groups: animeapplication.CloneStringSlice(value.Groups),
		Mode:   animedomain.ProviderGroupDefaultMode(value.Mode), Score: value.Score,
	}
	if value.PinnedScores != nil {
		result.PinnedScores = make([]animedomain.PinnedGroupScore, len(value.PinnedScores))
		for i, pin := range value.PinnedScores {
			result.PinnedScores[i] = animedomain.PinnedGroupScore{GroupRef: pin.GroupRef, Score: pin.Score}
		}
	}
	return result
}

// providerSelectionsFromApplicationSection projects a complete subgroups
// section into the owner's provider-group selection rows. The effective flag
// comes from the domain group-selection authority, so the persisted owner rows
// can never disagree with the saved revisions or with the runtime selection.
// normalizeSubgroupsEffectiveSelection applies the runtime decision core to one
// section and returns both the application-shaped section whose entries carry
// the computed effective flags and the owner-shaped selection projection. Every
// read that hands a section to a client goes through it, so the flags a caller
// reads are the ones the runtime would apply instead of a stale discovery-time
// value it would have to re-derive.
func normalizeSubgroupsEffectiveSelection(section SubgroupsSection, subscriptionID string, version int64) (SubgroupsSection, []ProviderGroupSelection, error) {
	normalized, err := animedomain.SubgroupsSectionWithEffectiveSelections(domainSubgroupsSection(section))
	if err != nil {
		return SubgroupsSection{}, nil, err
	}
	return applicationSubgroupsSectionFromDomain(normalized), providerSelectionsFromDomainSection(normalized, subscriptionID, version), nil
}

// providerSelectionsFromDomainSection projects an already-normalized section.
func providerSelectionsFromDomainSection(normalized animedomain.SubgroupsSection, subscriptionID string, version int64) []ProviderGroupSelection {
	result := make([]ProviderGroupSelection, 0, len(normalized.Entries))
	for _, entry := range normalized.Entries {
		score := entry.Score
		if entry.SelectionMode == animedomain.SelectionModeManual && entry.ManualSelected != nil && *entry.ManualSelected {
			// A required group is written and compared at the fixed maximum.
			score = selectiondomain.AlwaysGroupScore
		}
		result = append(result, ProviderGroupSelection{
			ID: subscriptionID + ":" + entry.GroupRef, SubscriptionID: subscriptionID,
			Provider: string(entry.Provider), GroupRef: entry.GroupRef, DisplayName: entry.DisplayName,
			SelectionMode: string(entry.SelectionMode), ManualSelected: cloneBoolValue(entry.ManualSelected),
			EffectiveSelected: entry.EffectiveSelected, EffectiveScore: score, Version: version,
		})
	}
	return result
}

func applicationSubgroupEntryKey(provider, groupRef string) string {
	return provider + ":" + groupRef
}

// applyGroupActions mutates the supplied subgroups section by applying manual
// include/exclude or restore-auto actions. Actions target entries that must
// already exist in the section.
func applyGroupActions(section *SubgroupsSection, actions []GroupAction) error {
	if len(actions) == 0 {
		return nil
	}
	indexByKey := make(map[string]int, len(section.Entries))
	for i, e := range section.Entries {
		indexByKey[applicationSubgroupEntryKey(e.Provider, e.GroupRef)] = i
	}
	for _, action := range actions {
		key := applicationSubgroupEntryKey(action.Provider, action.GroupRef)
		idx, ok := indexByKey[key]
		if !ok {
			return fmt.Errorf("group action targets unknown group %s", key)
		}
		entry := &section.Entries[idx]
		switch action.Action {
		case GroupActionForceInclude:
			entry.SelectionMode = string(animedomain.SelectionModeManual)
			selected := true
			entry.ManualSelected = &selected
			entry.EffectiveSelected = true
		case GroupActionForceExclude:
			entry.SelectionMode = string(animedomain.SelectionModeManual)
			selected := false
			entry.ManualSelected = &selected
			entry.EffectiveSelected = false
		case GroupActionRestoreAuto:
			entry.SelectionMode = string(animedomain.SelectionModeAuto)
			entry.ManualSelected = nil
			entry.EffectiveSelected = false
		default:
			return fmt.Errorf("unknown group action %q", action.Action)
		}
	}
	return nil
}
