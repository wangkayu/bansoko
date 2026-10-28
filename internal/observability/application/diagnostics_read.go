package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
)

const (
	defaultDiagnosticLimit = 20
	maxDiagnosticLimit     = 100

	// Cursor ranks of the two groups; the open group sorts first.
	diagnosticRankOpen      = 0
	diagnosticRankRecovered = 1

	DiagnosticStateOpen      = "open"
	DiagnosticStateRecovered = "recovered"
)

// ListDiagnostics returns the user-facing episodes: the open group by last
// failure, then the recovered group by recovery time, each newest first, with
// keyset pagination. Internal episodes are never listed; they fold into the
// Internal summary of the first page.
func (s *Service) ListDiagnostics(ctx context.Context, params DiagnosticQuery) (DiagnosticPage, error) {
	limit := defaultDiagnosticLimit
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxDiagnosticLimit {
		limit = maxDiagnosticLimit
	}
	state := ""
	if params.State != nil {
		state = *params.State
	}
	if state != "" && state != DiagnosticStateOpen && state != DiagnosticStateRecovered {
		return DiagnosticPage{}, &apperrors.BadRequest{Code: "INVALID_REQUEST", Message: "The state filter is invalid."}
	}

	const group = `CASE WHEN succeeded_after_at IS NULL THEN 0 ELSE 1 END`
	where := `kind <> 'internal'`
	args := []any{}
	switch state {
	case DiagnosticStateOpen:
		where += ` AND succeeded_after_at IS NULL`
	case DiagnosticStateRecovered:
		where += ` AND succeeded_after_at IS NOT NULL`
	}
	firstPage := params.Cursor == nil || *params.Cursor == ""
	if !firstPage {
		t, rank, id, err := decodeCursor(*params.Cursor)
		if err != nil || (rank != diagnosticRankOpen && rank != diagnosticRankRecovered) {
			return DiagnosticPage{}, invalidCursorError()
		}
		args = append(args, rank, t, id)
		where += fmt.Sprintf(` AND ((%[1]s) > $1 OR ((%[1]s) = $1 AND (COALESCE(succeeded_after_at, last_failed_at), id) < ($2, $3)))`, group)
	}
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, kind, variant, severity, target_id, target_name, target_host, error_class, http_status, via,
       affected_ids, recent, first_failed_at, last_failed_at, succeeded_after_at, occurrence_count, retention_until
FROM diagnostic_episodes
WHERE %s
ORDER BY (%s), COALESCE(succeeded_after_at, last_failed_at) DESC, id DESC
LIMIT $%d`, where, group, len(args)), args...)
	if err != nil {
		return DiagnosticPage{}, fmt.Errorf("query diagnostic episodes: %w", err)
	}
	defer rows.Close()

	var episodes []DiagnosticEpisode
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return DiagnosticPage{}, err
		}
		episodes = append(episodes, ep)
	}
	if err := rows.Err(); err != nil {
		return DiagnosticPage{}, fmt.Errorf("iterate diagnostic episodes: %w", err)
	}

	page := DiagnosticPage{Episodes: episodes}
	if len(episodes) > limit {
		last := episodes[limit-1]
		page.Episodes = episodes[:limit]
		rank, at := diagnosticRankOpen, last.LastFailedAt
		if last.SucceededAfterAt != nil {
			rank, at = diagnosticRankRecovered, *last.SucceededAfterAt
		}
		c := encodeCursor(at, rank, last.ID)
		page.NextCursor = &c
	}
	if page.Episodes == nil {
		page.Episodes = []DiagnosticEpisode{}
	}
	if firstPage && state != DiagnosticStateRecovered {
		internal, err := s.readInternalSummary(ctx)
		if err != nil {
			return DiagnosticPage{}, err
		}
		page.Internal = internal
	}
	return page, nil
}

func (s *Service) readInternalSummary(ctx context.Context) (*DiagnosticInternal, error) {
	var (
		count     int
		lastAt    sql.NullTime
		requestID sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(occurrence_count), 0)::int, MAX(last_failed_at),
       (array_agg(last_request_id ORDER BY last_failed_at DESC) FILTER (WHERE last_request_id IS NOT NULL))[1]
FROM diagnostic_episodes
WHERE kind = 'internal' AND succeeded_after_at IS NULL`).Scan(&count, &lastAt, &requestID)
	if err != nil {
		return nil, fmt.Errorf("query internal diagnostic summary: %w", err)
	}
	if count == 0 || !lastAt.Valid {
		return nil, nil
	}
	return &DiagnosticInternal{OccurrenceCount: count, LastFailedAt: lastAt.Time, LastRequestID: nullableStringPointer(requestID)}, nil
}

type rowScanner interface{ Scan(...any) error }

func scanEpisode(row rowScanner) (DiagnosticEpisode, error) {
	var (
		ep                              DiagnosticEpisode
		variant, name, host, class, via sql.NullString
		status                          sql.NullInt64
		affected                        pq.StringArray
		recent                          []byte
		succeeded                       sql.NullTime
	)
	if err := row.Scan(&ep.ID, &ep.Kind, &variant, &ep.Severity, &ep.Target.ID, &name, &host, &class, &status, &via,
		&affected, &recent, &ep.FirstFailedAt, &ep.LastFailedAt, &succeeded, &ep.OccurrenceCount, &ep.RetentionUntil); err != nil {
		return ep, fmt.Errorf("scan diagnostic episode: %w", err)
	}
	ep.Variant = variant.String
	ep.Target.Kind = ep.Kind
	ep.Target.Name = ep.Target.ID
	if name.Valid {
		ep.Target.Name = name.String
	}
	ep.Target.Host = nullableStringPointer(host)
	if succeeded.Valid {
		ep.SucceededAfterAt = &succeeded.Time
	}
	if class.Valid {
		detail := &DiagnosticDetail{ErrorClass: class.String, Via: nullableStringPointer(via)}
		if status.Valid {
			v := int(status.Int64)
			detail.HTTPStatus = &v
		}
		ep.Detail = detail
	}
	var occurrences []struct {
		At         time.Time `json:"at"`
		ErrorClass string    `json:"error_class"`
		Via        string    `json:"via"`
		Host       string    `json:"host"`
	}
	if err := json.Unmarshal(recent, &occurrences); err != nil {
		return ep, fmt.Errorf("decode recent occurrences: %w", err)
	}
	ep.Recent = make([]DiagnosticOccurrence, 0, len(occurrences))
	// Stored oldest first; the product shows the newest first.
	for i := len(occurrences) - 1; i >= 0; i-- {
		o := occurrences[i]
		ep.Recent = append(ep.Recent, DiagnosticOccurrence{At: o.At, ErrorClass: strPtr(o.ErrorClass), Via: strPtr(o.Via), Host: strPtr(o.Host)})
	}
	spec, ok := observabilityport.SpecOf(observabilityport.Condition{Kind: observabilityport.ConditionKind(ep.Kind), Variant: observabilityport.ConditionVariant(ep.Variant)})
	if !ok {
		return ep, fmt.Errorf("stored diagnostic condition %s.%s is not in the catalog", ep.Kind, ep.Variant)
	}
	ep.Retry = DiagnosticRetry{Mode: string(spec.Retry)}
	if spec.Settings != "" {
		target := string(spec.Settings)
		ep.Retry.SettingsTarget = &target
	}
	if spec.Affected != "" && len(affected) > 0 {
		ep.Affected = &DiagnosticAffected{Kind: string(spec.Affected), Count: len(affected)}
	}
	return ep, nil
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
