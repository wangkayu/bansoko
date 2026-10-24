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
SELECT id, subscription_id, managed_anime_id, kind, outcome, occurred_at,
       title, summary, rss_source_id, release_id, group_ref, details,
       dedupe_key, retention_until, reason_code, subject_type, subject_id,
       subject_version, correlation_id, causation_type, causation_id
FROM subscription_log_entries
WHERE subscription_id = $1 AND managed_anime_id = $2
ORDER BY occurred_at DESC, id DESC
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
		var details []byte
		var reasonCode, subjectType, subjectID, correlationID, causationType, causationID sql.NullString
		var subjectVersion sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.SubscriptionID, &entry.ManagedAnimeID, &kind, &outcome, &entry.OccurredAt,
			&entry.Title, &entry.Summary, &rssSourceID, &releaseID, &groupRef, &details, &entry.DedupeKey, &entry.RetentionUntil,
			&reasonCode, &subjectType, &subjectID, &subjectVersion, &correlationID, &causationType, &causationID); err != nil {
			return nil, fmt.Errorf("scan subscription log: %w", err)
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
