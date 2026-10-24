package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// Exits for a missing or mismatched placement.
// A scan that finds a placement's file missing (`missing`) or holding other
// content (`fingerprint_conflict`) records an observation; the placement
// stays active and recovers on its own when the file comes back. The user has
// two manual, per-file exits: redownload (the RSS owner) and release from
// management, here. Neither is ever taken automatically.

// PlacementIssueView is one active placement whose latest scan observation is
// missing or fingerprint_conflict, with whether a redownload is pending.
type PlacementIssueView struct {
	PlacementID         string
	FilePath            string
	ReasonCode          string
	RedownloadRequested bool
}

// PlacementReleaseResult is the outcome of releasing one placement.
type PlacementReleaseResult struct {
	PlacementID    string
	ManagedAnimeID string
	Released       int
}

// PlacementExitError says the placement does not admit the exit now; Reason
// is placement_not_active or file_not_missing.
type PlacementExitError struct{ Reason string }

func (e *PlacementExitError) Error() string { return "PLACEMENT_EXIT_UNAVAILABLE: " + e.Reason }

// PlacementReleaseActor is the released_by of a user release from the
// placement issue list.
const PlacementReleaseActor = "user:placement_issue"

// latestObservationIssue reads the disposition of the placement's latest scan
// observation when it is missing or fingerprint_conflict, else "".
const latestObservationIssue = `
COALESCE((SELECT rf.disposition FROM reconciliation_files rf
          WHERE rf.managed_anime_id = p.managed_anime_id AND rf.facts->>'placement_id' = p.id
            AND rf.disposition IN ('missing', 'fingerprint_conflict')
            AND rf.generation = (SELECT MAX(generation) FROM reconciliation_files
                                 WHERE managed_anime_id = p.managed_anime_id AND disposition IS NOT NULL)
          ORDER BY rf.file_path LIMIT 1), '')`

// ListPlacementIssues lists the active placements of one anime whose latest
// scan found the file missing or mismatched (at most 200).
func (s *PlacementService) ListPlacementIssues(ctx context.Context, animeID string) ([]PlacementIssueView, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, physical_path, issue, redownload_requested_at IS NOT NULL FROM (
  SELECT p.id, p.physical_path, p.redownload_requested_at, `+latestObservationIssue+` AS issue
  FROM placements p
  WHERE p.managed_anime_id = $1 AND p.state = 'active' AND p.verification_state = 'verified'
) issues WHERE issue <> ''
ORDER BY physical_path, id LIMIT 200`, animeID)
	if err != nil {
		return nil, fmt.Errorf("read placement issues: %w", err)
	}
	defer rows.Close()
	issues := []PlacementIssueView{}
	for rows.Next() {
		var issue PlacementIssueView
		if err := rows.Scan(&issue.PlacementID, &issue.FilePath, &issue.ReasonCode, &issue.RedownloadRequested); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// ReleasePlacement ends the management of one missing or mismatched placement
// (the `released` terminal): every active row of the instance at its path is
// released, their companion relations end, nothing on disk changes, and the
// episode stays uncovered. Replaying the same key returns the first result.
func (s *PlacementService) ReleasePlacement(ctx context.Context, placementID, idempotencyKey string) (PlacementReleaseResult, error) {
	placementID = strings.TrimSpace(placementID)
	key := strings.TrimSpace(idempotencyKey)
	if len(key) < 8 || len(key) > 128 {
		return PlacementReleaseResult{}, &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key must be between 8 and 128 characters."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlacementReleaseResult{}, err
	}
	defer tx.Rollback()
	var state, animeID, path, issue string
	var requestKey sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT p.state, p.managed_anime_id, p.physical_path, p.release_request_key, `+latestObservationIssue+`
FROM placements p WHERE p.id = $1 FOR UPDATE`, placementID).Scan(&state, &animeID, &path, &requestKey, &issue)
	if errors.Is(err, sql.ErrNoRows) || placementID == "" {
		return PlacementReleaseResult{}, &apperrors.NotFound{Resource: "placement", ID: placementID}
	}
	if err != nil {
		return PlacementReleaseResult{}, fmt.Errorf("lock placement: %w", err)
	}
	result := PlacementReleaseResult{PlacementID: placementID, ManagedAnimeID: animeID}
	if state == "released" && requestKey.String == key {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM placements WHERE release_request_key = $1 AND managed_anime_id = $2`, key, animeID).Scan(&result.Released); err != nil {
			return PlacementReleaseResult{}, err
		}
		return result, nil
	}
	if state != "active" {
		return PlacementReleaseResult{}, &PlacementExitError{Reason: "placement_not_active"}
	}
	if issue == "" {
		return PlacementReleaseResult{}, &PlacementExitError{Reason: "file_not_missing"}
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle_state FROM managed_anime WHERE id = $1 FOR SHARE`, animeID).Scan(&lifecycle); err != nil || lifecycle != "active" {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return PlacementReleaseResult{}, err
		}
		return PlacementReleaseResult{}, &apperrors.Business{Status: 409, Code: "ANIME_RETIRED", Message: "The anime no longer admits changes."}
	}
	rows, err := tx.QueryContext(ctx, `
UPDATE placements
SET state = 'released', released_at = CURRENT_TIMESTAMP, released_by = $3, release_request_key = $4,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND physical_path = $2 AND state = 'active'
RETURNING id, version`, animeID, path, PlacementReleaseActor, key)
	if err != nil {
		return PlacementReleaseResult{}, fmt.Errorf("release placement: %w", err)
	}
	var released []string
	var subjectVersion int64
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			rows.Close()
			return PlacementReleaseResult{}, err
		}
		released = append(released, id)
		if id == placementID {
			subjectVersion = version
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PlacementReleaseResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE placement_companions SET state = 'ended', ended_reason = 'released', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE placement_id = ANY($1) AND state = 'active'`, released); err != nil {
		return PlacementReleaseResult{}, fmt.Errorf("end released companions: %w", err)
	}
	if s.activity != nil {
		subscriptionID, err := subscriptionIDForAnime(ctx, tx, animeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return PlacementReleaseResult{}, fmt.Errorf("read subscription for placement activity: %w", err)
		}
		if err == nil {
			if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
				SubscriptionID:  subscriptionID,
				ManagedAnimeID:  animeID,
				Kind:            "placement_changed",
				Outcome:         "info",
				ReasonCode:      "placement_released",
				SubjectType:     "placement",
				SubjectID:       placementID,
				SubjectVersion:  subjectVersion,
				StableResultKey: "placement_released",
				Title:           "文件已脱离管理",
				Summary:         "文件保留在原处，不再由订阅管理",
			}); err != nil {
				return PlacementReleaseResult{}, fmt.Errorf("append placement activity: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return PlacementReleaseResult{}, err
	}
	result.Released = len(released)
	return result, nil
}

// ListPlacementIssuesApplication and ReleasePlacementApplication are the
// transport boundary of the placement exits.
func (s *Service) ListPlacementIssuesApplication(ctx context.Context, animeID string) ([]PlacementIssueView, error) {
	return s.placements.ListPlacementIssues(ctx, animeID)
}

func (s *Service) ReleasePlacementApplication(ctx context.Context, placementID, idempotencyKey string) (PlacementReleaseResult, error) {
	return s.placements.ReleasePlacement(ctx, placementID, idempotencyKey)
}
