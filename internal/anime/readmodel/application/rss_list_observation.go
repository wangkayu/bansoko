package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	providerdomain "bansoko/internal/provider/domain"
	selectionapplication "bansoko/internal/selection/application"
	selectiondomain "bansoko/internal/selection/domain"
)

// Where a group's selection in the RSS list came from.
const (
	// RSSListSelectionSourceConfigured marks a group the draft's own subgroups
	// section configures explicitly.
	RSSListSelectionSourceConfigured = "configured"
	// RSSListSelectionSourceObservedDefault marks a group the provider lists for
	// the show but nobody configured: it takes the provider default policy, the
	// same selection the runtime would make once the show is subscribed.
	RSSListSelectionSourceObservedDefault = "observed_default"
)

// States of the observation a view was composed with.
const (
	RSSListObservationUsed        = "used"
	RSSListObservationNotRetained = "not_retained"
)

// RSSListObservationRef names one persisted provider group observation: the
// snapshot that group discovery wrote and returned the version of. The preview
// reads it; it never triggers discovery.
type RSSListObservationRef struct {
	Provider         string
	ProviderEntryRef string
	SnapshotVersion  int64
}

// RSSListObservation reports which observation a view used, or that the one
// asked for is no longer retained and the view was composed without it.
type RSSListObservation struct {
	State            string
	Provider         string
	ProviderEntryRef string
	SnapshotVersion  int64
	ObservedAt       *time.Time
	GroupCount       int
}

// ObservedGroupFact is one group of a persisted observation.
type ObservedGroupFact struct {
	GroupRef    string
	DisplayName string
}

// GroupObservation is one persisted provider group observation.
type GroupObservation struct {
	ObservedAt time.Time
	Groups     []ObservedGroupFact
}

// GroupObservationReader reads persisted group observations. A version that is
// not (or no longer) retained is reported as not found, never as an error.
type GroupObservationReader interface {
	ReadGroupObservation(ctx context.Context, ref RSSListObservationRef) (GroupObservation, bool, error)
}

func invalidObservation(message string) error {
	return &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: message}
}

// validateObservationMatchesDraft refuses an observation that does not belong to
// the draft's own catalog entry: an observation is evidence about one show on one
// provider, and mixing it into another draft would list the wrong groups.
func validateObservationMatchesDraft(draft animedomain.AnimeDraft, ref RSSListObservationRef) error {
	if ref.SnapshotVersion < 1 {
		return invalidObservation("observation.snapshot_version must be a positive version.")
	}
	if draft.Origin != animedomain.SubscriptionOriginAnibt && draft.Origin != animedomain.SubscriptionOriginMikan {
		return invalidObservation("an observation applies only to a provider draft.")
	}
	if ref.Provider != string(draft.Origin) {
		return invalidObservation("observation.provider does not match the draft's provider.")
	}
	entryRef := ""
	if draft.OriginEntryRef != nil {
		entryRef = strings.TrimSpace(*draft.OriginEntryRef)
	}
	if entryRef == "" || ref.ProviderEntryRef != entryRef {
		return invalidObservation("observation.provider_entry_ref does not match the draft's catalog entry.")
	}
	return nil
}

// applyObservedGroups is applyObservedGroupsWithSite under the code-default
// site policy.
func applyObservedGroups(draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, observed []ObservedGroupFact) (animedomain.EffectiveConfig, map[string]string, error) {
	effective, sources, _, err := applyObservedGroupsWithSite(draft, effective, observed, selectionapplication.DefaultSitePolicy())
	return effective, sources, err
}

// applyObservedGroupsWithSite returns the effective configuration with every
// observed group listed and the whole union ranked once under the live policy:
// the resolved section's max_rss and manual entries plus the site policy.
// Explicit entries keep their identity and mode, with the decision's selection
// and score projected onto them; a group nobody configured resolves through
// the site policy and is marked as such. The second result maps each group ref
// to its selection source, the third each group ref to its decision.
//
// The draft itself is never changed: the extra entries exist only in this read.
func applyObservedGroupsWithSite(draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, observed []ObservedGroupFact, site selectionapplication.SitePolicy) (animedomain.EffectiveConfig, map[string]string, map[string]selectiondomain.GroupSelectionDecision, error) {
	if draft.Origin != animedomain.SubscriptionOriginAnibt && draft.Origin != animedomain.SubscriptionOriginMikan {
		return effective, nil, nil, errors.New("observed groups apply only to a provider draft")
	}
	kind := providerdomain.Kind(draft.Origin)
	provider := animedomain.ProviderKind(draft.Origin)

	raw, err := json.Marshal(effective.Selection)
	if err != nil {
		return effective, nil, nil, fmt.Errorf("encode subgroups section: %w", err)
	}
	policy, err := selectionapplication.LiveDiscoveryPolicy(raw, effective.Versions[animedomain.SectionSubgroups], site)
	if err != nil {
		return effective, nil, nil, err
	}

	explicit := make(map[string]animedomain.SubgroupScoreEntry, len(effective.Selection.Entries))
	others := make([]animedomain.SubgroupScoreEntry, 0)
	for _, entry := range effective.Selection.Entries {
		if entry.Provider != provider {
			others = append(others, entry)
			continue
		}
		explicit[entry.GroupRef] = entry
	}

	union := make([]selectiondomain.ObservedGroup, 0, len(observed)+len(explicit))
	names := make(map[string]string, len(observed))
	seen := make(map[string]struct{}, len(observed))
	for _, fact := range observed {
		ref := strings.TrimSpace(fact.GroupRef)
		if ref == "" {
			return effective, nil, nil, errors.New("an observed group has no group_ref")
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		names[ref] = fact.DisplayName
		union = append(union, selectiondomain.ObservedGroup{ExternalID: ref, Name: fact.DisplayName, Available: true})
	}
	for ref, entry := range explicit {
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		union = append(union, selectiondomain.ObservedGroup{ExternalID: ref, Name: entry.DisplayName, Available: true})
	}

	decisions, err := selectiondomain.RankObservedGroups(kind, union, policy)
	if err != nil {
		return effective, nil, nil, err
	}

	sources := make(map[string]string, len(union))
	entries := make([]animedomain.SubgroupScoreEntry, 0, len(union)+len(others))
	entries = append(entries, others...)
	refs := make([]string, 0, len(decisions))
	byRef := make(map[string]selectiondomain.GroupSelectionDecision, len(decisions))
	for _, decision := range decisions {
		ref := decision.Group.Ref.ExternalID
		refs = append(refs, ref)
		byRef[ref] = decision
	}
	sort.Strings(refs)
	for _, ref := range refs {
		decision := byRef[ref]
		selected := decision.Outcome == selectiondomain.GroupSelected
		if entry, ok := explicit[ref]; ok {
			entry.EffectiveSelected = selected
			if entry.SelectionMode == animedomain.SelectionModeAuto {
				// An automatic entry has no score of its own: it shows the
				// score the live site policy gave it.
				entry.Score = decision.Group.Score
			}
			entries = append(entries, entry)
			sources[ref] = RSSListSelectionSourceConfigured
			continue
		}
		entry := animedomain.SubgroupScoreEntry{
			Provider: provider, GroupRef: ref, DisplayName: names[ref], EffectiveSelected: selected,
		}
		switch decision.Group.Mode {
		case selectiondomain.Ranked:
			entry.SelectionMode = animedomain.SelectionModeAuto
			entry.Score = decision.Group.Score
			entry.CountInLimit = true
		case selectiondomain.Always:
			on := true
			entry.SelectionMode = animedomain.SelectionModeManual
			entry.ManualSelected = &on
			entry.CountInLimit = decision.Group.CountInLimit
		default:
			off := false
			entry.SelectionMode = animedomain.SelectionModeManual
			entry.ManualSelected = &off
		}
		entries = append(entries, entry)
		sources[ref] = RSSListSelectionSourceObservedDefault
	}
	effective.Selection.Entries = entries
	return effective, sources, byRef, nil
}

func (s *Service) readGroupObservation(ctx context.Context, ref RSSListObservationRef) (GroupObservation, bool, error) {
	if s.observations != nil {
		return s.observations.ReadGroupObservation(ctx, ref)
	}
	return dbGroupObservationReader{db: s.db}.ReadGroupObservation(ctx, ref)
}

// dbGroupObservationReader reads the persisted provider group snapshot that group
// discovery wrote. It is a pure read: no provider request, no write.
type dbGroupObservationReader struct{ db *sql.DB }

func (r dbGroupObservationReader) ReadGroupObservation(ctx context.Context, ref RSSListObservationRef) (GroupObservation, bool, error) {
	if r.db == nil {
		return GroupObservation{}, false, errors.New("group observation database is nil")
	}
	var snapshotID string
	var observation GroupObservation
	err := r.db.QueryRowContext(ctx, `
SELECT id, observed_at
FROM provider_group_snapshots
WHERE provider = $1 AND catalog_entry_ref = $2 AND version = $3`,
		ref.Provider, ref.ProviderEntryRef, ref.SnapshotVersion).Scan(&snapshotID, &observation.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupObservation{}, false, nil
	}
	if err != nil {
		return GroupObservation{}, false, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT e->>'group_ref', e->>'display_name'
FROM provider_group_snapshots s
CROSS JOIN LATERAL jsonb_array_elements(s.entries) e
WHERE s.id = $1 AND s.provider = $2
ORDER BY e->>'group_ref'`, snapshotID, ref.Provider)
	if err != nil {
		return GroupObservation{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var fact ObservedGroupFact
		if err := rows.Scan(&fact.GroupRef, &fact.DisplayName); err != nil {
			return GroupObservation{}, false, err
		}
		observation.Groups = append(observation.Groups, fact)
	}
	if err := rows.Err(); err != nil {
		return GroupObservation{}, false, err
	}
	return observation, true, nil
}
