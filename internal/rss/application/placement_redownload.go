package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/apperrors"
)

// Placement redownload: a placement whose latest
// scan found its file missing or holding different content keeps its state;
// nothing is redownloaded automatically. On request, the episode's
// acquisition runs again as a new attempt of the Release that produced the
// placement, through normal acceptance. The request is recorded on the
// placement: while it is pending the placement no longer holds its episodes
// for deduplication, and the next placement of the instance that covers one of
// them displaces it (cause redownload), wherever that file landed. A request
// that does not start a download is withdrawn.

// Placement exit reasons travel as the detail code of a 409.
const (
	PlacementExitNotActive      = "placement_not_active"
	PlacementExitFileNotMissing = "file_not_missing"
	PlacementExitNoSource       = "source_release_unavailable"
)

// PlacementExitError says the placement does not admit the requested exit
// now; Reason is one of the PlacementExit* codes or a RetryReason* code.
type PlacementExitError struct{ Reason string }

func (e *PlacementExitError) Error() string { return "PLACEMENT_EXIT_UNAVAILABLE: " + e.Reason }

// ReleaseRedownloadability decides whether a Release may run again for a
// placement redownload. Unlike a plain retry, a Release that already placed
// its files (or adopted them) may run again; anything in progress or retired
// may not.
func ReleaseRedownloadability(facts ReleaseRetryFacts) ReleaseRetryDecision {
	decision := ReleaseRetryability(facts)
	switch decision.ReasonCode {
	case RetryReasonAlreadyPlaced, RetryReasonAlreadyPresent, RetryReasonPresentAtResume:
		return ReleaseRetryDecision{Allowed: true}
	}
	return decision
}

// releaseRetryDecision is the decision one snapshot's evaluation applies.
func releaseRetryDecision(snapshot releaseSnapshot, facts ReleaseRetryFacts) ReleaseRetryDecision {
	if snapshot.redownload {
		return ReleaseRedownloadability(facts)
	}
	return ReleaseRetryability(facts)
}

// placementExitIssueQuery reads one placement and the disposition of its
// latest scan observation (empty when the latest scan did not observe it).
const placementExitIssueQuery = `
SELECT p.state, p.managed_anime_id, COALESCE(p.quality_facts_ref, ''), p.redownload_request_key,
       COALESCE((SELECT rf.disposition FROM reconciliation_files rf
                 WHERE rf.managed_anime_id = p.managed_anime_id AND rf.facts->>'placement_id' = p.id
                   AND rf.disposition IS NOT NULL
                   AND rf.generation = (SELECT MAX(generation) FROM reconciliation_files
                                        WHERE managed_anime_id = p.managed_anime_id AND disposition IS NOT NULL)
                 ORDER BY rf.file_path LIMIT 1), '')
FROM placements p WHERE p.id = $1`

// RedownloadPlacement requests a new download of a missing or mismatched
// placement's episode. The result is the release retry result of the
// placement's Release; only an accepted result keeps the request. Replaying
// the same Idempotency-Key returns the first result.
func (s *Service) RedownloadPlacement(ctx context.Context, placementID, idempotencyKey string) (ReleaseRetryResult, error) {
	if s == nil || s.db == nil || s.workItems == nil {
		return ReleaseRetryResult{}, errors.New("rss service is not configured")
	}
	placementID = strings.TrimSpace(placementID)
	key := strings.TrimSpace(idempotencyKey)
	if len(key) < 8 || len(key) > 128 {
		return ReleaseRetryResult{}, &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key must be between 8 and 128 characters."}
	}
	var state, animeID, releaseID, disposition string
	var pendingKey sql.NullString
	err := s.db.QueryRowContext(ctx, placementExitIssueQuery, placementID).Scan(&state, &animeID, &releaseID, &pendingKey, &disposition)
	if errors.Is(err, sql.ErrNoRows) || placementID == "" {
		return ReleaseRetryResult{}, &apperrors.NotFound{Resource: "placement", ID: placementID}
	}
	if err != nil {
		return ReleaseRetryResult{}, fmt.Errorf("read placement: %w", err)
	}
	requestHash := placementRedownloadRequestHash(placementID)
	if releaseID != "" {
		if replay, found, err := readReleaseRetryRecord(ctx, s.db, releaseID, idempotencyKey, requestHash); err != nil || found {
			return replay, err
		}
	}
	if state != "active" {
		return ReleaseRetryResult{}, &PlacementExitError{Reason: PlacementExitNotActive}
	}
	if disposition != "missing" && disposition != "fingerprint_conflict" {
		return ReleaseRetryResult{}, &PlacementExitError{Reason: PlacementExitFileNotMissing}
	}
	var owned bool
	if releaseID != "" {
		if err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM releases r JOIN rss_sources src ON src.id = r.rss_source_id
              JOIN subscriptions sub ON sub.id = src.subscription_id
              WHERE r.id = $1 AND sub.managed_anime_id = $2)`, releaseID, animeID).Scan(&owned); err != nil {
			return ReleaseRetryResult{}, fmt.Errorf("read placement release: %w", err)
		}
	}
	if !owned {
		return ReleaseRetryResult{}, &PlacementExitError{Reason: PlacementExitNoSource}
	}
	// The request is recorded before the evaluation reads the instance's
	// placements, so deduplication no longer counts this one. A request
	// already pending (an earlier accepted one) stays as it is.
	recorded, err := s.db.ExecContext(ctx, `
UPDATE placements SET redownload_requested_at = CURRENT_TIMESTAMP, redownload_request_key = $2,
       version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active' AND redownload_requested_at IS NULL`, placementID, key)
	if err != nil {
		return ReleaseRetryResult{}, fmt.Errorf("record placement redownload: %w", err)
	}
	ownMarker, err := recorded.RowsAffected()
	if err != nil {
		return ReleaseRetryResult{}, err
	}
	result, err := s.retryRelease(ctx, releaseID, idempotencyKey, requestHash, true)
	if ownMarker > 0 && (err != nil || result.Outcome != RetryOutcomeAccepted) {
		// Nothing was downloaded: the placement holds its episodes again.
		if _, clearErr := s.db.ExecContext(context.WithoutCancel(ctx), `
UPDATE placements SET redownload_requested_at = NULL, redownload_request_key = NULL,
       version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active' AND redownload_request_key = $2`, placementID, key); clearErr != nil && err == nil {
			err = fmt.Errorf("withdraw placement redownload: %w", clearErr)
		}
	}
	if err != nil {
		var notRetryable *ReleaseNotRetryableError
		if errors.As(err, &notRetryable) {
			return ReleaseRetryResult{}, &PlacementExitError{Reason: notRetryable.Reason}
		}
		return ReleaseRetryResult{}, err
	}
	return result, nil
}

func placementRedownloadRequestHash(placementID string) string {
	sum := sha256.Sum256([]byte("placement_redownload\x1f" + placementID))
	return "sha256:" + hex.EncodeToString(sum[:])
}
