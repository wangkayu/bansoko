package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"

	"github.com/lib/pq"
)

// GetAnimeWorkspaceConfigurationApplication never reads execution payloads. Only
// owner configuration, progress aggregates, tag arrays and source-level chain
// summaries cross the database boundary.
func (s *Service) GetAnimeWorkspaceConfigurationApplication(ctx context.Context, managedAnimeID string) (AnimeWorkspaceConfiguration, error) {
	owners, err := s.readWorkspaceOwners(ctx, managedAnimeID)
	if err != nil {
		return AnimeWorkspaceConfiguration{}, err
	}
	if err := s.readWorkspaceConfigurationProgress(ctx, &owners); err != nil {
		return AnimeWorkspaceConfiguration{}, err
	}
	summary, err := s.readWorkspaceReleaseSummary(ctx, owners)
	if err != nil {
		return AnimeWorkspaceConfiguration{}, err
	}
	return AnimeWorkspaceConfiguration{
		ManagedAnime: owners.ManagedAnime, Subscription: owners.Subscription,
		Metadata: owners.Metadata, MetadataHold: owners.MetadataHold,
		Subgroups: owners.Subgroups, Rules: owners.Rules,
		RSSSources: owners.RSSSources, RSSSourceRuntimes: owners.RSSSourceRuntimes,
		EpisodeProgress: owners.EpisodeProgress, ConfigWaitingFiles: owners.ConfigWaitingFiles,
		ReleaseSummary: summary,
	}, nil
}

func (s *Service) readWorkspaceReleaseSummary(ctx context.Context, owners AnimeWorkspace) (AnimeWorkspaceReleaseSummary, error) {
	// Match the full release ordering (including its discovered_at ordering
	// fallback), but project only the timestamp and tag arrays. discovered_at
	// never contributes to LatestPublishedAt. No history window is applied.
	rows, err := s.db.QueryContext(ctx, `
SELECT r.raw_facts->'published_at', r.source_facts #> '{anibt,tags}', r.source_facts #> '{title_tags,tags}'
FROM releases r JOIN rss_sources src ON src.id = r.rss_source_id
WHERE src.subscription_id = $1
ORDER BY (CASE WHEN r.raw_facts->>'published_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}' THEN (r.raw_facts->>'published_at')::timestamptz ELSE r.discovered_at END) DESC, r.id DESC`, owners.Subscription.ID)
	if err != nil {
		return AnimeWorkspaceReleaseSummary{}, fmt.Errorf("read workspace release tags: %w", err)
	}
	defer rows.Close()
	summary := AnimeWorkspaceReleaseSummary{TagObservations: make([]AnimeWorkspaceTagObservation, 0)}
	for rows.Next() {
		var published, anibt, title []byte
		if err := rows.Scan(&published, &anibt, &title); err != nil {
			return AnimeWorkspaceReleaseSummary{}, fmt.Errorf("scan workspace release tags: %w", err)
		}
		if err := appendWorkspaceReleaseObservation(&summary, published, anibt, title); err != nil {
			return AnimeWorkspaceReleaseSummary{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return AnimeWorkspaceReleaseSummary{}, fmt.Errorf("iterate workspace release tags: %w", err)
	}
	rows.Close()
	history, err := s.readWorkspaceSourceHistory(ctx, owners.Subscription.ID)
	if err != nil {
		return AnimeWorkspaceReleaseSummary{}, err
	}
	summary.OwnerChainStable = workspaceOwnerChainStable(owners, history)
	return summary, nil
}

func appendWorkspaceReleaseObservation(summary *AnimeWorkspaceReleaseSummary, published, anibt, title []byte) error {
	var publishedAt *time.Time
	if len(published) != 0 {
		if err := json.Unmarshal(published, &publishedAt); err != nil {
			return fmt.Errorf("decode workspace published timestamp: %w", err)
		}
	}
	// Full workspace RawReleaseFacts uses time.Time. Its JSON string is RFC3339Nano,
	// preserving the offset, not chronological max or a UTC-normalized SQL max.
	if publishedAt != nil {
		value := publishedAt.Format(time.RFC3339Nano)
		if summary.LatestPublishedAt == nil || value > *summary.LatestPublishedAt {
			summary.LatestPublishedAt = &value
		}
	}
	observation := AnimeWorkspaceTagObservation{AniBTTags: []string{}, TitleTags: []string{}}
	if len(anibt) != 0 {
		if err := json.Unmarshal(anibt, &observation.AniBTTags); err != nil {
			return fmt.Errorf("decode workspace AniBT tags: %w", err)
		}
		// Full workspace SourceFacts uses StoredAniBTTags.UnmarshalJSON. Reuse
		// its existing dimension reclassification, never add canonicalization.
		observation.AniBTTags = acquisitiondomain.ReclassifyAniBTTags(observation.AniBTTags)
	}
	if len(title) != 0 {
		if err := json.Unmarshal(title, &observation.TitleTags); err != nil {
			return fmt.Errorf("decode workspace title tags: %w", err)
		}
	}
	summary.Count++
	if len(observation.AniBTTags) > 0 || len(observation.TitleTags) > 0 {
		summary.TagObservations = append(summary.TagObservations, observation)
	}
	return nil
}

// workspaceSourceHistory contains all-history existential facts, not per-release
// chains. Current task identity is always releases.download_task_id; stale tasks
// and their artifacts/operations never participate.
type workspaceSourceHistory struct {
	ReleaseCount                    int64
	UnverifiedArtifacts             bool
	VerifiedWithoutOperations       bool
	UnfinishedOperations            bool
	UnfinishedTasksWithoutArtifacts bool
	TasklessVerdicts                []string
}

func (s *Service) readWorkspaceSourceHistory(ctx context.Context, subscriptionID string) (map[string]workspaceSourceHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.rss_source_id, count(*),
       COALESCE(bool_or(chain.unverified), false),
       COALESCE(bool_or(chain.verified_without_operations), false),
       COALESCE(bool_or(chain.unfinished_operations), false),
       COALESCE(bool_or(t.id IS NOT NULL AND chain.artifact_count = 0 AND t.state <> 'failed'), false),
       COALESCE(array_agg(DISTINCT r.verdict) FILTER (WHERE t.id IS NULL), ARRAY[]::text[])
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
LEFT JOIN download_tasks t ON t.id = r.download_task_id
LEFT JOIN LATERAL (
  SELECT count(*) AS artifact_count,
         bool_or(a.state NOT IN ('failed', 'verified')) AS unverified,
         bool_or(a.state = 'verified' AND NOT EXISTS (
           SELECT 1 FROM file_operations op WHERE op.artifact_id = a.id
         )) AS verified_without_operations,
         bool_or(EXISTS (
           SELECT 1 FROM file_operations op WHERE op.artifact_id = a.id AND op.state NOT IN ('completed', 'failed')
         )) AS unfinished_operations
  FROM artifacts a WHERE a.download_task_id = t.id
) chain ON true
WHERE src.subscription_id = $1
GROUP BY r.rss_source_id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read workspace source history: %w", err)
	}
	defer rows.Close()
	result := make(map[string]workspaceSourceHistory)
	for rows.Next() {
		var sourceID string
		var history workspaceSourceHistory
		if err := rows.Scan(&sourceID, &history.ReleaseCount, &history.UnverifiedArtifacts,
			&history.VerifiedWithoutOperations, &history.UnfinishedOperations,
			&history.UnfinishedTasksWithoutArtifacts, pq.Array(&history.TasklessVerdicts)); err != nil {
			return nil, fmt.Errorf("scan workspace source history: %w", err)
		}
		result[sourceID] = history
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace source history: %w", err)
	}
	return result, nil
}

func workspaceSourceHistoryStable(history workspaceSourceHistory) bool {
	if history.UnverifiedArtifacts || history.VerifiedWithoutOperations || history.UnfinishedOperations || history.UnfinishedTasksWithoutArtifacts {
		return false
	}
	for _, verdict := range history.TasklessVerdicts {
		switch verdict {
		case "waiting_metadata", "subscription_stopped", "source_unavailable", "parse_failed", "filtered", "adopted_existing", "acquisition_failed", "resume_baseline", "start_baseline":
		default:
			return false
		}
	}
	return true
}

// Keep parity with frontend/src/features/workspace/ownerChain.ts: selection is
// source provenance + effective group selection, not running work counts.
func workspaceOwnerChainStable(owners AnimeWorkspace, history map[string]workspaceSourceHistory) bool {
	selected := make(map[string]bool)
	for _, group := range owners.Subgroups.EffectiveSelection {
		if group.EffectiveSelected {
			selected[group.Provider+":"+group.GroupRef] = true
		}
	}
	runtimes := make(map[string]RSSSourceRuntime, len(owners.RSSSourceRuntimes))
	for _, runtime := range owners.RSSSourceRuntimes {
		runtimes[runtime.RSSSourceID] = runtime
	}
	active := 0
	for _, source := range owners.RSSSources {
		provenance := source.ProviderProvenance
		if source.SourceKind != "custom_rss" && (provenance == nil || !selected[string(provenance.Provider)+":"+provenance.ProviderGroupRef]) {
			continue
		}
		active++
		sourceHistory := history[source.ID]
		if sourceHistory.ReleaseCount > 0 {
			if !workspaceSourceHistoryStable(sourceHistory) {
				return false
			}
		} else if runtime, exists := runtimes[source.ID]; !exists || runtime.Health == "unknown" {
			return false
		}
	}
	return active > 0
}
