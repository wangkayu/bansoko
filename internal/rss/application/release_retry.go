package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	terminal "bansoko/internal/acquisition/terminal/application"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

// Release retry outcomes. A retry is normal acceptance run once on request:
// accepted creates a new download task; awaiting puts the release back into
// its active subscription's competition, which decides it with the other
// waiting releases; the others report why nothing was downloaded and are
// results, not errors.
const (
	RetryOutcomeAccepted = "accepted"
	RetryOutcomeAwaiting = "awaiting"
	RetryOutcomeFiltered = "filtered"
	RetryOutcomeHeld     = "held"
	RetryOutcomePresent  = "present"
	RetryOutcomeFailed   = "failed"
)

// ReleaseRetryResult is the synchronous answer of one retry. ReasonCode is the
// Release's stable reason code (empty for accepted); DownloadTaskID is set
// only when a new download task was accepted.
type ReleaseRetryResult struct {
	ReleaseID      string `json:"release_id"`
	Verdict        string `json:"verdict"`
	Outcome        string `json:"outcome"`
	ReasonCode     string `json:"reason_code,omitempty"`
	DownloadTaskID string `json:"download_task_id,omitempty"`
}

// ReleaseNotRetryableError says the Release may not be retried now; Reason is
// one of the RetryReason* codes.
type ReleaseNotRetryableError struct{ Reason string }

func (e *ReleaseNotRetryableError) Error() string {
	return "RELEASE_NOT_RETRYABLE: " + e.Reason
}

var (
	// ErrReleaseChanged: the Release changed while the retry evaluated it.
	ErrReleaseChanged = &apperrors.Business{Status: 409, Code: "RELEASE_CHANGED", Message: "The release changed while it was being retried. Try again."}
	// ErrReleaseRetryUnavailable: a dependency the evaluation needs (the
	// downloader lookup, the naming program time limit) did not answer;
	// nothing was written.
	ErrReleaseRetryUnavailable = &apperrors.Business{Status: 503, Code: "RELEASE_RETRY_UNAVAILABLE", Message: "The release could not be evaluated right now. Try again later."}

	errReleaseVersionConflict = errors.New("release version conflict")
	errReleaseInputsChanged   = errors.New("release evaluation inputs changed during target preflight")
)

// releaseEntryAcceptor is the terminal entry a Release acceptance uses: the
// automatic entry for the release_evaluate worker, the manual retry entry for
// the retry command. Both re-check the same admission.
type releaseEntryAcceptor interface {
	Prepare(context.Context, txscope.Tx, terminal.EntryTask) (terminal.PreparedHandle, error)
	Accept(context.Context, txscope.Tx, terminal.PreparedHandle) (terminal.PendingHandle, error)
}

func releaseAcceptor(db *sql.DB, revisions *draft.Service, manualRetry bool) (releaseEntryAcceptor, error) {
	if manualRetry {
		return terminal.NewManualRetryAcceptor(db, revisions)
	}
	return terminal.NewRSSAutomaticAcceptor(db, revisions)
}

// loadReleaseRetryFactsTx reads the retry facts of a Release the caller has
// locked: its verdict, current task and acquisition states, the owner
// lifecycle and how many download tasks the Release has owned.
func loadReleaseRetryFactsTx(ctx context.Context, q queryer, releaseID string) (ReleaseRetryFacts, int, error) {
	var facts ReleaseRetryFacts
	var count int
	if err := q.QueryRowContext(ctx, `
SELECT r.verdict, COALESCE(task.state, ''), COALESCE(acq.state, ''), COALESCE(lifecycle.lifecycle_state, ''), sub.state,
       (SELECT count(*) FROM download_tasks owned WHERE owned.release_id = r.id),
       `+ReleasePlacementIssueSQL("sub.managed_anime_id")+`
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
LEFT JOIN managed_anime lifecycle ON lifecycle.id = sub.managed_anime_id
LEFT JOIN download_tasks task ON task.id = r.download_task_id
LEFT JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE r.id = $1`, releaseID).Scan(&facts.Verdict, &facts.DownloadTaskState, &facts.AcquisitionState, &facts.LifecycleState, &facts.SubscriptionState, &count, &facts.PlacementMissing); err != nil {
		return ReleaseRetryFacts{}, 0, fmt.Errorf("read release retry facts: %w", err)
	}
	return facts, count, nil
}

// detachRetriedTaskTx runs inside the owner write of a retry, after the
// Release row is locked. It repeats the retry decision on the locked state
// (only one acquisition per Release may be in progress), checks the Release
// did not change since it was evaluated, and takes the failed task (or, for a
// placement redownload, the completed one) off the Release so the acceptance
// can attach a new one. A run again may also detach a completed task (failed
// post-processing, or a placed file the scan found missing). The detached task
// stays as a record; detaching changes no Release version.
func detachRetriedTaskTx(ctx context.Context, tx *sql.Tx, snapshot releaseSnapshot, currentVersion, expectedVersion int64, currentTask sql.NullString) error {
	facts, count, err := loadReleaseRetryFactsTx(ctx, tx, snapshot.releaseID)
	if err != nil {
		return err
	}
	if decision := releaseRetryDecision(snapshot, facts); !decision.Allowed {
		return &ReleaseNotRetryableError{Reason: decision.ReasonCode}
	}
	if currentVersion != expectedVersion || count != snapshot.taskCount {
		return ErrReleaseChanged
	}
	if !currentTask.Valid {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE releases SET download_task_id = NULL
WHERE id = $1 AND download_task_id = $2`, snapshot.releaseID, currentTask.String); err != nil {
		return fmt.Errorf("detach the failed download task: %w", err)
	}
	return nil
}

// RetryRelease evaluates one Release again through normal acceptance, on
// explicit request. It runs the same evaluation as the release_evaluate
// worker (current rules, the metadata hold, the batch plan with
// fill-missing deduplication, the replacement decision, the pre-download
// probes) and writes through the same owner writer, so a retry can only reach
// a verdict automatic acceptance would reach. An accepted Release gets a new
// download task; its previous task is kept. This is the RSS row's "run again":
// the whole chain re-enqueued for a Release with no download, a failed
// download, failed post-processing or a placed file the scan found missing. A stopped subscription still
// admits the explicit retry. The idempotency key replays the first result.
func (s *Service) RetryRelease(ctx context.Context, releaseID, idempotencyKey string) (ReleaseRetryResult, error) {
	if s == nil || s.db == nil || s.workItems == nil {
		return ReleaseRetryResult{}, errors.New("rss service is not configured")
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return ReleaseRetryResult{}, &apperrors.NotFound{Resource: "release", ID: releaseID}
	}
	if key := strings.TrimSpace(idempotencyKey); len(key) < 8 || len(key) > 128 {
		return ReleaseRetryResult{}, &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key must be between 8 and 128 characters."}
	}
	return s.retryRelease(ctx, releaseID, idempotencyKey, releaseRetryRequestHash(releaseID), false)
}

// retryRelease runs the retry evaluation. redownload applies the placement
// redownload decision (ReleaseRedownloadability) instead of the plain retry
// rule; requestHash scopes the idempotency record to the request.
func (s *Service) retryRelease(ctx context.Context, releaseID, idempotencyKey, requestHash string, redownload bool) (ReleaseRetryResult, error) {
	if replay, found, err := readReleaseRetryRecord(ctx, s.db, releaseID, idempotencyKey, requestHash); err != nil || found {
		return replay, err
	}
	snapshot, err := s.loadReleaseSnapshot(ctx, releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return ReleaseRetryResult{}, &apperrors.NotFound{Resource: "release", ID: releaseID}
	}
	if err != nil {
		return ReleaseRetryResult{}, err
	}
	snapshot.redownload = redownload
	if decision := releaseRetryDecision(snapshot, snapshot.retryFacts()); !decision.Allowed {
		return ReleaseRetryResult{}, &ReleaseNotRetryableError{Reason: decision.ReasonCode}
	}
	// A Release whose torrent a refresh never fetched (a start_baseline item under
	// new-releases-only) fetches it now, once, so every rule can judge it.
	changed, err := s.ensureRetryTorrentInput(ctx, snapshot)
	if err != nil {
		return ReleaseRetryResult{}, err
	}
	if changed {
		redownload := snapshot.redownload
		if snapshot, err = s.loadReleaseSnapshot(ctx, releaseID); err != nil {
			return ReleaseRetryResult{}, err
		}
		snapshot.redownload = redownload
		if decision := releaseRetryDecision(snapshot, snapshot.retryFacts()); !decision.Allowed {
			return ReleaseRetryResult{}, &ReleaseNotRetryableError{Reason: decision.ReasonCode}
		}
	}
	snapshot.manualRetry = true
	snapshot.retryKey = strings.TrimSpace(idempotencyKey)
	// A run again of a Release whose placed file the latest scan found missing
	// or changed requests a redownload of those placements first (the
	// placement redownload mechanism), so deduplication no longer counts them
	// and the new placement displaces them. A request that does not start a
	// download is withdrawn.
	if !redownload && snapshot.placementMissing {
		marked, err := s.requestReleasePlacementRedownloads(ctx, snapshot, idempotencyKey)
		if err != nil {
			return ReleaseRetryResult{}, err
		}
		if marked {
			result, err := s.evaluateReleaseRetry(ctx, releaseID, idempotencyKey, requestHash, snapshot)
			// A release waiting for the competition keeps the request with
			// the release (competition_retry_key): the competition compares
			// it without the missing placements, and a decision that
			// downloads nothing withdraws it.
			if err != nil || (result.Outcome != RetryOutcomeAccepted && result.Outcome != RetryOutcomeAwaiting) {
				if clearErr := s.withdrawReleasePlacementRedownloads(ctx, snapshot, idempotencyKey); clearErr != nil && err == nil {
					err = fmt.Errorf("withdraw release placement redownload: %w", clearErr)
				}
			}
			return result, err
		}
	}
	return s.evaluateReleaseRetry(ctx, releaseID, idempotencyKey, requestHash, snapshot)
}

// requestReleasePlacementRedownloads records a redownload request on every
// active placement of the Release whose latest scan found its file missing or
// changed and that has none pending. It reports whether it recorded any.
func (s *Service) requestReleasePlacementRedownloads(ctx context.Context, snapshot releaseSnapshot, idempotencyKey string) (bool, error) {
	recorded, err := s.db.ExecContext(ctx, `
UPDATE placements p SET redownload_requested_at = CURRENT_TIMESTAMP, redownload_request_key = $3,
       version = p.version + 1, updated_at = CURRENT_TIMESTAMP
WHERE p.managed_anime_id = $1 AND p.quality_facts_ref = $2 AND p.redownload_requested_at IS NULL
  AND `+placementIssueCondition, snapshot.managedAnimeID, snapshot.releaseID, strings.TrimSpace(idempotencyKey))
	if err != nil {
		return false, fmt.Errorf("record release placement redownload: %w", err)
	}
	count, err := recorded.RowsAffected()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// withdrawReleasePlacementRedownloads clears the requests this run recorded:
// nothing was downloaded, so the placements hold their episodes again.
func (s *Service) withdrawReleasePlacementRedownloads(ctx context.Context, snapshot releaseSnapshot, idempotencyKey string) error {
	_, err := s.db.ExecContext(context.WithoutCancel(ctx), withdrawPlacementRedownloadsSQL,
		snapshot.managedAnimeID, snapshot.releaseID, strings.TrimSpace(idempotencyKey))
	return err
}

// withdrawPlacementRedownloadsSQL clears the redownload requests one retry
// (request key $3) recorded on the placements of release $2.
const withdrawPlacementRedownloadsSQL = `
UPDATE placements SET redownload_requested_at = NULL, redownload_request_key = NULL,
       version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND quality_facts_ref = $2 AND state = 'active' AND redownload_request_key = $3`

// withdrawPlacementRedownloadsTx is withdrawReleasePlacementRedownloads in the
// caller's transaction (a waiting retry the competition did not download).
func withdrawPlacementRedownloadsTx(ctx context.Context, tx *sql.Tx, managedAnimeID, releaseID, requestKey string) error {
	if _, err := tx.ExecContext(ctx, withdrawPlacementRedownloadsSQL, managedAnimeID, releaseID, strings.TrimSpace(requestKey)); err != nil {
		return fmt.Errorf("withdraw release placement redownload: %w", err)
	}
	return nil
}

// evaluateReleaseRetry runs the evaluation of an admitted retry and writes
// its result. A stopped subscription's retry whose subscription was resumed
// meanwhile is evaluated once more from a fresh snapshot: it waits for the
// competition like any release of an active subscription.
func (s *Service) evaluateReleaseRetry(ctx context.Context, releaseID, idempotencyKey, requestHash string, snapshot releaseSnapshot) (ReleaseRetryResult, error) {
	result, err := s.evaluateReleaseRetryOnce(ctx, releaseID, idempotencyKey, requestHash, snapshot)
	if !errors.Is(err, errCompetitionActivated) {
		return result, err
	}
	fresh, err := s.loadReleaseSnapshot(ctx, releaseID)
	if err != nil {
		return ReleaseRetryResult{}, err
	}
	fresh.manualRetry, fresh.retryKey, fresh.redownload = true, snapshot.retryKey, snapshot.redownload
	result, err = s.evaluateReleaseRetryOnce(ctx, releaseID, idempotencyKey, requestHash, fresh)
	if errors.Is(err, errCompetitionActivated) {
		return ReleaseRetryResult{}, ErrReleaseChanged
	}
	return result, err
}

func (s *Service) evaluateReleaseRetryOnce(ctx context.Context, releaseID, idempotencyKey, requestHash string, snapshot releaseSnapshot) (ReleaseRetryResult, error) {
	claim := workitems.Claim{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID}, Kind: workitems.KindReleaseEvaluate,
		ExpectedOwnerVersion: snapshot.releaseVersion, ManagedAnimeID: snapshot.managedAnimeID,
	}
	outcome, err := s.evaluateRelease(ctx, claim, snapshot)
	if err != nil {
		return ReleaseRetryResult{}, err
	}
	switch outcome.Kind {
	case worker.OutcomeFinish:
	case worker.OutcomeRetry:
		return ReleaseRetryResult{}, ErrReleaseRetryUnavailable
	default:
		return ReleaseRetryResult{}, fmt.Errorf("release retry: unexpected evaluation outcome %q", outcome.Kind)
	}
	if outcome.WriteOwner == nil && outcome.OwnerResult.ReasonCode == "parser_policy_required" {
		return ReleaseRetryResult{}, &apperrors.Business{Status: 409, Code: "INSTANCE_PARSER_POLICY_REQUIRED", Message: "The anime has no usable parser policy."}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReleaseRetryResult{}, fmt.Errorf("begin release retry: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "release_retry\x1f"+releaseID+"\x1f"+idempotencyKey); err != nil {
		return ReleaseRetryResult{}, fmt.Errorf("lock release retry request: %w", err)
	}
	if replay, found, err := readReleaseRetryRecord(ctx, tx, releaseID, idempotencyKey, requestHash); err != nil || found {
		// A concurrent request with the same key committed first; this
		// evaluation is discarded.
		return replay, err
	}
	if s.competition != nil {
		// The subscription row before the release (the competition commit's
		// order): the owner writer and the enqueue below lock it again.
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, snapshot.managedAnimeID); err != nil {
			return ReleaseRetryResult{}, fmt.Errorf("lock subscription for release retry: %w", err)
		}
	}
	var result ReleaseRetryResult
	if outcome.WriteOwner == nil {
		// Nothing was evaluated (the provider group is not selected, or the
		// evaluation could not run): no Release write.
		result = ReleaseRetryResult{ReleaseID: releaseID, Verdict: snapshot.verdict, Outcome: RetryOutcomeFiltered, ReasonCode: outcome.OwnerResult.ReasonCode}
		if outcome.OwnerResult.State == workitems.StateFailed {
			result.Outcome = RetryOutcomeFailed
		}
	} else {
		if _, err := outcome.WriteOwner(ctx, txscope.Wrap(tx), outcome.OwnerResult); err != nil {
			if errors.Is(err, errCompetitionActivated) {
				return ReleaseRetryResult{}, err
			}
			return ReleaseRetryResult{}, mapReleaseRetryWriteError(err)
		}
		result, err = readReleaseRetryResultTx(ctx, tx, releaseID)
		if err != nil {
			return ReleaseRetryResult{}, err
		}
		if s.competition != nil {
			if _, err := competition.EnqueueTx(ctx, tx, snapshot.managedAnimeID, competition.TriggerReleaseEvaluated, competition.WhenAwaiting); err != nil {
				return ReleaseRetryResult{}, err
			}
		}
	}
	if err := writeReleaseRetryRecordTx(ctx, tx, releaseID, idempotencyKey, requestHash, result); err != nil {
		return ReleaseRetryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReleaseRetryResult{}, fmt.Errorf("commit release retry: %w", err)
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetRSS, ManagedAnimeID: snapshot.managedAnimeID})
	}
	if outcome.AfterCommit != nil {
		outcome.AfterCommit(ctx)
	}
	return result, nil
}

func mapReleaseRetryWriteError(err error) error {
	var notRetryable *ReleaseNotRetryableError
	if errors.As(err, &notRetryable) {
		return err
	}
	var business *apperrors.Business
	if errors.As(err, &business) {
		return err
	}
	if errors.Is(err, errReleaseVersionConflict) || errors.Is(err, errReleaseInputsChanged) {
		return ErrReleaseChanged
	}
	var acceptance *terminal.AcceptanceError
	if errors.As(err, &acceptance) {
		switch acceptance.Status {
		case 409:
			return ErrReleaseChanged
		case 503:
			return ErrReleaseRetryUnavailable
		}
	}
	return err
}

// readReleaseRetryResultTx derives the retry outcome from the Release row the
// owner writer just wrote.
func readReleaseRetryResultTx(ctx context.Context, tx *sql.Tx, releaseID string) (ReleaseRetryResult, error) {
	var verdict, reasonCode string
	var taskID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT verdict, reason_code, download_task_id FROM releases WHERE id = $1`, releaseID).Scan(&verdict, &reasonCode, &taskID); err != nil {
		return ReleaseRetryResult{}, fmt.Errorf("read retried release: %w", err)
	}
	return releaseRetryResult(releaseID, verdict, reasonCode, taskID.String), nil
}

// releaseRetryResult maps the written Release onto the retry outcome.
func releaseRetryResult(releaseID, verdict, reasonCode, taskID string) ReleaseRetryResult {
	result := ReleaseRetryResult{ReleaseID: releaseID, Verdict: verdict, ReasonCode: reasonCode}
	switch {
	case taskID != "":
		result.Outcome, result.DownloadTaskID, result.ReasonCode = RetryOutcomeAccepted, taskID, ""
	case verdict == competition.VerdictAwaiting:
		result.Outcome = RetryOutcomeAwaiting
	case verdict == "filtered":
		result.Outcome = RetryOutcomeFiltered
	case verdict == "waiting_metadata":
		result.Outcome = RetryOutcomeHeld
	case verdict == "adopted_existing":
		result.Outcome = RetryOutcomePresent
	default:
		result.Outcome = RetryOutcomeFailed
	}
	return result
}

func releaseRetryRequestHash(releaseID string) string {
	sum := sha256.Sum256([]byte("release_retry\x1f" + releaseID))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readReleaseRetryRecord(ctx context.Context, q queryer, releaseID, idempotencyKey, requestHash string) (ReleaseRetryResult, bool, error) {
	var existingHash string
	var snapshot []byte
	err := q.QueryRowContext(ctx, `
SELECT request_hash, result_snapshot
FROM idempotency_records
WHERE kind = 'release_retry' AND release_id = $1 AND idempotency_key = $2`, releaseID, idempotencyKey).Scan(&existingHash, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return ReleaseRetryResult{}, false, nil
	}
	if err != nil {
		return ReleaseRetryResult{}, false, fmt.Errorf("read release retry idempotency: %w", err)
	}
	if existingHash != requestHash {
		return ReleaseRetryResult{}, false, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different request."}
	}
	var result ReleaseRetryResult
	if err := json.Unmarshal(snapshot, &result); err != nil {
		return ReleaseRetryResult{}, false, fmt.Errorf("decode release retry idempotency: %w", err)
	}
	return result, true, nil
}

func writeReleaseRetryRecordTx(ctx context.Context, tx *sql.Tx, releaseID, idempotencyKey, requestHash string, result ReleaseRetryResult) error {
	snapshot, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode release retry idempotency: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, release_id, result_snapshot)
VALUES ('release_retry', $1, $2, $3, $4)`, idempotencyKey, requestHash, releaseID, snapshot); err != nil {
		return fmt.Errorf("record release retry idempotency: %w", err)
	}
	return nil
}
