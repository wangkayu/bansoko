package application

import (
	animeapplication "bansoko/internal/anime/application"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/apperrors"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

type DiscoverSubgroupsInput struct {
	ExpectedVersion int64
}

type DiscoverSubgroupsOutcome string

const (
	DiscoverSubgroupsOutcomeConfigError   DiscoverSubgroupsOutcome = "config_error"
	DiscoverSubgroupsOutcomeDiscovered    DiscoverSubgroupsOutcome = "discovered"
	DiscoverSubgroupsOutcomeNoGroups      DiscoverSubgroupsOutcome = "no_groups"
	DiscoverSubgroupsOutcomeProviderError DiscoverSubgroupsOutcome = "provider_error"
)

// ProviderGroupSelection is the application-owned read model for one persisted
// provider selection. The HTTP transport projects it onto the generated DTO.
type ProviderGroupSelection struct {
	Available              bool
	AvailabilityGeneration int64
	DisplayName            string
	EffectiveScore         int
	EffectiveSelected      bool
	GroupRef               string
	ID                     string
	ManualSelected         *bool
	Provider               string
	// ReasonCode answers why the group is in this state, taken from the same
	// group-selection decision that produced EffectiveSelected. It is set for
	// both states (policy_always / ranked_within_max_rss for a selected group),
	// so a caller never has to reconstruct the rules from the flags.
	ReasonCode *string
	// ScoreBreakdown is the same decision's score explanation, present
	// exactly when ReasonCode is.
	ScoreBreakdown *SubgroupScoreBreakdown
	SelectionMode  string
	SubscriptionID string
	Version        int64
}

type SubgroupScoreEntry struct {
	DisplayName       string
	EffectiveSelected bool
	GroupRef          string
	ManualSelected    *bool
	Provider          string
	Score             int
	SelectionMode     string
}

type NameKeywordRule struct {
	Keyword string
	Score   int
}

type ProviderNameKeywordRules struct {
	Anibt *[]NameKeywordRule
	Mikan *[]NameKeywordRule
}

type PinnedGroupScore struct {
	GroupRef string
	Score    int
}

type ProviderGroupDefault struct {
	Groups       []string
	Mode         string
	Score        int
	PinnedScores []PinnedGroupScore
}

type ProviderGroupDefaults struct {
	Anibt *ProviderGroupDefault
	Mikan *ProviderGroupDefault
}

type SubgroupsSection struct {
	Entries          []SubgroupScoreEntry
	MaxRss           int
	NameKeywords     *ProviderNameKeywordRules
	ProviderDefaults *ProviderGroupDefaults
}

type DiscoverRSSSource struct {
	AnibtAuxMode          *string
	GroupName             string
	HttpFeedConnectionRef *string
	ID                    string
	LocatorDisplay        string
	NetworkRouteRef       *string
	EpisodeOffset         int
	ProviderProvenance    *DiscoverProviderProvenance
	SourceKind            string
	SubscriptionID        string
	Title                 string
	Version               int64
}

type DiscoverProviderProvenance struct {
	DiscoverySnapshotVersion string
	Provider                 string
	ProviderEntryRef         string
	ProviderGroupRef         string
}

// DiscoverSubgroupsOwnerResult is the transport-neutral subgroups owner view
// returned with a discovery outcome.
type DiscoverSubgroupsOwnerResult struct {
	EffectiveSelection []ProviderGroupSelection
	ManagedAnimeID     string
	RSSources          []DiscoverRSSSource
	Subgroups          SubgroupsSection
	Version            int64
}

// DiscoverSubgroupsResult is the transport-neutral result of synchronous
// provider-backed subgroup discovery.
type DiscoverSubgroupsResult struct {
	ManagedAnimeID string
	Outcome        DiscoverSubgroupsOutcome
	Reason         *string
	Subgroups      DiscoverSubgroupsOwnerResult
}

type discoverTarget struct {
	subscriptionID string
	origin         string
	entryRef       sql.NullString
	state          sql.NullString
	title          string
	ownerVersion   sql.NullInt64
}

// DiscoverAnimeSubgroups runs per-anime group discovery synchronously for the
// Subgroups Tab one-click action. It validates the subscription and subgroups
// owner CAS, then delegates to the wired GroupDiscoveryProvider (the automation
// Dispatcher). Provider and configuration failures are typed outcomes, not
// transport errors. Discovery writes to provider_group_selections and
// rss_sources; it does not bump the subgroups owner version.
func (s *Service) DiscoverAnimeSubgroups(ctx context.Context, managedAnimeID string, input DiscoverSubgroupsInput) (DiscoverSubgroupsResult, error) {
	target, err := readDiscoverTarget(ctx, s.db, managedAnimeID)
	if err != nil {
		return DiscoverSubgroupsResult{}, err
	}
	if !target.state.Valid {
		return DiscoverSubgroupsResult{}, &apperrors.Business{
			Status:  409,
			Code:    "SUBSCRIPTION_INACTIVE",
			Message: "Group discovery requires an active subscription.",
		}
	}
	// Group discovery is a configuration action, not automatic scheduling: an
	// explicit discovery is allowed for pending, active and stopped
	// Subscriptions (a pending one is still being configured; one-off tasks
	// stop the instance and still need to discover groups before refresh). It
	// schedules a refresh only for an active one. Only a missing/unknown state
	// is rejected.
	if state := subscriptiondomain.SubscriptionState(target.state.String); !state.Valid() {
		return DiscoverSubgroupsResult{}, &apperrors.Business{
			Status:  409,
			Code:    "SUBSCRIPTION_INACTIVE",
			Message: "Group discovery requires an active subscription.",
		}
	}
	if !target.ownerVersion.Valid {
		return DiscoverSubgroupsResult{}, &apperrors.NotFound{Resource: "subgroups_owner", ID: managedAnimeID}
	}
	if target.ownerVersion.Int64 != input.ExpectedVersion {
		return DiscoverSubgroupsResult{}, &apperrors.VersionConflict{Owner: "subgroups_owner", CurrentVersion: target.ownerVersion.Int64}
	}

	// Provider subscriptions only; custom RSS has no catalog entry to discover from.
	if (target.origin != "anibt" && target.origin != "mikan") || !target.entryRef.Valid {
		return s.buildDiscoverResult(ctx, managedAnimeID, target, DiscoverSubgroupsOutcomeConfigError, "Group discovery requires a provider subscription with a catalog entry.")
	}
	if animeapplication.IsTypedNil(s.groupDiscovery) {
		return s.buildDiscoverResult(ctx, managedAnimeID, target, DiscoverSubgroupsOutcomeConfigError, "Group discovery is not configured.")
	}

	result, discoverErr := s.groupDiscovery.DiscoverGroups(ctx, managedAnimeID)
	if discoverErr != nil {
		return s.buildDiscoverResult(ctx, managedAnimeID, target, DiscoverSubgroupsOutcomeProviderError, animeapplication.SafeReason(discoverErr.Error()))
	}

	outcome := DiscoverSubgroupsOutcome(result.Outcome)
	reason := result.Reason
	if reason == "" {
		switch outcome {
		case DiscoverSubgroupsOutcomeDiscovered:
			reason = fmt.Sprintf("Discovered %d group(s).", result.DiscoveredCount)
		case DiscoverSubgroupsOutcomeNoGroups:
			reason = "No groups were found."
		}
	}
	return s.buildDiscoverResult(ctx, managedAnimeID, target, outcome, reason)
}

func (s *Service) buildDiscoverResult(ctx context.Context, managedAnimeID string, target discoverTarget, outcome DiscoverSubgroupsOutcome, reason string) (DiscoverSubgroupsResult, error) {
	subgroups, err := s.readDiscoverSubgroupsOwner(ctx, managedAnimeID, target.subscriptionID)
	if err != nil {
		return DiscoverSubgroupsResult{}, err
	}
	result := DiscoverSubgroupsResult{
		ManagedAnimeID: managedAnimeID,
		Outcome:        outcome,
		Subgroups:      subgroups,
	}
	if reason != "" {
		r := animeapplication.SafeReason(reason)
		result.Reason = &r
	}
	return result, nil
}

func (s *Service) readDiscoverSubgroupsOwner(ctx context.Context, managedAnimeID, subscriptionID string) (DiscoverSubgroupsOwnerResult, error) {
	value, err := s.readSubgroupsOwnerApplication(ctx, managedAnimeID, subscriptionID)
	if err != nil {
		return DiscoverSubgroupsOwnerResult{}, err
	}
	result := DiscoverSubgroupsOwnerResult{
		ManagedAnimeID:     value.ManagedAnimeID,
		EffectiveSelection: cloneProviderGroupSelections(value.EffectiveSelection),
		RSSources:          make([]DiscoverRSSSource, 0, len(value.RSSSources)),
		Subgroups:          cloneSubgroupsSection(value.Subgroups),
		Version:            value.Version,
	}
	for _, source := range value.RSSSources {
		mapped := DiscoverRSSSource{
			GroupName:             source.GroupName,
			ID:                    source.ID,
			LocatorDisplay:        source.LocatorDisplay,
			EpisodeOffset:         source.EpisodeOffset,
			SourceKind:            string(source.SourceKind),
			SubscriptionID:        source.SubscriptionID,
			Title:                 source.Title,
			Version:               source.Version,
			HttpFeedConnectionRef: animeapplication.CloneString(source.HttpFeedConnectionRef),
			NetworkRouteRef:       animeapplication.CloneString(source.NetworkRouteRef),
		}
		if source.AnibtAuxMode != nil {
			mode := string(*source.AnibtAuxMode)
			mapped.AnibtAuxMode = &mode
		}
		if source.ProviderProvenance != nil {
			mapped.ProviderProvenance = &DiscoverProviderProvenance{
				DiscoverySnapshotVersion: source.ProviderProvenance.DiscoverySnapshotVersion,
				Provider:                 string(source.ProviderProvenance.Provider),
				ProviderEntryRef:         source.ProviderProvenance.ProviderEntryRef,
				ProviderGroupRef:         source.ProviderProvenance.ProviderGroupRef,
			}
		}
		result.RSSources = append(result.RSSources, mapped)
	}
	return result, nil
}

func cloneProviderGroupSelections(values []ProviderGroupSelection) []ProviderGroupSelection {
	if values == nil {
		return nil
	}
	result := make([]ProviderGroupSelection, len(values))
	for i, value := range values {
		result[i] = value
		result[i].ManualSelected = animeapplication.CloneBool(value.ManualSelected)
		result[i].ScoreBreakdown = cloneSubgroupScoreBreakdown(value.ScoreBreakdown)
	}
	return result
}

func cloneSubgroupsSection(value SubgroupsSection) SubgroupsSection {
	result := value
	if value.Entries != nil {
		result.Entries = make([]SubgroupScoreEntry, len(value.Entries))
		for i, entry := range value.Entries {
			result.Entries[i] = entry
			result.Entries[i].ManualSelected = animeapplication.CloneBool(entry.ManualSelected)
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &ProviderNameKeywordRules{
			Anibt: cloneNameKeywordRules(value.NameKeywords.Anibt),
			Mikan: cloneNameKeywordRules(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &ProviderGroupDefaults{
			Anibt: cloneProviderGroupDefault(value.ProviderDefaults.Anibt),
			Mikan: cloneProviderGroupDefault(value.ProviderDefaults.Mikan),
		}
	}
	return result
}

func cloneNameKeywordRules(value *[]NameKeywordRule) *[]NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]NameKeywordRule, len(*value))
	copy(result, *value)
	return &result
}

func cloneProviderGroupDefault(value *ProviderGroupDefault) *ProviderGroupDefault {
	if value == nil {
		return nil
	}
	result := *value
	result.Groups = animeapplication.CloneStringSlice(value.Groups)
	if value.PinnedScores != nil {
		result.PinnedScores = append([]PinnedGroupScore{}, value.PinnedScores...)
	}
	return &result
}

func readDiscoverTarget(ctx context.Context, db *sql.DB, managedAnimeID string) (discoverTarget, error) {
	var t discoverTarget
	err := db.QueryRowContext(ctx, `
SELECT s.id, s.origin, s.origin_entry_ref, s.state, ma.title,
       o.subgroups_version
FROM subscriptions s
JOIN managed_anime ma ON ma.id = s.managed_anime_id
LEFT JOIN anime_business_configs o ON o.managed_anime_id = ma.id
WHERE s.managed_anime_id = $1`, managedAnimeID).Scan(
		&t.subscriptionID, &t.origin, &t.entryRef, &t.state, &t.title, &t.ownerVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return discoverTarget{}, &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
	}
	if err != nil {
		return discoverTarget{}, fmt.Errorf("read discover target: %w", err)
	}
	return t, nil
}
