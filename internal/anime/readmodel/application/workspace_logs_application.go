package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *Service) readSubscriptionLogsApplication(ctx context.Context, subscriptionID, managedAnimeID string) ([]SubscriptionLogEntry, error) {
	// Activity lineage is persisted by the writer (046_activity_envelope) and
	// must survive the read projection: reason code, subject, and
	// correlation/causation are owner-first truth, not derived at read time.
	rows, err := s.db.QueryContext(ctx, `
SELECT l.id, l.subscription_id, l.managed_anime_id, l.kind, l.outcome, l.occurred_at,
       l.message_code, l.message_params, l.rss_source_id, l.release_id, l.group_ref, l.details,
       l.dedupe_key, l.retention_until, l.reason_code, l.subject_type, l.subject_id,
       l.subject_version, l.correlation_id, l.causation_type, l.causation_id,
       COALESCE(left(COALESCE(NULLIF(btrim(ma.title_override), ''), ma.title), 512), ''),
       CASE WHEN s.origin = 'oneshot' THEN 'one_shot' ELSE 'subscription' END
FROM subscription_log_entries l
JOIN subscriptions s ON s.id = l.subscription_id
LEFT JOIN managed_anime ma ON ma.id = l.managed_anime_id
WHERE l.subscription_id = $1 AND l.managed_anime_id = $2
ORDER BY l.occurred_at DESC, l.id DESC
LIMIT 100`, subscriptionID, managedAnimeID)
	if err != nil {
		return nil, fmt.Errorf("read subscription logs: %w", err)
	}
	defer rows.Close()
	result := make([]SubscriptionLogEntry, 0, 100)
	for rows.Next() {
		var entry SubscriptionLogEntry
		var kind, outcome string
		var rssSourceID, releaseID, groupRef sql.NullString
		var details, params []byte
		var reasonCode, subjectType, subjectID, correlationID, causationType, causationID sql.NullString
		var subjectVersion sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.SubscriptionID, &entry.ManagedAnimeID, &kind, &outcome, &entry.OccurredAt,
			&entry.MessageCode, &params, &rssSourceID, &releaseID, &groupRef, &details, &entry.DedupeKey, &entry.RetentionUntil,
			&reasonCode, &subjectType, &subjectID, &subjectVersion, &correlationID, &causationType, &causationID,
			&entry.AnimeTitle, &entry.Source); err != nil {
			return nil, fmt.Errorf("scan subscription log: %w", err)
		}
		if err := json.Unmarshal(params, &entry.MessageParams); err != nil {
			return nil, fmt.Errorf("decode subscription log message params: %w", err)
		}
		entry.Kind, entry.Outcome = kind, outcome
		entry.RSSSourceID = cloneNullableString(rssSourceID)
		entry.ReleaseID = cloneNullableString(releaseID)
		entry.GroupRef = cloneNullableString(groupRef)
		entry.ReasonCode = cloneNullableString(reasonCode)
		entry.SubjectType = cloneNullableString(subjectType)
		entry.SubjectID = cloneNullableString(subjectID)
		entry.CorrelationID = cloneNullableString(correlationID)
		entry.CausationType = cloneNullableString(causationType)
		entry.CausationID = cloneNullableString(causationID)
		if subjectVersion.Valid {
			value := subjectVersion.Int64
			entry.SubjectVersion = &value
		}
		if details != nil {
			var value SubscriptionLogDetails
			if err := json.Unmarshal(details, &value); err != nil {
				return nil, fmt.Errorf("decode subscription log details: %w", err)
			}
			entry.Details = &value
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscription logs: %w", err)
	}
	return result, nil
}
