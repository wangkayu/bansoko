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
	automationapp "bansoko/internal/automation/application"
	selectionapp "bansoko/internal/selection/application"
	selectiondomain "bansoko/internal/selection/domain"
)

// This file holds the owner-chain reads and owner projections that the
// ownership use case owns and mutates (subgroups owner selection, RSS source
// rows, metadata bindings). The read models call them; ownership patches them.
// They live in an ownership file so the read model's own file keeps only its
// projection logic.

// ReadRulesOwnerApplication reads the persisted rules owner projection. The
// read model consumes this owner's projection instead of reaching into its
// tables, so the flags a caller reads are the ones the owner computes.
func (s *Service) ReadRulesOwnerApplication(ctx context.Context, managedAnimeID string) (RulesOwner, error) {
	return s.readRulesOwnerApplication(ctx, managedAnimeID)
}

// ReadSubgroupsOwnerApplication reads the persisted subgroup owner projection.
func (s *Service) ReadSubgroupsOwnerApplication(ctx context.Context, managedAnimeID, subscriptionID string) (SubgroupsOwnerResult, error) {
	return s.readSubgroupsOwnerApplication(ctx, managedAnimeID, subscriptionID)
}

// ReadRSSSourcesApplication reads the source owner rows for a subscription.
func (s *Service) ReadRSSSourcesApplication(ctx context.Context, subscriptionID string) ([]RSSSource, error) {
	return s.readRSSSourcesApplication(ctx, subscriptionID)
}

// ReadRSSSourceRuntimesApplication reads runtime state for a subscription's sources.
func (s *Service) ReadRSSSourceRuntimesApplication(ctx context.Context, subscriptionID string) ([]RSSSourceRuntime, error) {
	return s.readRSSSourceRuntimesApplication(ctx, subscriptionID)
}

func (s *Service) readSubgroupsOwnerApplication(ctx context.Context, managedAnimeID, subscriptionID string) (SubgroupsOwnerResult, error) {
	var ownerVersion int64
	if err := s.db.QueryRowContext(ctx, `SELECT subgroups_version FROM anime_business_configs WHERE managed_anime_id = $1`, managedAnimeID).Scan(&ownerVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SubgroupsOwnerResult{}, &apperrors.NotFound{Resource: "subgroups_owner", ID: managedAnimeID}
		}
		return SubgroupsOwnerResult{}, fmt.Errorf("read subgroups owner: %w", err)
	}
	subgroupsOverride, rulesOverrides, err := s.readOverrides(ctx, managedAnimeID)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	effective, err := animedomain.ResolveEffective(subgroupsOverride, rulesOverrides)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	selection := applicationSubgroupsSectionFromDomain(effective.Selection)
	selection, err = s.mergePersistedProviderSelectionsApplication(ctx, subscriptionID, selection)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	// The section and the effective selection present one group order
	// (subgroups_order.go); the persisted reads stay deterministic on their own.
	sortSubgroupScoreEntries(selection.Entries)
	sources, err := s.readRSSSourcesApplication(ctx, subscriptionID)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	normalized := selection
	effectiveSelection, err := s.readPersistedSelections(ctx, subscriptionID)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	// Every row is explained. The reason and score breakdown come from the live
	// decision the runtime recompute makes (the stored section plus the live
	// site policy, over the persisted rows and latest group snapshot). When the
	// stored flag has drifted from it (a site policy change the next recompute
	// has not applied yet), the row still carries the live verdict: that is
	// what the next recompute will do, and exactly what the user needs to see.
	// A row the live decision does not cover (a subscription that is not
	// provider-origin, or another provider's row) falls back to the replay of
	// the stored section, which is the only verdict such a row ever receives.
	// A retired group keeps its row but is decided unavailable either way.
	decisions, err := s.liveOwnerDecisions(ctx, subscriptionID, effective.Selection, ownerVersion)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	replayed, err := replayedOwnerReasons(normalized, effectiveSelection)
	if err != nil {
		return SubgroupsOwnerResult{}, err
	}
	for i := range effectiveSelection {
		key := applicationSubgroupEntryKey(effectiveSelection[i].Provider, effectiveSelection[i].GroupRef)
		if decision, ok := decisions[key]; ok {
			value := decision.Reason
			effectiveSelection[i].ReasonCode = &value
			effectiveSelection[i].ScoreBreakdown = SubgroupScoreBreakdownFromDomain(decision.Group.Breakdown)
			continue
		}
		if decision, ok := replayed[key]; ok {
			value := decision.Reason
			effectiveSelection[i].ReasonCode = &value
		}
	}
	sortProviderGroupSelections(effectiveSelection)
	return SubgroupsOwnerResult{
		ManagedAnimeID:     managedAnimeID,
		Subgroups:          normalized,
		EffectiveSelection: effectiveSelection,
		RSSSources:         sources,
		Version:            ownerVersion,
	}, nil
}

func applicationSubgroupsSectionFromDomain(value animedomain.SubgroupsSection) SubgroupsSection {
	result := SubgroupsSection{MaxRss: value.MaxRss, Entries: make([]SubgroupScoreEntry, len(value.Entries))}
	for i, entry := range value.Entries {
		result.Entries[i] = SubgroupScoreEntry{
			DisplayName:       entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected, GroupRef: entry.GroupRef,
			ManualSelected: animeapplication.CloneBool(entry.ManualSelected), Provider: string(entry.Provider),
			Score: entry.Score, SelectionMode: string(entry.SelectionMode),
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &ProviderNameKeywordRules{
			Anibt: applicationNameKeywordRules(value.NameKeywords.Anibt),
			Mikan: applicationNameKeywordRules(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &ProviderGroupDefaults{
			Anibt: applicationProviderGroupDefault(value.ProviderDefaults.Anibt),
			Mikan: applicationProviderGroupDefault(value.ProviderDefaults.Mikan),
		}
	}
	return result
}

func applicationNameKeywordRules(value *[]animedomain.NameKeywordRule) *[]NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]NameKeywordRule, len(*value))
	for i, rule := range *value {
		result[i] = NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score}
	}
	return &result
}

func applicationProviderGroupDefault(value *animedomain.ProviderGroupDefault) *ProviderGroupDefault {
	if value == nil {
		return nil
	}
	result := &ProviderGroupDefault{Groups: animeapplication.CloneStringSlice(value.Groups), Mode: string(value.Mode), Score: value.Score}
	if value.PinnedScores != nil {
		result.PinnedScores = make([]PinnedGroupScore, len(value.PinnedScores))
		for i, pin := range value.PinnedScores {
			result.PinnedScores[i] = PinnedGroupScore{GroupRef: pin.GroupRef, Score: pin.Score}
		}
	}
	return result
}

func (s *Service) mergePersistedProviderSelectionsApplication(ctx context.Context, subscriptionID string, section SubgroupsSection) (SubgroupsSection, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, group_ref, display_name, selection_mode, manual_selected,
       effective_selected, effective_score
FROM provider_group_selections
WHERE subscription_id = $1
ORDER BY provider, group_ref`, subscriptionID)
	if err != nil {
		return SubgroupsSection{}, fmt.Errorf("read persisted provider selections: %w", err)
	}
	defer rows.Close()
	result := section
	entries := make([]SubgroupScoreEntry, len(section.Entries))
	copy(entries, section.Entries)
	byKey := make(map[string]int, len(entries))
	for index, entry := range entries {
		byKey[applicationSubgroupEntryKey(entry.Provider, entry.GroupRef)] = index
	}
	for rows.Next() {
		var provider, groupRef, displayName, mode string
		var manualSelected sql.NullBool
		var effectiveSelected bool
		var score int
		if err := rows.Scan(&provider, &groupRef, &displayName, &mode, &manualSelected, &effectiveSelected, &score); err != nil {
			return SubgroupsSection{}, fmt.Errorf("scan persisted provider selection: %w", err)
		}
		if mode == string(animedomain.SelectionModeAuto) {
			// An automatic entry's score is a projection of the live site
			// policy, not an input (a site-excluded group scores 0, keywords
			// may pass 100). Keep it inside the section's 1..100 range so the
			// value this read hands out saves back unchanged; the real score
			// and its breakdown are on the effective selection.
			score = min(max(score, 1), 100)
		}
		entry := SubgroupScoreEntry{Provider: provider, GroupRef: groupRef, DisplayName: displayName, SelectionMode: mode, EffectiveSelected: effectiveSelected, Score: score}
		if manualSelected.Valid {
			value := manualSelected.Bool
			entry.ManualSelected = &value
		}
		if index, ok := byKey[applicationSubgroupEntryKey(provider, groupRef)]; ok {
			entries[index] = entry
		} else {
			byKey[applicationSubgroupEntryKey(provider, groupRef)] = len(entries)
			entries = append(entries, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return SubgroupsSection{}, fmt.Errorf("iterate persisted provider selections: %w", err)
	}
	result.Entries = entries
	return result, nil
}

func (s *Service) readRSSSourceRuntimesApplication(ctx context.Context, subscriptionID string) ([]RSSSourceRuntime, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT rt.rss_source_id, rt.cursor, rt.last_good_facts, rt.last_refresh_at, rt.last_success_at,
       rt.health, rt.reason_code, rt.reason_detail, rt.version, fw.id
FROM rss_source_runtime rt
JOIN rss_sources src ON src.id = rt.rss_source_id
LEFT JOIN LATERAL (
  SELECT w.id FROM work_item_outcomes w
  WHERE w.owner_type = 'rss_source_runtime' AND w.owner_id = rt.rss_source_id AND w.state = 'failed'
  ORDER BY w.terminal_at DESC, w.id DESC
  LIMIT 1
) fw ON TRUE
WHERE src.subscription_id = $1
ORDER BY rt.rss_source_id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read rss source runtimes: %w", err)
	}
	defer rows.Close()
	result := make([]RSSSourceRuntime, 0)
	for rows.Next() {
		var item RSSSourceRuntime
		var cursor, lastGood []byte
		var lastRefresh, lastSuccess sql.NullTime
		var health string
		var reasonCode, reasonDetail, failedWorkItemID sql.NullString
		if err := rows.Scan(&item.RSSSourceID, &cursor, &lastGood, &lastRefresh, &lastSuccess, &health, &reasonCode, &reasonDetail, &item.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan rss source runtime: %w", err)
		}
		item.Health = health
		if len(cursor) > 0 {
			var value RSSCursor
			if err := json.Unmarshal(cursor, &value); err == nil {
				item.Cursor = &value
			}
		}
		if len(lastGood) > 0 {
			var value RSSLastGoodFacts
			if err := json.Unmarshal(lastGood, &value); err == nil {
				item.LastGoodFacts = &value
			}
		}
		if lastRefresh.Valid {
			value := lastRefresh.Time
			item.LastRefreshAt = &value
		}
		if lastSuccess.Valid {
			value := lastSuccess.Time
			item.LastSuccessAt = &value
		}
		if reasonCode.Valid {
			value := reasonCode.String
			item.ReasonCode = &value
		}
		if reasonDetail.Valid {
			value := reasonDetail.String
			item.ReasonDetail = &value
		}
		if failedWorkItemID.Valid {
			value := failedWorkItemID.String
			item.FailedWorkItemID = &value
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rss source runtimes: %w", err)
	}
	return result, nil
}

func (s *Service) readRSSSourcesApplication(ctx context.Context, subscriptionID string) ([]RSSSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT rs.id, rs.source_kind, rs.locator_display, rs.group_name, rs.title, rs.episode_offset,
       rs.anibt_aux_mode, rs.provenance, rs.http_feed_connection_ref, rs.network_route_ref, rs.version
FROM rss_sources rs
LEFT JOIN provider_group_selections pgs ON pgs.subscription_id = rs.subscription_id
  AND pgs.provider = rs.provider AND pgs.group_ref = rs.provider_group_ref
WHERE rs.subscription_id = $1
-- The RSS tab presents sources in the group order (subgroups_order.go):
-- the custom source first, then selected provider groups, a higher effective
-- score next, the display name breaking ties and created_at/id keeping rows
-- with equal keys stable.
ORDER BY (rs.source_kind = 'custom_rss') DESC,
         (pgs.effective_selected AND pgs.available) DESC NULLS LAST,
         pgs.effective_score DESC NULLS LAST,
         COALESCE(NULLIF(rs.group_name, ''), rs.provider_group_ref, '') COLLATE "C" ASC,
         rs.provider_group_ref COLLATE "C" ASC NULLS LAST,
         rs.created_at ASC, rs.id ASC`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read rss sources: %w", err)
	}
	defer rows.Close()
	result := make([]RSSSource, 0)
	for rows.Next() {
		var source RSSSource
		var sourceKind, locatorDisplay, groupName, title sql.NullString
		var anibtAuxMode sql.NullString
		var connectionRef, routeRef sql.NullString
		var rawProvenance []byte
		if err := rows.Scan(&source.ID, &sourceKind, &locatorDisplay, &groupName, &title, &source.EpisodeOffset, &anibtAuxMode, &rawProvenance, &connectionRef, &routeRef, &source.Version); err != nil {
			return nil, fmt.Errorf("scan rss source: %w", err)
		}
		source.SubscriptionID = subscriptionID
		source.SourceKind = animedomain.SourceKind(sourceKind.String)
		source.LocatorDisplay = animedomain.SanitizeLocatorDisplay(locatorDisplay.String)
		source.GroupName, source.Title = groupName.String, title.String
		if anibtAuxMode.Valid {
			value := animedomain.RSSSourceDraftAnibtAuxMode(anibtAuxMode.String)
			source.AnibtAuxMode = &value
		}
		if connectionRef.Valid {
			value := connectionRef.String
			source.HttpFeedConnectionRef = &value
		}
		if routeRef.Valid {
			value := routeRef.String
			source.NetworkRouteRef = &value
		}
		if rawProvenance != nil {
			var value animedomain.ProviderProvenance
			if err := json.Unmarshal(rawProvenance, &value); err != nil {
				return nil, fmt.Errorf("decode rss source provenance: %w", err)
			}
			source.ProviderProvenance = &value
		}
		result = append(result, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rss sources: %w", err)
	}
	return result, nil
}

type SubgroupsOwnerResult struct {
	EffectiveSelection []ProviderGroupSelection
	ManagedAnimeID     string
	RSSSources         []RSSSource
	Subgroups          SubgroupsSection
	Version            int64
}

func applicationBindingsFromRow(bgmWorkID, tmdbWorkID, tmdbMediaKind sql.NullString, tmdbSeason sql.NullInt32) (*BGMBinding, *TMDBBinding) {
	var bgm *BGMBinding
	if bgmWorkID.Valid {
		bgm = &BGMBinding{WorkID: bgmWorkID.String}
	}
	var tmdb *TMDBBinding
	if tmdbWorkID.Valid {
		tmdb = &TMDBBinding{WorkID: tmdbWorkID.String, MediaKind: tmdbMediaKind.String, Season: int(tmdbSeason.Int32)}
	}
	return bgm, tmdb
}

func metadataHoldFromApplication(bgm *BGMBinding, tmdb *TMDBBinding) MetadataHold {
	var domainBGM *animedomain.NullableBGMBinding
	if bgm != nil {
		domainBGM = &animedomain.NullableBGMBinding{WorkId: bgm.WorkID}
	}
	var domainTMDB *animedomain.NullableTMDBBinding
	if tmdb != nil {
		domainTMDB = &animedomain.NullableTMDBBinding{WorkId: tmdb.WorkID, MediaKind: animedomain.TMDBMediaKind(tmdb.MediaKind), Season: tmdb.Season}
	}
	hold := animedomain.MetadataHoldFromBindings(domainBGM, domainTMDB)
	missing := make([]string, len(hold.Missing))
	for i, value := range hold.Missing {
		missing[i] = string(value)
	}
	return MetadataHold{Held: hold.Held, Missing: missing}
}

// replayedOwnerReasons replays the stored section (merged with the persisted
// rows) through the decision core, per provider, for rows the live decision
// does not cover. An unavailable row is explained as availability, never as a
// policy verdict.
func replayedOwnerReasons(section SubgroupsSection, rows []ProviderGroupSelection) (map[string]animedomain.SubgroupSelectionDecision, error) {
	unavailable := make(map[string]struct{}, len(rows))
	keys := make([]string, len(rows))
	for i, row := range rows {
		key := applicationSubgroupEntryKey(row.Provider, row.GroupRef)
		keys[i] = key
		if !row.Available {
			unavailable[key] = struct{}{}
		}
	}
	return animedomain.SubgroupsSectionSelectionReasonsForKeys(domainSubgroupsSection(section), keys, unavailable)
}

// liveOwnerDecisions is the decision the runtime recompute would make now for
// one subscription, keyed "<provider>:<group_ref>". Only a provider-origin
// (anibt/mikan) subscription has one: the recompute never runs for any other,
// so its section is not composed into a live policy here either.
func (s *Service) liveOwnerDecisions(ctx context.Context, subscriptionID string, section animedomain.SubgroupsSection, ownerVersion int64) (map[string]selectiondomain.GroupSelectionDecision, error) {
	if subscriptionID == "" {
		return nil, nil
	}
	var origin string
	if err := s.db.QueryRowContext(ctx, `SELECT origin FROM subscriptions WHERE id = $1`, subscriptionID).Scan(&origin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read subscription origin: %w", err)
	}
	if origin != "anibt" && origin != "mikan" {
		return nil, nil
	}
	raw, err := json.Marshal(section)
	if err != nil {
		return nil, fmt.Errorf("encode subgroups section: %w", err)
	}
	policy, err := selectionapp.ReadLiveDiscoveryPolicy(ctx, s.db, raw, ownerVersion)
	if err != nil {
		return nil, animeapplication.SectionResolutionError(err)
	}
	preview, err := automationapp.PreviewSubscriptionSelections(ctx, s.db, subscriptionID, policy, nil)
	if err != nil {
		return nil, err
	}
	result := make(map[string]selectiondomain.GroupSelectionDecision, len(preview.Decisions))
	for _, decision := range preview.Decisions {
		result[applicationSubgroupEntryKey(preview.Provider, decision.Group.Ref.ExternalID)] = decision
	}
	return result, nil
}
